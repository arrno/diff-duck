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

// fakeGo writes a stand-in go command. `go env -json` answers with gobin and gopath, as the real
// one does, and `go install` records what it was asked to install and puts the verification
// binary where Go would: gobin, or bin under gopath when gobin is empty. It returns the
// directory to put first on PATH, the file holding the install request, and the binary's path.
func fakeGo(t *testing.T, gobin, gopath string) (bin, request, installed string) {
	t.Helper()
	bin = t.TempDir()
	request = filepath.Join(t.TempDir(), "request")
	dir := gobin
	if dir == "" {
		dir = filepath.Join(gopath, "bin")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	installed = filepath.Join(dir, "diffduck")
	env, _ := json.Marshal(map[string]string{"GOBIN": gobin, "GOPATH": gopath})
	// Like go install, replace the file rather than write into it: the app may be running it.
	script := "#!/bin/sh\ncase \"$1 $2\" in\n" +
		"'env -json') cat <<'EOF'\n" + string(env) + "\nEOF\n;;\n" +
		"install*) echo \"$2\" > '" + request + "'; cp '" + binary + "' '" + installed + ".new' && mv '" + installed + ".new' '" + installed + "' ;;\n" +
		"*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, request, installed
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
	// GOBIN is usually unset, so Go installs under GOPATH.
	bin, request, installed := fakeGo(t, "", t.TempDir())
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if url := appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0")); url == "" {
		t.Fatal("up printed no address")
	}
	c.must("settings", "set", "--reading-level", "phd")

	code, stdout, stderr := c.run("update")
	if code != 0 {
		t.Fatalf("update exited %d: %s%s", code, stdout, stderr)
	}
	if got, _ := os.ReadFile(request); strings.TrimSpace(string(got)) != "github.com/arrno/diffduck/cmd/diffduck@latest" {
		t.Fatalf("installed %q", got)
	}
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
	if got, _ := os.ReadFile(request); strings.TrimSpace(string(got)) != "github.com/arrno/diffduck/cmd/diffduck@v1.2.3" {
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

	// A GOBIN, when set, is where Go installs.
	gobin := t.TempDir()
	bin, _, installed = fakeGo(t, gobin, t.TempDir())
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if out := c.must("update"); !strings.Contains(out, filepath.Join(gobin, "diffduck")) {
		t.Fatalf("update with GOBIN set: %s", out)
	}

	// Without Go there is nothing to install with.
	c.env = []string{"PATH=" + t.TempDir()}
	if code, _, stderr := c.run("update"); code != 1 || !strings.Contains(stderr, "no go command") {
		t.Fatalf("update without go: exit %d: %s", code, stderr)
	}
}
