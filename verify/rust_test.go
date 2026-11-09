package verify

import (
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

const rsShapesBase = `//! Shapes.
use std::fmt;

#[derive(Debug)]
pub struct Point {
    pub x: f64,
    pub y: f64,
}

impl Point {
    pub fn norm(&self) -> f64 {
        (self.x * self.x + self.y * self.y).sqrt()
    }

    pub fn obsolete(&self) -> f64 {
        0.0
    }
}

impl fmt::Display for Point {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "({}, {})", self.x, self.y)
    }
}

pub enum Shape {
    Circle(f64),
}

pub fn origin() -> Point {
    Point { x: 0.0, y: 0.0 }
}
`

const rsShapesHead = `//! Shapes.
use std::fmt;

#[derive(Debug)]
pub struct Point {
    pub x: f64,
    pub y: f64,
    pub z: f64,
}

impl Point {
    pub fn norm(&self) -> f64 {
        (self.x * self.x + self.y * self.y + self.z * self.z).sqrt()
    }
}

impl fmt::Display for Point {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "({}, {}, {})", self.x, self.y, self.z)
    }
}

pub enum Shape {
    Circle(f64),
    Square(f64),
}

pub fn origin() -> Point {
    Point { x: 0.0, y: 0.0, z: 0.0 }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn origin_is_zero() {
        assert_eq!(origin().norm(), 0.0);
    }
}
`

// TestRustChangesBecomeCodeUnits proves B1 and B2 for Rust through the real binary: each change
// is a unit node with the right name, kind, nesting, and lines; coverage stays complete; and a
// broken file is a plain file node with a warning.
func TestRustChangesBecomeCodeUnits(t *testing.T) {
	s := repo(t,
		map[string]string{
			"src/shapes.rs": rsShapesBase,
			"src/util.rs":   "pub fn unchanged() -> u8 {\n    1\n}\n",
			"src/broken.rs": "fn ok() -> u8 {\n    1\n}\n",
		},
		map[string]string{
			"src/shapes.rs": rsShapesHead,
			"src/util.rs":   "// A comment only.\npub fn unchanged() -> u8 {\n    1\n}\n",
			"src/broken.rs": "fn ok( -> u8 {\n    1\n}\n",
			"tests/it.rs":   "#[test]\nfn works() {\n    assert!(true);\n}\n",
		},
	)
	r := structure(t, s)
	checkTotals(t, s, r)

	type want struct {
		kind       domain.UnitKind
		op         domain.ChangeOp
		path       string // ancestors from the package down
		start, end int    // lines in the side the node shows
		direct     bool
	}
	wants := map[string]want{
		"shapes":                            {domain.KindModule, domain.OpChange, "src > shapes", 1, 40, true},
		"shapes.Point":                      {domain.KindStruct, domain.OpChange, "src > shapes > shapes.Point", 4, 9, false},
		"shapes.Point.z":                    {domain.KindField, domain.OpAdd, "src > shapes > shapes.Point > shapes.Point.z", 8, 8, true},
		"shapes.Point.norm()":               {domain.KindMethod, domain.OpChange, "src > shapes > shapes.Point > shapes.Point.norm()", 12, 14, true},
		"shapes.Point.obsolete()":           {domain.KindMethod, domain.OpRemove, "src > shapes > shapes.Point > shapes.Point.obsolete()", 15, 17, true},
		"shapes.Point.<fmt::Display>.fmt()": {domain.KindMethod, domain.OpChange, "src > shapes > shapes.Point > shapes.Point.<fmt::Display>.fmt()", 18, 20, true},
		"shapes.Shape":                      {domain.KindEnum, domain.OpChange, "src > shapes > shapes.Shape", 23, 26, false},
		"shapes.Shape.Square":               {domain.KindEnumMember, domain.OpAdd, "src > shapes > shapes.Shape > shapes.Shape.Square", 25, 25, true},
		"shapes.origin()":                   {domain.KindFunction, domain.OpChange, "src > shapes > shapes.origin()", 28, 30, true},
		"shapes.tests.origin_is_zero()":     {domain.KindFunction, domain.OpAdd, "src > shapes > shapes.tests.origin_is_zero()", 36, 39, true},
		"it":                                {domain.KindModule, domain.OpAdd, "tests > it", 1, 4, true},
		"it.works()":                        {domain.KindFunction, domain.OpAdd, "tests > it > it.works()", 1, 4, true},
	}
	for name, w := range wants {
		nodes := allNamed(r, name)
		if len(nodes) != 1 {
			t.Errorf("%s: want one node, got %d", name, len(nodes))
			continue
		}
		n := nodes[0]
		var trail []string
		for p := r.parent[n.ID]; p != nil; p = r.parent[p.ID] {
			trail = append([]string{p.Name}, trail...)
		}
		gotPath := strings.Join(append(trail, n.Name), " > ")
		loc := n.Locations[0]
		if n.Kind != w.kind || n.Op != w.op || gotPath != w.path || loc.StartLine != w.start || loc.EndLine != w.end || n.Direct != w.direct {
			t.Errorf("%s: got %s %s %q lines %d-%d direct=%v; want %s %s %q lines %d-%d direct=%v",
				name, n.Kind, n.Op, gotPath, loc.StartLine, loc.EndLine, n.Direct, w.kind, w.op, w.path, w.start, w.end, w.direct)
		}
	}
	for _, name := range []string{"shapes.tests.origin_is_zero()", "it.works()"} {
		if n := allNamed(r, name); len(n) == 1 && !n[0].Test {
			t.Errorf("%s is a test", name)
		}
	}

	// A comment-only change is traced to the package's other lines, not to a unit.
	if n := allNamed(r, "util"); len(n) != 0 {
		t.Errorf("a comment is not a module change: %+v", n)
	}
	regions := allNamed(r, "util.rs")
	if len(regions) != 1 || regions[0].Kind != domain.KindRegion || regions[0].Added != 1 {
		t.Errorf("the comment belongs to a region: %+v", regions)
	}

	// B2: the broken file is a plain file node, and the warning names it.
	broken := allNamed(r, "src/broken.rs")
	if len(broken) != 1 || broken[0].Kind != domain.KindFile {
		t.Fatalf("a broken file is a file node: %+v", broken)
	}
	if !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "src/broken.rs") {
		t.Errorf("warnings %v", r.diff.Warnings)
	}
}

func TestRustMethodRenameIsRecognized(t *testing.T) {
	body := "(&self) -> u32 {\n        let mut total = 0;\n        for x in &self.items {\n            total += x;\n        }\n        total\n    }\n}\n"
	s := repo(t,
		map[string]string{"cart.rs": "pub struct Cart {\n    items: Vec<u32>,\n}\n\nimpl Cart {\n    pub fn sum" + body},
		map[string]string{"cart.rs": "pub struct Cart {\n    items: Vec<u32>,\n}\n\nimpl Cart {\n    pub fn total" + body},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	n := allNamed(r, "cart.Cart.total()")
	if len(n) != 1 || n[0].Op != domain.OpChange || n[0].PreviousName != "cart.Cart.sum()" {
		t.Fatalf("a renamed method with an identical body keeps its identity: %+v", n)
	}
	if len(allNamed(r, "cart.Cart.sum()")) != 0 {
		t.Error("the old name must not also appear as removed")
	}
}

// A getter often takes its field's name. Replacing a field with a getter must read as a removed
// field and an added method, never as one unit that changed kind.
func TestRustFieldReplacedByGetter(t *testing.T) {
	s := repo(t,
		map[string]string{"user.rs": "pub struct User {\n    pub name: String,\n}\n"},
		map[string]string{"user.rs": "pub struct User {\n    first: String,\n}\n\nimpl User {\n    pub fn name(&self) -> &str {\n        &self.first\n    }\n}\n"},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	for name, want := range map[string]struct {
		kind domain.UnitKind
		op   domain.ChangeOp
	}{
		"user.User.name":   {domain.KindField, domain.OpRemove},
		"user.User.first":  {domain.KindField, domain.OpAdd},
		"user.User.name()": {domain.KindMethod, domain.OpAdd},
	} {
		n := allNamed(r, name)
		if len(n) != 1 || n[0].Kind != want.kind || n[0].Op != want.op || n[0].PreviousSignature != "" {
			t.Errorf("%s: %+v, want %s %s", name, n, want.kind, want.op)
		}
	}
}
