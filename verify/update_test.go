package verify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeGo writes a stand-in go command that answers `go env` with gobin and, on `go install`,
// records what it was asked to install and copies the verification binary into gobin. It
// returns the directory to put first on PATH and the file holding the install request.
func fakeGo(t *testing.T, gobin string) (bin, request string) {
	t.Helper()
	bin = t.TempDir()
	request = filepath.Join(t.TempDir(), "request")
	// Like go install, replace the file rather than write into it: the app may be running it.
	target := filepath.Join(gobin, "diffduck")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"env) echo '" + gobin + "'; echo /nonexistent ;;\n" +
		"install) echo \"$2\" > '" + request + "'; cp '" + binary + "' '" + target + ".new' && mv '" + target + ".new' '" + target + "' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, request
}

func TestVersion(t *testing.T) {
	c := newAppCLI(t)
	for _, args := range [][]string{{"version"}, {"--version"}} {
		out := c.must(args...)
		if !strings.HasPrefix(out, "diffduck v") && out != "diffduck devel\n" {
			t.Fatalf("%v: %q", args, out)
		}
	}
}

// update installs through go install and brings a running app back on the new binary, with
// its saved settings.
func TestUpdate(t *testing.T) {
	c := newAppCLI(t)
	gobin := t.TempDir()
	bin, request := fakeGo(t, gobin)
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if url := appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0")); url == "" {
		t.Fatal("up printed no address")
	}
	c.must("settings", "set", "--reading-level", "phd")

	code, stdout, stderr := c.run("update")
	if code != 0 {
		t.Fatalf("update exited %d: %s%s", code, stdout, stderr)
	}
	if got, _ := os.ReadFile(request); strings.TrimSpace(string(got)) != "github.com/arrno/diff-duck/cmd/diffduck@latest" {
		t.Fatalf("installed %q", got)
	}
	installed := filepath.Join(gobin, "diffduck")
	if !strings.Contains(stdout, installed) || !strings.Contains(stdout, "running again") {
		t.Fatalf("update output: %s", stdout)
	}
	// The diffduck on this PATH is the verification binary, not the one just installed.
	if !strings.Contains(stderr, "warning: diffduck on PATH") && !strings.Contains(stderr, "is not on PATH") {
		t.Fatalf("update must warn that the installed binary is not the one on PATH: %s", stderr)
	}

	// The app now runs the installed binary, with its flags and settings.
	var record struct {
		PID  int      `json:"pid"`
		Args []string `json:"args"`
	}
	data, err := os.ReadFile(filepath.Join(c.home, "server.json"))
	if err != nil || json.Unmarshal(data, &record) != nil {
		t.Fatalf("server record: %v %s", err, data)
	}
	ps, err := exec.Command("ps", "-o", "command=", "-p", strconv.Itoa(record.PID)).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(ps)), installed+" serve") {
		t.Fatalf("the app must run the installed binary, runs %q (%v)", ps, err)
	}
	if !strings.Contains(strings.Join(record.Args, " "), "scripted:normal") {
		t.Fatalf("the app must keep its flags: %v", record.Args)
	}
	if s := settingsJSON(t, c.must("settings", "--json")); s.ReadingLevel != "phd" || s.Pinned != "scripted:normal" {
		t.Fatalf("settings after update: %+v", s)
	}

	c.must("update", "v1.2.3")
	if got, _ := os.ReadFile(request); strings.TrimSpace(string(got)) != "github.com/arrno/diff-duck/cmd/diffduck@v1.2.3" {
		t.Fatalf("installed %q", got)
	}

	os.Remove(request)
	for _, bad := range []string{"latest;rm", "-x", "../v1", "v1", "a b"} {
		if code, _, _ := c.run("update", bad); code != 2 {
			t.Errorf("update %q: exit %d, want 2", bad, code)
		}
	}
	if _, err := os.Stat(request); err == nil {
		t.Fatal("a refused update ran go install")
	}

	// Without Go there is nothing to install with.
	c.env = []string{"PATH=" + t.TempDir()}
	if code, _, stderr := c.run("update"); code != 1 || !strings.Contains(stderr, "no go command") {
		t.Fatalf("update without go: exit %d: %s", code, stderr)
	}
}
