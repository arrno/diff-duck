package verify

import (
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

const javaRepoBase = `package com.shop;

import java.util.List;

/** Stores carts. */
public class CartRepo {
    private int size;

    public CartRepo() {}

    public int count() {
        return size;
    }

    public void obsolete() {
        size = 0;
    }

    static class Cache {
        void clear() {
            size = 0;
        }
    }

    enum Mode { FAST, SAFE }
}
`

const javaRepoHead = `package com.shop;

import java.util.List;

/** Stores carts. */
public class CartRepo {
    private int size;
    private String owner = "me";

    public CartRepo() {}

    public CartRepo(String owner) {
        this.owner = owner;
    }

    public int count() {
        return size + 1;
    }

    static class Cache {
        void clear() {
            size = -1;
        }
    }

    enum Mode { FAST, SAFE, STRICT }
}

record Item(String sku, int qty) {}
`

// TestJavaChangesBecomeCodeUnits proves B1 and B2 for Java through the real binary: each change
// is a unit node with the right name, kind, nesting, and lines under its package; coverage stays
// complete; and a broken file is a plain file node with a warning.
func TestJavaChangesBecomeCodeUnits(t *testing.T) {
	s := repo(t,
		map[string]string{
			"src/main/java/com/shop/CartRepo.java": javaRepoBase,
			"src/main/java/com/shop/Util.java":     "package com.shop;\n\nclass Util {\n    static int one() { return 1; }\n}\n",
			"src/main/java/com/shop/Broken.java":   "package com.shop;\n\nclass Broken {}\n",
		},
		map[string]string{
			"src/main/java/com/shop/CartRepo.java":     javaRepoHead,
			"src/main/java/com/shop/Util.java":         "package com.shop;\n\n// A comment only.\nclass Util {\n    static int one() { return 1; }\n}\n",
			"src/main/java/com/shop/Broken.java":       "package com.shop;\n\nclass Broken {\n",
			"src/test/java/com/shop/CartRepoTest.java": "package com.shop;\n\nclass CartRepoTest {\n    void counts() {}\n}\n",
		},
	)
	r := structure(t, s)
	checkTotals(t, s, r)

	const main, test = "src/main/java/com/shop", "src/test/java/com/shop"
	type want struct {
		kind       domain.UnitKind
		op         domain.ChangeOp
		path       string // ancestors from the package down
		start, end int    // lines in the side the node shows
		direct     bool
	}
	wants := map[string]want{
		"CartRepo":                  {domain.KindClass, domain.OpChange, main + " > CartRepo", 5, 27, false},
		"CartRepo.owner":            {domain.KindField, domain.OpAdd, main + " > CartRepo > CartRepo.owner", 8, 8, true},
		"CartRepo.CartRepo(String)": {domain.KindMethod, domain.OpAdd, main + " > CartRepo > CartRepo.CartRepo(String)", 12, 14, true},
		"CartRepo.count()":          {domain.KindMethod, domain.OpChange, main + " > CartRepo > CartRepo.count()", 16, 18, true},
		"CartRepo.obsolete()":       {domain.KindMethod, domain.OpRemove, main + " > CartRepo > CartRepo.obsolete()", 15, 17, true},
		"CartRepo.Cache":            {domain.KindClass, domain.OpChange, main + " > CartRepo > CartRepo.Cache", 20, 24, false},
		"CartRepo.Cache.clear()":    {domain.KindMethod, domain.OpChange, main + " > CartRepo > CartRepo.Cache > CartRepo.Cache.clear()", 21, 23, true},
		"CartRepo.Mode":             {domain.KindEnum, domain.OpChange, main + " > CartRepo > CartRepo.Mode", 26, 26, false},
		"CartRepo.Mode.STRICT":      {domain.KindEnumMember, domain.OpAdd, main + " > CartRepo > CartRepo.Mode > CartRepo.Mode.STRICT", 26, 26, true},
		"Item":                      {domain.KindRecord, domain.OpAdd, main + " > Item", 29, 29, true},
		"Item.sku":                  {domain.KindField, domain.OpAdd, main + " > Item > Item.sku", 29, 29, true},
		"CartRepoTest":              {domain.KindClass, domain.OpAdd, test + " > CartRepoTest", 3, 5, true},
		"CartRepoTest.counts()":     {domain.KindMethod, domain.OpAdd, test + " > CartRepoTest > CartRepoTest.counts()", 4, 4, true},
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
	pkgs := allNamed(r, main)
	if len(pkgs) != 1 || pkgs[0].Kind != domain.KindPackage || pkgs[0].Signature != "package com.shop" {
		t.Errorf("the package node carries the declared package: %+v", pkgs)
	}
	for _, name := range []string{"CartRepo.size", "CartRepo.Mode.FAST", "Util", "Util.one()", "CartRepo.CartRepo()"} {
		if n := allNamed(r, name); len(n) != 0 {
			t.Errorf("an unchanged unit must not appear: %s", name)
		}
	}
	if n := allNamed(r, "CartRepoTest.counts()"); len(n) == 1 && !n[0].Test {
		t.Error("a test file's units are tests")
	}

	// A comment-only change is traced to the file's other lines, not to a unit.
	regions := allNamed(r, "Util.java")
	if len(regions) != 1 || regions[0].Kind != domain.KindRegion || regions[0].Added != 1 {
		t.Errorf("the comment belongs to a region: %+v", regions)
	}

	// B2: the broken file is a plain file node, and the warning names it.
	broken := allNamed(r, "src/main/java/com/shop/Broken.java")
	if len(broken) != 1 || broken[0].Kind != domain.KindFile {
		t.Fatalf("a broken file is a file node: %+v", broken)
	}
	if !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "src/main/java/com/shop/Broken.java") {
		t.Errorf("warnings %v", r.diff.Warnings)
	}
}

// A field and a method may share a name in Java. Removing the field and changing the method must
// report exactly that, never pair one with the other.
func TestJavaFieldAndMethodWithOneNameStayApart(t *testing.T) {
	s := repo(t,
		map[string]string{"Person.java": "class Person {\n    private String name;\n\n    String name() {\n        return name;\n    }\n}\n"},
		map[string]string{"Person.java": "class Person {\n    String name() {\n        return \"x\";\n    }\n}\n"},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	field, method := allNamed(r, "Person.name"), allNamed(r, "Person.name()")
	if len(field) != 1 || field[0].Kind != domain.KindField || field[0].Op != domain.OpRemove {
		t.Errorf("the field is removed: %+v", field)
	}
	if len(method) != 1 || method[0].Kind != domain.KindMethod || method[0].Op != domain.OpChange {
		t.Errorf("the method is changed, not removed: %+v", method)
	}
}

func TestJavaMethodRenameIsRecognized(t *testing.T) {
	body := "(int[] xs) {\n        int t = 0;\n        for (int x : xs) t += x;\n        return t;\n    }\n"
	s := repo(t,
		map[string]string{"Cart.java": "class Cart {\n    int sum" + body + "}\n"},
		map[string]string{"Cart.java": "class Cart {\n    int total" + body + "}\n"},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	n := allNamed(r, "Cart.total(int[])")
	if len(n) != 1 || n[0].Op != domain.OpChange || n[0].PreviousName != "Cart.sum(int[])" {
		t.Fatalf("a renamed method with an identical body keeps its identity: %+v", n)
	}
	if len(allNamed(r, "Cart.sum(int[])")) != 0 {
		t.Error("the old name must not also appear as removed")
	}
}
