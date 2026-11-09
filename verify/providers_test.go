package verify

import (
	"database/sql"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"

	_ "modernc.org/sqlite" // Lets a test hold the app's database open.
)

// One answer that satisfies both the summaries and the outcomes schema, so a stand-in CLI
// needs no knowledge of which request it got.
const stubAnswer = `{"summaries":[],"overview":"Written by NAME.","outcomes":[]}`

// stubProviders writes stand-in `claude` and `codex` binaries that speak each CLI's output
// protocol, record that they ran, and touch nothing else: no network, no real login. It
// returns the directory to put first on PATH and the directory holding the run records.
func stubProviders(t *testing.T) (bin, ran string) {
	t.Helper()
	bin, ran = t.TempDir(), t.TempDir()
	claude := strings.ReplaceAll(stubAnswer, "NAME", "claude")
	codex := strings.ReplaceAll(strings.ReplaceAll(stubAnswer, "NAME", "codex"), `"`, `\"`)
	scripts := map[string]string{
		"claude": `{"type":"result","is_error":false,"stop_reason":"end_turn","structured_output":` + claude +
			`,"usage":{"input_tokens":1,"output_tokens":1}}`,
		"codex": `{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"` + codex + `"}}` + "\n" +
			`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`,
	}
	for name, output := range scripts {
		script := "#!/bin/sh\ncat >/dev/null\ntouch '" + filepath.Join(ran, name) + "'\ncat <<'EOS'\n" + output + "\nEOS\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin, ran
}

type settingsView struct {
	Provider       string `json:"provider"`
	ReadingLevel   string `json:"readingLevel"`
	Pinned         string `json:"pinned"`
	PinnedProvider string `json:"pinnedProvider"`
}

var diffID = regexp.MustCompile(`#/diffs/([A-Za-z0-9]+)`)

// runDiff creates a diff of the fixture through the CLI, waits for it, and returns what the
// app saved for it.
func runDiff(t *testing.T, c *appCLI, url string) (domain.Generation, string) {
	t.Helper()
	out := c.must("diff", ".", "-f", "base", "-t", "head", "--no-open", "--wait")
	id := diffID.FindStringSubmatch(out)
	if id == nil {
		t.Fatalf("no diff address: %q", out)
	}
	var saved struct {
		Status string `json:"status"`
		Result struct {
			Overview   string            `json:"overview"`
			Generation domain.Generation `json:"generation"`
		} `json:"result"`
	}
	appJSON(t, "GET", url+"api/diffs/"+id[1], "", &saved)
	if saved.Status != "done" {
		t.Fatalf("diff status %q", saved.Status)
	}
	return saved.Result.Generation, saved.Result.Overview
}

// B3 on the real, unpinned app: the provider saved in the settings is the one the next diff
// runs, and switching it switches the next diff.
func TestB3UnpinnedAppRunsTheSavedProvider(t *testing.T) {
	bin, ran := stubProviders(t)
	c := newAppCLI(t)
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	url := appURL.FindString(c.must("up", "--no-cache", "--port", "0"))
	if url == "" {
		t.Fatal("up printed no address")
	}
	var settings settingsView
	appJSON(t, "GET", url+"api/settings", "", &settings)
	if settings.Pinned != "" || settings.PinnedProvider != "" {
		t.Fatalf("started without --adapter, the app must not be pinned: %+v", settings)
	}

	for _, step := range []struct{ provider, binary string }{{"codex", "codex"}, {"claude-code", "claude"}} {
		appJSON(t, "PUT", url+"api/settings", `{"provider":"`+step.provider+`","readingLevel":"college"}`, &settings)
		if settings.Provider != step.provider || settings.Pinned != "" {
			t.Fatalf("save %s: %+v", step.provider, settings)
		}
		_ = os.Remove(filepath.Join(ran, "claude"))
		_ = os.Remove(filepath.Join(ran, "codex"))

		g, overview := runDiff(t, c, url)
		if string(g.Provider) != step.provider || g.ReadingLevel != domain.ReadingCollege {
			t.Fatalf("the diff after saving %s recorded %+v", step.provider, g)
		}
		if overview != "Written by "+step.binary+"." {
			t.Fatalf("the diff after saving %s was written by someone else: %q", step.provider, overview)
		}
		for _, name := range []string{"claude", "codex"} {
			_, err := os.Stat(filepath.Join(ran, name))
			if ranIt := err == nil; ranIt != (name == step.binary) {
				t.Fatalf("after saving %s, %s ran: %v", step.provider, name, ranIt)
			}
		}
	}
}

// A reset that cannot finish says so, and still brings back the app it stopped.
func TestFailedResetRestartsTheApp(t *testing.T) {
	c := newAppCLI(t)
	db := filepath.Join(t.TempDir(), "diffs.db")
	url := appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0", "--db", db))
	if url == "" {
		t.Fatal("up printed no address")
	}
	runDiff(t, c, url)

	// Another process reading the database keeps the deleted diffs' log on disk.
	other, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM diffs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("reading the database: %d %v", n, err)
	}

	code, stdout, stderr := c.run("reset", "--yes")
	if code == 0 || !strings.Contains(stderr, "another process has the database open") {
		t.Fatalf("a blocked reset must fail visibly: %d %q", code, stderr)
	}
	if !strings.Contains(stdout, "running again") {
		t.Fatalf("a failed reset must restart the app: %q", stdout)
	}
	if code, out, _ := c.run("status"); code != 0 || !strings.Contains(out, "running") {
		t.Fatalf("the app must be running after a failed reset: %d %q", code, out)
	}
}

// B7: a pin to a real provider is reported as that provider, while the saved provider stays
// what the person chose. A pin to a verification adapter names no provider.
func TestB7PinnedAdapterReportsItsProvider(t *testing.T) {
	c := newAppCLI(t)
	// The adapter is built once at start-up, which makes no request; no diff runs here.
	c.env = []string{"OPENAI_API_KEY=sk-test-not-a-real-key"}
	url := appURL.FindString(c.must("up", "--adapter", "openai-api", "--no-cache", "--port", "0"))
	if url == "" {
		t.Fatal("up printed no address")
	}
	var settings settingsView
	appJSON(t, "PUT", url+"api/settings", `{"provider":"claude-code","readingLevel":"phd"}`, &settings)
	if settings.Pinned != "openai-api" || settings.PinnedProvider != "openai-api" || settings.Provider != "claude-code" {
		t.Fatalf("pinned to openai-api: %+v", settings)
	}
	c.must("down")

	url = appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0"))
	appJSON(t, "GET", url+"api/settings", "", &settings)
	if settings.Pinned != "scripted:normal" || settings.PinnedProvider != "" {
		t.Fatalf("pinned to a verification adapter: %+v", settings)
	}
}
