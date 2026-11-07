package verify

import (
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

const csRepoBase = `using System;

namespace Acme.Store
{
    /// <summary>Stores items.</summary>
    public partial class Repo
    {
        public int Count { get; set; }

        public Item Find(int id)
        {
            var item = Load(id);
            return item;
        }

        public Item Find(string name) => Load(name);

        public void Obsolete()
        {
        }

        private class Cache
        {
            public void Clear() { Items = 0; }
        }
    }

    public enum Status
    {
        Active,
    }
}
`

const csRepoHead = `using System;

namespace Acme.Store
{
    /// <summary>Stores items.</summary>
    public partial class Repo
    {
        public int Count { get; set; }

        public Item Find(int id)
        {
            var item = Load(id) ?? Item.Empty;
            return item;
        }

        public Item Find(string name) => Load(name);

        public Item Find(Guid key) => Load(key.ToString());

        private class Cache
        {
            public void Clear() { Items = -1; }
        }
    }

    public enum Status
    {
        Active,
        Archived,
    }
}
`

// TestCSharpChangesBecomeCodeUnits proves B1 and B2 for C# through the real binary: each change
// is a unit node with the right name, kind, nesting, and lines, in its namespace's package;
// partial class parts in different files share the package; coverage stays complete; and a
// broken file is a plain file node with a warning.
func TestCSharpChangesBecomeCodeUnits(t *testing.T) {
	s := repo(t,
		map[string]string{
			"src/Store/Repo.cs":      csRepoBase,
			"src/Store/Repo.Save.cs": "namespace Acme.Store;\n\npublic partial class Repo\n{\n    public void Save() { Flush(); }\n}\n",
			"src/Store/Util.cs":      "namespace Acme.Store;\n\nstatic class Util { }\n",
			"src/Store/Broken.cs":    "namespace Acme.Store;\n\nclass Broken { }\n",
			"Program.cs":             "Console.WriteLine(\"a\");\n",
		},
		map[string]string{
			"src/Store/Repo.cs":      csRepoHead,
			"src/Store/Repo.Save.cs": "namespace Acme.Store;\n\npublic partial class Repo\n{\n    public void Save() { Flush(); }\n\n    public void SaveAll() { Flush(true); }\n}\n",
			"src/Store/Util.cs":      "namespace Acme.Store;\n\n// Helpers.\nstatic class Util { }\n",
			"src/Store/Broken.cs":    "namespace Acme.Store;\n\nclass Broken {\n",
			"Program.cs":             "Console.WriteLine(\"b\");\n",
			"tests/RepoTests.cs":     "using Xunit;\n\npublic class RepoTests\n{\n    [Fact]\n    public void Finds() { }\n}\n",
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
		"Repo.Find(int)":         {domain.KindMethod, domain.OpChange, "src/Store > Repo > Repo.Find(int)", 10, 14, true},
		"Repo.Find(Guid)":        {domain.KindMethod, domain.OpAdd, "src/Store > Repo > Repo.Find(Guid)", 18, 18, true},
		"Repo.Obsolete":          {domain.KindMethod, domain.OpRemove, "src/Store > Repo > Repo.Obsolete", 18, 20, true},
		"Repo.Cache":             {domain.KindClass, domain.OpChange, "src/Store > Repo > Repo.Cache", 20, 23, false},
		"Repo.Cache.Clear":       {domain.KindMethod, domain.OpChange, "src/Store > Repo > Repo.Cache > Repo.Cache.Clear", 22, 22, true},
		"Repo.SaveAll":           {domain.KindMethod, domain.OpAdd, "src/Store > Repo > Repo.SaveAll", 7, 7, true},
		"Status":                 {domain.KindEnum, domain.OpChange, "src/Store > Status", 26, 30, false},
		"Status.Archived":        {domain.KindEnumMember, domain.OpAdd, "src/Store > Status > Status.Archived", 29, 29, true},
		"<top-level statements>": {domain.KindFunction, domain.OpChange, ". > <top-level statements>", 1, 1, true},
		"RepoTests":              {domain.KindClass, domain.OpAdd, "tests > RepoTests", 3, 7, true},
		"RepoTests.Finds":        {domain.KindMethod, domain.OpAdd, "tests > RepoTests > RepoTests.Finds", 5, 6, true},
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
	for _, name := range []string{"Repo.Find(string)", "Repo.Save", "Repo.Count", "Status.Active"} {
		if n := allNamed(r, name); len(n) != 0 {
			t.Errorf("unchanged %s must not appear: %+v", name, n)
		}
	}
	if n := allNamed(r, "RepoTests.Finds"); len(n) == 1 && !n[0].Test {
		t.Error("a test file's units are tests")
	}

	// Both partial parts live in one package, named by the namespace.
	var stores []*domain.Node
	for _, root := range r.diff.Nodes {
		if root.Kind == domain.KindPackage && root.Name == "src/Store" {
			stores = append(stores, root)
		}
	}
	if len(stores) != 1 || stores[0].Signature != "package Acme.Store" {
		t.Fatalf("one package for the namespace: %+v", stores)
	}
	// Each part is a declaration of the one type: its members sit under the part in their file.
	parts := map[string]bool{}
	for _, n := range allNamed(r, "Repo") {
		if n.Kind != domain.KindClass || r.parent[n.ID] != stores[0] {
			t.Errorf("a partial part belongs to the namespace's package: %+v", n)
		}
		parts[n.Locations[0].Path] = true
	}
	if !parts["src/Store/Repo.cs"] || !parts["src/Store/Repo.Save.cs"] || len(parts) != 2 {
		t.Errorf("partial parts by file: %v", parts)
	}

	// A comment-only change is traced to the package's other lines, not to a unit.
	if n := allNamed(r, "Util"); len(n) != 0 {
		t.Errorf("a comment is not a class change: %+v", n)
	}
	regions := allNamed(r, "Util.cs")
	if len(regions) != 1 || regions[0].Kind != domain.KindRegion || regions[0].Added != 1 {
		t.Errorf("the comment belongs to a region: %+v", regions)
	}

	// B2: the broken file is a plain file node, and the warning names it.
	broken := allNamed(r, "src/Store/Broken.cs")
	if len(broken) != 1 || broken[0].Kind != domain.KindFile {
		t.Fatalf("a broken file is a file node: %+v", broken)
	}
	if !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "src/Store/Broken.cs") {
		t.Errorf("warnings %v", r.diff.Warnings)
	}
}

func TestCSharpMethodRenameIsRecognized(t *testing.T) {
	body := "    {\n        var total = 0;\n        foreach (var x in items) total += x;\n        return total;\n    }\n"
	s := repo(t,
		map[string]string{"Cart.cs": "class Cart\n{\n    int Sum()\n" + body + "}\n"},
		map[string]string{"Cart.cs": "class Cart\n{\n    int Total()\n" + body + "}\n"},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	n := allNamed(r, "Cart.Total")
	if len(n) != 1 || n[0].Op != domain.OpChange || n[0].PreviousName != "Cart.Sum" {
		t.Fatalf("a renamed method with an identical body keeps its identity: %+v", n)
	}
	if len(allNamed(r, "Cart.Sum")) != 0 {
		t.Error("the old name must not also appear as removed")
	}
}
