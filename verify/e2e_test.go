// Package verify exercises the duck-diff binary through its real CLI entry point, against
// a real Git repository, and checks structure against independent oracles.
package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"duck-diff/internal/domain"
)

var (
	binary  string
	fixture string
)

func TestMain(m *testing.M) {
	work, err := os.MkdirTemp("", "duck-diff-e2e-")
	must(err)
	defer os.RemoveAll(work)
	binary = filepath.Join(work, "duck-diff")
	out, err := exec.Command("go", "build", "-tags", "verify", "-o", binary, "../cmd/duck-diff").CombinedOutput()
	must(wrap(err, out))
	fixture = filepath.Join(work, "fixture")
	out, err = exec.Command("./make-fixture.sh", fixture).CombinedOutput()
	must(wrap(err, out))
	code := m.Run()
	os.RemoveAll(work)
	os.Exit(code)
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func wrap(err error, out []byte) error {
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

type run struct {
	code   int
	stdout string
	stderr string
	dir    string
	diff   domain.CompressedDiff
	byName map[string]*domain.Node
	parent map[string]*domain.Node
}

func execute(t *testing.T, args ...string) *run {
	t.Helper()
	r := &run{dir: t.TempDir(), byName: map[string]*domain.Node{}, parent: map[string]*domain.Node{}}
	cmd := exec.Command(binary, append([]string{"--out", r.dir}, args...)...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r.stdout, r.stderr = stdout.String(), stderr.String()
	if exit, ok := err.(*exec.ExitError); ok {
		r.code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(r.dir, "compressed-diff.json")); err == nil {
		if err := json.Unmarshal(data, &r.diff); err != nil {
			t.Fatal(err)
		}
		var index func(n, parent *domain.Node)
		index = func(n, parent *domain.Node) {
			r.byName[n.Name] = n
			if parent != nil {
				r.parent[n.ID] = parent
			}
			for _, c := range n.Children {
				index(c, n)
			}
		}
		for _, n := range r.diff.Nodes {
			index(n, nil)
		}
	}
	return r
}

func normal(t *testing.T, extra ...string) *run {
	t.Helper()
	r := execute(t, append([]string{"--adapter", "scripted:normal", "--no-cache", fixture, "base", "head"}, extra...)...)
	if r.code != 0 {
		t.Fatalf("exit %d: %s %s", r.code, r.stdout, r.stderr)
	}
	return r
}

// git asks Git directly, independent of the tool's own parser.
func git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", fixture}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestA1EveryChangeIsTracedAndTotalsMatchGit(t *testing.T) {
	r := normal(t)
	c := r.diff.Coverage
	if !c.Complete() || c.ChangedLines == 0 || len(c.Untraced) > 0 {
		t.Fatalf("coverage: %+v", c)
	}
	added, removed, files := 0, 0, 0
	for _, line := range strings.Split(strings.TrimSpace(git(t, "diff", "--numstat", "-M", "base", "head")), "\n") {
		f := strings.Fields(line)
		files++
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		added += a
		removed += d
	}
	if r.diff.Added != added || r.diff.Removed != removed || r.diff.Files != files {
		t.Fatalf("tool +%d -%d %d files; git +%d -%d %d files", r.diff.Added, r.diff.Removed, r.diff.Files, added, removed, files)
	}
	if c.ChangedLines != added+removed {
		t.Fatalf("changed lines %d, git says %d", c.ChangedLines, added+removed)
	}
	// Each changed line is owned by exactly one node, except lines shared by "a, b int" fields.
	seen := map[string]int{}
	for _, root := range r.diff.Nodes {
		root.Walk(func(n *domain.Node) {
			for _, d := range n.Diff {
				for _, l := range d.Lines {
					if l.IsChange() {
						seen[fmt.Sprintf("%s|%s|%d|%d", d.OldPath, d.NewPath, l.OldLine, l.NewLine)]++
					}
				}
			}
		})
	}
	for key, count := range seen {
		if count != 1 {
			t.Errorf("line %s appears in %d nodes", key, count)
		}
	}
}

func TestA2TreeMirrorsSourceStructure(t *testing.T) {
	r := normal(t)
	for child, parent := range map[string]string{
		"Service.Create":   "Service",
		"Service":          "users",
		"User.DateOfBirth": "User",
		"Store.Delete":     "Store",
		"ValidateDOB":      "users",
		"Cache.Get":        "Cache",
		"Cache":            "billing",
		"Fallback":         "legacy",
	} {
		n, ok := r.byName[child]
		if !ok {
			t.Errorf("%s missing", child)
			continue
		}
		if got := r.parent[n.ID]; got == nil || got.Name != parent {
			t.Errorf("%s: parent %+v, want %s", child, got, parent)
		}
	}
	if _, ok := r.byName["Service.Untouched"]; ok {
		t.Error("unchanged code must be omitted")
	}
	if n := r.byName["Service.Create"]; n.Op != domain.OpChange || n.Signature != "func (s *Service) Create(u User) error" {
		t.Errorf("Create: %s %q", n.Op, n.Signature)
	}
	wantOps := map[string]domain.ChangeOp{"ValidateDOB": domain.OpAdd, "Fallback": domain.OpRemove, "legacy": domain.OpRemove, "users": domain.OpChange}
	for name, op := range wantOps {
		if r.byName[name].Op != op {
			t.Errorf("%s: %s want %s", name, r.byName[name].Op, op)
		}
	}
	// Source locations point at the real declaration.
	loc := r.byName["ValidateDOB"].Locations[0]
	text := git(t, "show", "head:"+loc.Path)
	lines := strings.Split(text, "\n")
	if !strings.Contains(lines[loc.StartLine-1], "ValidateDOB") || !strings.HasPrefix(lines[loc.EndLine-1], "}") {
		t.Errorf("location %+v does not match source", loc)
	}
	// Duplicate init functions: only the changed one appears.
	inits := 0
	for _, c := range r.byName["billing"].Children {
		if c.Name == "init" {
			inits++
		}
	}
	if inits != 1 {
		t.Errorf("expected only the changed init, got %d", inits)
	}
}

func TestA4DirectAndInheritedChange(t *testing.T) {
	r := normal(t)
	for name, direct := range map[string]bool{"Service": false, "Store": false, "User": false, "Service.Create": true, "users": false} {
		if r.byName[name].Direct != direct {
			t.Errorf("%s: direct=%v", name, r.byName[name].Direct)
		}
	}
}

func TestA5RenameAndMoveKeepIdentity(t *testing.T) {
	r := normal(t)
	n, ok := r.byName["addAndDouble"]
	if !ok || n.Op != domain.OpChange || n.PreviousName != "oldHelperName" {
		t.Fatalf("rename: %+v", n)
	}
	if _, stale := r.byName["oldHelperName"]; stale {
		t.Error("a detected rename must not also appear as a removal")
	}
	if n.Summary.Origin != domain.OriginStructural {
		t.Error("an identical-body rename needs no generated explanation")
	}
	m := r.byName["Relocated"]
	if m == nil || !m.Moved || m.Op != domain.OpChange || len(m.Locations) != 2 {
		t.Fatalf("move: %+v", m)
	}
	f := r.byName["web/after.txt"]
	if f == nil || f.PreviousName != "web/before.txt" || !f.Moved {
		t.Fatalf("file rename: %+v", f)
	}
}

func TestA6FormattingAndCommentsAreTracedNotCodeChanges(t *testing.T) {
	r := normal(t)
	for _, name := range []string{"formatted", "commented"} {
		if _, ok := r.byName[name]; ok {
			t.Errorf("%s changed only formatting or comments, so it is not a code change", name)
		}
	}
	region := r.byName["user.go"]
	if region == nil || region.Kind != domain.KindRegion {
		t.Fatalf("region: %+v", region)
	}
	text := ""
	for _, d := range region.Diff {
		for _, l := range d.Lines {
			if l.IsChange() {
				text += l.Text + "\n"
			}
		}
	}
	for _, want := range []string{`"time"`, "return a + 1", "Two is the right number"} {
		if !strings.Contains(text, want) {
			t.Errorf("region should trace %q; has:\n%s", want, text)
		}
	}
	if high := r.byName["High"]; high == nil || high.Op != domain.OpChange || high.Summary.Text == "" {
		t.Errorf("a const whose iota value shifted is a real change and must say why: %+v", high)
	}
}

func TestPlainFileHandling(t *testing.T) {
	r := normal(t)
	if n := r.byName["users/broken.go"]; n == nil || n.Kind != domain.KindFile || !strings.Contains(n.Summary.Uncertainty, "could not be parsed") || n.Summary.Confidence != domain.ConfidenceLow {
		t.Errorf("unparseable file must be honest about it: %+v", n)
	}
	if !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "users/broken.go") {
		t.Error("expected a warning for the unparseable file")
	}
	for _, name := range []string{"users/gen.go", "go.sum", "web/logo.bin"} {
		n := r.byName[name]
		if n == nil || n.Summary.Origin != domain.OriginStructural {
			t.Errorf("%s must not cost a model call: %+v", name, n)
		}
	}
	if !r.byName["web/logo.bin"].Diff[0].Binary {
		t.Error("binary flag")
	}
	if n := r.byName["odd dir/notes with space.md"]; n == nil || n.Added != 1 {
		t.Errorf("path with spaces: %+v", n)
	}
}

func TestA7BottomUpSummariesAndHonestFailure(t *testing.T) {
	r := normal(t)
	if got := r.byName["Service"].Summary.Text; !strings.Contains(got, "parent of 1") {
		t.Errorf("parent should be summarized from its children: %q", got)
	}
	// Generated text fills only the summary: fields come from source.
	if r.byName["Service.Create"].Summary.Origin != domain.OriginModel {
		t.Error("leaf origin")
	}
	if n := r.byName["User.DateOfBirth"]; n.Summary.Origin != domain.OriginStructural || !strings.Contains(n.Signature, "json:\"dob\"") {
		t.Errorf("field: %+v", n)
	}

	failed := execute(t, "--adapter", "scripted:fail", "--no-cache", fixture, "base", "head")
	if failed.code != 3 || !strings.Contains(failed.stderr, "every model call failed") {
		t.Fatalf("total failure must exit nonzero and explain: %d %q", failed.code, failed.stderr)
	}
	if !failed.diff.Coverage.Complete() || len(failed.diff.Nodes) == 0 {
		t.Fatal("the structural diff must still be written")
	}
	for _, root := range failed.diff.Nodes {
		root.Walk(func(n *domain.Node) {
			if n.Summary.Origin == domain.OriginModel {
				t.Errorf("%s has a generated summary despite total failure", n.ID)
			}
			if n.Summary.Origin == domain.OriginNone && (n.Summary.Text != "" || n.Summary.Uncertainty == "") {
				t.Errorf("%s must say it is unexplained, not invent text: %+v", n.ID, n.Summary)
			}
		})
	}

	partial := execute(t, "--adapter", "scripted:partial", "--no-cache", fixture, "base", "head")
	if partial.code != 0 {
		t.Fatalf("partial failure still produces usable output: %d %s", partial.code, partial.stderr)
	}
	if partial.diff.Metrics.MissingSummaries == 0 || !strings.Contains(strings.Join(partial.diff.Warnings, "\n"), "returned no summary") {
		t.Errorf("missing summaries must be reported: %+v", partial.diff.Metrics)
	}
	for _, root := range partial.diff.Nodes {
		root.Walk(func(n *domain.Node) {
			if strings.Contains(n.ID, "invented") {
				t.Errorf("a model must not be able to add nodes: %s", n.ID)
			}
			if n.Summary.Text == "Invented." {
				t.Errorf("a summary for an unknown ID was applied to %s", n.ID)
			}
		})
	}
}

func TestA8OutcomesReferenceOnlyRealNodes(t *testing.T) {
	r := normal(t)
	if len(r.diff.Outcomes) == 0 {
		t.Fatal("expected outcomes")
	}
	if n := r.byName["Service.Create"]; len(n.Outcomes) == 0 {
		t.Error("descendants of a referenced unit belong to its outcome")
	}
	if !slicesContains(r.diff.Unassigned, "file:go.mod") {
		t.Errorf("unassociated leaves must be listed: %v", r.diff.Unassigned)
	}
	partial := execute(t, "--adapter", "scripted:partial", "--no-cache", fixture, "base", "head")
	for _, o := range partial.diff.Outcomes {
		if o.Title == "Fabricated outcome" {
			t.Error("an outcome with no real nodes must be dropped")
		}
		for _, id := range o.NodeIDs {
			if strings.Contains(id, "invented") {
				t.Errorf("unknown node kept in %q", o.Title)
			}
		}
	}
	if !strings.Contains(strings.Join(partial.diff.Warnings, "\n"), "unknown node") {
		t.Error("rejected references must be reported")
	}
}

func slicesContains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestA11PromptVariantsAreInjectedAndRecorded(t *testing.T) {
	builtIn := normal(t)
	dir := filepath.Join(t.TempDir(), "terse-v2")
	must(os.MkdirAll(dir, 0o755))
	for _, name := range []string{"leaf", "parent", "outcomes"} {
		must(os.WriteFile(filepath.Join(dir, name+".md"), []byte("Variant prompt for "+name), 0o644))
	}
	variant := normal(t, "--prompts", dir)
	if variant.diff.Generation.PromptVariant != "terse-v2" || builtIn.diff.Generation.PromptVariant != "default" {
		t.Fatalf("variants: %q %q", variant.diff.Generation.PromptVariant, builtIn.diff.Generation.PromptVariant)
	}
	if variant.diff.Generation.Prompts[0].Hash == builtIn.diff.Generation.Prompts[0].Hash {
		t.Error("prompt hashes should identify the text")
	}

	must(os.Remove(filepath.Join(dir, "parent.md")))
	broken := execute(t, "--adapter", "scripted:normal", "--no-cache", "--prompts", dir, fixture, "base", "head")
	if broken.code != 1 || !strings.Contains(broken.stderr, "terse-v2") {
		t.Fatalf("an incomplete variant must fail before spending: %d %q", broken.code, broken.stderr)
	}
}

func TestCacheAvoidsRepeatCallsAndNeverCrossesVariants(t *testing.T) {
	cache := t.TempDir()
	flags := []string{"--adapter", "scripted:normal", "--cache-dir", cache, fixture, "base", "head"}
	first := execute(t, flags...)
	if first.diff.Generation.ModelCalls == 0 || first.diff.Generation.CacheHits != 0 {
		t.Fatalf("first: %+v", first.diff.Generation)
	}
	second := execute(t, flags...)
	if second.diff.Generation.ModelCalls != 0 || second.diff.Generation.CacheHits != first.diff.Generation.ModelCalls {
		t.Fatalf("second: %+v", second.diff.Generation)
	}
	if second.byName["Service"].Summary.Text != first.byName["Service"].Summary.Text {
		t.Error("cached output should match")
	}
	dir := filepath.Join(t.TempDir(), "other")
	must(os.MkdirAll(dir, 0o755))
	for _, name := range []string{"leaf", "parent", "outcomes"} {
		must(os.WriteFile(filepath.Join(dir, name+".md"), []byte("x"), 0o644))
	}
	third := execute(t, append([]string{"--prompts", dir}, flags...)...)
	if third.diff.Generation.CacheHits != 0 {
		t.Fatalf("a different prompt must not reuse answers: %+v", third.diff.Generation)
	}
}

func TestA13TargetsAndBadInput(t *testing.T) {
	sha := strings.TrimSpace(git(t, "rev-parse", "base"))
	bySHA := execute(t, "--adapter", "scripted:normal", "--no-cache", fixture, sha, "head~0")
	if bySHA.code != 0 || bySHA.diff.BaseSHA != sha {
		t.Fatalf("any revision form should work: %d %s", bySHA.code, bySHA.stderr)
	}
	reversed := execute(t, "--structure-only", fixture, "head", "base")
	if reversed.byName["ValidateDOB"].Op != domain.OpRemove || !reversed.diff.Coverage.Complete() {
		t.Error("the commit pair is directional")
	}
	same := execute(t, "--structure-only", fixture, "base", "base")
	if same.code != 0 || len(same.diff.Nodes) != 0 {
		t.Fatalf("an empty diff is valid: %d %s", same.code, same.stderr)
	}
	raw, err := os.ReadFile(filepath.Join(same.dir, "compressed-diff.json"))
	must(err)
	if !strings.Contains(string(raw), `"nodes": []`) {
		t.Fatal("an empty diff must publish an empty list, not null")
	}
	sentinel := filepath.Join(t.TempDir(), "pwned")
	for name, args := range map[string][]string{
		"unknown revision":  {fixture, "nope", "head"},
		"not a repository":  {t.TempDir(), "base", "head"},
		"missing path":      {"/no/such/path", "base", "head"},
		"command transport": {"ext::sh -c 'touch " + sentinel + "'", "base", "head"},
		"file url":          {"file://" + fixture, "base", "head"},
		"unknown adapter":   {"--adapter", "nope", fixture, "base", "head"},
	} {
		t.Run(name, func(t *testing.T) {
			if r := execute(t, args...); r.code == 0 {
				t.Fatalf("expected failure: %s", r.stdout)
			}
		})
	}
	if _, err := os.Stat(sentinel); err == nil {
		t.Fatal("a target string executed a command")
	}
	if r := execute(t, fixture, "--upload-pack=x", "head"); r.code != 2 {
		t.Fatalf("a dash-prefixed revision is a flag and must never reach git: %d", r.code)
	}
	if r := execute(t, fixture, "base"); r.code != 2 {
		t.Fatalf("missing argument: %d", r.code)
	}
}

func TestStructureOnlyNeedsNoModel(t *testing.T) {
	r := execute(t, "--structure-only", "--adapter", "nope", fixture, "base", "head")
	if r.code != 0 || r.diff.Generation.ModelCalls != 0 || !r.diff.Coverage.Complete() {
		t.Fatalf("%d %s %+v", r.code, r.stderr, r.diff.Generation)
	}
}

func TestViewerIsSelfContainedAndInertToRepositoryContent(t *testing.T) {
	r := normal(t)
	page, err := os.ReadFile(filepath.Join(r.dir, "index.html"))
	must(err)
	if bytesContain(page, "</script><script>window.__xss") {
		t.Fatal("repository content escaped its JSON script block")
	}
	if regexp.MustCompile(`(src|href)="https?://`).Match(page) {
		t.Fatal("the page must not load anything over the network")
	}
}

func bytesContain(b []byte, s string) bool { return strings.Contains(string(b), s) }
