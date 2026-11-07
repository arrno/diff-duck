package domain

import "testing"

func change() FileChange {
	return FileChange{
		Status: FileModified, OldPath: "a.go", NewPath: "a.go",
		Hunks: []Hunk{{Lines: []DiffLine{
			{Kind: LineRemoved, OldLine: 1, Text: "x"},
			{Kind: LineAdded, NewLine: 1, Text: "y"},
			{Kind: LineContext, OldLine: 2, NewLine: 2, Text: "z"},
		}}},
	}
}

func TestCoverageCompleteOnlyWhenEveryLineIsTraced(t *testing.T) {
	full := &Node{ID: "n", Locations: []SourceLocation{{Path: "a.go"}}, Diff: []FileDiff{{OldPath: "a.go", NewPath: "a.go", Lines: change().Hunks[0].Lines}}}
	if c := MeasureCoverage([]FileChange{change()}, []*Node{full}); !c.Complete() || c.ChangedLines != 2 {
		t.Fatalf("expected complete: %+v", c)
	}

	partial := &Node{ID: "n", Locations: []SourceLocation{{Path: "a.go"}}, Diff: []FileDiff{{OldPath: "a.go", NewPath: "a.go", Lines: change().Hunks[0].Lines[:1]}}}
	c := MeasureCoverage([]FileChange{change()}, []*Node{partial})
	if c.Complete() || c.TracedLines != 1 || len(c.Untraced) != 1 {
		t.Fatalf("a missing line must be detected: %+v", c)
	}

	if c := MeasureCoverage([]FileChange{change()}, nil); c.Complete() || c.TracedFiles != 0 {
		t.Fatalf("a missing file must be detected: %+v", c)
	}
}

func TestOutcomesCoverSubtreesAndRejectUnknownNodes(t *testing.T) {
	leafA := &Node{ID: "a"}
	leafB := &Node{ID: "b"}
	leafC := &Node{ID: "c"}
	root := &Node{ID: "p", Children: []*Node{leafA, leafB}}
	kept, unassigned, rejected := AssociateOutcomes([]*Node{root, leafC}, []Outcome{
		{ID: "o1", Title: "whole package", NodeIDs: []string{"p", "p", "nope"}},
		{ID: "o2", Title: "fabricated", NodeIDs: []string{"nope"}},
	})
	if len(kept) != 1 || kept[0].ID != "o1" || len(kept[0].NodeIDs) != 1 {
		t.Fatalf("kept: %+v", kept)
	}
	if len(leafA.Outcomes) != 1 || len(leafB.Outcomes) != 1 {
		t.Fatalf("a parent reference must reach its descendants: %v %v", leafA.Outcomes, leafB.Outcomes)
	}
	if len(unassigned) != 1 || unassigned[0] != "c" {
		t.Fatalf("unassigned: %v", unassigned)
	}
	if len(rejected) != 3 { // two unknown references, one dropped outcome
		t.Fatalf("rejected: %v", rejected)
	}
}

func TestRollUpCountsEachLineOnce(t *testing.T) {
	child := &Node{Diff: []FileDiff{{Lines: []DiffLine{{Kind: LineAdded}, {Kind: LineContext}}}}}
	parent := &Node{Diff: []FileDiff{{Lines: []DiffLine{{Kind: LineRemoved}}}}, Children: []*Node{child}}
	RollUpStats(parent)
	if parent.Added != 1 || parent.Removed != 1 || child.Added != 1 {
		t.Fatalf("parent +%d -%d child +%d", parent.Added, parent.Removed, child.Added)
	}
}

func TestDirectParentNotInAnOutcomeIsUnassigned(t *testing.T) {
	child := &Node{ID: "field"}
	parent := &Node{ID: "struct", Direct: true, Children: []*Node{child}}
	_, unassigned, _ := AssociateOutcomes([]*Node{parent}, []Outcome{{ID: "o1", Title: "t", NodeIDs: []string{"field"}}})
	if len(unassigned) != 1 || unassigned[0] != "struct" {
		t.Fatalf("the struct's own change must not disappear: %v", unassigned)
	}
	inherited := &Node{ID: "inherited", Children: []*Node{{ID: "f2"}}}
	_, unassigned, _ = AssociateOutcomes([]*Node{inherited}, []Outcome{{ID: "o1", Title: "t", NodeIDs: []string{"f2"}}})
	if len(unassigned) != 0 {
		t.Fatalf("a parent that did not itself change has nothing to assign: %v", unassigned)
	}
}

func TestOutcomeIDAppearsOnceAndUnassignedIsSorted(t *testing.T) {
	leaf := &Node{ID: "leaf"}
	parent := &Node{ID: "parent", Children: []*Node{leaf}}
	AssociateOutcomes([]*Node{parent}, []Outcome{{ID: "o1", Title: "t", NodeIDs: []string{"parent", "leaf"}}})
	if len(leaf.Outcomes) != 1 {
		t.Fatalf("%v", leaf.Outcomes)
	}
	_, unassigned, _ := AssociateOutcomes([]*Node{{ID: "z"}, {ID: "a"}}, nil)
	if len(unassigned) != 2 || unassigned[0] != "a" {
		t.Fatalf("%v", unassigned)
	}
}

func TestRequalifyRenamesOnlyNamesInsideTheScope(t *testing.T) {
	for _, c := range [][4]string{
		{"old", "old", "new", "new"},
		{"old.Repo.get", "old", "new", "new.Repo.get"},
		{"older.Repo", "old", "new", "older.Repo"},
		{"other.f", "old", "new", "other.f"},
		{"old.f", "", "new", "old.f"},
	} {
		if got := Requalify(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("Requalify(%q, %q, %q) = %q, want %q", c[0], c[1], c[2], got, c[3])
		}
	}
	if QualifiedName("util", "Repo") != "util.Repo" {
		t.Error("qualified names join with a dot")
	}
}
