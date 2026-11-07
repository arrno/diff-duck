package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

// scriptedAnalyzer returns fixed analyses by revision and path, standing in for any language.
type scriptedAnalyzer struct {
	files map[string]domain.FileAnalysis // "sha:path"
	err   func(ctx context.Context) error
}

func (a *scriptedAnalyzer) Supports(p string) bool { return strings.HasSuffix(p, ".py") }

func (a *scriptedAnalyzer) Analyze(ctx context.Context, p string, _ []byte) (domain.FileAnalysis, error) {
	if a.err != nil {
		if err := a.err(ctx); err != nil {
			return domain.FileAnalysis{}, err
		}
	}
	return a.files[p], nil
}

// shaRepo hands the analyzer the revision through the path it reads.
type shaRepo struct {
	fakeRepo
	analyzer *scriptedAnalyzer
	sides    map[string]map[string]domain.FileAnalysis // sha -> path -> analysis
}

func (r *shaRepo) ReadFile(_ context.Context, sha, p string) ([]byte, error) {
	r.analyzer.files[p] = r.sides[sha][p]
	return nil, nil
}

func pyUnit(kind domain.UnitKind, name, parent string, start, end int, hash string) domain.Unit {
	return domain.Unit{Package: domain.PackageRef{Dir: "app", Name: "app"}, Kind: kind, Name: name, Parent: parent,
		File: "app/m.py", StartLine: start, EndLine: end, Hash: hash}
}

func buildTree(t *testing.T, ctx context.Context, base, head map[string]domain.FileAnalysis, changes ...domain.FileChange) ([]*domain.Node, []string, error) {
	t.Helper()
	a := &scriptedAnalyzer{files: map[string]domain.FileAnalysis{}}
	repo := &shaRepo{fakeRepo: fakeRepo{changes: changes}, analyzer: a, sides: map[string]map[string]domain.FileAnalysis{"b": base, "h": head}}
	return NewStructuralDiffer(repo, a).Build(ctx, "b", "h", changes)
}

func edit(path string, oldLine, newLine int) domain.FileChange {
	return domain.FileChange{Status: domain.FileModified, OldPath: path, NewPath: path, Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
		{Kind: domain.LineRemoved, OldLine: oldLine, Text: "old"},
		{Kind: domain.LineAdded, NewLine: newLine, Text: "new"},
	}}}}
}

// pathTo returns the names from a root down to the named node.
func pathTo(roots []*domain.Node, name string) []string {
	var walk func(n *domain.Node, trail []string) []string
	walk = func(n *domain.Node, trail []string) []string {
		trail = append(trail, n.Name)
		if n.Name == name {
			return trail
		}
		for _, c := range n.Children {
			if found := walk(c, trail); found != nil {
				return found
			}
		}
		return nil
	}
	for _, r := range roots {
		if found := walk(r, nil); found != nil {
			return found
		}
	}
	return nil
}

func nestedFile(methodHash, innerHash string) domain.FileAnalysis {
	return domain.FileAnalysis{Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: []domain.Unit{
		pyUnit(domain.KindModule, "m", "", 1, 20, "mod"),
		pyUnit(domain.KindClass, "m.Outer", "m", 3, 20, "outer"),
		pyUnit(domain.KindClass, "m.Outer.Inner", "m.Outer", 5, 20, innerHash),
		pyUnit(domain.KindMethod, "m.Outer.Inner.run", "m.Outer.Inner", 10, 12, methodHash),
	}}
}

func TestDeeplyNestedUnitsKeepEveryAncestor(t *testing.T) {
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": nestedFile("v1", "inner")},
		map[string]domain.FileAnalysis{"app/m.py": nestedFile("v2", "inner")},
		edit("app/m.py", 11, 11))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(pathTo(roots, "m.Outer.Inner.run"), " > ")
	if got != "app > m > m.Outer > m.Outer.Inner > m.Outer.Inner.run" {
		t.Fatalf("path %q", got)
	}
	for _, name := range []string{"m", "m.Outer", "m.Outer.Inner"} {
		n := findNode(roots, name)
		if n.Direct || n.Op != domain.OpChange {
			t.Errorf("%s only contains the change: direct=%v op=%s", name, n.Direct, n.Op)
		}
	}
	if run := findNode(roots, "m.Outer.Inner.run"); !run.Direct || run.Added != 1 || run.Removed != 1 {
		t.Errorf("the method owns its lines: %+v", run)
	}
}

func TestNestedAncestorThatAlsoChangedStaysInPlace(t *testing.T) {
	// Both the inner class and its method changed. Whichever is attached first, the class must
	// sit under its own parent, not directly under the package.
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": nestedFile("v1", "inner1")},
		map[string]domain.FileAnalysis{"app/m.py": nestedFile("v2", "inner2")},
		domain.FileChange{Status: domain.FileModified, OldPath: "app/m.py", NewPath: "app/m.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
			{Kind: domain.LineAdded, NewLine: 6, Text: "doc"},
			{Kind: domain.LineAdded, NewLine: 11, Text: "body"},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pathTo(roots, "m.Outer.Inner.run"), " > "); got != "app > m > m.Outer > m.Outer.Inner > m.Outer.Inner.run" {
		t.Fatalf("path %q", got)
	}
	if inner := findNode(roots, "m.Outer.Inner"); !inner.Direct {
		t.Error("the inner class changed itself")
	}
}

func moduleFile(module, path string) domain.FileAnalysis {
	u := func(kind domain.UnitKind, name, parent string, start, end int, hash string) domain.Unit {
		x := pyUnit(kind, name, parent, start, end, hash)
		x.File = path
		return x
	}
	return domain.FileAnalysis{Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: []domain.Unit{
		u(domain.KindModule, module, "", 1, 9, "mod"),
		u(domain.KindFunction, module+".helper", module, 3, 5, "helper"),
		u(domain.KindClass, module+".Repo", module, 7, 9, "repo"),
	}}
}

func TestRenamedModuleKeepsIdentityOfItsUnits(t *testing.T) {
	rename := domain.FileChange{Status: domain.FileRenamed, OldPath: "app/old.py", NewPath: "app/new.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
		{Kind: domain.LineRemoved, OldLine: 4, Text: "a"},
		{Kind: domain.LineAdded, NewLine: 4, Text: "b"},
	}}}}
	head := moduleFile("new", "app/new.py")
	head.Units[1].Hash = "helper2"
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/old.py": moduleFile("old", "app/old.py")},
		map[string]domain.FileAnalysis{"app/new.py": head},
		rename)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"new", "new.helper", "new.Repo"} {
		n := findNode(roots, name)
		if n == nil || n.Op != domain.OpChange || !n.Moved {
			t.Fatalf("%s should be the same unit, moved: %+v", name, n)
		}
	}
	for _, name := range []string{"old", "old.helper", "old.Repo"} {
		if n := findNode(roots, name); n != nil {
			t.Errorf("%s should not appear as removed: %+v", name, n)
		}
	}
	if !strings.Contains(findNode(roots, "new.Repo").Summary.Text, "no code change") {
		t.Errorf("an unchanged class only moved: %q", findNode(roots, "new.Repo").Summary.Text)
	}
}

func TestCancellationDuringAnalysisIsAnErrorNotAPlainFile(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := &scriptedAnalyzer{files: map[string]domain.FileAnalysis{}, err: func(ctx context.Context) error {
		cancel()
		return errors.New("parse abandoned")
	}}
	change := edit("app/m.py", 1, 1)
	repo := &shaRepo{fakeRepo: fakeRepo{changes: []domain.FileChange{change}}, analyzer: a}
	_, _, err := NewStructuralDiffer(repo, a).Build(ctx, "b", "h", []domain.FileChange{change})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
}

func TestAnalysisFailureStillDegradesToAFileNode(t *testing.T) {
	a := &scriptedAnalyzer{files: map[string]domain.FileAnalysis{}, err: func(context.Context) error { return errors.New("syntax error") }}
	change := edit("app/m.py", 1, 1)
	repo := &shaRepo{fakeRepo: fakeRepo{changes: []domain.FileChange{change}}, analyzer: a}
	roots, warnings, err := NewStructuralDiffer(repo, a).Build(context.Background(), "b", "h", []domain.FileChange{change})
	if err != nil || len(roots) != 1 || roots[0].Kind != domain.KindFile || len(warnings) != 1 {
		t.Fatalf("roots %+v warnings %v err %v", roots, warnings, err)
	}
}

func findNode(roots []*domain.Node, name string) *domain.Node {
	var found *domain.Node
	for _, r := range roots {
		r.Walk(func(n *domain.Node) {
			if found == nil && n.Name == name {
				found = n
			}
		})
	}
	return found
}

func classFile(dir, module, path string, methods ...string) domain.FileAnalysis {
	pkg := domain.PackageRef{Dir: dir, Name: dir}
	u := func(kind domain.UnitKind, name, parent string, start, end int) domain.Unit {
		return domain.Unit{Package: pkg, Kind: kind, Name: name, Parent: parent, File: path, StartLine: start, EndLine: end, Hash: string(kind) + strings.TrimPrefix(name, module)}
	}
	units := []domain.Unit{u(domain.KindModule, module, "", 1, 20), u(domain.KindClass, module+".K", module, 2, 20)}
	for i, m := range methods {
		units = append(units, u(domain.KindMethod, module+".K."+m, module+".K", 3+i*2, 4+i*2))
	}
	return domain.FileAnalysis{Package: pkg, Units: units}
}

func TestModuleMovedToAnotherPackageWithARemovedMemberStaysInItsNewPackage(t *testing.T) {
	rename := domain.FileChange{Status: domain.FileRenamed, OldPath: "a/util.py", NewPath: "b/helpers.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
		{Kind: domain.LineRemoved, OldLine: 5, Text: "def n"},
		{Kind: domain.LineRemoved, OldLine: 6, Text: "pass"},
	}}}}
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"a/util.py": classFile("a", "util", "a/util.py", "m", "n")},
		map[string]domain.FileAnalysis{"b/helpers.py": classFile("b", "helpers", "b/helpers.py", "m")},
		rename)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(pathTo(roots, "helpers.K"), " > "); got != "b > helpers > helpers.K" {
		t.Errorf("the moved class belongs to its new package: %q", got)
	}
	if got := strings.Join(pathTo(roots, "util.K.n"), " > "); got != "b > helpers > helpers.K > util.K.n" {
		t.Errorf("the removed method sits in the class it was removed from: %q", got)
	}
	for _, r := range roots {
		if r.Kind == domain.KindPackage && len(r.Children) == 0 {
			t.Errorf("package %s is empty", r.Name)
		}
	}
}

func TestMemberOfARepeatedClassSitsInTheDeclarationThatContainsIt(t *testing.T) {
	file := func(body string) domain.FileAnalysis {
		return domain.FileAnalysis{Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: []domain.Unit{
			pyUnit(domain.KindModule, "m", "", 1, 12, "mod"),
			pyUnit(domain.KindClass, "m.C", "m", 3, 5, "c-if"),
			pyUnit(domain.KindMethod, "m.C.m", "m.C", 4, 5, "m-if"),
			pyUnit(domain.KindClass, "m.C", "m", 8, 10, "c-else"),
			pyUnit(domain.KindMethod, "m.C.m", "m.C", 9, 10, body),
		}}
	}
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": file("m-else-1")},
		map[string]domain.FileAnalysis{"app/m.py": file("m-else-2")},
		edit("app/m.py", 10, 10))
	if err != nil {
		t.Fatal(err)
	}
	var method, class *domain.Node
	for _, r := range roots {
		r.Walk(func(n *domain.Node) {
			if n.Name == "m.C.m" {
				method = n
			}
		})
		r.Walk(func(n *domain.Node) {
			for _, c := range n.Children {
				if c == method {
					class = n
				}
			}
		})
	}
	if method == nil || class == nil || class.Locations[0].StartLine != 8 {
		t.Fatalf("the changed method's class must be the one at lines 8-10, got %+v", class)
	}
}

// Parent names that loop, or nest absurdly deep, must not hang or crash the build.
func TestMalformedParentChainsTerminate(t *testing.T) {
	units := []domain.Unit{
		pyUnit(domain.KindClass, "A", "B", 1, 2, "a1"),
		pyUnit(domain.KindClass, "B", "A", 3, 4, "b1"),
	}
	for i := 1; i <= 70; i++ {
		units = append(units, pyUnit(domain.KindClass, fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i-1), 5, 6, fmt.Sprint(i)))
	}
	head := make([]domain.Unit, len(units))
	copy(head, units)
	head[0].Hash, head[1].Hash, head[len(head)-1].Hash = "a2", "b2", "deep"
	roots, _, err := buildTree(t, context.Background(),
		map[string]domain.FileAnalysis{"app/m.py": {Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: units}},
		map[string]domain.FileAnalysis{"app/m.py": {Package: domain.PackageRef{Dir: "app", Name: "app"}, Units: head}},
		domain.FileChange{Status: domain.FileModified, OldPath: "app/m.py", NewPath: "app/m.py", Hunks: []domain.Hunk{{Lines: []domain.DiffLine{
			{Kind: domain.LineAdded, NewLine: 1, Text: "a"}, {Kind: domain.LineAdded, NewLine: 3, Text: "b"}, {Kind: domain.LineAdded, NewLine: 5, Text: "c"},
		}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "n70"} {
		if findNode(roots, name) == nil {
			t.Errorf("%s is missing from the tree", name)
		}
	}
}
