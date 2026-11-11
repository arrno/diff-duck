package treesitter

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"sort"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

func analyzeCs(t *testing.T, rt *Runtime, p, src string) domain.FileAnalysis {
	t.Helper()
	a, err := NewCSharp(rt).Analyze(context.Background(), p, []byte(src))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return a
}

func unitNames(a domain.FileAnalysis) []string {
	var names []string
	for _, u := range a.Units {
		names = append(names, u.Name)
	}
	sort.Strings(names)
	return names
}

const csSample = `using System;
using System.Collections.Generic;

namespace Acme.Store
{
    /// <summary>
    /// Stores items.
    /// </summary>
    [Serializable]
    public partial class Repo<T> : IRepo where T : class
    {
        private int count, limit = 10;
        public const string Name = "repo";
        public event EventHandler Changed;

        public int Count { get; private set; }

        public T this[int index] => items[index];

        static Repo() { }

        public Repo(int limit)
        {
            this.limit = limit;
        }

        ~Repo() { }

        public void Add(T item)
        {
            items.Add(item);
            count++;
        }

        public void Add(IEnumerable<T> items, bool replace) { }

        public static Repo<T> operator +(Repo<T> a, Repo<T> b) => a;

        public static implicit operator int(Repo<T> r) => r.count;

        void IRepo.Clear() { }

        public delegate void Handler(T item);

        private class Node
        {
            public T Value;

            public Node Next() => null;
        }
    }

    public interface IRepo
    {
        void Clear();
        int Size { get; }
        void Log() { }
    }

    public enum Color : byte
    {
        Red = 1,
        Green,
    }

    public record Point(int X, int Y);

    public struct Pair
    {
        public int A;
        public int B;
    }
}
`

func TestCSharpUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzeCs(t, newTestRuntime(t), "src/Store/Repo.cs", csSample)
	pkg := domain.PackageRef{Dir: "src/Store", Name: "Acme.Store", Hierarchical: true}
	if a.Package != pkg {
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
		"Repo":                                {domain.KindClass, "", 9, 51},
		"Repo.count":                          {domain.KindField, "Repo", 12, 12},
		"Repo.limit":                          {domain.KindField, "Repo", 12, 12},
		"Repo.Name":                           {domain.KindConst, "Repo", 13, 13},
		"Repo.Changed":                        {domain.KindField, "Repo", 14, 14},
		"Repo.Count":                          {domain.KindProperty, "Repo", 16, 16},
		"Repo.this[int]":                      {domain.KindProperty, "Repo", 18, 18},
		"Repo.static Repo()":                  {domain.KindMethod, "Repo", 20, 20},
		"Repo.Repo(int)":                      {domain.KindMethod, "Repo", 22, 25},
		"Repo.~Repo()":                        {domain.KindMethod, "Repo", 27, 27},
		"Repo.Add(T)":                         {domain.KindMethod, "Repo", 29, 33},
		"Repo.Add(IEnumerable<T>, bool)":      {domain.KindMethod, "Repo", 35, 35},
		"Repo.operator +(Repo<T>, Repo<T>)":   {domain.KindMethod, "Repo", 37, 37},
		"Repo.implicit operator int(Repo<T>)": {domain.KindMethod, "Repo", 39, 39},
		"Repo.IRepo.Clear()":                  {domain.KindMethod, "Repo", 41, 41},
		"Repo.Handler":                        {domain.KindType, "Repo", 43, 43},
		"Repo.Node":                           {domain.KindClass, "Repo", 45, 50},
		"Repo.Node.Value":                     {domain.KindField, "Repo.Node", 47, 47},
		"Repo.Node.Next()":                    {domain.KindMethod, "Repo.Node", 49, 49},
		"IRepo":                               {domain.KindInterface, "", 53, 58},
		"IRepo.Clear()":                       {domain.KindInterfaceMethod, "IRepo", 55, 55},
		"IRepo.Size":                          {domain.KindProperty, "IRepo", 56, 56},
		"IRepo.Log()":                         {domain.KindMethod, "IRepo", 57, 57},
		"Color":                               {domain.KindEnum, "", 60, 64},
		"Color.Red":                           {domain.KindEnumMember, "Color", 62, 62},
		"Color.Green":                         {domain.KindEnumMember, "Color", 63, 63},
		"Point":                               {domain.KindRecord, "", 66, 66},
		"Pair":                                {domain.KindStruct, "", 68, 72},
		"Pair.A":                              {domain.KindField, "Pair", 70, 70},
		"Pair.B":                              {domain.KindField, "Pair", 71, 71},
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
		if u.File != "src/Store/Repo.cs" || u.Package != pkg || u.Test {
			t.Errorf("%s: file %q package %+v test %v", name, u.File, u.Package, u.Test)
		}
	}
	if len(a.Units) != len(wants) {
		t.Errorf("unexpected units: %v", unitNames(a))
	}
	for _, u := range a.Units {
		if u.Kind == domain.KindModule {
			t.Error("C# is a package language: no module unit")
		}
	}
}

func TestCSharpSignaturesDocsAndSource(t *testing.T) {
	got := unitsByName(analyzeCs(t, newTestRuntime(t), "Repo.cs", csSample))
	for name, sig := range map[string]string{
		"Repo":                                "[Serializable] public partial class Repo<T> : IRepo where T : class",
		"Repo.count":                          "private int count, limit = 10",
		"Repo.Count":                          "public int Count { get; private set; }",
		"Repo.this[int]":                      "public T this[int index]",
		"Repo.Repo(int)":                      "public Repo(int limit)",
		"Repo.Add(T)":                         "public void Add(T item)",
		"Repo.implicit operator int(Repo<T>)": "public static implicit operator int(Repo<T> r)",
		"Repo.Handler":                        "public delegate void Handler(T item)",
		"IRepo.Clear()":                       "void Clear()",
		"Color":                               "public enum Color : byte",
		"Color.Red":                           "Red = 1",
		"Point":                               "public record Point(int X, int Y)",
	} {
		if got[name].Signature != sig {
			t.Errorf("%s signature %q, want %q", name, got[name].Signature, sig)
		}
	}
	if d := got["Repo"].Doc; d != "Stores items." {
		t.Errorf("doc %q", d)
	}
	if got["Repo.Repo(int)"].Doc != "" {
		t.Error("only /// comments directly before a declaration are its doc")
	}
	if src := got["Repo"].Source; !strings.HasPrefix(src, "[Serializable]") || !strings.HasSuffix(src, "}") {
		t.Errorf("a type's code includes its attributes: %q", src)
	}
}

func TestCSharpNamespaces(t *testing.T) {
	rt := newTestRuntime(t)
	fileScoped := analyzeCs(t, rt, "src/App/Svc.cs", "namespace Acme.App;\n\npublic class Svc { }\n")
	if fileScoped.Package != (domain.PackageRef{Dir: "src/App", Name: "Acme.App", Hierarchical: true}) || fileScoped.Units[0].Package != fileScoped.Package {
		t.Errorf("file-scoped namespace: %+v %+v", fileScoped.Package, fileScoped.Units[0].Package)
	}
	global := analyzeCs(t, rt, "src/App/G.cs", "class G { }\n")
	if global.Package != (domain.PackageRef{Dir: "src/App", Name: "App", Hierarchical: true}) {
		t.Errorf("the global namespace is named after the directory: %+v", global.Package)
	}
	root := analyzeCs(t, rt, "G.cs", "class G { }\n")
	if root.Package != (domain.PackageRef{Dir: ".", Hierarchical: true}) {
		t.Errorf("the global namespace at the root: %+v", root.Package)
	}

	// Each unit keeps its own namespace; the file's package is the first one it declares.
	multi := analyzeCs(t, rt, "lib/M.cs", `namespace Outer
{
    namespace Inner { class A { } }
    class B { }
}
namespace Other.Ns { class C { void M() { } } }
class D { }
`)
	if multi.Package != (domain.PackageRef{Dir: "lib", Name: "Outer", Hierarchical: true}) {
		t.Errorf("file package %+v", multi.Package)
	}
	for name, ns := range map[string]string{"A": "Outer.Inner", "B": "Outer", "C": "Other.Ns", "C.M()": "Other.Ns", "D": "lib"} {
		if u := unitsByName(multi)[name]; u.Package != (domain.PackageRef{Dir: "lib", Name: ns, Hierarchical: true}) {
			t.Errorf("%s: package %+v, want %s", name, u.Package, ns)
		}
	}
}

// Partial class parts in different files of one directory share their package and type name,
// so the differ treats them as one type.
func TestCSharpPartialClassesShareIdentity(t *testing.T) {
	rt := newTestRuntime(t)
	one := unitsByName(analyzeCs(t, rt, "src/Repo.cs", "namespace Acme;\npartial class Repo { void Load() { } }\n"))
	two := unitsByName(analyzeCs(t, rt, "src/Repo.Save.cs", "namespace Acme\n{\n    partial class Repo { void Save() { } }\n}\n"))
	if one["Repo"].Package != two["Repo"].Package || one["Repo"].Kind != domain.KindClass || two["Repo"].Kind != domain.KindClass {
		t.Errorf("partial parts: %+v %+v", one["Repo"], two["Repo"])
	}
	if one["Repo.Load()"].Parent != "Repo" || two["Repo.Save()"].Parent != "Repo" {
		t.Error("members of each part belong to the type")
	}
}

func TestCSharpOverloadNamesAreStableAndUnique(t *testing.T) {
	rt := newTestRuntime(t)
	names := func(src string) []string {
		return unitNames(analyzeCs(t, rt, "m.cs", "class C {\n"+src+"\n}\n"))
	}
	a := names("void F(int x) { }\nvoid F(string s, ref Dictionary<string,List<int>> d) { }\nvoid F<T>(T t) { }\nvoid G() { }")
	want := []string{"C", "C.F(int)", "C.F(string, ref Dictionary<string, List<int>>)", "C.F<T>(T)", "C.G()"}
	if strings.Join(a, " | ") != strings.Join(want, " | ") {
		t.Errorf("overloads %v, want %v", a, want)
	}
	reordered := names("void G() { }\nvoid F<T>(T t) { }\nvoid F( string  s , ref Dictionary< string , List<int> > d ) { }\nvoid F(int y) { }")
	if strings.Join(reordered, " | ") != strings.Join(want, " | ") {
		t.Errorf("order and formatting must not change names: %v", reordered)
	}
	ctors := names("C() { }\nC(int x) { }\nstatic C() { }\npublic int this[int i] => i;\npublic int this[string s] => 0;")
	if strings.Join(ctors, " | ") != "C | C.C() | C.C(int) | C.static C() | C.this[int] | C.this[string]" {
		t.Errorf("constructors and indexers: %v", ctors)
	}
	tuples := names("void T((int a, string b) p) { }\nvoid T(int[] a, params int[][] b) { }\nstatic void T(this string s, out int n, [Attr] int d = -1, string e = \"x, y\") { }")
	if strings.Join(tuples, " | ") != "C | C.T((int, string)) | C.T(int[], params int[][]) | C.T(this string, out int, int, string)" {
		t.Errorf("tuple and array parameters: %v", tuples)
	}
	generic := unitNames(analyzeCs(t, rt, "m.cs", "class Box { }\nclass Box<T> { }\nclass Box<K, V> { }\n"))
	if strings.Join(generic, " | ") != "Box | Box<K, V> | Box<T>" {
		t.Errorf("generic arity: %v", generic)
	}
	conditional := names("#if NET8\nvoid H(int x) { }\n#else\nvoid H(long x) { }\n#endif")
	if strings.Join(conditional, " | ") != "C | C.H(int) | C.H(long)" {
		t.Errorf("members in #if blocks: %v", conditional)
	}
}

func TestCSharpHashIgnoresFormattingAndComments(t *testing.T) {
	rt := newTestRuntime(t)
	base := "class C {\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n}\n"
	same := map[string]string{
		"comments":         "class C {\n    // note\n    int F(int a) { /* why */\n        return G(a, \"x\", new[] { 1, 2 }); // end\n    }\n}\n",
		"whitespace":       "class C {\n    int F( int a )\n    {\n\n        return G( a,\"x\" , new[]{1,2} );\n    }\n}\n",
		"trailing comma":   "class C {\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2, });\n    }\n}\n",
		"redundant parens": "class C {\n    int F(int a) {\n        return (G(a, \"x\", new[] { 1, 2 }));\n    }\n}\n",
		"doc comment":      "class C {\n    /// <summary>Doc.</summary>\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n}\n",
		"region":           "class C {\n    #region R\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n    #endregion\n}\n",
	}
	different := map[string]string{
		"argument":  "class C {\n    int F(int a) {\n        return G(1, \"x\", new[] { 1, 2 });\n    }\n}\n",
		"string":    "class C {\n    int F(int a) {\n        return G(a, \"y\", new[] { 1, 2 });\n    }\n}\n",
		"verbatim":  "class C {\n    int F(int a) {\n        return G(a, @\"x\", new[] { 1, 2 });\n    }\n}\n",
		"name":      "class C {\n    int H(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n}\n",
		"attribute": "class C {\n    [Pure]\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n}\n",
		"modifier":  "class C {\n    static int F(int a) {\n        return G(a, \"x\", new[] { 1, 2 });\n    }\n}\n",
		"element":   "class C {\n    int F(int a) {\n        return G(a, \"x\", new[] { 1, 2, 3 });\n    }\n}\n",
	}
	hashOf := func(src string) string {
		for _, u := range analyzeCs(t, rt, "m.cs", src).Units {
			if u.Kind == domain.KindMethod {
				return u.Hash
			}
		}
		t.Fatalf("no method in %q", src)
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

// Text that tree-sitter keeps in hidden tokens, or whose spaces matter, must still count.
func TestCSharpHashSeesEveryCharacterOfLiterals(t *testing.T) {
	rt := newTestRuntime(t)
	hashOf := func(expr string) (string, string) {
		u := unitsByName(analyzeCs(t, rt, "m.cs", "class C {\n    object F(int a) {\n        var x = 1;\n        return "+expr+";\n    }\n}\n"))["C.F(int)"]
		return u.Hash, u.BodyHash
	}
	for name, pair := range map[string][2]string{
		"string":               {`"a b"`, `"ab"`},
		"escapes":              {`"a\tb"`, `"a\nb"`},
		"verbatim":             {`@"a  b"`, `@"a b"`},
		"verbatim quotes":      {`@"a""b"`, `@"a"""`},
		"raw":                  {`"""a  b"""`, `"""a b"""`},
		"raw multi-line":       {"\"\"\"\n    a  b\n    \"\"\"", "\"\"\"\n    a b\n    \"\"\""},
		"interpolated":         {`$"a {a} b"`, `$"a {a}b"`},
		"interpolated escapes": {`$"a\t{a}"`, `$"a\n{a}"`},
		"format clause":        {`$"{a:N2}"`, `$"{a:N3}"`},
		"format clause spaces": {`$"{a:hh mm}"`, `$"{a:hhmm}"`},
		"alignment":            {`$"{a,5}"`, `$"{a,-5}"`},
		"raw interpolated":     {`$"""{a} x"""`, `$"""{a}x"""`},
		"verbatim interpol":    {`$@"{a} \d"`, `$@"{a}\d"`},
		"braces":               {`$"{{a}}"`, `$"{{b}}"`},
		"char":                 {`' '`, `'\t'`},
		"utf8":                 {`"ab"u8`, `"a b"u8`},
		"array rank":           {`new int[,] { }`, `new int[] { }`},
	} {
		h1, b1 := hashOf(pair[0])
		h2, b2 := hashOf(pair[1])
		if h1 == h2 || b1 == b2 {
			t.Errorf("%s: %s and %s must hash differently", name, pair[0], pair[1])
		}
	}
}

func TestCSharpContainerHashExcludesMembers(t *testing.T) {
	rt := newTestRuntime(t)
	base := unitsByName(analyzeCs(t, rt, "m.cs", "class C : B {\n    int x = 1;\n    void M() { }\n}\nenum E { A, B }\n"))
	method := unitsByName(analyzeCs(t, rt, "m.cs", "class C : B {\n    int x = 1;\n    void M() { Run(); }\n}\nenum E { A, B }\n"))
	if method["C"].Hash != base["C"].Hash || method["C.M()"].Hash == base["C.M()"].Hash {
		t.Error("changing a method changes the method, not its class")
	}
	header := unitsByName(analyzeCs(t, rt, "m.cs", "[Obsolete]\nclass C : B {\n    int x = 1;\n    void M() { }\n}\nenum E { A, B }\n"))
	if header["C"].Hash == base["C"].Hash || header["C.M()"].Hash != base["C.M()"].Hash {
		t.Error("changing the class's attributes changes the class only")
	}
	member := unitsByName(analyzeCs(t, rt, "m.cs", "class C : B {\n    int x = 1;\n    void M() { }\n}\nenum E { A, B, C }\n"))
	if member["E"].Hash != base["E"].Hash {
		t.Error("adding an enum member changes the member, not the enum")
	}
	trailing := unitsByName(analyzeCs(t, rt, "m.cs", "class C : B {\n    int x = 1;\n    void M() { }\n}\nenum E { A, B, }\n"))
	if trailing["E"].Hash != base["E"].Hash || trailing["E.B"].Hash != base["E.B"].Hash {
		t.Error("a trailing comma is formatting")
	}
}

func TestCSharpTopLevelStatements(t *testing.T) {
	a := analyzeCs(t, newTestRuntime(t), "app/Program.cs", "using System;\n\nvar app = Build(args);\nConsole.WriteLine(app);\n\nstatic int Build(string[] a) => a.Length;\n\nclass Helper { }\n")
	got := unitsByName(a)
	top := got[csTopLevel]
	if top.Kind != domain.KindFunction || top.StartLine != 3 || top.EndLine != 6 || top.Package != (domain.PackageRef{Dir: "app", Name: "app", Hierarchical: true}) || top.Parent != "" {
		t.Errorf("top-level statements: %+v", top)
	}
	if got["Helper"].Kind != domain.KindClass || len(a.Units) != 2 {
		t.Errorf("units %v", unitNames(a))
	}
}

func TestCSharpSyntaxErrorsAreErrors(t *testing.T) {
	rt := newTestRuntime(t)
	for name, src := range map[string]string{
		"unclosed brace":   "namespace A { class B { void M() { }\n",
		"unclosed paren":   "class A { void M(int x { } }\n",
		"unclosed string":  "class A { string s = \"abc; }\n",
		"bad name":         "class 1A { }\n",
		"missing semi":     "class A { int x = 1 }\n",
		"stray token":      "class A { void M() { return $; } }\n",
		"bad member":       "class A { 123 }\n",
		"unbalanced #else": "class A {\n#if X\n  void M() {\n#else\n  void M() { }\n#endif\n}\n",
	} {
		_, err := NewCSharp(rt).Analyze(context.Background(), "m.cs", []byte(src))
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: want a syntax error, got %v", name, err)
		}
	}
}

func TestCSharpTestAndGeneratedDetection(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]bool{
		"src/RepoTests.cs": true, "src/RepoTest.cs": true, "Tests/Helpers.cs": true, "test/Helpers.cs": true,
		"Acme.Store.Tests/Util.cs": true, "Acme.UnitTests/Util.cs": true, "src/Repo.cs": false,
		"src/Latest.cs": false, "src/Contest.cs": false, "src/Testing/Util.cs": false,
	} {
		a := analyzeCs(t, rt, p, "class C { void M() { } }\n")
		if a.Test != want || a.Units[0].Test != want || a.Units[1].Test != want {
			t.Errorf("%s: test=%v want %v", p, a.Test, want)
		}
	}
	for _, attr := range []string{"[Fact]", "[Theory]", "[Test]", "[TestMethod]", "[Xunit.FactAttribute]", "[NUnit.Framework.TestCase(1)]"} {
		a := analyzeCs(t, rt, "src/Checks.cs", "class C {\n    "+attr+"\n    void M() { }\n}\n")
		if !a.Test || !a.Units[0].Test {
			t.Errorf("%s marks a test file", attr)
		}
	}
	if analyzeCs(t, rt, "src/C.cs", "[Serializable] class C { [Obsolete] void M() { } }\n").Test {
		t.Error("other attributes do not make tests")
	}
	for p, src := range map[string]string{
		"src/Resources.Designer.cs": "class C { }\n",
		"obj/Api.g.cs":              "class C { }\n",
		"obj/View.g.i.cs":           "class C { }\n",
		"src/Proto.cs":              "// <auto-generated>\n//     Generated by a tool.\n// </auto-generated>\nusing System;\nclass C { }\n",
		"src/Old.cs":                "//------\n// <autogenerated />\n//------\nclass C { }\n",
	} {
		if !analyzeCs(t, rt, p, src).Generated {
			t.Errorf("%s is generated", p)
		}
	}
	if analyzeCs(t, rt, "src/C.cs", "class C { }\n// <auto-generated>\n").Generated {
		t.Error("only the leading comments count")
	}
}

func TestCSharpSupports(t *testing.T) {
	c := NewCSharp(nil)
	if !c.Supports("a/b.cs") || c.Supports("a/b.csx") || c.Supports("a/b.csproj") || c.Supports("a/b.go") || c.Supports("cs") {
		t.Error("Supports must accept exactly .cs files")
	}
}

// One declaration with many names must not copy its text once per name.
func TestCSharpManyFieldsInOneDeclarationShareItsText(t *testing.T) {
	rt := newTestRuntime(t)
	var b strings.Builder
	b.WriteString("class C { int ")
	for i := 0; i < 16000; i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "a%d", i)
	}
	b.WriteString("; }\n")
	src := []byte(b.String())
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	a, err := NewCSharp(rt).Analyze(context.Background(), "m.cs", src)
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

func BenchmarkCSharpAnalyze(b *testing.B) {
	rt, err := NewRuntime(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer rt.Close(context.Background())
	src := []byte(strings.Repeat(csSample, 5))
	a := NewCSharp(rt)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := a.Analyze(context.Background(), "m.cs", src); err != nil {
			b.Fatal(err)
		}
	}
}

func TestCSharpBodyHashFollowsRenamesAndParameterTypeChanges(t *testing.T) {
	rt := newTestRuntime(t)
	body := " {\n        var b = a + 1;\n        return b;\n    }\n"
	get := func(src, name string) domain.Unit {
		t.Helper()
		u, ok := unitsByName(analyzeCs(t, rt, "m.cs", "class C {\n    "+src+body+"}\n"))[name]
		if !ok {
			t.Fatalf("no unit %s in %q", name, src)
		}
		return u
	}
	old := get("int Old(int a)", "C.Old(int)")
	if old.BodyHash == "" {
		t.Fatal("a body of two statements can prove a rename")
	}
	for name, u := range map[string]domain.Unit{
		"renamed":            get("int New(int a)", "C.New(int)"),
		"parameter type":     get("int Old(long a)", "C.Old(long)"),
		"explicit interface": get("int I.Old(int a)", "C.I.Old(int)"),
	} {
		if u.BodyHash != old.BodyHash || u.Hash == old.Hash {
			t.Errorf("%s: the body hash leaves out what names the method", name)
		}
	}
	for name, u := range map[string]domain.Unit{
		"modifier":       get("static int Old(int a)", "C.Old(int)"),
		"parameter name": get("int Old(int x)", "C.Old(int)"),
		"result":         get("long Old(int a)", "C.Old(int)"),
		"attribute":      get("[Pure] int Old(int a)", "C.Old(int)"),
	} {
		if u.BodyHash == old.BodyHash {
			t.Errorf("%s is part of the body hash", name)
		}
	}
	short := unitsByName(analyzeCs(t, rt, "m.cs", "class C {\n    int F() { return 1; }\n    int G() => 1;\n}\n"))
	if short["C.F()"].BodyHash != "" || short["C.G()"].BodyHash != "" {
		t.Error("a body of one statement or expression cannot prove a rename")
	}
	ctorOld := unitsByName(analyzeCs(t, rt, "m.cs", "class Old {\n    Old(int a)"+body+"}\n"))["Old.Old(int)"]
	ctorNew := unitsByName(analyzeCs(t, rt, "m.cs", "class New {\n    New(int a)"+body+"}\n"))["New.New(int)"]
	if ctorOld.BodyHash == "" || ctorOld.BodyHash != ctorNew.BodyHash {
		t.Error("a constructor follows its class's rename")
	}
	method := unitsByName(analyzeCs(t, rt, "m.cs", "class New {\n    void New2(int a)"+body+"}\n"))["New.New2(int)"]
	if method.BodyHash == ctorNew.BodyHash {
		t.Error("a method never matches a constructor")
	}
}

// Every callable carries its parameters, so adding or removing an overload renames nothing
// else, and a method never shares a name with a property, field, event, or nested type.
func TestCSharpNamesDoNotDependOnSiblings(t *testing.T) {
	rt := newTestRuntime(t)
	one := unitsByName(analyzeCs(t, rt, "m.cs", "class C {\n    int Find(int id) => id;\n}\n"))
	two := unitsByName(analyzeCs(t, rt, "m.cs", "class C {\n    int Find(int id) => id;\n    int Find(string s) => 0;\n}\n"))
	if _, ok := one["C.Find(int)"]; !ok || one["C.Find(int)"].Hash != two["C.Find(int)"].Hash {
		t.Errorf("adding an overload leaves its sibling's name and hash: %v / %v", unitNames(analyzeCs(t, rt, "m.cs", "class C {\n    int Find(int id) => id;\n}\n")), two)
	}
	mixed := analyzeCs(t, rt, "m.cs", `class C {
    int Size { get; }
    int Size() => 0;
    int Count;
    int Count() => 0;
    event Action Changed;
    void Changed() { }
    class Node { }
    void Node() { }
}
`)
	kinds := map[string]domain.UnitKind{}
	for _, u := range mixed.Units {
		if _, dup := kinds[u.Name]; dup {
			t.Errorf("two units named %s", u.Name)
		}
		kinds[u.Name] = u.Kind
	}
	for name, kind := range map[string]domain.UnitKind{
		"C.Size": domain.KindProperty, "C.Size()": domain.KindMethod, "C.Count": domain.KindField, "C.Count()": domain.KindMethod,
		"C.Changed": domain.KindField, "C.Changed()": domain.KindMethod, "C.Node": domain.KindClass, "C.Node()": domain.KindMethod,
	} {
		if kinds[name] != kind {
			t.Errorf("%s: kind %q, want %s", name, kinds[name], kind)
		}
	}
	nullable := unitNames(analyzeCs(t, rt, "m.cs", "class C {\n    void F(int? a, string? b, (int x, string y) t) { }\n}\n"))
	if strings.Join(nullable, " | ") != "C | C.F(int?, string?, (int, string))" {
		t.Errorf("nullable annotations stay, tuple element names go: %v", nullable)
	}
	explicit := unitNames(analyzeCs(t, rt, "m.cs", "class C : IList, IOps<C> {\n    int this[int i] => i;\n    int IList.this[int i] => i;\n    static C IOps<C>.operator +(C a, C b) => a;\n    public static C operator +(C a, C b) => a;\n}\n"))
	if strings.Join(explicit, " | ") != "C | C.IList.this[int] | C.IOps<C>.operator +(C, C) | C.operator +(C, C) | C.this[int]" {
		t.Errorf("explicit interface indexers and operators: %v", explicit)
	}
}

func TestCSharpMoreMemberKinds(t *testing.T) {
	got := unitsByName(analyzeCs(t, newTestRuntime(t), "m.cs", `class C {
    event EventHandler E { add { } remove { } }
    int P { get { return x; } }
#if A
    void M1() { }
#elif B
    void M2() { }
#endif
    int count, limit;
}
`))
	if u := got["C.E"]; u.Kind != domain.KindProperty || u.StartLine != 2 || u.EndLine != 2 {
		t.Errorf("an event with accessors is a property: %+v", u)
	}
	if got["C.P"].Signature != "int P" {
		t.Errorf("accessor bodies are not signature: %q", got["C.P"].Signature)
	}
	if got["C.M2()"].Kind != domain.KindMethod || got["C.M1()"].Kind != domain.KindMethod {
		t.Errorf("members in #if and #elif arms: %v", got)
	}
	if got["C.count"].Hash == got["C.limit"].Hash {
		t.Error("fields of one declaration have their own hashes")
	}
}

func TestCSharpContainerHashExcludesEveryMemberKind(t *testing.T) {
	rt := newTestRuntime(t)
	src := "class C {\n    int x = 1;\n    int P => 1;\n    event Action E;\n    delegate void D(int a);\n    class N { int y; }\n    void M() { }\n}\n"
	base := unitsByName(analyzeCs(t, rt, "m.cs", src))
	for name, change := range map[string][2]string{
		"C.x":   {"int x = 1;", "int x = 2;"},
		"C.P":   {"int P => 1;", "int P => 2;"},
		"C.E":   {"event Action E;", "event Func<int> E;"},
		"C.D":   {"delegate void D(int a);", "delegate void D(long a);"},
		"C.N":   {"class N { int y; }", "class N : B { int y; }"},
		"C.N.y": {"int y;", "long y;"},
		"C.M()": {"void M() { }", "void M() { Run(); }"},
	} {
		got := unitsByName(analyzeCs(t, rt, "m.cs", strings.Replace(src, change[0], change[1], 1)))
		if got["C"].Hash != base["C"].Hash {
			t.Errorf("changing %s changed its class", name)
		}
		if got[name].Hash == base[name].Hash {
			t.Errorf("changing %s did not change it", name)
		}
	}
}

// An enum member's value depends on its position when it has no value of its own.
func TestCSharpEnumImplicitValues(t *testing.T) {
	rt := newTestRuntime(t)
	enum := func(body string) map[string]domain.Unit {
		return unitsByName(analyzeCs(t, rt, "m.cs", "enum E { "+body+" }\n"))
	}
	base := enum("Red, Green, Blue = 10, Pink")
	reordered := enum("Green, Red, Blue = 10, Pink")
	if reordered["E.Red"].Hash == base["E.Red"].Hash || reordered["E.Green"].Hash == base["E.Green"].Hash {
		t.Error("swapping implicit members swaps their values")
	}
	if reordered["E.Blue"].Hash != base["E.Blue"].Hash || reordered["E.Pink"].Hash != base["E.Pink"].Hash || reordered["E"].Hash != base["E"].Hash {
		t.Error("members whose values did not change are unchanged")
	}
	inserted := enum("Black, Red, Green, Blue = 10, Pink")
	if inserted["E.Red"].Hash == base["E.Red"].Hash || inserted["E.Green"].Hash == base["E.Green"].Hash || inserted["E.Pink"].Hash != base["E.Pink"].Hash {
		t.Error("inserting a member shifts the implicit values after it, up to the next explicit one")
	}
	renumbered := enum("Red, Green, Blue = 20, Pink")
	if renumbered["E.Pink"].Hash == base["E.Pink"].Hash {
		t.Error("a member after an explicit value follows that value")
	}
	if base["E.Red"].ChangeNote == "" || base["E.Blue"].ChangeNote != "" {
		t.Error("only implicit members explain a value change they cannot show")
	}
}

func TestCSharpDocsBelongToTheirDeclaration(t *testing.T) {
	got := unitsByName(analyzeCs(t, newTestRuntime(t), "m.cs", `class C {
    /// The a.
    int a;
    int b;
    /// Not c.
    // plain
    int c;
    // plain
    int d;
    /// Runs.
    /// Twice.
    void M() { }
}
/// Colors.
enum E {
    /// The red.
    Red,
}
`))
	for name, doc := range map[string]string{"C.a": "The a.", "C.b": "", "C.c": "", "C.d": "", "C.M()": "Runs.\nTwice.", "E": "Colors.", "E.Red": "The red."} {
		if got[name].Doc != doc {
			t.Errorf("%s doc %q, want %q", name, got[name].Doc, doc)
		}
	}
}

func TestCSharpTopLevelStatementsCoverEveryStatement(t *testing.T) {
	rt := newTestRuntime(t)
	one := unitsByName(analyzeCs(t, rt, "Program.cs", "var a = 1;\nConsole.WriteLine(a);\n"))[csTopLevel]
	two := unitsByName(analyzeCs(t, rt, "Program.cs", "var a = 1;\nConsole.WriteLine(a + 1);\n"))[csTopLevel]
	if one.Hash == two.Hash {
		t.Error("a change to the last statement changes the unit")
	}
	if one.Source != "var a = 1;\nConsole.WriteLine(a);" {
		t.Errorf("source %q", one.Source)
	}
}

func TestCSharpMoreTestAndGeneratedConventions(t *testing.T) {
	rt := newTestRuntime(t)
	for p, src := range map[string]string{
		"obj/X.generated.cs":              "class C { }\n",
		"obj/TemporaryGeneratedFile_1.cs": "class C { }\n",
		"src/A.cs":                        "// <Auto-Generated />\nclass C { }\n",
	} {
		if !analyzeCs(t, rt, p, src).Generated {
			t.Errorf("%s is generated", p)
		}
	}
	if !analyzeCs(t, rt, "Acme.Test/Util.cs", "class C { }\n").Test {
		t.Error("a *.Test project holds tests")
	}
}

// Units of nested types slice one copy of the file, and qualified names are bounded, so a file
// under the size limit cannot make the analysis hold gigabytes.
func TestCSharpNestedTypesDoNotMultiplyMemory(t *testing.T) {
	rt := newTestRuntime(t)
	measure := func(src string) (domain.FileAnalysis, error, uint64) {
		var before, after goruntime.MemStats
		goruntime.GC()
		goruntime.ReadMemStats(&before)
		a, err := NewCSharp(rt).Analyze(context.Background(), "m.cs", []byte(src))
		goruntime.ReadMemStats(&after)
		return a, err, after.TotalAlloc - before.TotalAlloc
	}
	const depth = 900
	open := strings.Repeat("class C{", depth)
	closing := strings.Repeat("}", depth)
	literal := strings.Repeat("x", 1<<20)
	a, err, alloc := measure(open + `string s = "` + literal + `";` + closing)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Units) != depth+1 {
		t.Fatalf("units: %d", len(a.Units))
	}
	if alloc > 300<<20 {
		t.Errorf("a %d KB file allocated %d MB", (len(open)+len(literal))>>10, alloc>>20)
	}
	longName := strings.Repeat("N", 1500)
	_, err, alloc = measure(strings.Repeat("class "+longName+"{", depth) + strings.Repeat("}", depth))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("names that grow far beyond the file are refused: %v", err)
	}
	if alloc > 300<<20 {
		t.Errorf("refusing long names allocated %d MB", alloc>>20)
	}
}
