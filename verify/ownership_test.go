package verify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arrno/duck-diff/internal/domain"
)

func changedText(n *domain.Node) string {
	var b strings.Builder
	for _, d := range n.Diff {
		for _, l := range d.Lines {
			if l.IsChange() {
				b.WriteString(l.Text + "\n")
			}
		}
	}
	return b.String()
}

// Coverage proves a line is traced somewhere. These prove it is owned by the right unit.
func TestEachLineBelongsToTheInnermostChangedUnit(t *testing.T) {
	r := normal(t)
	for name, want := range map[string]string{
		"Service.Create":   "ValidateDOB(u.DateOfBirth)",
		"User.DateOfBirth": `json:"dob"`,
		"Store.Delete":     "Delete(id string) error",
		"ValidateDOB":      "future date",
		"Cache.Get":        "var zero T",
	} {
		if text := changedText(r.byName[name]); !strings.Contains(text, want) {
			t.Errorf("%s should own %q; owns:\n%s", name, want, text)
		}
	}
	// The field's line is the field's, and not also its struct's.
	if strings.Contains(changedText(r.byName["User"]), "DateOfBirth time.Time") {
		t.Error("an enclosing struct must not also own its field's line")
	}
	if !strings.Contains(changedText(r.byName["User"]), "now with a birth date") {
		t.Errorf("the struct owns its own doc comment: %q", changedText(r.byName["User"]))
	}
	// The method's first and last lines are inside its range.
	create := changedText(r.byName["Service.Create"])
	if !strings.Contains(create, "after validating them") {
		t.Errorf("a doc comment belongs to its function: %q", create)
	}
	if strings.Contains(changedText(r.byName["user.go"]), "ValidateDOB") {
		t.Error("code lines must not fall into the region")
	}
}

func TestFirstAndLastLinesOfAUnitAreItsOwn(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "package p\n\nfunc F() int {\n\tx := 1\n\treturn x\n}\n\nfunc G() {}\n"},
		map[string]string{"p/a.go": "package p\n\nfunc F(a int) int {\n\tx := 1\n\treturn x\n\t}\n\nfunc G() { println() }\n"},
	))
	if text := changedText(r.byName["F"]); !strings.Contains(text, "func F(a int)") || !strings.Contains(text, "\t}") {
		t.Errorf("F owns its first and last lines: %q", text)
	}
	if r.byName["a.go"] != nil {
		t.Errorf("nothing belongs in the region: %q", changedText(r.byName["a.go"]))
	}
}

func TestRenameRequiresAUniqueMatchOfTheSameKind(t *testing.T) {
	body := "(a int) int {\n\tb := a\n\treturn b\n}\n"
	t.Run("two candidates stay remove plus add", func(t *testing.T) {
		r := structure(t, repo(t,
			map[string]string{"p/a.go": "package p\n\nfunc Old1" + body + "\nfunc Old2" + body},
			map[string]string{"p/a.go": "package p\n\nfunc New1" + body},
		))
		if n := r.byName["New1"]; n == nil || n.Op != domain.OpAdd || n.PreviousName != "" {
			t.Fatalf("an ambiguous match must not invent continuity: %+v", n)
		}
		if r.byName["Old1"].Op != domain.OpRemove || r.byName["Old2"].Op != domain.OpRemove {
			t.Fatal("both removals stay")
		}
	})
	t.Run("different kinds never match", func(t *testing.T) {
		r := structure(t, repo(t,
			map[string]string{"p/a.go": "package p\n\ntype T struct{}\n\nfunc F" + body},
			map[string]string{"p/a.go": "package p\n\ntype T struct{}\n\nfunc (t T) M" + body},
		))
		if n := r.byName["T.M"]; n == nil || n.Op != domain.OpAdd {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("trivial bodies never match", func(t *testing.T) {
		r := structure(t, repo(t,
			map[string]string{"p/a.go": "package p\n\nfunc Old() error { return nil }\n"},
			map[string]string{"p/a.go": "package p\n\nfunc New() error { return nil }\n"},
		))
		if n := r.byName["New"]; n == nil || n.Op != domain.OpAdd {
			t.Fatalf("%+v", n)
		}
	})
	t.Run("a receiver change keeps the previous signature", func(t *testing.T) {
		r := structure(t, repo(t,
			map[string]string{"p/a.go": "package p\n\ntype T struct{}\n\nfunc (t T) M" + body},
			map[string]string{"p/a.go": "package p\n\ntype T struct{}\n\nfunc (t *T) N" + body},
		))
		n := r.byName["T.N"]
		if n == nil || n.PreviousName != "T.M" || !strings.Contains(n.PreviousSignature, "(t T) M") {
			t.Fatalf("%+v", n)
		}
	})
}

func TestDirectlyChangedParentKeepsDirectFlag(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "package p\n\ntype S[K any] struct {\n\tA K\n}\n"},
		map[string]string{"p/a.go": "package p\n\ntype S[K comparable] struct {\n\tA K\n\tB K\n}\n"},
	))
	s := r.byName["S"]
	if s == nil || !s.Direct || !strings.Contains(s.PreviousSignature, "any") {
		t.Fatalf("%+v", s)
	}
	if !slicesContains(r.diff.Unassigned, s.ID) {
		t.Errorf("a direct change in no outcome must be listed: %v", r.diff.Unassigned)
	}
}

func TestBuildTaggedDuplicatesEditInPlace(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{
			"p/a_linux.go":  "//go:build linux\n\npackage p\n\nfunc F() int { return 1 }\n",
			"p/a_darwin.go": "//go:build darwin\n\npackage p\n\nfunc F() int { return 2 }\n",
		},
		map[string]string{
			"p/a_linux.go":  "//go:build linux\n\npackage p\n\nfunc F() int { return 3 }\n",
			"p/a_darwin.go": "//go:build darwin\n\npackage p\n\nfunc F() int { return 2 }\n",
		},
	))
	f := allNamed(r, "F")
	if len(f) != 1 || f[0].Op != domain.OpChange || f[0].Locations[0].Path != "p/a_linux.go" {
		t.Fatalf("%v", opsOf(f))
	}
}

func TestAdvancedGitConfigCannotRunCommands(t *testing.T) {
	s := repo(t,
		map[string]string{".gitattributes": "*.go diff=evil\n", "p/a.go": "package p\n\nfunc F() int { return 1 }\n"},
		map[string]string{"p/a.go": "package p\n\nfunc F() int { return 2 }\n"},
	)
	marker := filepath.Join(t.TempDir(), "ran")
	script := filepath.Join(t.TempDir(), "evil.sh")
	must(os.WriteFile(script, []byte("#!/bin/sh\ntouch "+marker+"\ncat \"$1\"\n"), 0o755))
	s.git("config", "diff.external", script)
	s.git("config", "diff.evil.textconv", script)
	s.git("config", "diff.evil.command", script)
	s.git("config", "core.fsmonitor", script)
	r := structure(t, s)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository configuration ran a command")
	}
	if f := r.byName["F"]; f == nil || f.Added != 1 {
		t.Fatalf("%+v", f)
	}
}

// fakeClaudeOnPath runs the production adapter against a recording stand-in.
func fakeClaude(t *testing.T) (env []string, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "` + argsFile + `"
cat > /dev/null
echo '{"is_error":false,"stop_reason":"end_turn","structured_output":{"summaries":[],"overview":"from fake claude","outcomes":[]},"usage":{}}'
`
	must(os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755))
	return []string{"PATH=" + dir + ":" + os.Getenv("PATH")}, argsFile
}

func withEnv(t *testing.T, env []string, args ...string) (*domain.CompressedDiff, int, string) {
	t.Helper()
	out := t.TempDir()
	cmd := exec.Command(binary, append([]string{"--out", out}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	combined, err := cmd.CombinedOutput()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var diff domain.CompressedDiff
	if data, err := os.ReadFile(filepath.Join(out, "compressed-diff.json")); err == nil {
		must(json.Unmarshal(data, &diff))
	}
	return &diff, code, string(combined)
}

func TestClaudeCLIAdapterIsTheDefaultAndReceivesItsConfiguration(t *testing.T) {
	env, argsFile := fakeClaude(t)
	diff, code, out := withEnv(t, env, "--no-cache", "--effort", "low", fixture, "base", "head")
	if code != 0 || diff.Overview != "from fake claude" {
		t.Fatalf("%d %s %+v", code, out, diff.Overview)
	}
	data, _ := os.ReadFile(argsFile)
	args := string(data)
	for _, want := range []string{"claude-sonnet-5-5", "--effort\nlow", "--json-schema", "--system-prompt", "You explain code changes", "--tools"} {
		if !strings.Contains(args, want) {
			t.Errorf("missing %q", want)
		}
	}
	diff, _, _ = withEnv(t, env, "--no-cache", "--model", "claude-haiku-5-5", fixture, "base", "head")
	if !strings.Contains(diff.Generation.Model, "claude-haiku-5-5") {
		t.Errorf("--model ignored: %s", diff.Generation.Model)
	}
}

func TestAnthropicAPIAdapterIsSelectableByFlag(t *testing.T) {
	var model string
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &req)
		model = req.Model
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"m","type":"message","role":"assistant","model":"x","stop_reason":"end_turn","content":[{"type":"text","text":"{\"summaries\":[],\"overview\":\"from api\",\"outcomes\":[]}"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer srv.Close()
	env := []string{"ANTHROPIC_BASE_URL=" + srv.URL, "ANTHROPIC_API_KEY=test"}
	diff, code, out := withEnv(t, env, "--adapter", "anthropic-api", "--no-cache", "--model", "claude-haiku-5-5", fixture, "base", "head")
	if code != 0 || diff.Overview != "from api" || calls == 0 || model != "claude-haiku-5-5" {
		t.Fatalf("%d %s overview=%q calls=%d model=%q", code, out, diff.Overview, calls, model)
	}
}

func TestNoCacheWritesNothing(t *testing.T) {
	dir := t.TempDir()
	execute(t, "--adapter", "scripted:normal", "--no-cache", "--cache-dir", dir, fixture, "base", "head")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%d entries", len(entries))
	}
}

// A real clone, redirected to a local source by git's own URL rewriting.
func TestRemoteTargetsAreClonedIncludingBranches(t *testing.T) {
	s := repo(t, map[string]string{"p/a.go": "package p\n\nfunc F() int { return 1 }\n"}, nil)
	// A tag named "head" collides with HEAD on case-insensitive file systems.
	s.git("tag", "-d", "head")
	s.git("checkout", "-q", "-b", "feature")
	s.write(map[string]string{"p/a.go": "package p\n\nfunc F() int { return 2 }\n"})
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "feature")
	s.git("checkout", "-q", "main")

	global := filepath.Join(t.TempDir(), "gitconfig")
	config := "[url \"" + s.dir + "\"]\n\tinsteadOf = https://example.invalid/repo\n\tinsteadOf = https://github.com/acme/repo\n"
	must(os.WriteFile(global, []byte(config), 0o644))
	env := []string{"GIT_CONFIG_GLOBAL=" + global}

	for _, target := range []string{"https://example.invalid/repo", "github.com/acme/repo"} {
		diff, code, out := withEnv(t, env, "--structure-only", target, "main", "feature")
		if code != 0 || diff.Files != 1 || diff.Repository != target {
			t.Fatalf("%s: %d %s %+v", target, code, out, diff.Files)
		}
	}
	_, code, out := withEnv(t, env, "--structure-only", "https://example.invalid/none", "main", "feature")
	if code == 0 || !strings.Contains(out, "clone") {
		t.Fatalf("a failed clone must be reported: %d %s", code, out)
	}
}
