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
	const spawn = `cat >/dev/null; sleep 30 & echo $! > "RECORD/pid"; `
	for name, tc := range map[string]struct {
		script  string
		timeout time.Duration
		cancel  bool // cancel the caller's context once the child is running
		want    string
	}{
		"cancelled": {spawn + `wait`, time.Minute, true, "context canceled"},
		"timed out": {spawn + `wait`, 500 * time.Millisecond, false, "did not finish"},
		"tool use": {spawn + `echo '{"type":"item.started","item":{"type":"command_execution","command":"cat ~/.ssh/id_rsa"}}'; wait`,
			time.Minute, false, "used a tool"},
	} {
		t.Run(name, func(t *testing.T) {
			m, record := fakeCodex(t, tc.script)
			m.timeout = tc.timeout
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			pidFile := filepath.Join(record, "pid")
			if tc.cancel {
				go func() {
					for ctx.Err() == nil {
						if b, _ := os.ReadFile(pidFile); len(b) > 0 {
							cancel()
							return
						}
						time.Sleep(10 * time.Millisecond)
					}
				}()
			}
			start := time.Now()
			_, err := m.Complete(ctx, ports.ModelRequest{Schema: []byte(`{}`)})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			// The child sleeps 30s; waiting for it, or for the 2s wait delay, would exceed this.
			if took := time.Since(start); took > 1800*time.Millisecond {
				t.Fatalf("Complete waited %s for a process it should have killed", took)
			}
			raw, _ := os.ReadFile(pidFile)
			pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if perr != nil {
				t.Fatalf("pid: %q", raw)
			}
			for range 50 {
				if syscall.Kill(pid, 0) != nil {
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
			t.Fatalf("child %d is still running", pid)
		})
	}
}
