package services

import (
	"context"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

// findNodes returns every node with the name, in tree order.
func findNodes(roots []*domain.Node, name string) []*domain.Node {
	var found []*domain.Node
	for _, r := range roots {
		r.Walk(func(n *domain.Node) {
			if n.Name == name {
				found = append(found, n)
			}
		})
	}
	return found
}

// When a name is shared by units of different kinds, such as a field and a method, units are
// paired only with their own kind, even when content and position would pair them otherwise.
func TestDuplicateNamesNeverPairUnitsOfDifferentKinds(t *testing.T) {
	unit := func(kind domain.UnitKind, sig, hash string, start int) domain.Unit {
		u := pyUnit(kind, "m.C.x", "m.C", start, start+1, hash)
		u.Signature = sig
		return u
	}
	file := func(units ...domain.Unit) map[string]domain.FileAnalysis {
		module := pyUnit(domain.KindModule, "m", "", 1, 20, "mod")
		class := pyUnit(domain.KindClass, "m.C", "m", 2, 20, "class")
		return map[string]domain.FileAnalysis{"app/m.py": {Package: domain.PackageRef{Dir: "app", Name: "app"},
			Units: append([]domain.Unit{module, class}, units...)}}
	}
	// Both changed, and the method now comes first, so pairing by file alone would match the
	// old field with the new method.
	roots, _, err := buildTree(t, context.Background(),
		file(unit(domain.KindField, "x = 1", "f1", 3), unit(domain.KindMethod, "def x(self)", "m1", 5)),
		file(unit(domain.KindMethod, "def x(self, y)", "m2", 3), unit(domain.KindField, "x = 2", "f2", 6)),
		edit("app/m.py", 3, 3))
	if err != nil {
		t.Fatal(err)
	}
	nodes := findNodes(roots, "m.C.x")
	if len(nodes) != 2 {
		t.Fatalf("want the field and the method, got %d nodes", len(nodes))
	}
	want := map[domain.UnitKind]string{domain.KindField: "x = 1", domain.KindMethod: "def x(self)"}
	for _, n := range nodes {
		if n.Op != domain.OpChange || n.PreviousSignature != want[n.Kind] {
			t.Errorf("%s %s paired with %q, want its own earlier version %q", n.Op, n.Kind, n.PreviousSignature, want[n.Kind])
		}
	}
}

// A unit that moves to another namespace in the same directory, as in C#, is reported as moved
// from that namespace, not from the directory both share.
func TestMoveBetweenPackagesInOneDirectoryNamesThePackage(t *testing.T) {
	file := func(pkg string) map[string]domain.FileAnalysis {
		ref := domain.PackageRef{Dir: "app", Name: pkg}
		return map[string]domain.FileAnalysis{"app/m.py": {Package: ref, Units: []domain.Unit{{
			Package: ref, Kind: domain.KindMethod, Name: "Svc.Run()", File: "app/m.py",
			StartLine: 1, EndLine: 5, Hash: "h-" + pkg, BodyHash: "body", Signature: "void Run()",
		}}}}
	}
	roots, _, err := buildTree(t, context.Background(), file("Shop.Old"), file("Shop.New"), edit("app/m.py", 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	n := findNode(roots, "Svc.Run()")
	if n == nil || n.Op != domain.OpChange || !n.Moved {
		t.Fatalf("a move with an identical body: %+v", n)
	}
	if n.Summary.Text != "Moved from package Shop.Old with an identical body." || n.PreviousName != "Shop.Old.Svc.Run()" {
		t.Errorf("summary %q, previous name %q", n.Summary.Text, n.PreviousName)
	}
}

// A unit leaving the root directory's unnamed package (C#'s global namespace) says so, rather
// than naming the directory "." or a previous name with an empty package.
func TestMoveOutOfTheUnnamedPackageSaysSo(t *testing.T) {
	file := func(pkg string) map[string]domain.FileAnalysis {
		ref := domain.PackageRef{Dir: ".", Name: pkg}
		return map[string]domain.FileAnalysis{"m.py": {Package: ref, Units: []domain.Unit{{
			Package: ref, Kind: domain.KindMethod, Name: "Svc.Run()", File: "m.py",
			StartLine: 1, EndLine: 5, Hash: "h-" + pkg, BodyHash: "body",
		}}}}
	}
	roots, _, err := buildTree(t, context.Background(), file(""), file("Shop"), edit("m.py", 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	n := findNode(roots, "Svc.Run()")
	if n == nil || !n.Moved || n.Summary.Text != "Moved from the unnamed package with an identical body." || n.PreviousName != "Svc.Run()" {
		t.Fatalf("move out of the unnamed package: %+v", n)
	}
}

// A move between directories still names the directory the unit left.
func TestMoveBetweenDirectoriesNamesTheDirectory(t *testing.T) {
	side := func(dir string) map[string]domain.FileAnalysis {
		ref := domain.PackageRef{Dir: dir, Name: "util"}
		p := dir + "/m.py"
		return map[string]domain.FileAnalysis{p: {Package: ref, Units: []domain.Unit{{
			Package: ref, Kind: domain.KindFunction, Name: "run", File: p,
			StartLine: 1, EndLine: 5, Hash: "h-" + dir, BodyHash: "body",
		}}}}
	}
	roots, _, err := buildTree(t, context.Background(), side("old/util"), side("new/util"),
		domain.FileChange{Status: domain.FileDeleted, OldPath: "old/util/m.py"},
		domain.FileChange{Status: domain.FileAdded, NewPath: "new/util/m.py"})
	if err != nil {
		t.Fatal(err)
	}
	n := findNode(roots, "run")
	if n == nil || n.Summary.Text != "Moved from package old/util with an identical body." || n.PreviousName != "old/util.run" {
		t.Fatalf("move between directories: %+v", n)
	}
}
