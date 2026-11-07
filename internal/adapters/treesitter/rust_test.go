package treesitter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

func analyzeRs(t *testing.T, rt *Runtime, p, src string) domain.FileAnalysis {
	t.Helper()
	a, err := NewRust(rt).Analyze(context.Background(), p, []byte(src))
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return a
}

const rsSample = `//! Shapes and their names.
use std::fmt;

/// A point in space.
///
/// Second line.
#[derive(Debug, Clone)]
pub struct Point<'a, T: Clone> where T: Default {
    /// The x coordinate.
    pub x: T,
    #[serde(skip)]
    label: &'a str,
}

pub struct Pair(pub u8, String);

pub enum Shape {
    Circle(f64),
    Rect { w: f64, h: f64 },
    #[default]
    Empty = 3,
}

pub trait Named {
    type Out;
    const N: usize;
    fn name(&self) -> String;
    fn shout(&self) -> String {
        let n = self.name();
        n.to_uppercase()
    }
}

impl<'a, T: Clone + Default> Point<'a, T> {
    pub fn new(x: T) -> Self {
        let label = "p";
        Point { x, label }
    }
    const ORIGIN: u8 = 0;
}

impl<T: Clone + Default> fmt::Display for Point<'_, T> {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        write!(f, "{}", self.label)
    }
}

impl Named for Point<'_, u8> {
    type Out = u8;
    const N: usize = 1;
    fn name(&self) -> String {
        String::new()
    }
}

impl From<u8> for Pair {
    fn from(v: u8) -> Self { Pair(v, String::new()) }
}

impl From<u16> for Pair {
    fn from(v: u16) -> Self { Pair(v as u8, String::new()) }
}

impl Named for String {
    type Out = ();
    const N: usize = 0;
    fn name(&self) -> String { self.clone() }
}

pub const LIMIT: usize = 10;
static mut COUNTER: u32 = 0;
pub type Points<'a> = Vec<Point<'a, u8>>;

macro_rules! square {
    ($x:expr) => { $x * $x };
}

pub async fn area(s: &Shape) -> f64 {
    fn helper() -> f64 { 1.0 }
    helper()
}

mod geometry {
    pub struct Line;
    impl Line {
        pub fn len(&self) -> f64 { 0.0 }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn it_works() {
        assert_eq!(LIMIT, 10);
    }
}
`

func TestRustUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzeRs(t, newTestRuntime(t), "crates/geo/src/shapes.rs", rsSample)
	if a.Package != (domain.PackageRef{Dir: "crates/geo/src", Name: "src"}) {
		t.Errorf("package %+v", a.Package)
	}
	if a.Test || a.Generated || a.Constraints != "" {
		t.Errorf("test=%v generated=%v constraints=%q", a.Test, a.Generated, a.Constraints)
	}
	type want struct {
		kind       domain.UnitKind
		parent     string
		start, end int
		test       bool
	}
	wants := map[string]want{
		"shapes":                          {domain.KindModule, "", 1, 98, false},
		"shapes.Point":                    {domain.KindStruct, "shapes", 4, 13, false},
		"shapes.Point.x":                  {domain.KindField, "shapes.Point", 9, 10, false},
		"shapes.Point.label":              {domain.KindField, "shapes.Point", 11, 12, false},
		"shapes.Pair":                     {domain.KindStruct, "shapes", 15, 15, false},
		"shapes.Pair.0":                   {domain.KindField, "shapes.Pair", 15, 15, false},
		"shapes.Pair.1":                   {domain.KindField, "shapes.Pair", 15, 15, false},
		"shapes.Shape":                    {domain.KindEnum, "shapes", 17, 22, false},
		"shapes.Shape.Circle":             {domain.KindEnumMember, "shapes.Shape", 18, 18, false},
		"shapes.Shape.Rect":               {domain.KindEnumMember, "shapes.Shape", 19, 19, false},
		"shapes.Shape.Empty":              {domain.KindEnumMember, "shapes.Shape", 20, 21, false},
		"shapes.Named":                    {domain.KindTrait, "shapes", 24, 32, false},
		"shapes.Named.Out":                {domain.KindType, "shapes.Named", 25, 25, false},
		"shapes.Named.N":                  {domain.KindConst, "shapes.Named", 26, 26, false},
		"shapes.Named.name":               {domain.KindInterfaceMethod, "shapes.Named", 27, 27, false},
		"shapes.Named.shout":              {domain.KindMethod, "shapes.Named", 28, 31, false},
		"shapes.Point.new":                {domain.KindMethod, "shapes.Point", 35, 38, false},
		"shapes.Point.ORIGIN":             {domain.KindConst, "shapes.Point", 39, 39, false},
		"shapes.Point.<fmt::Display>.fmt": {domain.KindMethod, "shapes.Point", 43, 45, false},
		"shapes.Point.<Named>.Out":        {domain.KindType, "shapes.Point", 49, 49, false},
		"shapes.Point.<Named>.N":          {domain.KindConst, "shapes.Point", 50, 50, false},
		"shapes.Point.<Named>.name":       {domain.KindMethod, "shapes.Point", 51, 53, false},
		"shapes.Pair.<From<u8>>.from":     {domain.KindMethod, "shapes.Pair", 57, 57, false},
		"shapes.Pair.<From<u16>>.from":    {domain.KindMethod, "shapes.Pair", 61, 61, false},
		"shapes.String.<Named>.Out":       {domain.KindType, "shapes", 65, 65, false},
		"shapes.String.<Named>.N":         {domain.KindConst, "shapes", 66, 66, false},
		"shapes.String.<Named>.name":      {domain.KindMethod, "shapes", 67, 67, false},
		"shapes.LIMIT":                    {domain.KindConst, "shapes", 70, 70, false},
		"shapes.COUNTER":                  {domain.KindVar, "shapes", 71, 71, false},
		"shapes.Points":                   {domain.KindType, "shapes", 72, 72, false},
		"shapes.square!":                  {domain.KindFunction, "shapes", 74, 76, false},
		"shapes.area":                     {domain.KindFunction, "shapes", 78, 81, false},
		"shapes.geometry.Line":            {domain.KindStruct, "shapes", 84, 84, false},
		"shapes.geometry.Line.len":        {domain.KindMethod, "shapes.geometry.Line", 86, 86, false},
		"shapes.tests.it_works":           {domain.KindFunction, "shapes", 94, 97, true},
	}
	got := map[string]domain.Unit{}
	for _, u := range a.Units {
		if _, dup := got[u.Name]; dup {
			t.Errorf("duplicate unit %s", u.Name)
		}
		got[u.Name] = u
	}
	for name, w := range wants {
		u, ok := got[name]
		if !ok {
			t.Errorf("missing unit %s", name)
			continue
		}
		if u.Kind != w.kind || u.Parent != w.parent || u.StartLine != w.start || u.EndLine != w.end || u.Test != w.test {
			t.Errorf("%s: got %s parent=%q lines %d-%d test=%v, want %s parent=%q lines %d-%d test=%v",
				name, u.Kind, u.Parent, u.StartLine, u.EndLine, u.Test, w.kind, w.parent, w.start, w.end, w.test)
		}
		if u.File != "crates/geo/src/shapes.rs" || u.Package != a.Package {
			t.Errorf("%s: file %q package %+v", name, u.File, u.Package)
		}
		if !strings.HasPrefix(name, "shapes") {
			t.Errorf("%s is not qualified by its module", name)
		}
	}
	if len(a.Units) != len(wants) {
		var extra []string
		for n := range got {
			if _, ok := wants[n]; !ok {
				extra = append(extra, n)
			}
		}
		sort.Strings(extra)
		t.Errorf("got %d units, want %d; unexpected: %v", len(a.Units), len(wants), extra)
	}
	if a.Units[0].Kind != domain.KindModule {
		t.Error("the module comes first")
	}
	// The domain rule: one MODULE per file, and every parent chain ends at it.
	for _, u := range a.Units[1:] {
		if u.Kind == domain.KindModule {
			t.Errorf("a second module %s", u.Name)
		}
		p, steps := u.Parent, 0
		for p != "shapes" && steps < 10 {
			p, steps = got[p].Parent, steps+1
		}
		if p != "shapes" {
			t.Errorf("%s: parent chain does not reach the module", u.Name)
		}
	}
}

func TestRustSignaturesDocsAndSource(t *testing.T) {
	got := unitsByName(analyzeRs(t, newTestRuntime(t), "shapes.rs", rsSample))
	for name, sig := range map[string]string{
		"shapes":                          "module shapes.rs",
		"shapes.Point":                    "#[derive(Debug, Clone)] pub struct Point<'a, T: Clone> where T: Default",
		"shapes.Point.label":              "#[serde(skip)] label: &'a str",
		"shapes.Pair":                     "pub struct Pair(pub u8, String)",
		"shapes.Pair.0":                   "pub u8",
		"shapes.Shape.Empty":              "#[default] Empty = 3",
		"shapes.Named.name":               "fn name(&self) -> String",
		"shapes.Named.shout":              "fn shout(&self) -> String",
		"shapes.Point.<fmt::Display>.fmt": "fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result",
		"shapes.LIMIT":                    "pub const LIMIT: usize = 10",
		"shapes.square!":                  "macro_rules! square",
		"shapes.area":                     "pub async fn area(s: &Shape) -> f64",
		"shapes.tests.it_works":           "#[test] fn it_works()",
	} {
		if got[name].Signature != sig {
			t.Errorf("%s signature %q, want %q", name, got[name].Signature, sig)
		}
	}
	if d := got["shapes.Point"].Doc; d != "A point in space.\n\nSecond line." {
		t.Errorf("struct doc %q", d)
	}
	if d := got["shapes.Point.x"].Doc; d != "The x coordinate." {
		t.Errorf("field doc %q", d)
	}
	if d := got["shapes"].Doc; d != "Shapes and their names." {
		t.Errorf("module doc %q", d)
	}
	if src := got["shapes.Point"].Source; !strings.HasPrefix(src, "/// A point") || !strings.HasSuffix(src, "label: &'a str,\n}") {
		t.Errorf("a unit's code spans its docs, attributes, and body: %q", src)
	}
	if got["shapes.area"].Source != "pub async fn area(s: &Shape) -> f64 {\n    fn helper() -> f64 { 1.0 }\n    helper()\n}" {
		t.Errorf("source %q", got["shapes.area"].Source)
	}
}

func TestRustBlockDocComments(t *testing.T) {
	got := unitsByName(analyzeRs(t, newTestRuntime(t), "m.rs", "/*! Crate doc. */\n// plain\n/**\n * Block doc.\n * More.\n */\n// note\nfn f() {}\n"))
	if got["m.f"].Doc != "Block doc.\nMore." || got["m.f"].StartLine != 3 {
		t.Errorf("block doc %q from line %d", got["m.f"].Doc, got["m.f"].StartLine)
	}
	if got["m"].Doc != "Crate doc." {
		t.Errorf("inner block doc %q", got["m"].Doc)
	}
}

func TestRustHashIgnoresFormattingAndComments(t *testing.T) {
	rt := newTestRuntime(t)
	base := "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n"
	same := map[string]string{
		"comments":        "/// doc\nfn f(a: u8, b: (u8,)) -> u8 { // note\n    /* c */ match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"whitespace":      "fn f( a:u8,b:(u8,) )->u8{match a{1=>{g( \"x  y\",'c' )} _=>2,}}\n",
		"trailing commas": "fn f(a: u8, b: (u8,),) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c',) },\n        _ => 2\n    }\n}\n",
	}
	different := map[string]string{
		"argument":         "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'd') }\n        _ => 2,\n    }\n}\n",
		"string spaces":    "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"one-tuple":        "fn f(a: u8, b: (u8)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"name":             "fn h(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"attribute":        "#[inline]\nfn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"visibility":       "pub fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"raw string":       "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(r\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"macro comma":      "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { m!(\"x  y\", 'c',) }\n        _ => 2,\n    }\n}\n",
		"lifetime":         "fn f<'a>(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { g(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
		"paren call field": "fn f(a: u8, b: (u8,)) -> u8 {\n    match a {\n        1 => { (s.g)(\"x  y\", 'c') }\n        _ => 2,\n    }\n}\n",
	}
	hashOf := func(src string) string {
		for _, u := range analyzeRs(t, rt, "m.rs", src).Units {
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
	if hashOf("fn m() { m!(a, b) }\n") == hashOf("fn m() { m!(a, b,) }\n") {
		t.Error("a comma inside a macro call is macro input")
	}
}

// Text that tree-sitter keeps in hidden tokens or splits around escapes must still count.
func TestRustHashSeesTextInsideLiterals(t *testing.T) {
	rt := newTestRuntime(t)
	hashOf := func(lit string) (string, string) {
		u := unitsByName(analyzeRs(t, rt, "m.rs", "fn f() -> &'static str {\n    let a = 1;\n    "+lit+"\n}\n"))["m.f"]
		return u.Hash, u.BodyHash
	}
	for name, pair := range map[string][2]string{
		"escaped string":    {`"hello\n"`, `"rm -rf /\n"`},
		"spaces in string":  {`"a b\n"`, `"ab\n"`},
		"byte string":       {`b"a\x00b"`, `b"zz\x00yy"`},
		"raw string":        {`r#"a  "b""#`, `r#"a "b""#`},
		"char":              {`' '`, `'\t'`},
		"byte char":         {`b'a'`, `b'b'`},
		"string vs bytes":   {`"ab"`, `b"ab"`},
		"multi-line":        {"\"a\n  b\"", "\"a\nb\""},
		"line continuation": {"\"a\\\n   b\"", "\"a\\\n b\""},
	} {
		h1, b1 := hashOf(pair[0])
		h2, b2 := hashOf(pair[1])
		if h1 == h2 || b1 == b2 {
			t.Errorf("%s: %s and %s must hash differently", name, pair[0], pair[1])
		}
	}
}

func TestRustContainerHashExcludesMembers(t *testing.T) {
	rt := newTestRuntime(t)
	src := func(field, method, header string) string {
		return "use std::io;\n\n" + header + " {\n    a: " + field + ",\n}\n\nimpl S {\n    fn m(&self) -> u8 { " + method + " }\n}\n"
	}
	base := unitsByName(analyzeRs(t, rt, "m.rs", src("u8", "1", "struct S")))
	field := unitsByName(analyzeRs(t, rt, "m.rs", src("u16", "1", "struct S")))
	if field["m.S"].Hash != base["m.S"].Hash || field["m"].Hash != base["m"].Hash || field["m.S.a"].Hash == base["m.S.a"].Hash {
		t.Error("changing a field changes only the field")
	}
	method := unitsByName(analyzeRs(t, rt, "m.rs", src("u8", "2", "struct S")))
	if method["m.S"].Hash != base["m.S"].Hash || method["m"].Hash != base["m"].Hash || method["m.S.m"].Hash == base["m.S.m"].Hash {
		t.Error("changing a method changes only the method")
	}
	added := unitsByName(analyzeRs(t, rt, "m.rs", strings.Replace(src("u8", "1", "struct S"), "a: u8,", "a: u8,\n    b: u8", 1)))
	if added["m.S"].Hash != base["m.S"].Hash || added["m.S.b"].Hash == "" {
		t.Error("adding a field, and the comma it needs, does not change its struct")
	}
	header := unitsByName(analyzeRs(t, rt, "m.rs", src("u8", "1", "#[derive(Debug)]\npub struct S")))
	if header["m.S"].Hash == base["m.S"].Hash || header["m"].Hash != base["m"].Hash {
		t.Error("changing the struct's header or attributes changes the struct only")
	}
	imports := unitsByName(analyzeRs(t, rt, "m.rs", "use std::fmt;\n"+src("u8", "1", "struct S")[len("use std::io;\n"):]))
	if imports["m"].Hash == base["m"].Hash || imports["m.S"].Hash != base["m.S"].Hash {
		t.Error("changing an import changes the module only")
	}
	impl := unitsByName(analyzeRs(t, rt, "m.rs", strings.Replace(src("u8", "1", "struct S"), "impl S", "impl<'a> S", 1)))
	if impl["m"].Hash == base["m"].Hash || impl["m.S.m"].Hash != base["m.S.m"].Hash {
		t.Error("an impl header is the module's own code")
	}
	trait := func(body string) map[string]domain.Unit {
		return unitsByName(analyzeRs(t, rt, "m.rs", "trait T {\n    fn a(&self) -> u8 { "+body+" }\n}\nenum E { A, B }\n"))
	}
	if trait("1")["m.T"].Hash != trait("2")["m.T"].Hash || trait("1")["m.T.a"].Hash == trait("2")["m.T.a"].Hash {
		t.Error("a provided method is not its trait's code")
	}
	moreVariants := unitsByName(analyzeRs(t, rt, "m.rs", "trait T {\n    fn a(&self) -> u8 { 1 }\n}\nenum E { A, B, C }\n"))
	tuple := func(fields string) string {
		return unitsByName(analyzeRs(t, rt, "m.rs", "struct P("+fields+");\n"))["m.P"].Hash
	}
	if moreVariants["m.E"].Hash != trait("1")["m.E"].Hash || tuple("u8") != tuple("u8, u16") {
		t.Error("adding a variant or tuple field does not change its enum or struct")
	}
	variant := unitsByName(analyzeRs(t, rt, "m.rs", "trait T {\n    fn a(&self) -> u8 { 1 }\n}\nenum E { A, B(u8) }\n"))
	if variant["m.E"].Hash != trait("1")["m.E"].Hash || variant["m.E.B"].Hash == trait("1")["m.E.B"].Hash {
		t.Error("changing a variant changes only the variant")
	}
}

func TestRustBodyHashProvesRenamesOnly(t *testing.T) {
	rt := newTestRuntime(t)
	body := "(a: u8) -> u8 {\n    let b = a + 1;\n    b * 2\n}\n"
	old := unitsByName(analyzeRs(t, rt, "m.rs", "fn old"+body))["m.old"]
	renamed := unitsByName(analyzeRs(t, rt, "m.rs", "fn new"+body))["m.new"]
	if old.BodyHash == "" || old.BodyHash != renamed.BodyHash || old.Hash == renamed.Hash {
		t.Errorf("a rename keeps the body hash and changes the hash: %+v %+v", old, renamed)
	}
	asyncVersion := unitsByName(analyzeRs(t, rt, "m.rs", "async fn new"+body))["m.new"]
	attributed := unitsByName(analyzeRs(t, rt, "m.rs", "#[inline]\nfn new"+body))["m.new"]
	generic := unitsByName(analyzeRs(t, rt, "m.rs", "fn new<'a>"+body))["m.new"]
	for name, u := range map[string]domain.Unit{"async": asyncVersion, "attribute": attributed, "generics": generic} {
		if u.BodyHash == old.BodyHash {
			t.Errorf("a %s version is not an identical body", name)
		}
	}
	method := unitsByName(analyzeRs(t, rt, "m.rs", "struct S;\nimpl S {\n    fn total"+strings.ReplaceAll(body, "\n", "\n    ")+"}\n"))["m.S.total"]
	if method.BodyHash != old.BodyHash {
		t.Error("a method body proves a rename like a function body")
	}
	short := unitsByName(analyzeRs(t, rt, "m.rs", "fn f() -> u8 {\n    1\n}\n"))["m.f"]
	required := unitsByName(analyzeRs(t, rt, "m.rs", "trait T {\n    fn f(&self);\n}\n"))["m.T.f"]
	if short.BodyHash != "" || required.BodyHash != "" {
		t.Error("a one-expression body, or no body, cannot prove a rename")
	}
}

func TestRustImplNamesAreStableAndUnique(t *testing.T) {
	rt := newTestRuntime(t)
	src := `struct W<T>(T);
impl<T> W<T> { fn get(&self) {} }
impl<T: Clone> Clone for W<T> { fn clone(&self) -> Self { todo!() } }
impl<T> std::fmt::Debug for W<T> { fn fmt(&self) {} }
impl<T> std::fmt::Display for W<T> { fn fmt(&self) {} }
impl<T> Iterator for W<T> { type Item = T; fn next(&mut self) -> Option<T> { None } }
impl<'a> Trait for &'a W<u8> { fn go(&self) {} }
impl Trait for [u8] { fn go(&self) {} }
impl other::Ext for other::Thing { fn go(&self) {} }
impl Add<Output = u8> for W<u8> { fn go(&self) {} }
unsafe impl Send for W<u8> {}
`
	a := analyzeRs(t, rt, "m.rs", src)
	var names []string
	parents := map[string]string{}
	for _, u := range a.Units[1:] {
		names = append(names, u.Name)
		parents[u.Name] = u.Parent
	}
	want := []string{
		"m.W", "m.W.0", "m.W.get", "m.W.<Clone>.clone", "m.W.<std::fmt::Debug>.fmt", "m.W.<std::fmt::Display>.fmt",
		"m.W.<Iterator>.Item", "m.W.<Iterator>.next", "m.&'a W<u8>.<Trait>.go", "m.[u8].<Trait>.go",
		"m.other::Thing.<other::Ext>.go", "m.W.<Add<Output = u8>>.go",
	}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("names\n got %q\nwant %q", names, want)
	}
	if parents["m.W.<std::fmt::Display>.fmt"] != "m.W" || parents["m.[u8].<Trait>.go"] != "m" || parents["m.other::Thing.<other::Ext>.go"] != "m" {
		t.Errorf("a type declared here is the parent, otherwise the module: %v", parents)
	}
	// A type of the same name in another scope is not this impl's type.
	nested := unitsByName(analyzeRs(t, rt, "m.rs", "mod a { pub struct S; }\nimpl S { fn f(&self) {} }\n"))
	if nested["m.S.f"].Parent != "m" {
		t.Errorf("parent %q", nested["m.S.f"].Parent)
	}
	// Declaring the type after its impl still makes it the parent.
	after := unitsByName(analyzeRs(t, rt, "m.rs", "impl S { fn f(&self) {} }\nenum S { A }\n"))
	if after["m.S.f"].Parent != "m.S" {
		t.Errorf("parent %q", after["m.S.f"].Parent)
	}
}

func TestRustNestedModules(t *testing.T) {
	src := `mod outer {
    #[cfg(test)]
    mod inner {
        pub fn deep() {}
        pub struct S { pub a: u8 }
        impl S { pub fn m(&self) {} }
    }
    pub fn shallow() {}
}
mod declared;
`
	a := analyzeRs(t, newTestRuntime(t), "lib.rs", src)
	type want struct {
		parent string
		test   bool
	}
	wants := map[string]want{
		"lib.outer.inner.deep": {"lib", true},
		"lib.outer.inner.S":    {"lib", true},
		"lib.outer.inner.S.a":  {"lib.outer.inner.S", true},
		"lib.outer.inner.S.m":  {"lib.outer.inner.S", true},
		"lib.outer.shallow":    {"lib", false},
	}
	got := unitsByName(a)
	for name, w := range wants {
		if u, ok := got[name]; !ok || u.Parent != w.parent || u.Test != w.test {
			t.Errorf("%s: %+v", name, u)
		}
	}
	if len(a.Units) != len(wants)+1 {
		t.Errorf("units: %d", len(a.Units))
	}
	edited := unitsByName(analyzeRs(t, newTestRuntime(t), "lib.rs", strings.Replace(src, "mod inner {", "mod inner {\n        use super::*;", 1)))
	if edited["lib"].Hash == got["lib"].Hash || edited["lib.outer.inner.deep"].Hash != got["lib.outer.inner.deep"].Hash {
		t.Error("an inline module's own code belongs to the file module")
	}
}

func TestRustSyntaxErrorsAreErrors(t *testing.T) {
	rt := newTestRuntime(t)
	for name, src := range map[string]string{
		"unclosed brace":  "fn f() {\n    let x = 1;\n",
		"unclosed paren":  "fn f(a: u8 {\n}\n",
		"unclosed string": "fn f() { let s = \"abc; }\n",
		"missing name":    "fn (x: u8) {}\n",
		"bad token":       "fn f() { let x = @; }\n",
		"missing comma":   "enum E { A B }\n",
		"missing semi":    "const X: u8 = 1\nfn f() {}\n",
	} {
		_, err := NewRust(rt).Analyze(context.Background(), "m.rs", []byte(src))
		if !errors.Is(err, ErrSyntax) {
			t.Errorf("%s: want a syntax error, got %v", name, err)
		}
	}
}

func TestRustTestDetection(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]bool{
		"tests/it.rs": true, "crate/tests/common/mod.rs": true, "src/lib.rs": false, "src/tests.rs": false, "src/testing/x.rs": false,
	} {
		a := analyzeRs(t, rt, p, "fn f() {}\n")
		if a.Test != want || a.Units[0].Test != want || a.Units[1].Test != want {
			t.Errorf("%s: test=%v want %v", p, a.Test, want)
		}
	}
	got := unitsByName(analyzeRs(t, rt, "m.rs", `#[test]
fn a() {}
#[tokio::test]
async fn b() {}
#[cfg(all(test, unix))]
fn c() {}
#[cfg(not(test))]
fn d() {}
#[cfg(test)]
impl S { fn e(&self) {} }
fn f() {}
struct S {
    #[cfg(test)]
    g: u8,
}
`))
	for name, want := range map[string]bool{"m.a": true, "m.b": true, "m.c": true, "m.d": false, "m.S.e": true, "m.f": false, "m.S.g": true, "m.S": false, "m": false} {
		if got[name].Test != want {
			t.Errorf("%s: test=%v want %v", name, got[name].Test, want)
		}
	}
	whole := analyzeRs(t, rt, "src/m.rs", "#![cfg(test)]\nfn f() {}\n")
	if !whole.Test || !whole.Units[1].Test || whole.Constraints != "#![cfg(test)]" {
		t.Errorf("#![cfg(test)] makes the file a test: %+v", whole)
	}
}

func TestRustConstraintsAndGenerated(t *testing.T) {
	rt := newTestRuntime(t)
	a := analyzeRs(t, rt, "m.rs", "#![cfg(feature = \"x\")]\n#![allow(dead_code)]\nfn f() {}\n")
	b := analyzeRs(t, rt, "m.rs", "#![cfg(feature = \"y\")]\n#![allow(dead_code)]\nfn f() {}\n")
	if a.Constraints != `#![cfg(feature = "x")]` || b.Constraints == a.Constraints {
		t.Errorf("constraints %q %q", a.Constraints, b.Constraints)
	}
	if a.Units[0].Hash != b.Units[0].Hash {
		t.Error("a constraint is compared on its own, not as module code")
	}
	c := analyzeRs(t, rt, "m.rs", "#![cfg(feature = \"x\")]\n#![allow(unused)]\nfn f() {}\n")
	if c.Units[0].Hash == a.Units[0].Hash {
		t.Error("other inner attributes are module code")
	}
	for src, want := range map[string]bool{
		"// @generated by prost-build.\nfn f() {}\n":              true,
		"// Code generated by bindgen. DO NOT EDIT.\nfn f() {}\n": true,
		"fn f() {}\n// @generated\n":                              false,
		"// Hand written; generated ideas welcome.\nfn f() {}\n":  false,
	} {
		if got := analyzeRs(t, rt, "m.rs", src).Generated; got != want {
			t.Errorf("%q: generated=%v want %v", src, got, want)
		}
	}
}

func TestRustModuleNamingAndEmptyFile(t *testing.T) {
	rt := newTestRuntime(t)
	root := analyzeRs(t, rt, "build.rs", "fn main() {}\n")
	if root.Package != (domain.PackageRef{Dir: ".", Name: ""}) || root.Units[0].Name != "build" || root.Units[1].Name != "build.main" {
		t.Errorf("root module: %+v %+v", root.Package, root.Units)
	}
	mod := analyzeRs(t, rt, "src/net/mod.rs", "")
	if mod.Package != (domain.PackageRef{Dir: "src/net", Name: "net"}) || len(mod.Units) != 1 || mod.Units[0].Name != "mod" || mod.Units[0].EndLine != 1 {
		t.Errorf("empty mod.rs: %+v", mod)
	}
}

func TestRustSupports(t *testing.T) {
	r := NewRust(nil)
	if !r.Supports("a/b.rs") || r.Supports("a/b.rs.bk") || r.Supports("a/b.go") || r.Supports("rs") {
		t.Error("Supports must accept exactly .rs files")
	}
}
