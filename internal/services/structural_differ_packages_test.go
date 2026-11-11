package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

// dirRepo serves whole directories of analyses, by commit, so package existence can be asked.
type dirRepo struct {
	fakeRepo
	sides      map[string]map[string]domain.FileAnalysis // sha -> path -> analysis
	unreadable string                                    // a path whose read fails
	unlistable bool                                      // listing a directory fails
}

func (r *dirRepo) ReadFile(_ context.Context, sha, p string) ([]byte, error) {
	if p == r.unreadable {
		return nil, errors.New("unreadable")
	}
	return []byte(sha + ":" + p), nil
}

func (r *dirRepo) ListFiles(_ context.Context, sha, dir string) ([]string, error) {
	if r.unlistable {
		return nil, errors.New("unlistable")
	}
	var out []string
	for p := range r.sides[sha] {
		if strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	return out, nil
}

// countingAnalyzer answers from the repo's analyses and counts every analysis it runs.
type countingAnalyzer struct {
	repo  *dirRepo
	calls int
	fail  string // a path whose analysis fails
}

func (a *countingAnalyzer) Supports(p string) bool { return strings.HasSuffix(p, ".cs") }

func (a *countingAnalyzer) Analyze(_ context.Context, p string, src []byte) (domain.FileAnalysis, error) {
	a.calls++
	if p == a.fail {
		return domain.FileAnalysis{}, errors.New("syntax error")
	}
	sha, _, _ := strings.Cut(string(src), ":")
	return a.repo.sides[sha][p], nil
}

// csFile declares one namespace per name, as a C# file can, each holding one class.
func csFile(p string, namespaces ...string) domain.FileAnalysis {
	a := domain.FileAnalysis{Package: domain.PackageRef{Dir: "d", Name: "d"}}
	for i, ns := range namespaces {
		a.Units = append(a.Units, domain.Unit{Package: domain.PackageRef{Dir: "d", Name: ns}, Kind: domain.KindClass,
			Name: "C" + fmt.Sprint(i), File: p, StartLine: i + 1, EndLine: i + 1, Hash: ns})
	}
	return a
}

func addedFile(p string, lines int) domain.FileChange {
	h := domain.Hunk{}
	for i := 1; i <= lines; i++ {
		h.Lines = append(h.Lines, domain.DiffLine{Kind: domain.LineAdded, NewLine: i, Text: "x"})
	}
	return domain.FileChange{Status: domain.FileAdded, NewPath: p, Hunks: []domain.Hunk{h}}
}

// A file declaring many packages beside many existing files must not cost packages times files
// analyses: each file of a directory is analyzed once per commit, however many packages ask.
func TestPackageExistenceAnalyzesEachFileOnce(t *testing.T) {
	const existing, namespaces = 30, 40
	base := map[string]domain.FileAnalysis{}
	for i := range existing {
		p := fmt.Sprintf("d/f%d.cs", i)
		base[p] = csFile(p, "Old"+fmt.Sprint(i))
	}
	head := map[string]domain.FileAnalysis{}
	for p, a := range base {
		head[p] = a
	}
	var names []string
	for i := range namespaces {
		names = append(names, "New"+fmt.Sprint(i))
	}
	names = append(names, "Old3") // one namespace already has source at the base
	head["d/new.cs"] = csFile("d/new.cs", names...)

	repo := &dirRepo{sides: map[string]map[string]domain.FileAnalysis{"b": base, "h": head}}
	a := &countingAnalyzer{repo: repo}
	roots, _, err := NewStructuralDiffer(repo, a).Build(context.Background(), "b", "h", []domain.FileChange{addedFile("d/new.cs", len(names))})
	if err != nil {
		t.Fatal(err)
	}
	// One analysis of the changed file, then one per base file.
	if a.calls > 1+existing {
		t.Errorf("%d analyses for %d packages over %d files", a.calls, len(names), existing)
	}
	ops := map[string]domain.ChangeOp{}
	for _, r := range roots {
		ops[r.ID] = r.Op
	}
	if ops[domain.PackageRef{Dir: "d", Name: "New7"}.Key()] != domain.OpAdd {
		t.Errorf("a namespace with no source at the base was added: %v", ops)
	}
	if ops[domain.PackageRef{Dir: "d", Name: "Old3"}.Key()] != domain.OpChange {
		t.Errorf("a namespace another file declares already existed: %v", ops)
	}
}

// Adding a file to a package that already has source needs one file showing that, not an
// analysis of every file in the directory.
func TestPackageExistenceStopsAtTheFirstFileDeclaringIt(t *testing.T) {
	base := map[string]domain.FileAnalysis{}
	for i := range 30 {
		p := fmt.Sprintf("d/f%d.cs", i)
		base[p] = csFile(p, "P")
	}
	head := map[string]domain.FileAnalysis{"d/new.cs": csFile("d/new.cs", "P")}
	for p, a := range base {
		head[p] = a
	}
	repo := &dirRepo{sides: map[string]map[string]domain.FileAnalysis{"b": base, "h": head}}
	a := &countingAnalyzer{repo: repo}
	roots, _, err := NewStructuralDiffer(repo, a).Build(context.Background(), "b", "h", []domain.FileChange{addedFile("d/new.cs", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if a.calls != 2 {
		t.Errorf("%d analyses; the changed file and one existing file are enough", a.calls)
	}
	if len(roots) != 1 || roots[0].Op != domain.OpChange {
		t.Errorf("the package already existed: %+v", roots)
	}
}

// A file that cannot be listed, read, or analyzed leaves every package of its directory
// possibly existing.
func TestPackageExistenceIsUnclearWhenAFileFails(t *testing.T) {
	for _, failure := range []string{"analysis", "read", "list"} {
		t.Run(failure, func(t *testing.T) {
			base := map[string]domain.FileAnalysis{"d/a.cs": csFile("d/a.cs", "A"), "d/broken.cs": {}}
			head := map[string]domain.FileAnalysis{"d/a.cs": csFile("d/a.cs", "A"), "d/broken.cs": {}, "d/new.cs": csFile("d/new.cs", "N1", "N2")}
			repo := &dirRepo{sides: map[string]map[string]domain.FileAnalysis{"b": base, "h": head}}
			a := &countingAnalyzer{repo: repo}
			switch failure {
			case "analysis":
				a.fail = "d/broken.cs"
			case "read":
				repo.unreadable = "d/broken.cs"
			case "list":
				repo.unlistable = true
			}
			roots, _, err := NewStructuralDiffer(repo, a).Build(context.Background(), "b", "h", []domain.FileChange{addedFile("d/new.cs", 2)})
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range roots {
				if r.Kind == domain.KindPackage && r.Op != domain.OpChange {
					t.Errorf("%s: without positive evidence a package is not added: %s", r.ID, r.Op)
				}
			}
		})
	}
}

// Each commit has its own packages: in one diff a directory can gain one package, looked up at
// the base, and lose another, looked up at the head. A file with no units still declares its
// package.
func TestPackageExistenceIsAskedOfTheRightCommit(t *testing.T) {
	base := map[string]domain.FileAnalysis{"d/y.cs": csFile("d/y.cs", "Y"), "d/empty.cs": {Package: domain.PackageRef{Dir: "d", Name: "Z"}}}
	head := map[string]domain.FileAnalysis{"d/x.cs": csFile("d/x.cs", "X"), "d/z.cs": csFile("d/z.cs", "Z")}
	removed := domain.FileChange{Status: domain.FileDeleted, OldPath: "d/y.cs", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{{Kind: domain.LineRemoved, OldLine: 1, Text: "x"}}}}}
	repo := &dirRepo{sides: map[string]map[string]domain.FileAnalysis{"b": base, "h": head}}
	roots, _, err := NewStructuralDiffer(repo, &countingAnalyzer{repo: repo}).Build(context.Background(), "b", "h",
		[]domain.FileChange{addedFile("d/x.cs", 1), removed, addedFile("d/z.cs", 1)})
	if err != nil {
		t.Fatal(err)
	}
	ops := map[string]domain.ChangeOp{}
	for _, r := range roots {
		ops[r.ID] = r.Op
	}
	want := map[string]domain.ChangeOp{
		domain.PackageRef{Dir: "d", Name: "X"}.Key(): domain.OpAdd,
		domain.PackageRef{Dir: "d", Name: "Y"}.Key(): domain.OpRemove,
		domain.PackageRef{Dir: "d", Name: "Z"}.Key(): domain.OpChange, // empty.cs declared it
	}
	for key, op := range want {
		if ops[key] != op {
			t.Errorf("%s: %s, want %s (all: %v)", key, ops[key], op, ops)
		}
	}
}

// Two same-named classes swap places between revisions, so a changed method lies within one
// class's lines on each side. The class it belongs to is the one on its own side of the diff.
func TestEnclosingClassIsTakenFromTheUnitsOwnSide(t *testing.T) {
	file := func(classes [2][2]int, method string) domain.FileAnalysis {
		return domain.FileAnalysis{Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: []domain.Unit{
			pyUnit(domain.KindModule, "m", "", 1, 12, "mod"),
			pyUnit(domain.KindClass, "m.C", "m", classes[0][0], classes[0][1], "first"),
			pyUnit(domain.KindClass, "m.C", "m", classes[1][0], classes[1][1], "second"),
			pyUnit(domain.KindMethod, "m.C.run", "m.C", 9, 10, method),
		}}
	}
	// At the base the method is in the second class (lines 8-10); at the head the first class
	// moved to lines 6-10 and holds it, while the second moved to lines 1-4.
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": file([2][2]int{{3, 5}, {8, 10}}, "v1")},
		map[string]domain.FileAnalysis{"app/m.py": file([2][2]int{{6, 10}, {1, 4}}, "v2")},
		edit("app/m.py", 9, 9))
	if err != nil {
		t.Fatal(err)
	}
	var class *domain.Node
	for _, r := range roots {
		r.Walk(func(n *domain.Node) {
			for _, c := range n.Children {
				if c.Name == "m.C.run" {
					class = n
				}
			}
		})
	}
	if class == nil || class.Locations[0].Side != "head" || class.Locations[0].StartLine != 6 {
		t.Fatalf("the changed method belongs to the head class at lines 6-10, got %+v", class)
	}
}

// cancellingRepo cancels the run when it reads a file, and fails that read.
type cancellingRepo struct {
	shaRepo
	cancel  func()
	failing bool
	reads   int
}

func (r *cancellingRepo) ReadFile(ctx context.Context, sha, p string) ([]byte, error) {
	r.reads++
	if r.failing {
		r.cancel()
		return nil, errors.New("read abandoned")
	}
	return r.shaRepo.ReadFile(ctx, sha, p)
}

// Once the run is cancelled, a failure caused by it is the cancellation: the differ stops at
// once rather than reporting the file as unparsable and going on to the next.
func TestCancellationStopsTheDifferAtTheFailingFile(t *testing.T) {
	for _, during := range []string{"analysis", "read"} {
		t.Run(during, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			analyses := 0
			a := &scriptedAnalyzer{files: map[string]domain.FileAnalysis{}, err: func(context.Context) error {
				analyses++
				if during == "analysis" {
					cancel()
					return errors.New("parse abandoned")
				}
				return nil
			}}
			changes := []domain.FileChange{edit("app/a.py", 1, 1), edit("app/b.py", 1, 1)}
			repo := &cancellingRepo{shaRepo: shaRepo{fakeRepo: fakeRepo{changes: changes}, analyzer: a}, cancel: cancel, failing: during == "read"}
			roots, warnings, err := NewStructuralDiffer(repo, a).Build(ctx, "b", "h", changes)
			if !errors.Is(err, context.Canceled) || roots != nil || warnings != nil {
				t.Fatalf("want only the cancellation, got roots %v warnings %v err %v", roots, warnings, err)
			}
			if repo.reads != 1 || analyses > 1 {
				t.Errorf("work went on after the cancellation: %d reads, %d analyses", repo.reads, analyses)
			}
		})
	}
}

func TestPackagesSortBySubdirectoryFirst(t *testing.T) {
	sorted := []string{".", "src", "src/components", "src/components/some-service", "src-utils", "web"}
	for i := range sorted {
		for j := range sorted {
			if got := pathLess(sorted[i], sorted[j]); got != (i < j) {
				t.Errorf("pathLess(%q, %q) = %v", sorted[i], sorted[j], got)
			}
		}
	}
}

// nestingTree builds a finished tree from one changed function per package.
func nestingTree(t *testing.T, refs ...domain.PackageRef) []*domain.Node {
	t.Helper()
	b := &treeBuilder{nodes: map[string]*domain.Node{}, packages: map[string]*domain.Node{}, parents: map[string]*domain.Node{}, refs: map[string]domain.PackageRef{}}
	for i, ref := range refs {
		pkg := b.packageNode(ref)
		fn := &domain.Node{ID: ref.Key() + "::f", Kind: domain.KindFunction, Name: "f", Op: domain.OpChange, Direct: true,
			Diff: []domain.FileDiff{{NewPath: ref.Dir + "/f.x", Lines: []domain.DiffLine{{Kind: domain.LineAdded, NewLine: i + 1}}}}}
		pkg.Children = append(pkg.Children, fn)
	}
	b.finish(func(domain.PackageRef, domain.ChangeOp) bool { return true })
	return b.roots
}

func shape(nodes []*domain.Node) string {
	var parts []string
	for _, n := range nodes {
		if n.Kind != domain.KindPackage {
			continue
		}
		if inner := shape(n.Children); inner != "" {
			parts = append(parts, n.Name+"("+inner+")")
		} else {
			parts = append(parts, n.Name)
		}
	}
	return strings.Join(parts, " ")
}

func TestHierarchicalPackagesNestUnderTheNearestChangedFolder(t *testing.T) {
	js := func(dir string) domain.PackageRef {
		return domain.PackageRef{Dir: dir, Name: dir[strings.LastIndex(dir, "/")+1:], Hierarchical: true}
	}
	roots := nestingTree(t,
		js("apps/portal/src/components/session-clock"), js("apps/portal/src"), js("apps/portal/src-utils"),
		js("apps/portal/src/lib"), domain.PackageRef{Dir: ".", Hierarchical: true}, js("tools"))
	// src/components did not change, so session-clock joins src directly; the root never takes
	// children; src-utils is not inside src.
	want := ". apps/portal/src(apps/portal/src/components/session-clock apps/portal/src/lib) apps/portal/src-utils tools"
	if got := shape(roots); got != want {
		t.Fatalf("tree:\n got %s\nwant %s", got, want)
	}
	src := roots[1]
	if src.Added != 3 {
		t.Errorf("a folder's lines include its subfolders': +%d", src.Added)
	}
	if src.Children[0].Kind != domain.KindPackage {
		t.Errorf("subfolders come first, as in a file explorer: %s", src.Children[0].Name)
	}
}

// Go packages are independent of the directory above them, so a Go tree keeps every package at
// the top, and a Go package never joins a folder of another language.
func TestGoPackagesNeverNest(t *testing.T) {
	gopkg := func(dir string) domain.PackageRef {
		return domain.PackageRef{Dir: dir, Name: dir[strings.LastIndex(dir, "/")+1:]}
	}
	roots := nestingTree(t, gopkg("internal/services"), gopkg("internal/services/ports"),
		domain.PackageRef{Dir: "web", Name: "web", Hierarchical: true}, gopkg("web/server"))
	if got, want := shape(roots), "internal/services internal/services/ports web web/server"; got != want {
		t.Fatalf("tree:\n got %s\nwant %s", got, want)
	}
}

func TestAFolderChangedDifferentlyFromItsSubfolderIsAChange(t *testing.T) {
	b := &treeBuilder{nodes: map[string]*domain.Node{}, packages: map[string]*domain.Node{}, parents: map[string]*domain.Node{}, refs: map[string]domain.PackageRef{}}
	add := func(dir string, op domain.ChangeOp) {
		pkg := b.packageNode(domain.PackageRef{Dir: dir, Name: dir, Hierarchical: true})
		pkg.Children = append(pkg.Children, &domain.Node{ID: dir + "::f", Kind: domain.KindFunction, Name: "f", Op: op, Direct: true})
	}
	add("src", domain.OpAdd)
	add("src/new", domain.OpAdd)
	add("lib", domain.OpAdd)
	add("lib/old", domain.OpRemove)
	b.finish(func(domain.PackageRef, domain.ChangeOp) bool { return false })
	ops := map[string]domain.ChangeOp{}
	for _, r := range b.roots {
		ops[r.Name] = r.Op
	}
	if ops["src"] != domain.OpAdd || ops["lib"] != domain.OpChange {
		t.Fatalf("ops: %v", ops)
	}
}
