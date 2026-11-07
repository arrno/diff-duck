//go:build unix

package codexcli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/arrno/diff-duck/internal/services/ports"
)

func TestEachRequestGetsItsOwnInstructionsAndSchema(t *testing.T) {
	m, record := fakeCodex(t, `cat >/dev/null
for a in "$@"; do case "$a" in model_instructions_file=*) f="${a#model_instructions_file=}"; f="${f#\"}"; cat "${f%\"}" >> "RECORD/instructions"; echo >> "RECORD/instructions";; esac; done
prev=""; for a in "$@"; do [ "$prev" = "--output-schema" ] && { cat "$a" >> "RECORD/schema"; echo >> "RECORD/schema"; }; prev="$a"; done
cat <<'EOS'
`+okStream+`EOS`)
	for _, r := range []ports.ModelRequest{
		{System: "FIRST", Schema: []byte(`{"n":1}`)},
		{System: "SECOND", Schema: []byte(`{"n":2}`)},
		{System: "FIRST", Schema: []byte(`{"n":1}`)},
	} {
		if _, err := m.Complete(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(record, name)); return string(b) }
	if read("instructions") != "FIRST\nSECOND\nFIRST\n" || read("schema") != "{\"n\":1}\n{\"n\":2}\n{\"n\":1}\n" {
		t.Fatalf("instructions %q schema %q", read("instructions"), read("schema"))
	}
}

// A wrapper (like the Node `codex` launcher) whose child keeps the output open must not keep
// running, or keep Complete waiting, once Codex is told to stop.
func TestStoppingCodexKillsItsWholeProcessTree(t *testing.T) {
	// The stub starts a child that outlives it and records the child's pid. The pid file is
	// renamed into place, so it is never read half written.
	const spawn = `cat >/dev/null; sleep 30 & echo $! > "RECORD/pid.tmp"; mv "RECORD/pid.tmp" "RECORD/pid"; `
	for name, tc := range map[string]stopCase{
		"cancelled": {script: spawn + `wait`, timeout: time.Minute, cancel: true, want: "context canceled"},
		"timed out": {script: spawn + `wait`, timeout: 500 * time.Millisecond, timesOut: true, want: "did not finish"},
		"tool use": {script: spawn + `echo '{"type":"item.started","item":{"type":"command_execution","command":"cat ~/.ssh/id_rsa"}}'; wait`,
			timeout: time.Minute, want: "used a tool"},
	} {
		t.Run(name, func(t *testing.T) {
			// A timeout that fires before the stub has started its child (a slow start under
			// load) proves nothing about the child, so that run is repeated with twice the time.
			// Every run in which the child started must see it killed.
			for ; ; tc.timeout *= 2 {
				if pid, started := stopCodex(t, tc); started {
					requireExited(t, pid)
					return
				}
				if !tc.timesOut || tc.timeout >= 8*time.Second {
					t.Fatal("the stub never started its child")
				}
			}
		})
	}
}

type stopCase struct {
	script   string
	timeout  time.Duration
	cancel   bool // cancel the caller's context once the child is running
	timesOut bool // the call timeout is what stops Codex
	want     string
}

// stopCodex runs one call that is stopped while the stub's child is running, and returns the
// child's pid, or false when the call was stopped before the stub started its child.
func stopCodex(t *testing.T, tc stopCase) (pid int, started bool) {
	t.Helper()
	m, record := fakeCodex(t, tc.script)
	m.timeout = tc.timeout
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pidFile := filepath.Join(record, "pid")
	// seen receives when the child was first seen running; for "cancelled", that is when the
	// caller cancels.
	seen := make(chan time.Time, 1)
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		for ctx.Err() == nil {
			if _, err := os.Stat(pidFile); err == nil {
				seen <- time.Now()
				if tc.cancel {
					cancel()
				}
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	start := time.Now()
	_, err := m.Complete(ctx, ports.ModelRequest{Schema: []byte(`{}`)})
	returned := time.Now()
	cancel()
	<-watching
	if err == nil || !strings.Contains(err.Error(), tc.want) {
		t.Fatalf("want %q, got %v", tc.want, err)
	}
	raw, readErr := os.ReadFile(pidFile)
	if readErr != nil && tc.timesOut {
		return 0, false
	}
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if perr != nil {
		t.Fatalf("pid: %q", raw)
	}
	// What stopped Codex: the deadline, or the child starting (the stub reports the tool use
	// right after it, and "cancelled" cancels on seeing it).
	trigger := returned
	select {
	case at := <-seen:
		trigger = at
	default:
	}
	if tc.timesOut {
		trigger = start.Add(tc.timeout)
	}
	// The child sleeps 30s; waiting for it, or for the 2s wait delay, would exceed this.
	if took := returned.Sub(trigger); took > 1800*time.Millisecond {
		t.Fatalf("Complete waited %s for a process it should have killed", took)
	}
	return pid, true
}

func requireExited(t *testing.T, pid int) {
	t.Helper()
	for range 50 {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("child %d is still running", pid)
}
