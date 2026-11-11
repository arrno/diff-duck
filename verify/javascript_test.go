package verify

import (
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

const jsCartBase = `import { api } from "./api";

export class Cart {
  items = [];

  add(item) {
    this.items.push(item);
  }

  clear() {
    this.items = [];
  }
}

export const total = (cart) => {
  let sum = 0;
  return sum;
};
`

const jsCartHead = `import { api } from "./api";

export class Cart {
  items = [];
  owner = null;

  add(item) {
    this.items.push(item);
    return this;
  }
}

export const total = (cart) => {
  let sum = 0;
  return sum;
};

export async function load(id) {
  const data = await api(id);
  return new Cart(data);
}
`

const tsTypesBase = `export interface User {
  name: string;
}

export enum Role {
  Admin,
  Guest,
}
`

const tsTypesHead = `export interface User {
  name: string;
  email?: string;
}

export enum Role {
  Admin,
  Guest,
  Owner,
}

export type Id = string | number;
`

const tsxAppHead = `import { Cart } from "./cart";

export function App({ cart }: { cart: Cart }) {
  return <p>{cart.items.length} items</p>;
}
`

// TestJavaScriptAndTypeScriptChangesBecomeCodeUnits proves B1 and B2 for JavaScript and
// TypeScript (including TSX) through the real binary: each change is a unit node with the right
// name, kind, nesting, and lines; coverage stays complete; and a broken file in either language
// is a plain file node with a warning.
func TestJavaScriptAndTypeScriptChangesBecomeCodeUnits(t *testing.T) {
	s := repo(t,
		map[string]string{
			"web/cart.js":   jsCartBase,
			"web/types.ts":  tsTypesBase,
			"web/util.mjs":  "export function same() {\n  return 1;\n}\n",
			"web/broken.js": "export function ok() {\n  return 1;\n}\n",
			"web/broken.ts": "export const n: number = 1;\n",
		},
		map[string]string{
			"web/cart.js":                jsCartHead,
			"web/types.ts":               tsTypesHead,
			"web/App.tsx":                tsxAppHead,
			"web/util.mjs":               "// A comment only.\nexport function same() {\n  return 1;\n}\n",
			"web/broken.js":              "export function ok( {\n  return 1;\n}\n",
			"web/broken.ts":              "export const n: = 1;\n",
			"web/__tests__/cart.test.js": "test(\"adds\", () => {\n  expect(1).toBe(1);\n});\nexport const fixture = {};\n",
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
		"cart":              {domain.KindModule, domain.OpChange, "web > cart", 1, 21, false},
		"cart.Cart":         {domain.KindClass, domain.OpChange, "web > cart > cart.Cart", 3, 11, false},
		"cart.Cart.owner":   {domain.KindField, domain.OpAdd, "web > cart > cart.Cart > cart.Cart.owner", 5, 5, true},
		"cart.Cart.add":     {domain.KindMethod, domain.OpChange, "web > cart > cart.Cart > cart.Cart.add", 7, 10, true},
		"cart.Cart.clear":   {domain.KindMethod, domain.OpRemove, "web > cart > cart.Cart > cart.Cart.clear", 10, 12, true},
		"cart.load":         {domain.KindFunction, domain.OpAdd, "web > cart > cart.load", 18, 21, true},
		"types":             {domain.KindModule, domain.OpChange, "web > types", 1, 12, false},
		"types.User":        {domain.KindInterface, domain.OpChange, "web > types > types.User", 1, 4, false},
		"types.User.email":  {domain.KindField, domain.OpAdd, "web > types > types.User > types.User.email", 3, 3, true},
		"types.Role":        {domain.KindEnum, domain.OpChange, "web > types > types.Role", 6, 10, false},
		"types.Role.Owner":  {domain.KindEnumMember, domain.OpAdd, "web > types > types.Role > types.Role.Owner", 9, 9, true},
		"types.Id":          {domain.KindType, domain.OpAdd, "web > types > types.Id", 12, 12, true},
		"App":               {domain.KindModule, domain.OpAdd, "web > App", 1, 5, true},
		"App.App":           {domain.KindFunction, domain.OpAdd, "web > App > App.App", 3, 5, true},
		"cart.test":         {domain.KindModule, domain.OpAdd, "web > web/__tests__ > cart.test", 1, 4, true},
		"cart.test.fixture": {domain.KindVar, domain.OpAdd, "web > web/__tests__ > cart.test > cart.test.fixture", 4, 4, true},
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
	for _, name := range []string{"cart.total", "cart.Cart.items", "types.Role.Admin", "types.Role.Guest", "types.User.name"} {
		if n := allNamed(r, name); len(n) != 0 {
			t.Errorf("unchanged %s must not appear: %+v", name, n)
		}
	}
	if n := allNamed(r, "cart.test.fixture"); len(n) == 1 && !n[0].Test {
		t.Error("a test file's units are tests")
	}

	// A comment-only change is traced to the package's other lines, not to a unit.
	if n := allNamed(r, "util"); len(n) != 0 {
		t.Errorf("a comment is not a module change: %+v", n)
	}
	regions := allNamed(r, "util.mjs")
	if len(regions) != 1 || regions[0].Kind != domain.KindRegion || regions[0].Added != 1 {
		t.Errorf("the comment belongs to a region: %+v", regions)
	}

	// B2: each broken file is a plain file node, and the warnings name it.
	warnings := strings.Join(r.diff.Warnings, "\n")
	for _, p := range []string{"web/broken.js", "web/broken.ts"} {
		broken := allNamed(r, p)
		if len(broken) != 1 || broken[0].Kind != domain.KindFile {
			t.Errorf("a broken file is a file node: %s %+v", p, broken)
		}
		if !strings.Contains(warnings, p) {
			t.Errorf("no warning names %s: %v", p, r.diff.Warnings)
		}
	}
}

func TestTypeScriptMethodRenameIsRecognized(t *testing.T) {
	body := "    let total = 0;\n    for (const x of this.items) total += x;\n    return total;\n  }\n}\n"
	s := repo(t,
		map[string]string{"cart.ts": "export class Cart {\n  sum(): number {\n" + body},
		map[string]string{"cart.ts": "export class Cart {\n  total(): number {\n" + body},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	n := allNamed(r, "cart.Cart.total")
	if len(n) != 1 || n[0].Op != domain.OpChange || n[0].PreviousName != "cart.Cart.sum" {
		t.Fatalf("a renamed method with an identical body keeps its identity: %+v", n)
	}
	if len(allNamed(r, "cart.Cart.sum")) != 0 {
		t.Error("the old name must not also appear as removed")
	}
}

// Migrating a file from JavaScript to TypeScript keeps its units' identity: the family is one
// analyzer, so a rename across dialects is still analyzed and every unit keeps its identity.
func TestJavaScriptToTypeScriptRenameKeepsUnits(t *testing.T) {
	body := "  const data = await api(id);\n  return data;\n}\n\nexport function same() {\n  return 1;\n}\n"
	s := repo(t,
		map[string]string{"web/load.js": "export async function load(id) {\n" + body},
		map[string]string{"web/load.js": "", "web/load.ts": "export async function load(id: string) {\n" + body},
	)
	r := structure(t, s)
	checkTotals(t, s, r)
	n := allNamed(r, "load.load")
	if len(n) != 1 || n[0].Op != domain.OpChange || n[0].Kind != domain.KindFunction {
		t.Fatalf("the typed function is the same unit, changed: %+v", n)
	}
	// The unchanged function is the same unit too: moved with its file, not removed and added.
	if same := allNamed(r, "load.same"); len(same) != 1 || same[0].Op != domain.OpChange || !same[0].Moved {
		t.Errorf("an unchanged function moves with its renamed file: %+v", same)
	}
	if len(allNamed(r, "web/load.ts")) != 0 {
		t.Error("the renamed file is analyzed, not a plain file node")
	}
}
