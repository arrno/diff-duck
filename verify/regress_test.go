package verify

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

// scenario is a throwaway repository with commits tagged base and head.
type scenario struct {
	t   *testing.T
	dir string
}

func (s *scenario) git(args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = s.dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func (s *scenario) write(files map[string]string) {
	s.t.Helper()
	for name, content := range files {
		p := filepath.Join(s.dir, name)
		if content == "" {
			os.Remove(p)
			continue
		}
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		must(os.WriteFile(p, []byte(content), 0o644))
	}
}

func repo(t *testing.T, base, head map[string]string) *scenario {
	t.Helper()
	s := &scenario{t: t, dir: t.TempDir()}
	s.git("init", "-q", "-b", "main")
	s.git("config", "user.name", "t")
	s.git("config", "user.email", "t@example.com")
	s.write(base)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "base", "--allow-empty")
	s.git("tag", "base")
	s.write(head)
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "head", "--allow-empty")
	s.git("tag", "head")
	return s
}

func structure(t *testing.T, s *scenario) *run {
	t.Helper()
	r := execute(t, "--structure-only", s.dir, "base", "head")
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
	}
	return r
}

// checkTotals compares the report with git's own count, using a clean copy of the question.
func checkTotals(t *testing.T, s *scenario, r *run) {
	t.Helper()
	added, removed := 0, 0
	numstat := s.git("-c", "core.attributesFile=/dev/null", "diff", "--numstat", "--text", "-M", "base", "head")
	for _, line := range strings.Split(numstat, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		added += a
		removed += d
	}
	if r.diff.Added != added || r.diff.Removed != removed {
		t.Errorf("tool +%d -%d, git +%d -%d", r.diff.Added, r.diff.Removed, added, removed)
	}
	if !r.diff.Coverage.Complete() || r.diff.Coverage.ChangedLines != added+removed {
		t.Errorf("coverage %+v, git says %d changed lines", r.diff.Coverage, added+removed)
	}
}

func allNamed(r *run, name string) []*domain.Node {
	var out []*domain.Node
	for _, root := range r.diff.Nodes {
		root.Walk(func(n *domain.Node) {
			if n.Name == name {
				out = append(out, n)
			}
		})
	}
	return out
}

func TestDuplicateNamesMatchByContentNotPosition(t *testing.T) {
	const pkg = "package p\n\n"
	r := structure(t, repo(t,
		map[string]string{"p/a.go": pkg + "func init() { println(1) }\n\nfunc init() { println(2) }\n"},
		map[string]string{"p/a.go": pkg + "func init() { println(0) }\n\nfunc init() { println(1) }\n\nfunc init() { println(2) }\n"},
	))
	inits := allNamed(r, "init")
	if len(inits) != 1 || inits[0].Op != domain.OpAdd || inits[0].Added != 1 {
		t.Fatalf("one init was added and two are unchanged; got %d nodes: %+v", len(inits), opsOf(inits))
	}
}

func opsOf(nodes []*domain.Node) []string {
	var out []string
	for _, n := range nodes {
		out = append(out, string(n.Op)+" "+n.ID)
	}
	return out
}

func TestPackageOperationComesFromExistence(t *testing.T) {
	const pkg = "package p\n\n"
	s := repo(t,
		map[string]string{
			"grow/a.go":   "package grow\n\nfunc Keep() {}\n",
			"shrink/a.go": "package shrink\n\nfunc Keep() {}\n",
			"shrink/b.go": "package shrink\n\nfunc Gone() {}\n",
			"dead/a.go":   "package dead\n\nfunc Gone() {}\n",
		},
		map[string]string{
			"grow/b.go":   "package grow\n\nfunc New() {}\n",
			"shrink/b.go": "",
			"dead/a.go":   "",
			"fresh/a.go":  "package fresh\n\nfunc New() {}\n",
		})
	_ = pkg
	r := structure(t, s)
	for name, want := range map[string]domain.ChangeOp{"grow": domain.OpChange, "shrink": domain.OpChange, "dead": domain.OpRemove, "fresh": domain.OpAdd} {
		if n := r.byName[name]; n == nil || n.Op != want {
			t.Errorf("package %s: %+v, want %s", name, n, want)
		}
	}
}

func TestMovesAcrossPackagesAreReportedAsMoves(t *testing.T) {
	body := "func F(a int) int {\n\tb := a + 1\n\treturn b * 2\n}\n"
	r := structure(t, repo(t,
		map[string]string{"a/a.go": "package a\n\n" + body + "\nfunc Stay() {}\n", "b/b.go": "package b\n\nfunc Other() {}\n"},
		map[string]string{"a/a.go": "package a\n\nfunc Stay() {}\n", "b/b.go": "package b\n\nfunc Other() {}\n\n" + body},
	))
	f := allNamed(r, "F")
	if len(f) != 1 || f[0].Op != domain.OpChange || !f[0].Moved || !strings.Contains(f[0].Summary.Text, "Moved") || strings.Contains(f[0].Summary.Text, "Renamed") {
		t.Fatalf("got %+v", f)
	}
	if !strings.Contains(f[0].PreviousName, "a") {
		t.Errorf("the previous identity must say where it came from: %q", f[0].PreviousName)
	}
	if strings.Contains(r.byName["a"].Summary.Text, "No code units changed") {
		t.Errorf("code left package a, so this is false: %q", r.byName["a"].Summary.Text)
	}
}

func TestDirectoryRenameKeepsEveryUnitsIdentity(t *testing.T) {
	src := "package old\n\nfunc One() int { return 1 }\n\nfunc Two(a int) int {\n\tb := a\n\treturn b\n}\n\ntype T struct {\n\tA int\n}\n"
	s := repo(t, map[string]string{"old/a.go": src}, nil)
	s.git("mv", "old", "nu")
	s.git("commit", "-q", "-m", "rename")
	s.git("tag", "-f", "head")
	r := structure(t, s)
	for _, root := range r.diff.Nodes {
		root.Walk(func(n *domain.Node) {
			if n.Op != domain.OpChange {
				t.Errorf("%s is %s; git reports this file as an exact rename", n.ID, n.Op)
			}
			if n.Kind == domain.KindFunction && !n.Moved {
				t.Errorf("%s should be a move", n.ID)
			}
		})
	}
	checkTotals(t, s, r)
}

func TestFieldOrderChangeIsNotCalledFormatting(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "package p\n\ntype S struct {\n\tA int8\n\tB int64\n}\n"},
		map[string]string{"p/a.go": "package p\n\ntype S struct {\n\tB int64\n\tA int8\n}\n"},
	))
	s := r.byName["S"]
	if s == nil || s.Op != domain.OpChange || !s.Direct || !strings.Contains(strings.ToLower(s.Summary.Text), "order") {
		t.Fatalf("reordered fields are a real change to the struct: %+v", s)
	}
}

func TestBuildConstraintsAndDirectivesAreNotCalledFormatting(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "//go:build linux\n\npackage p\n\nfunc F() {}\n\nfunc G() {}\n"},
		map[string]string{"p/a.go": "//go:build ignore\n\npackage p\n\nfunc F() {}\n\n//go:noinline\nfunc G() {}\n"},
	))
	region := r.byName["a.go"]
	if region == nil || !strings.Contains(region.Summary.Text, "Build constraints") || !strings.Contains(region.Summary.Text, "ignore") {
		t.Fatalf("a build constraint change decides whether the file compiles: %+v", region)
	}
	if g := r.byName["G"]; g == nil || g.Op != domain.OpChange {
		t.Fatalf("a compiler directive changes the function: %+v", g)
	}
	if strings.Contains(r.byName["p"].Summary.Text, "only imports, comments, or formatting") {
		t.Fatalf("package summary is false: %q", r.byName["p"].Summary.Text)
	}
}

func TestFileReplacedBySymlinkDoesNotAbort(t *testing.T) {
	s := repo(t, map[string]string{"target.txt": "x\n", "link.txt": "was a file\n"}, nil)
	must(os.Remove(filepath.Join(s.dir, "link.txt")))
	must(os.Symlink("target.txt", filepath.Join(s.dir, "link.txt")))
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "symlink")
	s.git("tag", "-f", "head")
	checkTotals(t, s, structure(t, s))
}

func TestHostileOrUnusualDiffConfigCannotChangeTheResult(t *testing.T) {
	head := map[string]string{"p/a.go": "package p\n\nfunc A() {\n\n\tprintln(2)\n}\n\nfunc B() {\n\tprintln(3)\n}\n"}
	base := map[string]string{"p/a.go": "package p\n\nfunc A() {\n\n\tprintln(1)\n}\n\nfunc B() {\n\tprintln(2)\n}\n"}
	for name, config := range map[string][2]string{
		"blank context": {"diff.suppressBlankEmpty", "true"},
		"submodule log": {"diff.submodule", "log"},
		"prefixes":      {"diff.noprefix", "true"},
		"color":         {"color.diff", "always"},
		"relative":      {"diff.relative", "true"},
		"no renames":    {"diff.renames", "false"},
	} {
		t.Run(name, func(t *testing.T) {
			s := repo(t, base, head)
			s.git("config", config[0], config[1])
			r := structure(t, s)
			for _, fn := range []string{"A", "B"} {
				if n := r.byName[fn]; n == nil || n.Added != 1 || n.Removed != 1 {
					t.Errorf("%s: %+v", fn, n)
				}
			}
			if region := r.byName["a.go"]; region != nil {
				t.Errorf("no line belongs in a region: +%d -%d", region.Added, region.Removed)
			}
		})
	}
}

func TestSubmoduleBumpWithLogConfig(t *testing.T) {
	s := repo(t, map[string]string{"a.txt": "x\n"}, nil)
	sha := s.git("rev-parse", "HEAD")
	s.git("update-index", "--add", "--cacheinfo", "160000,"+sha+",sub")
	s.git("commit", "-q", "-m", "sub")
	s.git("tag", "-f", "base")
	s.git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("1", 40)+",sub")
	s.git("commit", "-q", "-m", "bump")
	s.git("tag", "-f", "head")
	s.git("config", "diff.submodule", "log")
	r := structure(t, s)
	if r.byName["sub"] == nil {
		t.Fatalf("the submodule change must be visible: %s", r.stdout)
	}
}

func TestSharedFieldLineCountsOnce(t *testing.T) {
	s := repo(t,
		map[string]string{"p/a.go": "package p\n\ntype S struct {\n\ta, b, c int\n}\n"},
		map[string]string{"p/a.go": "package p\n\ntype S struct {\n\ta, b, c string\n}\n"},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	if n := r.byName["S"]; n.Added != 1 || n.Removed != 1 {
		t.Errorf("struct +%d -%d, but one line changed", n.Added, n.Removed)
	}
	for _, field := range []string{"S.a", "S.b", "S.c"} {
		if r.byName[field] == nil || r.byName[field].Op != domain.OpChange {
			t.Errorf("%s changed: %+v", field, r.byName[field])
		}
	}
}

func TestLongConstEditGetsNoInventedExplanation(t *testing.T) {
	long := strings.Repeat("x", 400)
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "package p\n\nconst C = \"" + long + "a\"\n"},
		map[string]string{"p/a.go": "package p\n\nconst C = \"" + long + "b\"\n"},
	))
	if text := r.byName["C"].Summary.Text; strings.Contains(text, "iota") {
		t.Fatalf("the literal was edited, so this explanation is false: %q", text)
	}
}

func TestLineDirectivesCannotRelabelCode(t *testing.T) {
	s := repo(t,
		map[string]string{"p/a.go": "package p\n\n//line foo.y:500\nfunc F() int {\n\treturn 1\n}\n"},
		map[string]string{"p/a.go": "package p\n\n//line foo.y:500\nfunc F() int {\n\treturn 2 // malicious\n}\n"},
	)
	r := structure(t, s)
	f := r.byName["F"]
	if f == nil || f.Added != 1 || f.Removed != 1 {
		t.Fatalf("the code change must belong to F: %+v", f)
	}
	if f.Locations[0].StartLine != 3 && f.Locations[0].StartLine != 4 {
		t.Errorf("location must be the file's real line: %+v", f.Locations[0])
	}
	if region := r.byName["a.go"]; region != nil {
		t.Errorf("no line belongs in a region: +%d -%d", region.Added, region.Removed)
	}
}

func TestLineDirectiveDoesNotShiftLaterUnits(t *testing.T) {
	r := structure(t, repo(t,
		map[string]string{"p/a.go": "package p\n\n//line foo.y:500\nfunc E() {}\n\nfunc F() int {\n\treturn 1\n}\n"},
		map[string]string{"p/a.go": "package p\n\n//line foo.y:500\nfunc E() {}\n\nfunc F() int {\n\treturn 2\n}\n"},
	))
	f := r.byName["F"]
	if f == nil || f.Locations[0].StartLine != 6 || f.Locations[0].EndLine != 8 {
		t.Fatalf("locations must be the file's real lines: %+v", f)
	}
}

func TestAttributesCannotHideCodeAsBinary(t *testing.T) {
	s := repo(t,
		map[string]string{".gitattributes": "*.go -diff\n", "p/a.go": "package p\n\nfunc F() int { return 1 }\n", "real.bin": "\x00\x01one"},
		map[string]string{"p/a.go": "package p\n\nfunc F() int { return 2 }\n", "real.bin": "\x00\x01two"},
	)
	r := structure(t, s)
	f := r.byName["F"]
	if f == nil || f.Added != 1 || f.Removed != 1 {
		t.Fatalf("the code change must be analyzed: %+v (%s)", f, r.stdout)
	}
	if bin := r.byName["real.bin"]; bin == nil || !bin.Diff[0].Binary {
		t.Errorf("a real binary is still binary: %+v", bin)
	}
}

func TestHostileConfigCannotRunCommandsThroughLazyFetch(t *testing.T) {
	s := repo(t,
		map[string]string{"p/a.go": "package p\n\nfunc F() int { return 1 }\n"},
		map[string]string{"p/a.go": "package p\n\nfunc F() int { return 2 }\n"},
	)
	marker := filepath.Join(t.TempDir(), "ran")
	blob := s.git("rev-parse", "head:p/a.go")
	must(os.Remove(filepath.Join(s.dir, ".git", "objects", blob[:2], blob[2:])))
	s.git("config", "core.repositoryformatversion", "1")
	s.git("config", "extensions.partialClone", "origin")
	s.git("config", "remote.origin.promisor", "true")
	s.git("config", "remote.origin.url", "ssh://example.invalid/x")
	s.git("config", "core.sshCommand", "touch "+marker+" #")
	s.git("config", "remote.origin.uploadpack", "touch "+marker+" #")
	execute(t, "--structure-only", s.dir, "base", "head")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("repository configuration ran a command")
	}
}

func TestConfiguredWorktreeCannotHideFiles(t *testing.T) {
	s := repo(t, map[string]string{"p/a.go": "package p\n\nfunc F() int { return 1 }\n"}, map[string]string{"p/a.go": "package p\n\nfunc F() int { return 2 }\n"})
	s.git("config", "diff.relative", "true")
	s.git("config", "core.worktree", filepath.Dir(s.dir))
	r := execute(t, "--structure-only", s.dir, "base", "head")
	if r.code != 0 || r.diff.Files != 1 || r.byName["F"] == nil {
		t.Fatalf("a changed file was hidden: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

func runIn(t *testing.T, dir string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), string(out)
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, string(out)
}

func TestOutputDoesNotFollowPlantedSymlinks(t *testing.T) {
	s := repo(t, map[string]string{"a.txt": "1\n"}, map[string]string{"a.txt": "2\n"})
	name := s.git("rev-parse", "--short=8", "base") + ".." + s.git("rev-parse", "--short=8", "head")

	victim := filepath.Join(t.TempDir(), "victim.txt")
	must(os.WriteFile(victim, []byte("precious"), 0o644))
	dir := filepath.Join(s.dir, "diff-duck-out", name)
	must(os.MkdirAll(dir, 0o755))
	must(os.Symlink(victim, filepath.Join(dir, "index.html")))
	runIn(t, s.dir, "--structure-only", ".", "base", "head")
	if got, _ := os.ReadFile(victim); string(got) != "precious" {
		t.Fatal("a planted file symlink redirected the output")
	}

	must(os.RemoveAll(filepath.Join(s.dir, "diff-duck-out")))
	elsewhere := t.TempDir()
	must(os.MkdirAll(filepath.Join(s.dir, "diff-duck-out"), 0o755))
	must(os.Symlink(elsewhere, filepath.Join(s.dir, "diff-duck-out", name)))
	code, out := runIn(t, s.dir, "--structure-only", ".", "base", "head")
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Fatalf("a planted directory symlink redirected the output (exit %d: %s)", code, out)
	}
	if code == 0 {
		t.Fatal("the default output path was a symlink, which must be refused")
	}
}

func TestTerminalOutputEscapesControlCharacters(t *testing.T) {
	s := repo(t, nil, map[string]string{"evil\x1b]0;PWNED\x07.go": "package broken\nfunc {\n"})
	r := execute(t, "--structure-only", s.dir, "base", "head")
	if strings.ContainsRune(r.stdout+r.stderr, 0x1b) {
		t.Fatalf("raw escape reached the terminal: %q", r.stdout)
	}
	if !strings.Contains(r.stdout, "PWNED") {
		t.Fatalf("the warning should still name the file: %q", r.stdout)
	}
}

func TestModelCallLimitIsEnforcedAndVisible(t *testing.T) {
	r := execute(t, "--adapter", "scripted:normal", "--no-cache", "--max-model-calls", "1", fixture, "base", "head")
	if r.code != 0 {
		t.Fatalf("a limit still produces output: %d %s", r.code, r.stderr)
	}
	if r.diff.Generation.ModelCalls > 1 {
		t.Fatalf("limit exceeded: %+v", r.diff.Generation)
	}
	if r.diff.Metrics.MissingSummaries == 0 || !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "limit") {
		t.Fatalf("skipped work must be visible: %+v %v", r.diff.Metrics, r.diff.Warnings)
	}
}

func TestUnknownScriptedBehaviorIsRejected(t *testing.T) {
	if r := execute(t, "--adapter", "scripted:typo", "--no-cache", fixture, "base", "head"); r.code == 0 {
		t.Fatal("a mistyped behavior must not silently run as normal")
	}
}

func TestProductionBinaryHasNoScriptedModel(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "prod")
	out, err := exec.Command("go", "build", "-o", bin, "../cmd/diffduck").CombinedOutput()
	must(wrap(err, out))
	cmd := exec.Command(bin, "--adapter", "scripted:normal", "--no-cache", "--out", t.TempDir(), fixture, "base", "head")
	if err := cmd.Run(); err == nil {
		t.Fatal("canned summaries must not be reachable from the production binary")
	}
}
