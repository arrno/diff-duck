package treesitter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unsafe"

	"github.com/arrno/diff-duck/internal/domain"
)

func analyzeJava(t *testing.T, rt *Runtime, p, src string) domain.FileAnalysis {
	t.Helper()
	a, err := NewJava(rt).Analyze(context.Background(), p, []byte(src))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return a
}

const javaSample = `package com.example.app;

import java.util.List;

/**
 * A repository.
 *   Indented line.
 */
@SuppressWarnings("unchecked")
public class Repo<T extends Comparable<T>> extends Base implements Store {
    static final int MAX = 10, MIN = 0;
    private String name = "x", alias;
    static { init(); }

    /** Makes one. */
    public Repo() { this("r"); }
    Repo(String name) { this.name = name; }

    public List<T> find(String key) { return null; }
    public List<T> find(int a, Map<String, ? extends T> b) { return null; }
    void run(Repo this, String... args) {
        new Runnable() { public void run() {} };
        class Local { void m() {} }
    }

    class Inner {
        void close() {}
        enum Mode { ON, OFF }
    }
}

interface Store {
    int LIMIT = 5;
    void put(Object o);
    default void putAll() { put(null); }
}

enum Color {
    /** Red. */
    RED("r") {
        @Override int code() { return 1; }
    },
    GREEN("g"),
    ;
    private final String tag;
    Color(String tag) { this.tag = tag; }
    int code() { return 0; }
}

record Point(int x, @Deprecated int y) implements Store {
    Point {
        if (x < 0) throw new IllegalArgumentException();
    }
    Point(int x) { this(x, 0); }
    public void put(Object o) {}
}

@interface Tag {
    String value() default "";
    int LEVEL = 1;
}
`

func TestJavaUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzeJava(t, newTestRuntime(t), "src/main/java/com/example/app/Repo.java", javaSample)
	if a.Package != (domain.PackageRef{Dir: "src/main/java/com/example/app", Name: "com.example.app", Hierarchical: true}) {
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
		"Repo":                                   {domain.KindClass, "", 5, 30},
		"Repo.MAX":                               {domain.KindConst, "Repo", 11, 11},
		"Repo.MIN":                               {domain.KindConst, "Repo", 11, 11},
		"Repo.name":                              {domain.KindField, "Repo", 12, 12},
		"Repo.alias":                             {domain.KindField, "Repo", 12, 12},
		"Repo.Repo()":                            {domain.KindMethod, "Repo", 15, 16},
		"Repo.Repo(String)":                      {domain.KindMethod, "Repo", 17, 17},
		"Repo.find(String)":                      {domain.KindMethod, "Repo", 19, 19},
		"Repo.find(int,Map<String,? extends T>)": {domain.KindMethod, "Repo", 20, 20},
		"Repo.run(String...)":                    {domain.KindMethod, "Repo", 21, 24},
		"Repo.Inner":                             {domain.KindClass, "Repo", 26, 29},
		"Repo.Inner.close()":                     {domain.KindMethod, "Repo.Inner", 27, 27},
		"Repo.Inner.Mode":                        {domain.KindEnum, "Repo.Inner", 28, 28},
		"Repo.Inner.Mode.ON":                     {domain.KindEnumMember, "Repo.Inner.Mode", 28, 28},
		"Repo.Inner.Mode.OFF":                    {domain.KindEnumMember, "Repo.Inner.Mode", 28, 28},
		"Store":                                  {domain.KindInterface, "", 32, 36},
		"Store.LIMIT":                            {domain.KindConst, "Store", 33, 33},
		"Store.put(Object)":                      {domain.KindInterfaceMethod, "Store", 34, 34},
		"Store.putAll()":                         {domain.KindMethod, "Store", 35, 35},
		"Color":                                  {domain.KindEnum, "", 38, 48},
		"Color.RED":                              {domain.KindEnumMember, "Color", 39, 42},
		"Color.RED.code()":                       {domain.KindMethod, "Color.RED", 41, 41},
		"Color.GREEN":                            {domain.KindEnumMember, "Color", 43, 43},
		"Color.tag":                              {domain.KindField, "Color", 45, 45},
		"Color.Color(String)":                    {domain.KindMethod, "Color", 46, 46},
		"Color.code()":                           {domain.KindMethod, "Color", 47, 47},
		"Point":                                  {domain.KindRecord, "", 50, 56},
		"Point.x":                                {domain.KindField, "Point", 50, 50},
		"Point.y":                                {domain.KindField, "Point", 50, 50},
		"Point.Point(int,int)":                   {domain.KindMethod, "Point", 51, 53},
		"Point.Point(int)":                       {domain.KindMethod, "Point", 54, 54},
		"Point.put(Object)":                      {domain.KindMethod, "Point", 55, 55},
		"Tag":                                    {domain.KindInterface, "", 58, 61},
		"Tag.value()":                            {domain.KindInterfaceMethod, "Tag", 59, 59},
		"Tag.LEVEL":                              {domain.KindConst, "Tag", 60, 60},
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
		if u.File != "src/main/java/com/example/app/Repo.java" || u.Package != a.Package {
			t.Errorf("%s: file %q package %+v", name, u.File, u.Package)
		}
	}
	if len(a.Units) != len(wants) {
		var names []string
		for _, u := range a.Units {
			names = append(names, u.Name)
		}
		t.Errorf("got %d units, want %d: %v", len(a.Units), len(wants), names)
	}
	for _, u := range a.Units {
		if u.Kind == domain.KindModule {
			t.Errorf("Java is a package language, not a module language: %s", u.Name)
		}
	}
	if u := got["Repo"]; u.Doc != "A repository.\n  Indented line." || !strings.HasPrefix(u.Source, "/**") {
		t.Errorf("javadoc: doc %q source %.20q", u.Doc, u.Source)
	}
	if got["Repo.Repo()"].Doc != "Makes one." || got["Color.RED"].Doc != "Red." {
		t.Errorf("member javadoc: %q %q", got["Repo.Repo()"].Doc, got["Color.RED"].Doc)
	}
	if sig := got["Repo"].Signature; sig != `@SuppressWarnings("unchecked") public class Repo<T extends Comparable<T>> extends Base implements Store` {
		t.Errorf("class signature %q", sig)
	}
	if sig := got["Repo.MAX"].Signature; sig != "static final int MAX" {
		t.Errorf("field signature %q", sig)
	}
	if sig := got["Store.put(Object)"].Signature; sig != "void put(Object o)" {
		t.Errorf("interface method signature %q", sig)
	}
}

func TestJavaPackageWithoutDeclaration(t *testing.T) {
	rt := newTestRuntime(t)
	if a := analyzeJava(t, rt, "tools/Main.java", "class Main {}\n"); a.Package != (domain.PackageRef{Dir: "tools", Name: "tools", Hierarchical: true}) {
		t.Errorf("a file with no package takes its directory's name: %+v", a.Package)
	}
	if a := analyzeJava(t, rt, "Main.java", "class Main {}\n"); a.Package != (domain.PackageRef{Dir: ".", Name: "", Hierarchical: true}) {
		t.Errorf("root default package: %+v", a.Package)
	}
	a := analyzeJava(t, rt, "a/b/Main.java", "package  com . x ;\nclass Main {}\n")
	if a.Package.Name != "com.x" {
		t.Errorf("declared package %+v", a.Package)
	}
}

func TestJavaAnonymousAndLocalClassesStayInsideTheirMethod(t *testing.T) {
	got := unitsByName(analyzeJava(t, newTestRuntime(t), "Repo.java", javaSample))
	for name := range got {
		if strings.Contains(name, "Local") || strings.HasPrefix(name, "Repo.run(String...).") {
			t.Errorf("unexpected unit %s", name)
		}
	}
	if !strings.Contains(got["Repo.run(String...)"].Source, "class Local") {
		t.Error("a local class is part of its method's code")
	}
}

func TestJavaOverloadNamesAreStable(t *testing.T) {
	rt := newTestRuntime(t)
	// A method's name does not depend on its siblings: adding an overload, or a field of the
	// same name, leaves it alone.
	one := unitsByName(analyzeJava(t, rt, "A.java", "class A { int f(int x) { return x; } }"))
	two := unitsByName(analyzeJava(t, rt, "A.java", "class A { int f; int f(int x) { return x; } int f(long x) { return 1; } }"))
	if one["A.f(int)"].Hash == "" || one["A.f(int)"].Hash != two["A.f(int)"].Hash {
		t.Errorf("adding an overload renamed or changed the existing one: %v / %v", one, two)
	}
	// Formatting and parameter names do not change an overload's name.
	a := unitsByName(analyzeJava(t, rt, "A.java", "class A { void f(int x) {} void f(java.util.Map<String,Integer> m, int[] xs, String... r) {} }"))
	b := unitsByName(analyzeJava(t, rt, "A.java", "class A { void f(int y) {}\n void f(java.util.Map< String , /* c */ Integer > n, int ys[], String ... s) {} }"))
	for _, name := range []string{"A.f(int)", "A.f(java.util.Map<String,Integer>,int[],String...)"} {
		if _, ok := a[name]; !ok {
			t.Errorf("missing %s in %v", name, a)
		}
		if _, ok := b[name]; !ok {
			t.Errorf("missing %s after reformatting in %v", name, b)
		}
	}
}

func TestJavaMethodSharingAFieldsNameIsDistinct(t *testing.T) {
	a := analyzeJava(t, newTestRuntime(t), "A.java", `class A {
    private int size;
    int size() { return size; }
    static final int MAX = 1;
    int MAX(int x) { return x; }
    class Inner {}
    void Inner() {}
    int plain() { return 0; }
}
record R(int a, String... rest) {
    public int a() { return a; }
}
`)
	seen := map[string]domain.UnitKind{}
	for _, u := range a.Units {
		if k, dup := seen[u.Name]; dup {
			t.Errorf("%s names both a %s and a %s", u.Name, k, u.Kind)
		}
		seen[u.Name] = u.Kind
	}
	for name, kind := range map[string]domain.UnitKind{
		"A.size": domain.KindField, "A.size()": domain.KindMethod,
		"A.MAX": domain.KindConst, "A.MAX(int)": domain.KindMethod,
		"A.Inner": domain.KindClass, "A.Inner()": domain.KindMethod,
		"A.plain()": domain.KindMethod,
		"R.a":       domain.KindField, "R.a()": domain.KindMethod, "R.rest": domain.KindField,
	} {
		if seen[name] != kind {
			t.Errorf("%s: got kind %q, want %s (units %v)", name, seen[name], kind, seen)
		}
	}
	// A varargs component is a component like any other.
	r1 := unitsByName(analyzeJava(t, newTestRuntime(t), "R.java", "record R(int a, String... rest) {}"))
	r2 := unitsByName(analyzeJava(t, newTestRuntime(t), "R.java", "record R(int a, Object... rest) {}"))
	if r1["R.rest"].Hash == r2["R.rest"].Hash || r1["R.rest"].StartLine != 1 {
		t.Errorf("varargs component: %+v", r1["R.rest"])
	}
}

func TestJavaHashIgnoresFormattingAndComments(t *testing.T) {
	rt := newTestRuntime(t)
	base := `class A {
    /** Doc. */
    int f(int x) {
        return x + 0x1F; // trailing
    }
    int[] xs = {1, 2};
}
`
	reformatted := `class A {
    /** Other doc. */
    int f( int x )
    {
        /* note */
        return x+0x1f;
    }
    int[] xs = {1, 2,};
}
`
	a, b := unitsByName(analyzeJava(t, rt, "A.java", base)), unitsByName(analyzeJava(t, rt, "A.java", reformatted))
	for _, name := range []string{"A", "A.f(int)", "A.xs"} {
		if a[name].Hash != b[name].Hash {
			t.Errorf("%s: formatting and comments changed the hash", name)
		}
	}
	if a["A.f(int)"].BodyHash != "" {
		t.Error("a one-statement body cannot prove a rename")
	}
}

func TestJavaHashCountsLiteralContents(t *testing.T) {
	rt := newTestRuntime(t)
	hash := func(body string) string {
		return unitsByName(analyzeJava(t, rt, "A.java", "class A { void f() { "+body+" } }"))["A.f()"].Hash
	}
	pairs := [][2]string{
		{`String s = "a b";`, `String s = "a  b";`},
		{`String s = "a\tb";`, `String s = "a\nb";`},
		{`char c = 'a';`, `char c = 'b';`},
		{`char c = ' ';`, `char c = '\t';`},
		{"String s = \"\"\"\n  one\n  \"\"\";", "String s = \"\"\"\n  one \n  \"\"\";"},
		{"String s = \"\"\"\n  one\n  \"\"\";", "String s = \"\"\"\n    one\n  \"\"\";"},
	}
	for _, p := range pairs {
		if hash(p[0]) == hash(p[1]) {
			t.Errorf("%q and %q hash the same", p[0], p[1])
		}
	}
}

func TestJavaAnnotationsAndModifiersCount(t *testing.T) {
	rt := newTestRuntime(t)
	a := unitsByName(analyzeJava(t, rt, "A.java", "class A { void f() {} }"))
	b := unitsByName(analyzeJava(t, rt, "A.java", "class A { @Deprecated void f() {} }"))
	c := unitsByName(analyzeJava(t, rt, "A.java", "class A { synchronized void f() {} }"))
	if a["A.f()"].Hash == b["A.f()"].Hash || a["A.f()"].Hash == c["A.f()"].Hash {
		t.Error("annotations and modifiers are code")
	}
	if a["A"].Hash != b["A"].Hash {
		t.Error("a member's change belongs to the member, not its class")
	}
}

func TestJavaOnlyStaticFinalFieldsAreConstants(t *testing.T) {
	got := unitsByName(analyzeJava(t, newTestRuntime(t), "A.java", "class A { static int count; final int id = 1; static final int MAX = 2; }"))
	for name, kind := range map[string]domain.UnitKind{"A.count": domain.KindField, "A.id": domain.KindField, "A.MAX": domain.KindConst} {
		if got[name].Kind != kind {
			t.Errorf("%s: got %s, want %s", name, got[name].Kind, kind)
		}
	}
}

// Nested types share the file's text instead of copying it once per level of nesting.
func TestJavaNestedTypesShareText(t *testing.T) {
	const depth = 50
	src := strings.Repeat("class A {\n", depth) + strings.Repeat("}\n", depth)
	a := analyzeJava(t, newTestRuntime(t), "A.java", src)
	if len(a.Units) != depth {
		t.Fatalf("got %d units", len(a.Units))
	}
	outer := a.Units[0].Source
	lo := uintptr(unsafe.Pointer(unsafe.StringData(outer)))
	for _, u := range a.Units[1:] {
		p := uintptr(unsafe.Pointer(unsafe.StringData(u.Source)))
		if p < lo || p+uintptr(len(u.Source)) > lo+uintptr(len(outer)) {
			t.Fatalf("%s copies its text", u.Name)
		}
	}
}

// Every kind of member counts its whole declaration, not just its name.
func TestJavaMemberHashesCoverTheirDeclaration(t *testing.T) {
	rt := newTestRuntime(t)
	differs := func(name string, srcs ...string) {
		t.Helper()
		seen := map[string]string{}
		for _, src := range srcs {
			u, ok := unitsByName(analyzeJava(t, rt, "A.java", src))[name]
			if !ok {
				t.Fatalf("%s missing in %q", name, src)
			}
			if prev, dup := seen[u.Hash]; dup {
				t.Errorf("%s hashes the same in %q and %q", name, prev, src)
			}
			seen[u.Hash] = src
		}
	}
	differs("A.a",
		"class A { int a; }", "class A { long a; }", "class A { private int a; }",
		"class A { @Deprecated int a; }", "class A { int a[]; }")
	differs("E.RED",
		"enum E { RED(\"r\") }", "enum E { RED(\"s\") }",
		"enum E { RED(\"r\") { int code() { return 1; } } }", "enum E { RED(\"r\") { { init(); } int code() { return 1; } } }")
	differs("E.RED.code()",
		"enum E { RED { int code() { return 1; } } }", "enum E { RED { int code() { return 2; } } }")
	differs("P.x",
		"record P(int x) {}", "record P(long x) {}", "record P(@Deprecated int x) {}")
}

func TestJavaTypeHashCoversOwnCodeOnly(t *testing.T) {
	rt := newTestRuntime(t)
	a := unitsByName(analyzeJava(t, rt, "A.java", "class A { static { x(); } int f() { return 1; } }"))
	b := unitsByName(analyzeJava(t, rt, "A.java", "class A { static { y(); } int f() { return 2; } }"))
	if a["A"].Hash == b["A"].Hash {
		t.Error("an initializer block is the class's own code")
	}
	c := unitsByName(analyzeJava(t, rt, "A.java", "class A { static { x(); } int f() { return 2; } }"))
	if a["A"].Hash != c["A"].Hash {
		t.Error("a method's change must not change its class")
	}
	// A record's component order is its canonical constructor's parameter order.
	r1 := unitsByName(analyzeJava(t, rt, "P.java", "record P(int x, int y) {}"))
	r2 := unitsByName(analyzeJava(t, rt, "P.java", "record P(int y, int x) {}"))
	if r1["P"].Hash == r2["P"].Hash {
		t.Error("reordering record components changes the record")
	}
	if r1["P.x"].Hash != r2["P.x"].Hash {
		t.Error("a component's own code did not change")
	}
}

func TestJavaBodyHashSurvivesRename(t *testing.T) {
	rt := newTestRuntime(t)
	body := "{ int t = 0; for (int i : xs) t += i; return t; }"
	a := unitsByName(analyzeJava(t, rt, "A.java", "class A { int sum(int[] xs) "+body+" }"))
	b := unitsByName(analyzeJava(t, rt, "A.java", "class A { int total(int[] xs) "+body+" }"))
	c := unitsByName(analyzeJava(t, rt, "A.java", "class A { long total(int[] xs) "+body+" }"))
	if a["A.sum(int[])"].BodyHash == "" || a["A.sum(int[])"].BodyHash != b["A.total(int[])"].BodyHash {
		t.Error("a renamed method with an identical body keeps its body hash")
	}
	if a["A.sum(int[])"].BodyHash == c["A.total(int[])"].BodyHash {
		t.Error("the result type is part of the body hash")
	}
	// A parameter's type is in the name, so a changed type with an identical body is followed
	// through the body hash; a renamed parameter is a different body.
	d := unitsByName(analyzeJava(t, rt, "A.java", "class A { int sum(Integer[] xs) "+body+" }"))
	e := unitsByName(analyzeJava(t, rt, "A.java", "class A { int sum(int[] ys) "+body+" }"))
	if a["A.sum(int[])"].BodyHash != d["A.sum(Integer[])"].BodyHash {
		t.Error("a parameter type change with an identical body keeps its body hash")
	}
	if a["A.sum(int[])"].BodyHash == e["A.sum(int[])"].BodyHash {
		t.Error("parameter names are part of the body hash")
	}
}

func TestJavaMultiDeclaratorFieldsShareText(t *testing.T) {
	var b strings.Builder
	b.WriteString("class A {\n  int ")
	const n = 2000
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString("f")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString("_")
		b.WriteString(strings.Repeat("1", 1+i/26))
	}
	b.WriteString(";\n}\n")
	a := analyzeJava(t, newTestRuntime(t), "A.java", b.String())
	var first *byte
	count := 0
	for _, u := range a.Units {
		if u.Kind != domain.KindField {
			continue
		}
		count++
		p := unsafe.StringData(u.Source)
		if first == nil {
			first = p
		} else if p != first {
			t.Fatalf("%s copies the declaration's text", u.Name)
		}
		if len(u.Signature) > maxSignature+len("…") {
			t.Fatalf("%s: signature not bounded", u.Name)
		}
	}
	if count != n {
		t.Errorf("got %d fields, want %d", count, n)
	}
	// Each variable has its own hash: changing one initializer changes that variable alone.
	x := unitsByName(analyzeJava(t, newTestRuntime(t), "A.java", "class A { int a = 1, b = 2; }"))
	y := unitsByName(analyzeJava(t, newTestRuntime(t), "A.java", "class A { int a = 1, b = 3; }"))
	if x["A.a"].Hash != y["A.a"].Hash || x["A.b"].Hash == y["A.b"].Hash {
		t.Error("a declarator's hash covers that variable only")
	}
}

func TestJavaSyntaxErrorFails(t *testing.T) {
	_, err := NewJava(newTestRuntime(t)).Analyze(context.Background(), "A.java", []byte("class A {\n  void f( {\n}\n"))
	if !errors.Is(err, ErrSyntax) || !strings.Contains(err.Error(), "A.java") {
		t.Fatalf("got %v, want a syntax error naming the file", err)
	}
}

func TestJavaTestAndGeneratedDetection(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]bool{
		"src/test/java/a/RepoTest.java": true,
		"mod/src/test/java/a/Util.java": true,
		"a/RepoTest.java":               true,
		"a/RepoTests.java":              true,
		"a/RepoIT.java":                 true,
		"src/main/java/a/Repo.java":     false,
		"a/Testing.java":                false,
		"a/Contest.java":                false,
		"test/a/Util.java":              false,
	} {
		if got := analyzeJava(t, rt, p, "class X {}\n").Test; got != want {
			t.Errorf("%s: test=%v, want %v", p, got, want)
		}
	}
	for src, want := range map[string]bool{
		"// Generated by the protocol buffer compiler.  DO NOT EDIT!\npackage a;\nclass X {}\n": true,
		"/* @generated */\nclass X {}\n": true,
		"import javax.annotation.processing.Generated;\n@Generated(\"gen\")\nclass X {}\n": true,
		"@javax.annotation.Generated(value = \"x\")\npublic class X {}\n":                  true,
		"// Hand written.\nclass X {}\n":                                                   false,
		"class X { @Generated void f() {} }\n":                                             false,
		"// Generated. DO NOT MODIFY.\nclass X {}\n":                                       true,
		"@Generated\nclass X {}\n@Generated\nclass Y {}\n":                                 true,
		"class A {}\n// Generated code, do not edit\nclass B {}\n":                         false,
		// One annotated type does not hide the hand-written types beside it.
		"class Auth {}\n@Generated\nclass Z {}\n": false,
	} {
		if got := analyzeJava(t, rt, "X.java", src).Generated; got != want {
			t.Errorf("%q: generated=%v, want %v", src, got, want)
		}
	}
}

func TestJavaSupports(t *testing.T) {
	j := NewJava(nil)
	if !j.Supports("a/B.java") || j.Supports("a/B.jav") || j.Supports("a/B.java.txt") {
		t.Error("supports .java only")
	}
}
