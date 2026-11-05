package verify

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// appCLI runs the app commands against their own home directory, so they never touch the
// user's saved diffs or a server the user is running.
type appCLI struct {
	t    *testing.T
	home string
}

func newAppCLI(t *testing.T) *appCLI {
	c := &appCLI{t: t, home: t.TempDir()}
	t.Cleanup(func() { c.run("down") })
	return c
}

func (c *appCLI) run(args ...string) (int, string, string) {
	c.t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = fixture
	cmd.Env = append(os.Environ(), "DIFFDUCK_HOME="+c.home)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		c.t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func (c *appCLI) must(args ...string) string {
	c.t.Helper()
	code, stdout, stderr := c.run(args...)
	if code != 0 {
		c.t.Fatalf("diffduck %s exited %d: %s%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout
}

type listed struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Files  int    `json:"files"`
}

func (c *appCLI) list() []listed {
	c.t.Helper()
	var diffs []listed
	if err := json.Unmarshal([]byte(c.must("diff", "list", "--json")), &diffs); err != nil {
		c.t.Fatal(err)
	}
	return diffs
}

var appURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/`)

func TestAppCommandsLifecycle(t *testing.T) {
	c := newAppCLI(t)
	if code, out, _ := c.run("status"); code == 0 || !strings.Contains(out, "not running") {
		t.Fatalf("status before up: %d %q", code, out)
	}
	up := c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0")
	url := appURL.FindString(up)
	if url == "" {
		t.Fatalf("up printed no address: %q", up)
	}
	if again := c.must("up"); !strings.Contains(again, "already running at "+url) {
		t.Fatalf("a second up must reuse the running app: %q", again)
	}

	// A relative repository means the caller's directory, which here is the fixture.
	out := c.must("diff", ".", "-f", "base", "-t", "head", "--no-open", "--wait")
	if !strings.Contains(out, url+"#/diffs/") || !strings.Contains(out, "Done: ") {
		t.Fatalf("diff --wait: %q", out)
	}
	diffs := c.list()
	if len(diffs) != 1 || diffs[0].Status != "done" || diffs[0].Files == 0 {
		t.Fatalf("after one diff: %+v", diffs)
	}

	if code, _, stderr := c.run("diff", "-f", "no-such-commit", "--no-open", "--wait"); code == 0 || stderr == "" {
		t.Fatalf("a diff of a missing commit must fail visibly: %d %q", code, stderr)
	}
	if code, _, _ := c.run("diff", "delete", diffs[0].ID, "no-such-id"); code == 0 {
		t.Fatal("deleting a missing diff must fail")
	}
	if remaining := c.list(); len(remaining) != 1 || remaining[0].ID == diffs[0].ID {
		t.Fatalf("delete removed the wrong diffs: %+v", remaining)
	}

	if code, _, _ := c.run("reset"); code != 2 {
		t.Fatalf("reset without --yes and without an answer must refuse, got %d", code)
	}
	if len(c.list()) == 0 {
		t.Fatal("a refused reset deleted diffs")
	}
	reset := c.must("reset", "--yes")
	if !strings.Contains(reset, "running again") {
		t.Fatalf("reset must restart a running app: %q", reset)
	}
	if left := c.list(); len(left) != 0 {
		t.Fatalf("reset left diffs: %+v", left)
	}

	if out := c.must("down"); !strings.Contains(out, "stopped") {
		t.Fatalf("down: %q", out)
	}
	if code, _, _ := c.run("status"); code == 0 {
		t.Fatal("status after down must report not running")
	}
	if _, err := os.Stat(filepath.Join(c.home, "server.json")); !os.IsNotExist(err) {
		t.Fatalf("down left the server record: %v", err)
	}
}

func TestStaleServerRecordIsIgnored(t *testing.T) {
	c := newAppCLI(t)
	// A record left by a server that crashed, naming a port nothing answers on.
	stale := `{"pid":999999,"port":1,"db":"/nowhere"}`
	if err := os.WriteFile(filepath.Join(c.home, "server.json"), []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := c.run("status"); code == 0 || !strings.Contains(out, "not running") {
		t.Fatalf("status with a stale record: %d %q", code, out)
	}
	if out := c.must("down"); !strings.Contains(out, "not running") {
		t.Fatalf("down with a stale record must not signal anything: %q", out)
	}
}
