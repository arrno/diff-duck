package verify

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

type cliProvider struct {
	ID       string `json:"id"`
	KeySaved bool   `json:"keySaved"`
}

type cliSettings struct {
	Provider     string        `json:"provider"`
	ReadingLevel string        `json:"readingLevel"`
	Pinned       string        `json:"pinned"`
	Providers    []cliProvider `json:"providers"`
}

func (s cliSettings) keySaved(id string) bool {
	for _, p := range s.Providers {
		if p.ID == id {
			return p.KeySaved
		}
	}
	return false
}

func settingsJSON(t *testing.T, out string) cliSettings {
	t.Helper()
	var s cliSettings
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("settings output is not JSON: %v: %q", err, out)
	}
	return s
}

// An agent can read and change every model setting from a shell, and a key given that way is
// never repeated by any command.
func TestSettingsCommands(t *testing.T) {
	const key = "sk-test-not-a-real-key"
	c := newAppCLI(t)
	bin, _ := stubProviders(t)
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if url := appURL.FindString(c.must("up", "--no-cache", "--port", "0")); url == "" {
		t.Fatal("up printed no address")
	}

	s := settingsJSON(t, c.must("settings", "--json"))
	if s.Provider != "claude-code" || s.ReadingLevel != "6th-grade" {
		t.Fatalf("defaults: %+v", s)
	}

	// A flag left out keeps its saved value.
	s = settingsJSON(t, c.must("settings", "set", "--reading-level", "college", "--json"))
	if s.Provider != "claude-code" || s.ReadingLevel != "college" {
		t.Fatalf("reading level only: %+v", s)
	}
	s = settingsJSON(t, c.must("settings", "set", "--provider", "codex", "--json"))
	if s.Provider != "codex" || s.ReadingLevel != "college" {
		t.Fatalf("provider only: %+v", s)
	}

	// Refusals change nothing.
	refused := []struct {
		name  string
		stdin string
		want  int
		args  []string
	}{
		{"nothing to change", "", 2, []string{"set"}},
		{"key for no named provider", key, 2, []string{"set", "--api-key-stdin"}},
		{"no key on the pipe", "  \n", 2, []string{"set", "--provider", "openai-api", "--api-key-stdin"}},
		{"key on the command line", "", 2, []string{"set", "--provider", "openai-api", "--api-key=" + key}},
		{"unknown provider", "", 1, []string{"set", "--provider", "gemini"}},
		{"unknown level", "", 1, []string{"set", "--reading-level", "genius"}},
		{"API provider without a key", "", 1, []string{"set", "--provider", "anthropic-api"}},
		{"key for a provider that takes none", key, 1, []string{"set", "--provider", "codex", "--api-key-stdin"}},
		{"clearing the key of a CLI provider", "", 1, []string{"clear-key", "codex"}},
		{"clearing no provider", "", 2, []string{"clear-key"}},
	}
	for _, r := range refused {
		c.stdin = r.stdin
		code, stdout, stderr := c.run(append([]string{"settings"}, r.args...)...)
		if code != r.want {
			t.Errorf("%s: exit %d, want %d: %s", r.name, code, r.want, stderr)
		}
		if strings.Contains(stdout+stderr, key) {
			t.Errorf("%s: printed the key: %s%s", r.name, stdout, stderr)
		}
	}
	if s = settingsJSON(t, c.must("settings", "--json")); s.Provider != "codex" || s.keySaved("openai-api") {
		t.Fatalf("a refused command changed the settings: %+v", s)
	}

	// A key on the pipe is saved for the provider, surrounding whitespace removed.
	c.stdin = key + "\n"
	code, stdout, stderr := c.run("settings", "set", "--provider", "openai-api", "--api-key-stdin", "--json")
	if code != 0 || strings.Contains(stdout+stderr, key) {
		t.Fatalf("saving a key: exit %d: %s%s", code, stdout, stderr)
	}
	if s = settingsJSON(t, stdout); s.Provider != "openai-api" || !s.keySaved("openai-api") {
		t.Fatalf("after saving a key: %+v", s)
	}

	text := c.must("settings")
	for _, want := range []string{"OpenAI API (openai-api)", "College (college)", "saved", "not needed"} {
		if !strings.Contains(text, want) {
			t.Errorf("settings text lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, key) {
		t.Errorf("settings printed the key: %s", text)
	}

	// Clearing the selected provider's key warns that new diffs will fail.
	code, stdout, stderr = c.run("settings", "clear-key", "openai-api", "--json")
	if code != 0 || !strings.Contains(stderr, "still selected") {
		t.Fatalf("clear-key: exit %d: %s", code, stderr)
	}
	if s = settingsJSON(t, stdout); s.Provider != "openai-api" || s.keySaved("openai-api") {
		t.Fatalf("after clearing: %+v", s)
	}
}

// A pinned app says so when the provider is changed, and a finished diff can be read in full
// from a shell.
func TestPinnedWarningAndDiffShow(t *testing.T) {
	c := newAppCLI(t)
	bin, _ := stubProviders(t)
	c.env = []string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}
	if url := appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0")); url == "" {
		t.Fatal("up printed no address")
	}

	code, stdout, stderr := c.run("settings", "set", "--provider", "codex", "--json")
	if code != 0 || !strings.Contains(stderr, "scripted:normal") {
		t.Fatalf("a changed provider on a pinned app must warn: exit %d: %s", code, stderr)
	}
	if s := settingsJSON(t, stdout); s.Provider != "codex" || s.Pinned != "scripted:normal" {
		t.Fatalf("pinned: %+v", s)
	}
	if _, _, stderr = c.run("settings", "set", "--reading-level", "phd"); strings.Contains(stderr, "warning") {
		t.Fatalf("the reading level is not affected by a pin: %s", stderr)
	}
	if text := c.must("settings"); !strings.Contains(text, "Pinned: scripted:normal") {
		t.Fatalf("settings must report the pin: %s", text)
	}

	out := c.must("diff", ".", "-f", "base", "-t", "head", "--no-open", "--wait")
	match := diffID.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no diff address: %q", out)
	}
	id := match[1]

	// Flags may come after the ID.
	var saved domain.SavedDiff
	if err := json.Unmarshal([]byte(c.must("diff", "show", id, "--json")), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID != id || saved.Status != domain.SavedDiffDone || saved.Result == nil || len(saved.Result.Nodes) == 0 {
		t.Fatalf("diff show --json: %+v", saved)
	}

	text := c.must("diff", "show", id)
	for _, want := range []string{"Diff " + id, "Status: done", "Coverage:", "Changes:", saved.Result.Nodes[0].Name} {
		if !strings.Contains(text, want) {
			t.Errorf("diff show lacks %q:\n%s", want, text)
		}
	}
	if saved.Result.Overview != "" && !strings.Contains(text, saved.Result.Overview) {
		t.Errorf("diff show lacks the overview:\n%s", text)
	}
	for _, o := range saved.Result.Outcomes {
		if !strings.Contains(text, o.Title) {
			t.Errorf("diff show lacks outcome %q:\n%s", o.Title, text)
		}
	}

	if code, _, _ := c.run("diff", "show", "no-such-id"); code != 1 {
		t.Fatalf("a missing diff must fail, got %d", code)
	}
	if code, _, _ := c.run("diff", "show"); code != 2 {
		t.Fatalf("no ID is a usage error, got %d", code)
	}
}
