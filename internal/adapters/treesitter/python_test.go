package treesitter

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func analyzePy(t *testing.T, rt *Runtime, p, src string) domain.FileAnalysis {
	t.Helper()
	a, err := NewPython(rt).Analyze(context.Background(), p, []byte(src))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return a
}

func unitsByName(a domain.FileAnalysis) map[string]domain.Unit {
	out := map[string]domain.Unit{}
	for _, u := range a.Units {
		out[u.Name] = u
	}
	return out
}

const sample = `"""Module doc."""
import functools

MAX = 10
a, (b, *rest) = 1, (2, 3)


def top(x, y):
    """Add two numbers.

    Returns the sum.
    """
    return x + y


@functools.lru_cache(maxsize=None)
def cached(x):
    return x * 2


class Outer(Base, metaclass=Meta):
    """Outer doc."""

    limit: int = 3

    class Inner:
        def inner_method(self):
            pass

    @property
    def prop(self):
        return 1

    async def fetch(self, url) -> bytes:
        async with session() as s:
            return await s.get(url)

    @staticmethod
    def helper():
        def local():
            return 3
        return local()


if TYPE_CHECKING:
    def typed() -> None: ...
else:
    try:
        from fast import typed
    except ImportError:
        def typed():
            pass

type Pair[T] = tuple[T, T]


async def main():
    await top(1, 2)
`

func TestPythonUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzePy(t, newTestRuntime(t), "app/core/util.py", sample)
	if a.Package != (domain.PackageRef{Dir: "app/core", Name: "core", Hierarchical: true}) {
		t.Errorf("package %+v", a.Package)
	}
	if a.Test || a.Generated {
		t.Errorf("test=%v generated=%v", a.Test, a.Generated)
	}
	type want struct {
		kind       domain.UnitKind
		parent     string
		start, end int
	}
	wants := map[string]want{
		"util":                          {domain.KindModule, "", 1, 58},
		"util.MAX":                      {domain.KindVar, "util", 4, 4},
		"util.a":                        {domain.KindVar, "util", 5, 5},
		"util.b":                        {domain.KindVar, "util", 5, 5},
		"util.rest":                     {domain.KindVar, "util", 5, 5},
		"util.top":                      {domain.KindFunction, "util", 8, 13},
		"util.cached":                   {domain.KindFunction, "util", 16, 18},
		"util.Outer":                    {domain.KindClass, "util", 21, 42},
		"util.Outer.limit":              {domain.KindField, "util.Outer", 24, 24},
		"util.Outer.Inner":              {domain.KindClass, "util.Outer", 26, 28},
		"util.Outer.Inner.inner_method": {domain.KindMethod, "util.Outer.Inner", 27, 28},
		"util.Outer.prop":               {domain.KindMethod, "util.Outer", 30, 32},
		"util.Outer.fetch":              {domain.KindMethod, "util.Outer", 34, 36},
		"util.Outer.helper":             {domain.KindMethod, "util.Outer", 38, 42},
		"util.Pair":                     {domain.KindType, "util", 54, 54},
		"util.main":                     {domain.KindFunction, "util", 57, 58},
	}
	got := unitsByName(a)
	for name, w := range wants {
		u, ok := got[name]
		if !ok {
			t.Errorf("missing unit %s", name)
			continue
		}
		if u.Kind != w.kind || u.Parent != w.parent || u.StartLine != w.start || u.EndLine != w.end {
			t.Errorf("%s: got %s parent=%q lines %d-%d, want %s parent=%q lines %d-%d",
				name, u.Kind, u.Parent, u.StartLine, u.EndLine, w.kind, w.parent, w.start, w.end)
		}
		if u.File != "app/core/util.py" || u.Package != a.Package {
			t.Errorf("%s: file %q package %+v", name, u.File, u.Package)
		}
	}
	// local() belongs to helper; the conditional typed() appears twice, once per branch.
	if _, ok := got["util.Outer.helper.local"]; ok {
		t.Error("a function inside a function is part of it, not a unit")
	}
	var typed []string
	for _, u := range a.Units {
		if u.Name == "util.typed" {
			typed = append(typed, fmt.Sprintf("%s %s %d-%d", u.Kind, u.Parent, u.StartLine, u.EndLine))
		}
	}
	if strings.Join(typed, ", ") != "FUNCTION util 46-46, FUNCTION util 51-52" {
		t.Errorf("want both conditional definitions of typed, got %v", typed)
	}
	if len(a.Units) != len(wants)+2 {
		var names []string
		for _, u := range a.Units {
			names = append(names, u.Name)
		}
		t.Errorf("unexpected units: %v", names)
	}
}

func TestPythonSignaturesDocsAndSource(t *testing.T) {
	got := unitsByName(analyzePy(t, newTestRuntime(t), "util.py", sample))
	for name, sig := range map[string]string{
		"util.top":         "def top(x, y)",
		"util.cached":      "@functools.lru_cache(maxsize=None) def cached(x)",
		"util.Outer":       "class Outer(Base, metaclass=Meta)",
		"util.Outer.prop":  "@property def prop(self)",
		"util.Outer.fetch": "async def fetch(self, url) -> bytes",
		"util.MAX":         "MAX = 10",
		"util.Outer.limit": "limit: int = 3",
		"util":             "module util.py",
	} {
		if got[name].Signature != sig {
			t.Errorf("%s signature %q, want %q", name, got[name].Signature, sig)
		}
	}
	if d := got["util.top"].Doc; d != "Add two numbers.\n\nReturns the sum." {
		t.Errorf("docstring %q", d)
	}
	if got["util.Outer"].Doc != "Outer doc." || got["util"].Doc != "Module doc." {
		t.Errorf("docs %q %q", got["util.Outer"].Doc, got["util"].Doc)
	}
	if src := got["util.cached"].Source; !strings.HasPrefix(src, "@functools.lru_cache") || !strings.HasSuffix(src, "return x * 2") {
		t.Errorf("a unit's code includes its decorators: %q", src)
	}
}

func TestPythonPackageAndModuleNaming(t *testing.T) {
	rt := newTestRuntime(t)
	root := analyzePy(t, rt, "setup.py", "x = 1\n")
	if root.Package != (domain.PackageRef{Dir: ".", Name: "", Hierarchical: true}) || root.Units[0].Signature != "module setup.py" {
		t.Errorf("root module: %+v %q", root.Package, root.Units[0].Signature)
	}
	init := analyzePy(t, rt, "pkg/__init__.py", "")
	if init.Units[0].Name != "__init__" || init.Units[0].StartLine != 1 || init.Units[0].EndLine != 1 {
		t.Errorf("empty __init__: %+v", init.Units[0])
	}
}

func TestPythonHashIgnoresFormattingAndComments(t *testing.T) {
	rt := newTestRuntime(t)
	base := `def f(a, b):
    return g(a, "x", 0xff)
`
	same := map[string]string{
		"comments":            "def f(a, b):  # note\n    # explain\n    return g(a, \"x\", 0xff)\n",
		"whitespace":          "def f( a,b ):\n\n    return g( a , \"x\" , 0xff )\n",
		"quotes":              "def f(a, b):\n    return g(a, 'x', 0xff)\n",
		"trailing commas":     "def f(a, b,):\n    return g(a, \"x\", 0xff,)\n",
		"redundant parens":    "def f(a, b):\n    return (g(a, \"x\", 0xff))\n",
		"line continuation":   "def f(a, b):\n    return g(a,\n             \"x\", 0xff)\n",
		"hex digit case":      "def f(a, b):\n    return g(a, \"x\", 0XFF)\n",
		"backslash continued": "def f(a, b):\n    return \\\n        g(a, \"x\", 0xff)\n",
	}
	different := map[string]string{
		"argument":        "def f(a, b):\n    return g(b, \"x\", 0xff)\n",
		"string":          "def f(a, b):\n    return g(a, \"y\", 0xff)\n",
		"raw string":      "def f(a, b):\n    return g(a, r\"x\", 0xff)\n",
		"name":            "def h(a, b):\n    return g(a, \"x\", 0xff)\n",
		"decorator":       "@cache\ndef f(a, b):\n    return g(a, \"x\", 0xff)\n",
		"tuple not paren": "def f(a, b):\n    return (g(a, \"x\", 0xff),)\n",
	}
	hashOf := func(src string) string {
		for _, u := range analyzePy(t, rt, "m.py", src).Units {
			if u.Kind == domain.KindFunction {
				return u.Hash
			}
		}
		t.Fatalf("no function in %q", src)
		return ""
	}
	want := hashOf(base)
	for name, src := range same {
		if hashOf(src) != want {
			t.Errorf("%s changed the hash", name)
		}
	}
	for name, src := range different {
		if hashOf(src) == want {
			t.Errorf("%s did not change the hash", name)
		}
	}
}

func TestPythonContainerHashExcludesMembers(t *testing.T) {
	rt := newTestRuntime(t)
	base := unitsByName(analyzePy(t, rt, "m.py", "import os\n\nclass C:\n    x = 1\n    def m(self):\n        return 1\n"))
	method := unitsByName(analyzePy(t, rt, "m.py", "import os\n\nclass C:\n    x = 1\n    def m(self):\n        return 2\n"))
	if method["m.C"].Hash != base["m.C"].Hash || method["m"].Hash != base["m"].Hash {
		t.Error("changing a method must not change its class or module")
	}
	if method["m.C.m"].Hash == base["m.C.m"].Hash {
		t.Error("the method changed")
	}
	header := unitsByName(analyzePy(t, rt, "m.py", "import os\n\nclass C(Base):\n    x = 1\n    def m(self):\n        return 1\n"))
	if header["m.C"].Hash == base["m.C"].Hash {
		t.Error("changing the class header changes the class")
	}
	imports := unitsByName(analyzePy(t, rt, "m.py", "import sys\n\nclass C:\n    x = 1\n    def m(self):\n        return 1\n"))
	if imports["m"].Hash == base["m"].Hash || imports["m.C"].Hash != base["m.C"].Hash {
		t.Error("changing an import changes the module only")
	}
	comment := unitsByName(analyzePy(t, rt, "m.py", "# new\nimport os\n\nclass C:\n    x = 1\n    def m(self):\n        return 1\n"))
	if comment["m"].Hash != base["m"].Hash {
		t.Error("a comment is not a module change")
	}
}

func TestPythonBodyHashProvesRenamesOnly(t *testing.T) {
	rt := newTestRuntime(t)
	body := "(a):\n    b = a + 1\n    return b\n"
	old := unitsByName(analyzePy(t, rt, "m.py", "def old"+body))["m.old"]
	renamed := unitsByName(analyzePy(t, rt, "m.py", "def new"+body))["m.new"]
	if old.BodyHash == "" || old.BodyHash != renamed.BodyHash || old.Hash == renamed.Hash {
		t.Errorf("a rename keeps the body hash and changes the hash: %+v %+v", old, renamed)
	}
	asyncVersion := unitsByName(analyzePy(t, rt, "m.py", "async def new"+body))["m.new"]
	decorated := unitsByName(analyzePy(t, rt, "m.py", "@d\ndef new"+body))["m.new"]
	if asyncVersion.BodyHash == old.BodyHash || decorated.BodyHash == old.BodyHash {
		t.Error("async or decorated versions are not identical bodies")
	}
	short := unitsByName(analyzePy(t, rt, "m.py", "def f():\n    return None\n"))["m.f"]
	if short.BodyHash != "" {
		t.Error("a one-statement body cannot prove a rename")
	}
}

func TestPythonSyntaxErrorsAreErrors(t *testing.T) {
	rt := newTestRuntime(t)
	for name, src := range map[string]string{
		"unclosed paren":   "class A:\n    def f(self:\n        return 1\n\ndef g():\n    pass\n",
		"unclosed string":  "x = 'abc\n",
		"unclosed bracket": "x = (1, 2\ny = 3\n",
		"keyword as name":  "class = 3\n",
		"missing name":     "def (x):\n    pass\n",
		"bad token":        "def f():\n    return $\n",
	} {
		_, err := NewPython(rt).Analyze(context.Background(), "m.py", []byte(src))
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: want a syntax error, got %v", name, err)
		}
	}
}

func TestPythonTestAndGeneratedDetection(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]bool{
		"test_api.py": true, "pkg/api_test.py": true, "tests/helpers.py": true, "a/test/x.py": true,
		"conftest.py": true, "pkg/api.py": false, "pkg/testing.py": false, "contest.py": false,
	} {
		a := analyzePy(t, rt, p, "def f():\n    pass\n")
		if a.Test != want || a.Units[1].Test != want {
			t.Errorf("%s: test=%v want %v", p, a.Test, want)
		}
	}
	gen := analyzePy(t, rt, "api_pb2.py", "# -*- coding: utf-8 -*-\n# Generated by the protocol buffer compiler.  DO NOT EDIT!\n\"\"\"Doc.\"\"\"\nx = 1\n")
	if !gen.Generated {
		t.Error("protoc output is generated")
	}
	notGen := analyzePy(t, rt, "m.py", "x = 1\n# Generated by hand. DO NOT EDIT this line.\n")
	if notGen.Generated {
		t.Error("only the header before the first statement counts")
	}
}

func TestPythonSupports(t *testing.T) {
	p := NewPython(nil)
	if !p.Supports("a/b.py") || p.Supports("a/b.pyc") || p.Supports("a/b.go") || p.Supports("py") {
		t.Error("Supports must accept exactly .py files")
	}
}

func BenchmarkPythonAnalyze300Lines(b *testing.B) {
	rt, err := NewRuntime(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer rt.Close(context.Background())
	src := []byte(strings.Repeat(sample, 300/58+1))
	a := NewPython(rt)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Analyze(context.Background(), "m.py", src); err != nil {
			b.Fatal(err)
		}
	}
}

// Text that tree-sitter keeps in hidden tokens, such as string contents around escape
// sequences, must still count. Otherwise a real change reads as formatting.
func TestPythonHashSeesTextInStringsWithEscapesAndFormatSpecs(t *testing.T) {
	rt := newTestRuntime(t)
	hashOf := func(body string) (string, string) {
		u := unitsByName(analyzePy(t, rt, "m.py", "def f(a):\n    x = 1\n    return "+body+"\n"))["m.f"]
		return u.Hash, u.BodyHash
	}
	for name, pair := range map[string][2]string{
		"escaped string":      {`"hello\n"`, `"rm -rf /\n"`},
		"regex escapes":       {`re.compile("\d+")`, `re.compile("\w+")`},
		"format spec":         {`f"{a:>10}"`, `f"{a:<3}"`},
		"bytes escapes":       {`b"a\x00b"`, `b"zz\x00yy"`},
		"doubled braces":      {`f"{{a}}"`, `f"{{b}}"`},
		"triple quoted":       {`'''a\nb'''`, `'''zz\nb'''`},
		"space inside string": {`"a b\n"`, `"ab\n"`},
	} {
		h1, b1 := hashOf(pair[0])
		h2, b2 := hashOf(pair[1])
		if h1 == h2 || b1 == b2 {
			t.Errorf("%s: %s and %s must hash differently", name, pair[0], pair[1])
		}
	}
	if a, _ := hashOf(`"x\n"`); a != func() string { h, _ := hashOf(`'x\n'`); return h }() {
		t.Error("quote style alone is still formatting")
	}
}

// One statement binding many names must not copy the statement once per name, or a small file
// could need gigabytes.
func TestManyNamesInOneAssignmentShareItsText(t *testing.T) {
	rt := newTestRuntime(t)
	var b strings.Builder
	for i := 0; i < 16000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "a%d", i)
	}
	src := []byte(b.String() + " = 0\n")
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	a, err := NewPython(rt).Analyze(context.Background(), "m.py", src)
	goruntime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Units) != 16001 {
		t.Fatalf("units: %d", len(a.Units))
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 300<<20 {
		t.Fatalf("analyzing a %d KB file allocated %d MB", len(src)>>10, got>>20)
	}
}

func TestPythonDefinitionsInsideWithBlocksCount(t *testing.T) {
	got := unitsByName(analyzePy(t, newTestRuntime(t), "m.py", "with suppress(ImportError):\n    def f():\n        pass\n\nclass C:\n    with lock:\n        x = 1\n"))
	if got["m.f"].Kind != domain.KindFunction || got["m.f"].StartLine != 2 || got["m.C.x"].Kind != domain.KindField {
		t.Errorf("definitions in with blocks: %+v %+v", got["m.f"], got["m.C.x"])
	}
}
