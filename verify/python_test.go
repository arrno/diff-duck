package verify

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

const pyModelsBase = `"""Domain models."""
import dataclasses


@dataclasses.dataclass
class User:
    """An account."""

    name: str
    email: str = ""

    def greet(self):
        return "hi " + self.name

    def obsolete(self):
        return None

    class Meta:
        def table(self):
            return "users"


def make_user(name):
    user = User(name)
    return user
`

const pyModelsHead = `"""Domain models."""
import dataclasses


@dataclasses.dataclass
class User:
    """An account."""

    name: str
    email: str = ""
    active: bool = True

    def greet(self):
        return "hello " + self.name

    class Meta:
        def table(self):
            return "accounts"


def make_user(name):
    user = User(name)
    return user


async def fetch_user(client, name):
    data = await client.get(name)
    return User(**data)
`

// TestPythonChangesBecomeCodeUnits proves B1 and B2 for Python through the real binary: each
// change is a unit node with the right name, kind, nesting, and lines; coverage stays complete;
// and a broken file is a plain file node with a warning.
func TestPythonChangesBecomeCodeUnits(t *testing.T) {
	s := repo(t,
		map[string]string{
			"app/models.py": pyModelsBase,
			"app/util.py":   "def unchanged():\n    return 1\n",
			"app/broken.py": "def ok():\n    return 1\n",
		},
		map[string]string{
			"app/models.py":       pyModelsHead,
			"app/util.py":         "# A comment only.\ndef unchanged():\n    return 1\n",
			"app/broken.py":       "def ok(:\n    return 1\n",
			"app/tests/test_x.py": "def test_x():\n    assert True\n",
		},
	)
	r := structure(t, s)
	checkTotals(t, s, r)

	type want struct {
		kind       domain.UnitKind
		op         domain.ChangeOp
		path       string // ancestors from the package down
		start, end int    // lines in the side the node shows, as CPython's ast reports them
		direct     bool
	}
	wants := map[string]want{
		"models":                 {domain.KindModule, domain.OpChange, "app > models", 1, 28, false},
		"models.User":            {domain.KindClass, domain.OpChange, "app > models > models.User", 5, 18, false},
		"models.User.active":     {domain.KindField, domain.OpAdd, "app > models > models.User > models.User.active", 11, 11, true},
		"models.User.greet":      {domain.KindMethod, domain.OpChange, "app > models > models.User > models.User.greet", 13, 14, true},
		"models.User.obsolete":   {domain.KindMethod, domain.OpRemove, "app > models > models.User > models.User.obsolete", 15, 16, true},
		"models.User.Meta":       {domain.KindClass, domain.OpChange, "app > models > models.User > models.User.Meta", 16, 18, false},
		"models.User.Meta.table": {domain.KindMethod, domain.OpChange, "app > models > models.User > models.User.Meta > models.User.Meta.table", 17, 18, true},
		"models.fetch_user":      {domain.KindFunction, domain.OpAdd, "app > models > models.fetch_user", 26, 28, true},
		"test_x":                 {domain.KindModule, domain.OpAdd, "app > app/tests > test_x", 1, 2, true},
		"test_x.test_x":          {domain.KindFunction, domain.OpAdd, "app > app/tests > test_x > test_x.test_x", 1, 2, true},
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
	if n := allNamed(r, "models.make_user"); len(n) != 0 {
		t.Errorf("an unchanged function must not appear: %+v", n)
	}
	if n := allNamed(r, "test_x.test_x"); len(n) == 1 && !n[0].Test {
		t.Error("a test file's units are tests")
	}

	// A comment-only change is traced to the package's other lines, not to a unit.
	if n := allNamed(r, "util"); len(n) != 0 {
		t.Errorf("a comment is not a module change: %+v", n)
	}
	regions := allNamed(r, "util.py")
	if len(regions) != 1 || regions[0].Kind != domain.KindRegion || regions[0].Added != 1 {
		t.Errorf("the comment belongs to a region: %+v", regions)
	}

	// B2: the broken file is a plain file node, and the warning names it.
	broken := allNamed(r, "app/broken.py")
	if len(broken) != 1 || broken[0].Kind != domain.KindFile {
		t.Fatalf("a broken file is a file node: %+v", broken)
	}
	if !strings.Contains(strings.Join(r.diff.Warnings, "\n"), "app/broken.py") {
		t.Errorf("warnings %v", r.diff.Warnings)
	}
}

func TestPythonMethodRenameIsRecognized(t *testing.T) {
	body := "        total = 0\n        for x in self.items:\n            total += x\n        return total\n"
	s := repo(t,
		map[string]string{"cart.py": "class Cart:\n    def sum(self):\n" + body},
		map[string]string{"cart.py": "class Cart:\n    def total(self):\n" + body},
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

// The app analyzes Python too: a diff requested through the served app breaks a Python change
// into units rather than showing a plain file.
func TestServedAppAnalyzesPython(t *testing.T) {
	s := repo(t,
		map[string]string{"pkg/shop.py": "class Cart:\n    def total(self):\n        return 0\n"},
		map[string]string{"pkg/shop.py": "class Cart:\n    def total(self):\n        return 1\n"},
	)
	c := newAppCLI(t)
	url := appURL.FindString(c.must("up", "--adapter", "scripted:normal", "--no-cache", "--port", "0"))
	if url == "" {
		t.Fatal("up printed no address")
	}
	out := c.must("diff", s.dir, "-f", "base", "-t", "head", "--no-open", "--wait")
	if !strings.Contains(out, "Done: ") {
		t.Fatalf("diff --wait: %q", out)
	}
	diffs := c.list()
	if len(diffs) != 1 {
		t.Fatalf("diffs: %+v", diffs)
	}
	resp, err := http.Get(url + "api/diffs/" + diffs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{`"kind":"MODULE"`, `"kind":"CLASS"`, `"name":"shop.Cart.total"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("served diff lacks %s: %.400s", want, body)
		}
	}
}
