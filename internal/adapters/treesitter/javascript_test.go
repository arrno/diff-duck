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

// analyzeJS analyzes a file, with the grammar its extension selects.
func analyzeJS(t *testing.T, rt *Runtime, p, src string) domain.FileAnalysis {
	t.Helper()
	a, err := NewJavaScript(rt).Analyze(context.Background(), p, []byte(src))
	if err != nil {
		t.Fatalf("analyze %s: %v", p, err)
	}
	return a
}

type jsWant struct {
	kind       domain.UnitKind
	parent     string
	start, end int
}

// checkJSUnits compares every unit of a file with the expected set, which must be complete.
func checkJSUnits(t *testing.T, a domain.FileAnalysis, file string, wants map[string]jsWant) {
	t.Helper()
	got := map[string][]domain.Unit{}
	for _, u := range a.Units {
		got[u.Name] = append(got[u.Name], u)
		if u.File != file || u.Package != a.Package {
			t.Errorf("%s: file %q package %+v", u.Name, u.File, u.Package)
		}
	}
	for name, w := range wants {
		us := got[name]
		if len(us) != 1 {
			t.Errorf("%s: want one unit, got %d", name, len(us))
			continue
		}
		u := us[0]
		if u.Kind != w.kind || u.Parent != w.parent || u.StartLine != w.start || u.EndLine != w.end {
			t.Errorf("%s: got %s parent=%q lines %d-%d, want %s parent=%q lines %d-%d",
				name, u.Kind, u.Parent, u.StartLine, u.EndLine, w.kind, w.parent, w.start, w.end)
		}
	}
	for name := range got {
		if _, ok := wants[name]; !ok {
			t.Errorf("unexpected unit %s", name)
		}
	}
}

const jsSample = `// Shopping cart.
import { api } from "./api";

/** A cart of items. */
export class Cart extends Base {
  items = [];
  #secret = 1;

  @memo
  static of(list) {
    return new Cart(list);
  }

  get size() {
    return this.items.length;
  }

  onAdd = (item) => {
    this.items.push(item);
    return this;
  };
}

export function total(cart) {
  let sum = 0;
  for (const i of cart.items) sum += i.price;
  return sum;
}

export const TAX = 0.2, { round, floor: down } = Math;
const fmt = async (n) => {
  const s = await api(n);
  return s;
};
export default class {
  run() {}
}
function* ids() {
  function nested() {}
  yield nested;
}
module.exports.extra = function () {};
`

func TestJavaScriptUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzeJS(t, newTestRuntime(t), "web/shop/cart.js", jsSample)
	if a.Package != (domain.PackageRef{Dir: "web/shop", Name: "shop", Hierarchical: true}) || a.Test || a.Generated {
		t.Errorf("package %+v test=%v generated=%v", a.Package, a.Test, a.Generated)
	}
	checkJSUnits(t, a, "web/shop/cart.js", map[string]jsWant{
		"cart":              {domain.KindModule, "", 1, 42},
		"cart.Cart":         {domain.KindClass, "cart", 5, 22},
		"cart.Cart.items":   {domain.KindField, "cart.Cart", 6, 6},
		"cart.Cart.#secret": {domain.KindField, "cart.Cart", 7, 7},
		"cart.Cart.of":      {domain.KindMethod, "cart.Cart", 9, 12},
		"cart.Cart.size":    {domain.KindMethod, "cart.Cart", 14, 16},
		"cart.Cart.onAdd":   {domain.KindMethod, "cart.Cart", 18, 21},
		"cart.total":        {domain.KindFunction, "cart", 24, 28},
		"cart.TAX":          {domain.KindVar, "cart", 30, 30},
		"cart.round":        {domain.KindVar, "cart", 30, 30},
		"cart.down":         {domain.KindVar, "cart", 30, 30},
		"cart.fmt":          {domain.KindFunction, "cart", 31, 34},
		"cart.default":      {domain.KindClass, "cart", 35, 37},
		"cart.default.run":  {domain.KindMethod, "cart.default", 36, 36},
		"cart.ids":          {domain.KindFunction, "cart", 38, 41},
	})
	got := unitsByName(a)
	for name, sig := range map[string]string{
		"cart":            "module web/shop/cart.js",
		"cart.Cart":       "export class Cart extends Base",
		"cart.Cart.of":    "@memo static of(list)",
		"cart.Cart.size":  "get size()",
		"cart.Cart.onAdd": "onAdd = (item) =>",
		"cart.total":      "export function total(cart)",
		"cart.TAX":        "export const TAX = 0.2",
		"cart.round":      "export const { round, floor: down } = Math",
		"cart.fmt":        "const fmt = async (n) =>",
		"cart.default":    "export default class",
		"cart.ids":        "function* ids()",
	} {
		if got[name].Signature != sig {
			t.Errorf("%s signature %q, want %q", name, got[name].Signature, sig)
		}
	}
	if got["cart.Cart"].Doc != "A cart of items." || got["cart.total"].Doc != "" {
		t.Errorf("docs %q %q", got["cart.Cart"].Doc, got["cart.total"].Doc)
	}
	if src := got["cart.Cart.of"].Source; !strings.HasPrefix(src, "@memo") || !strings.HasSuffix(src, "}") {
		t.Errorf("a unit's code includes its decorators: %q", src)
	}
	if src := got["cart.TAX"].Source; src != "TAX = 0.2" {
		t.Errorf("one of several declarators spans itself: %q", src)
	}
	if src := got["cart.fmt"].Source; !strings.HasPrefix(src, "const fmt") || !strings.HasSuffix(src, "};") {
		t.Errorf("a lone declarator spans its statement: %q", src)
	}
}

func TestJSXUnits(t *testing.T) {
	src := `import React from "react";

export function Hello({ name }) {
  return <p className="greeting">Hello {name}</p>;
}

export const List = ({ items }) => (
  <ul>
    {items.map((i) => <li key={i}>{i}</li>)}
  </ul>
);
`
	a := analyzeJS(t, newTestRuntime(t), "ui/Hello.jsx", src)
	checkJSUnits(t, a, "ui/Hello.jsx", map[string]jsWant{
		"Hello":       {domain.KindModule, "", 1, 11},
		"Hello.Hello": {domain.KindFunction, "Hello", 3, 5},
		"Hello.List":  {domain.KindFunction, "Hello", 7, 11},
	})
}

const tsSample = `import { Injectable } from "@angular/core";

/** Stores users. */
@Injectable()
export abstract class Repo<T> implements Store {
  @Input() name: string = "repo";
  private readonly cache?: Map<string, T>;

  constructor(private http: Http) {
    super();
  }

  @HostListener("click")
  onClick(e: Event): void {
    this.go(e);
  }

  abstract load(id: string): Promise<T>;
  find(id: string): T;
  find(id: number): T;
  find(id: any): T {
    return this.cache.get(id);
  }
  [key: string]: unknown;
}

export interface Store<K = string> extends Base {
  /** The size. */
  size: number;
  get?(key: K): unknown;
  [index: number]: string;
}

export type Id = string | number;

export const enum Color {
  Red,
  Green = "g",
  "Blue" = 3,
}

export namespace Shapes.Flat {
  export function area(s: Shape): number {
    return s.w * s.h;
  }
}

declare global {
  interface Window {
    app: App;
  }
}

declare module "express" {
  interface Request {
    user?: User;
  }
}

export declare function parse(s: string): Id;
export function pick(a: string): string;
export function pick(a: any): any {
  return a;
}
export const handler = function named(req: Req): void {
  log(req);
  done();
};
export default interface Options {
  verbose: boolean;
}
`

func TestTypeScriptUnitsNamesKindsNestingAndLines(t *testing.T) {
	a := analyzeJS(t, newTestRuntime(t), "src/repo.ts", tsSample)
	wants := map[string]jsWant{
		"repo":                      {domain.KindModule, "", 1, 71},
		"repo.Repo":                 {domain.KindClass, "repo", 4, 25},
		"repo.Repo.name":            {domain.KindField, "repo.Repo", 6, 6},
		"repo.Repo.cache":           {domain.KindField, "repo.Repo", 7, 7},
		"repo.Repo.constructor":     {domain.KindMethod, "repo.Repo", 9, 11},
		"repo.Repo.onClick":         {domain.KindMethod, "repo.Repo", 13, 16},
		"repo.Repo.load":            {domain.KindMethod, "repo.Repo", 18, 18},
		"repo.Store":                {domain.KindInterface, "repo", 27, 32},
		"repo.Store.size":           {domain.KindField, "repo.Store", 29, 29},
		"repo.Store.get":            {domain.KindInterfaceMethod, "repo.Store", 30, 30},
		"repo.Id":                   {domain.KindType, "repo", 34, 34},
		"repo.Color":                {domain.KindEnum, "repo", 36, 40},
		"repo.Color.Red":            {domain.KindEnumMember, "repo.Color", 37, 37},
		"repo.Color.Green":          {domain.KindEnumMember, "repo.Color", 38, 38},
		"repo.Color.Blue":           {domain.KindEnumMember, "repo.Color", 39, 39},
		"repo.Shapes.Flat.area":     {domain.KindFunction, "repo", 43, 45},
		"repo.global.Window":        {domain.KindInterface, "repo", 49, 51},
		"repo.global.Window.app":    {domain.KindField, "repo.global.Window", 50, 50},
		"repo.express.Request":      {domain.KindInterface, "repo", 55, 57},
		"repo.express.Request.user": {domain.KindField, "repo.express.Request", 56, 56},
		"repo.parse":                {domain.KindFunction, "repo", 60, 60},
		"repo.handler":              {domain.KindFunction, "repo", 65, 68},
		"repo.Options":              {domain.KindInterface, "repo", 69, 71},
		"repo.Options.verbose":      {domain.KindField, "repo.Options", 70, 70},
	}
	// Overloads are one unit each, in source order.
	var find, pick []string
	rest := domain.FileAnalysis{Package: a.Package}
	for _, u := range a.Units {
		switch u.Name {
		case "repo.Repo.find":
			find = append(find, fmt.Sprintf("%s %d", u.Kind, u.StartLine))
		case "repo.pick":
			pick = append(pick, fmt.Sprintf("%s %d", u.Kind, u.StartLine))
		default:
			rest.Units = append(rest.Units, u)
		}
	}
	checkJSUnits(t, rest, "src/repo.ts", wants)
	if strings.Join(find, ",") != "METHOD 19,METHOD 20,METHOD 21" || strings.Join(pick, ",") != "FUNCTION 61,FUNCTION 62" {
		t.Errorf("overloads: %v %v", find, pick)
	}
	got := unitsByName(a)
	for name, sig := range map[string]string{
		"repo.Repo":         `@Injectable() export abstract class Repo<T> implements Store`,
		"repo.Repo.name":    `@Input() name: string = "repo"`,
		"repo.Repo.onClick": `@HostListener("click") onClick(e: Event): void`,
		"repo.Repo.load":    "abstract load(id: string): Promise<T>",
		"repo.Store":        "export interface Store<K = string> extends Base",
		"repo.Store.get":    "get?(key: K): unknown",
		"repo.Color":        "export const enum Color",
		"repo.parse":        "export declare function parse(s: string): Id;",
		"repo.handler":      "export const handler = function named(req: Req): void",
	} {
		if got[name].Signature != sig {
			t.Errorf("%s signature %q, want %q", name, got[name].Signature, sig)
		}
	}
	if got["repo.Repo"].Doc != "Stores users." || got["repo.Store.size"].Doc != "The size." {
		t.Errorf("docs %q %q", got["repo.Repo"].Doc, got["repo.Store.size"].Doc)
	}
	if src := got["repo.Repo.onClick"].Source; !strings.HasPrefix(src, `@HostListener("click")`) {
		t.Errorf("a TypeScript method's sibling decorators are part of it: %q", src)
	}
}

// The remaining documented shapes: default exports, class expressions, every binding pattern,
// var, generator expressions, setters, static blocks, function-valued fields, re-exports, and a
// namespace that is not exported.
func TestJSRemainingDeclarationShapes(t *testing.T) {
	rt := newTestRuntime(t)
	arrow := analyzeJS(t, rt, "a.js", "export default (a) => {\n  f(a);\n  return a;\n};\n")
	checkJSUnits(t, arrow, "a.js", map[string]jsWant{
		"a":         {domain.KindModule, "", 1, 4},
		"a.default": {domain.KindFunction, "a", 1, 4},
	})
	if u := arrow.Units[1]; u.BodyHash == "" || u.Signature != "export default (a) =>" {
		t.Errorf("anonymous default function: %+v", u)
	}
	object := analyzeJS(t, rt, "b.js", "export default { a: 1 };\n")
	checkJSUnits(t, object, "b.js", map[string]jsWant{
		"b":         {domain.KindModule, "", 1, 1},
		"b.default": {domain.KindVar, "b", 1, 1},
	})
	named := analyzeJS(t, rt, "c.mjs", "export default function named() {}\n")
	checkJSUnits(t, named, "c.mjs", map[string]jsWant{
		"c":       {domain.KindModule, "", 1, 1},
		"c.named": {domain.KindFunction, "c", 1, 1},
	})

	src := `export { api } from "./api";
export * from "./types";
export const Store = class extends Base {
  /** Reads it. */
  get() {
    return 1;
  }
  set size(v) {}
  cb = function () {
    a();
    b();
  };
  static {
    init();
  }
};
const [first, , second = 2, ...others] = list, { x = 1, ...restObj } = o;
var legacy = 1;
const gen = function* () {};
`
	a := analyzeJS(t, rt, "m.js", src)
	checkJSUnits(t, a, "m.js", map[string]jsWant{
		"m":            {domain.KindModule, "", 1, 19},
		"m.Store":      {domain.KindClass, "m", 3, 16},
		"m.Store.get":  {domain.KindMethod, "m.Store", 5, 7},
		"m.Store.size": {domain.KindMethod, "m.Store", 8, 8},
		"m.Store.cb":   {domain.KindMethod, "m.Store", 9, 12},
		"m.first":      {domain.KindVar, "m", 17, 17},
		"m.second":     {domain.KindVar, "m", 17, 17},
		"m.others":     {domain.KindVar, "m", 17, 17},
		"m.x":          {domain.KindVar, "m", 17, 17},
		"m.restObj":    {domain.KindVar, "m", 17, 17},
		"m.legacy":     {domain.KindVar, "m", 18, 18},
		"m.gen":        {domain.KindFunction, "m", 19, 19},
	})
	got := unitsByName(a)
	if got["m.Store"].Signature != "export const Store = class extends Base" || got["m.Store.get"].Doc != "Reads it." {
		t.Errorf("class expression: %q doc %q", got["m.Store"].Signature, got["m.Store.get"].Doc)
	}
	if got["m.Store.cb"].BodyHash == "" {
		t.Error("a function-valued field can prove a rename")
	}
	// The class expression's hash includes its statement's own code, not its members.
	unexported := unitsByName(analyzeJS(t, rt, "m.js", strings.Replace(src, "export const Store", "const Store", 1)))
	if unexported["m.Store"].Hash == got["m.Store"].Hash || unexported["m.Store.get"].Hash != got["m.Store.get"].Hash {
		t.Error("export changes the class expression only")
	}
	// A static block is class code.
	static := unitsByName(analyzeJS(t, rt, "m.js", strings.Replace(src, "init();", "setup();", 1)))
	if static["m.Store"].Hash == got["m.Store"].Hash {
		t.Error("a static block is part of its class")
	}

	ns := analyzeJS(t, rt, "n.ts", "namespace Inner {\n  export const k = 1;\n}\nmodule Old.Style {\n  function f() {}\n}\n")
	checkJSUnits(t, ns, "n.ts", map[string]jsWant{
		"n":             {domain.KindModule, "", 1, 6},
		"n.Inner.k":     {domain.KindVar, "n", 2, 2},
		"n.Old.Style.f": {domain.KindFunction, "n", 5, 5},
	})
}

// A quoted member name is the string's value, so quote style is not identity.
func TestJSQuotedMemberNamesDropTheirQuotes(t *testing.T) {
	rt := newTestRuntime(t)
	single := unitsByName(analyzeJS(t, rt, "m.ts", "class C {\n  'a-b'() {}\n  'f' = 1;\n}\ninterface I { 'p': string; 'q'(): void }\n"))
	double := unitsByName(analyzeJS(t, rt, "m.ts", "class C {\n  \"a-b\"() {}\n  \"f\" = 1;\n}\ninterface I { \"p\": string; \"q\"(): void }\n"))
	for _, n := range []string{"m.C.a-b", "m.C.f", "m.I.p", "m.I.q"} {
		if _, ok := single[n]; !ok {
			t.Errorf("missing %s", n)
		}
		if single[n].Hash != double[n].Hash {
			t.Errorf("%s: quote style changed the hash", n)
		}
	}
}

// A declarator that binds no name is module code, so changing it changes the module.
func TestJSEmptyPatternIsModuleCode(t *testing.T) {
	rt := newTestRuntime(t)
	a := unitsByName(analyzeJS(t, rt, "m.js", "const {} = a;\n"))
	b := unitsByName(analyzeJS(t, rt, "m.js", "const {} = b;\n"))
	if a["m"].Hash == b["m"].Hash {
		t.Error("the module did not see the change")
	}
}

func TestJSFieldMethodBodyHashProvesRenames(t *testing.T) {
	rt := newTestRuntime(t)
	for _, p := range []string{"m.js", "m.ts"} {
		field := func(name string) domain.Unit {
			u := unitsByName(analyzeJS(t, rt, p, "class C {\n  "+name+" = (a) => {\n    const b = a + 1;\n    return b;\n  };\n}\n"))["m.C."+name]
			if u.Kind != domain.KindMethod {
				t.Errorf("%s: an arrow-valued field is a method: %+v", p, u)
			}
			return u
		}
		a, b := field("onA"), field("onB")
		if a.BodyHash == "" || a.BodyHash != b.BodyHash || a.Hash == b.Hash {
			t.Errorf("%s: a renamed field method keeps its body hash: %q %q", p, a.BodyHash, b.BodyHash)
		}
	}
}

// A statement's own text before its first declarator, such as a long comment, is part of every
// declarator's signature. It must be bounded once, not copied per declarator, or one statement
// costs time and memory in the product of the two.
func TestJSManyDeclaratorsShareTheStatementPrefix(t *testing.T) {
	rt := newTestRuntime(t)
	if _, err := NewJavaScript(rt).Analyze(context.Background(), "m.js", []byte("var a;\n")); err != nil {
		t.Fatal(err) // compile the grammar outside the measurement
	}
	var b strings.Builder
	b.WriteString("var /*" + strings.Repeat("x ", 128<<10) + "*/ ")
	for i := 0; i < 20000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "a%d", i)
	}
	src := []byte(b.String() + ";\n")
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	a, err := NewJavaScript(rt).Analyze(context.Background(), "m.js", src)
	goruntime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Units) != 20001 {
		t.Fatalf("units: %d", len(a.Units))
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 300<<20 {
		t.Fatalf("analyzing a %d KB file allocated %d MB", len(src)>>10, got>>20)
	}
}

func TestTSXUsesTheTSXDialect(t *testing.T) {
	rt := newTestRuntime(t)
	src := `export const App = (p: Props) => <div>{p.title as string}</div>;
export const id = <T,>(x: T): T => x;
`
	a := analyzeJS(t, rt, "ui/App.tsx", src)
	checkJSUnits(t, a, "ui/App.tsx", map[string]jsWant{
		"App":     {domain.KindModule, "", 1, 2},
		"App.App": {domain.KindFunction, "App", 1, 1},
		"App.id":  {domain.KindFunction, "App", 2, 2},
	})
	// JSX is not TypeScript, and a type assertion is not TSX.
	js := NewJavaScript(rt)
	if _, err := js.Analyze(context.Background(), "ui/App.ts", []byte(src)); !errors.Is(err, ErrSyntax) {
		t.Errorf(".ts must parse without JSX: %v", err)
	}
	cast := "const n = <number>value;\n"
	for _, p := range []string{"a.ts", "a.mts", "a.cts"} {
		if _, err := js.Analyze(context.Background(), p, []byte(cast)); err != nil {
			t.Errorf("%s allows angle-bracket assertions: %v", p, err)
		}
	}
	if _, err := js.Analyze(context.Background(), "a.tsx", []byte(cast)); !errors.Is(err, ErrSyntax) {
		t.Errorf(".tsx must parse as TSX: %v", err)
	}
	// JavaScript files use the JavaScript grammar, which has no type annotations.
	for _, p := range []string{"a.js", "a.jsx", "a.mjs", "a.cjs"} {
		if _, err := js.Analyze(context.Background(), p, []byte("const n: number = 1;\n")); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s must parse as JavaScript: %v", p, err)
		}
	}
}

func TestJSPackageAndModuleNaming(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]struct {
		pkg    domain.PackageRef
		module string
	}{
		"index.js":              {domain.PackageRef{Dir: ".", Name: "", Hierarchical: true}, "index"},
		"src/types/index.d.ts":  {domain.PackageRef{Dir: "src/types", Name: "types", Hierarchical: true}, "index.d"},
		"src/Button.test.tsx":   {domain.PackageRef{Dir: "src", Name: "src", Hierarchical: true}, "Button.test"},
		"lib/config.mjs":        {domain.PackageRef{Dir: "lib", Name: "lib", Hierarchical: true}, "config"},
		"lib/legacy.cjs":        {domain.PackageRef{Dir: "lib", Name: "lib", Hierarchical: true}, "legacy"},
		"lib/node/esm.mts":      {domain.PackageRef{Dir: "lib/node", Name: "node", Hierarchical: true}, "esm"},
		"lib/node/commonjs.cts": {domain.PackageRef{Dir: "lib/node", Name: "node", Hierarchical: true}, "commonjs"},
	} {
		a := analyzeJS(t, rt, p, "export function f() {}\n")
		if a.Package != want.pkg || a.Units[0].Name != want.module || a.Units[0].Kind != domain.KindModule ||
			a.Units[1].Name != want.module+".f" || a.Units[1].Parent != want.module {
			t.Errorf("%s: package %+v units %+v", p, a.Package, a.Units)
		}
	}
	empty := analyzeJS(t, rt, "a.js", "")
	if len(empty.Units) != 1 || empty.Units[0].StartLine != 1 || empty.Units[0].EndLine != 1 {
		t.Errorf("empty file: %+v", empty.Units)
	}
}

func jsFunctionHash(t *testing.T, rt *Runtime, p, src string) (string, string) {
	t.Helper()
	for _, u := range analyzeJS(t, rt, p, src).Units {
		if u.Kind == domain.KindFunction {
			return u.Hash, u.BodyHash
		}
	}
	t.Fatalf("no function in %q", src)
	return "", ""
}

func TestJSHashIgnoresFormattingAndComments(t *testing.T) {
	rt := newTestRuntime(t)
	base := "function f(a, b) {\n  const x = g(a, \"x\", 0xff);\n  return x;\n}\n"
	same := map[string]string{
		"comments":         "function f(a, b) { // note\n  /* why */ const x = g(a, \"x\", 0xff);\n  return x;\n}\n",
		"whitespace":       "function f( a,b ){\n\n    const x=g( a , \"x\" , 0xff );\n  return x;\n}\n",
		"quotes":           "function f(a, b) {\n  const x = g(a, 'x', 0xff);\n  return x;\n}\n",
		"semicolons":       "function f(a, b) {\n  const x = g(a, \"x\", 0xff)\n  return x\n}\n",
		"trailing commas":  "function f(a, b,) {\n  const x = g(a, \"x\", 0xff,);\n  return x;\n}\n",
		"redundant parens": "function f(a, b) {\n  const x = (g(a, \"x\", 0xff));\n  return (x);\n}\n",
		"hex digit case":   "function f(a, b) {\n  const x = g(a, \"x\", 0XFF);\n  return x;\n}\n",
		"line breaks":      "function f(\n  a,\n  b,\n) {\n  const x = g(\n    a,\n    \"x\",\n    0xff,\n  );\n  return x;\n}\n",
	}
	different := map[string]string{
		"argument":        "function f(a, b) {\n  const x = g(b, \"x\", 0xff);\n  return x;\n}\n",
		"string":          "function f(a, b) {\n  const x = g(a, \"y\", 0xff);\n  return x;\n}\n",
		"template":        "function f(a, b) {\n  const x = g(a, `x`, 0xff);\n  return x;\n}\n",
		"name":            "function h(a, b) {\n  const x = g(a, \"x\", 0xff);\n  return x;\n}\n",
		"let":             "function f(a, b) {\n  let x = g(a, \"x\", 0xff);\n  return x;\n}\n",
		"async":           "async function f(a, b) {\n  const x = g(a, \"x\", 0xff);\n  return x;\n}\n",
		"exported":        "export function f(a, b) {\n  const x = g(a, \"x\", 0xff);\n  return x;\n}\n",
		"optional chain":  "function f(a, b) {\n  const x = g?.(a, \"x\", 0xff);\n  return x;\n}\n",
		"sequence":        "function f(a, b) {\n  const x = g((a, \"x\"), 0xff);\n  return x;\n}\n",
		"strict equality": "function f(a, b) {\n  const x = g(a == \"x\", 0xff);\n  return x;\n}\n",
	}
	want, _ := jsFunctionHash(t, rt, "m.js", base)
	for name, src := range same {
		if h, _ := jsFunctionHash(t, rt, "m.js", src); h != want {
			t.Errorf("%s changed the hash", name)
		}
	}
	for name, src := range different {
		if h, _ := jsFunctionHash(t, rt, "m.js", src); h == want {
			t.Errorf("%s did not change the hash", name)
		}
	}
	strict, _ := jsFunctionHash(t, rt, "m.js", "function f(a, b) {\n  const x = g(a === \"x\", 0xff);\n  return x;\n}\n")
	loose, _ := jsFunctionHash(t, rt, "m.js", different["strict equality"])
	if strict == loose {
		t.Error("== and === differ")
	}
}

func TestTSHashSeesTypes(t *testing.T) {
	rt := newTestRuntime(t)
	base := "function f(a: string): number {\n  return a.length;\n}\n"
	want, _ := jsFunctionHash(t, rt, "m.ts", base)
	for name, src := range map[string]string{
		"parameter type": "function f(a: number): number {\n  return a.length;\n}\n",
		"return type":    "function f(a: string): string {\n  return a.length;\n}\n",
		"optional":       "function f(a?: string): number {\n  return a.length;\n}\n",
		"non-null":       "function f(a: string): number {\n  return a!.length;\n}\n",
		"generic":        "function f<T>(a: string): number {\n  return a.length;\n}\n",
	} {
		if h, _ := jsFunctionHash(t, rt, "m.ts", src); h == want {
			t.Errorf("%s did not change the hash", name)
		}
	}
	if h, _ := jsFunctionHash(t, rt, "m.ts", "function f(a:string):number{return a.length}\n"); h != want {
		t.Error("formatting changed a TypeScript hash")
	}
}

// Text that tree-sitter keeps in tokens, such as string and template contents and regular
// expressions, must count exactly. Otherwise a real change reads as formatting.
func TestJSHashSeesTextInStringsTemplatesAndRegexes(t *testing.T) {
	rt := newTestRuntime(t)
	for name, pair := range map[string][2]string{
		"space inside string":    {`"a b"`, `"ab"`},
		"escape":                 {`"a\n"`, `"a\t"`},
		"space in template":      {"`a ${x} b`", "`a ${x}b`"},
		"space between holes":    {"`${x} ${y}`", "`${x}${y}`"},
		"template escape":        {"`a\\n`", "`a\\t`"},
		"multi-line template":    {"`a\n  b`", "`a\nb`"},
		"space in regex":         {`/a b/`, `/ab/`},
		"regex flags":            {`/a/g`, `/a/i`},
		"regex class":            {`/[ ]+/`, `/[\t]+/`},
		"jsx text":               {`<p>Hello world</p>`, `<p>Hello  world</p>`},
		"jsx attribute":          {`<p title="a b" />`, `<p title="ab" />`},
		"jsx text against a tag": {`<p><b>a</b> b</p>`, `<p><b>a</b>b</p>`},
		"array hole":             {`[1, , 2]`, `[1, 2]`},
		"array length":           {`[1, 2,,]`, `[1, 2,]`},
		"pattern hole":           {`(([, a]) => a)(x)`, `(([a]) => a)(x)`},
	} {
		h := func(expr string) (string, string) {
			return jsFunctionHash(t, rt, "m.jsx", "function f(x, y) {\n  const v = "+expr+";\n  return v;\n}\n")
		}
		h1, b1 := h(pair[0])
		h2, b2 := h(pair[1])
		if h1 == h2 || b1 == b2 {
			t.Errorf("%s: %s and %s must hash differently", name, pair[0], pair[1])
		}
	}
	// JSX text that only rewraps renders the same.
	wrap1, _ := jsFunctionHash(t, rt, "m.jsx", "function f() {\n  return <p>Hello world</p>;\n}\n")
	wrap2, _ := jsFunctionHash(t, rt, "m.jsx", "function f() {\n  return (\n    <p>\n      Hello\n      world\n    </p>\n  );\n}\n")
	if wrap1 != wrap2 {
		t.Error("rewrapping JSX text is formatting")
	}
}

func TestJSContainerHashExcludesMembers(t *testing.T) {
	rt := newTestRuntime(t)
	units := func(src string) map[string]domain.Unit { return unitsByName(analyzeJS(t, rt, "m.ts", src)) }
	base := units("import a from \"a\";\nclass C {\n  x = 1;\n  m() { return 1; }\n}\ninterface I { a: string; b(): void }\nenum E { A, B }\n")
	member := units("import a from \"a\";\nclass C {\n  x = 1;\n  m() { return 2; }\n}\ninterface I { a: number; b(): void }\nenum E { A, B }\n")
	if member["m.C"].Hash != base["m.C"].Hash || member["m"].Hash != base["m"].Hash || member["m.I"].Hash != base["m.I"].Hash {
		t.Error("changing a member must not change its class, interface, or module")
	}
	if member["m.C.m"].Hash == base["m.C.m"].Hash || member["m.I.a"].Hash == base["m.I.a"].Hash {
		t.Error("the members changed")
	}
	header := units("import a from \"a\";\nclass C extends B {\n  x = 1;\n  m() { return 1; }\n}\ninterface I { a: string; b(): void }\nenum E { A, B }\n")
	if header["m.C"].Hash == base["m.C"].Hash || header["m.C.m"].Hash != base["m.C.m"].Hash {
		t.Error("changing the class header changes the class only")
	}
	imports := units("import b from \"a\";\nclass C {\n  x = 1;\n  m() { return 1; }\n}\ninterface I { a: string; b(): void }\nenum E { A, B }\n")
	if imports["m"].Hash == base["m"].Hash || imports["m.C"].Hash != base["m.C"].Hash {
		t.Error("changing an import changes the module only")
	}
	// An enum member without an initializer takes its value from the member before it.
	enum := func(body string) map[string]domain.Unit { return units("enum E { " + body + " }\n") }
	e1, reordered, appended, inserted := enum("A, B, C = 5, D"), enum("B, A, C = 5, D"), enum("A, B, C = 5, D, X"), enum("A, X, B, C = 5, D")
	if reordered["m.E.A"].Hash == e1["m.E.A"].Hash || reordered["m.E.B"].Hash == e1["m.E.B"].Hash || reordered["m.E.C"].Hash != e1["m.E.C"].Hash {
		t.Error("reordering enum members changes those whose values move")
	}
	for _, n := range []string{"m.E", "m.E.A", "m.E.B", "m.E.C", "m.E.D"} {
		if appended[n].Hash != e1[n].Hash {
			t.Errorf("appending a member does not change %s", n)
		}
	}
	if inserted["m.E.B"].Hash == e1["m.E.B"].Hash || inserted["m.E.A"].Hash != e1["m.E.A"].Hash || inserted["m.E.D"].Hash != e1["m.E.D"].Hash {
		t.Error("inserting a member changes only the run of implicit values after it")
	}
	// Implicit values follow the whole run before them, not just the previous name.
	run, runInserted := enum("A, B, C"), enum("A, X, B, C")
	if runInserted["m.E.C"].Hash == run["m.E.C"].Hash {
		t.Error("inserting a member moves every implicit value after it")
	}
	init1, init2 := enum("A = 1, B, C"), enum("A = 2, B, C")
	if init2["m.E.B"].Hash == init1["m.E.B"].Hash || init2["m.E.C"].Hash == init1["m.E.C"].Hash {
		t.Error("changing an initializer moves the implicit values after it")
	}
	comment := units("// new\nimport a from \"a\";\nclass C {\n  /** doc */\n  x = 1;\n  m() { return 1; }\n}\ninterface I { a: string; b(): void }\nenum E { A, B }\n")
	for _, n := range []string{"m", "m.C", "m.C.x"} {
		if comment[n].Hash != base[n].Hash {
			t.Errorf("a comment is not a change to %s", n)
		}
	}
	// A declarator's unit changes with its statement's own code.
	v1 := units("const a = 1, b = 2;\n")
	v2 := units("export const a = 1, b = 2;\n")
	v3 := units("const a = 1, b = 3;\n")
	if v1["m.a"].Hash == v2["m.a"].Hash || v1["m.a"].Hash != v3["m.a"].Hash || v1["m.b"].Hash == v3["m.b"].Hash {
		t.Error("declarator hashes")
	}
}

func TestJSBodyHashProvesRenamesOnly(t *testing.T) {
	rt := newTestRuntime(t)
	body := "(a) {\n  const b = a + 1;\n  return b;\n}\n"
	_, old := jsFunctionHash(t, rt, "m.js", "function old"+body)
	newHash, renamed := jsFunctionHash(t, rt, "m.js", "function renamed"+body)
	oldHash, _ := jsFunctionHash(t, rt, "m.js", "function old"+body)
	if old == "" || old != renamed || oldHash == newHash {
		t.Error("a rename keeps the body hash and changes the hash")
	}
	if _, async := jsFunctionHash(t, rt, "m.js", "async function renamed"+body); async == old {
		t.Error("an async version is not an identical body")
	}
	arrowOld := unitsByName(analyzeJS(t, rt, "m.js", "const old = (a) => {\n  const b = a + 1;\n  return b;\n};\n"))["m.old"]
	arrowNew := unitsByName(analyzeJS(t, rt, "m.js", "const fresh = (a) => {\n  const b = a + 1;\n  return b;\n};\n"))["m.fresh"]
	if arrowOld.BodyHash == "" || arrowOld.BodyHash != arrowNew.BodyHash {
		t.Error("a renamed arrow function keeps its body hash")
	}
	methodOld := unitsByName(analyzeJS(t, rt, "m.ts", "class C {\n  @d sum(a: number) {\n    const b = a + 1;\n    return b;\n  }\n}\n"))["m.C.sum"]
	methodNew := unitsByName(analyzeJS(t, rt, "m.ts", "class C {\n  @d total(a: number) {\n    const b = a + 1;\n    return b;\n  }\n}\n"))["m.C.total"]
	plain := unitsByName(analyzeJS(t, rt, "m.ts", "class C {\n  total(a: number) {\n    const b = a + 1;\n    return b;\n  }\n}\n"))["m.C.total"]
	if methodOld.BodyHash == "" || methodOld.BodyHash != methodNew.BodyHash || plain.BodyHash == methodOld.BodyHash {
		t.Errorf("method body hashes: %q %q %q", methodOld.BodyHash, methodNew.BodyHash, plain.BodyHash)
	}
	for _, src := range []string{
		"function f() {\n  return null;\n}\n",
		"const f = (a) => a + 1;\n",
		"declare function f(a: string): void;\n",
	} {
		if _, b := jsFunctionHash(t, rt, "m.ts", src); b != "" {
			t.Errorf("%q cannot prove a rename", src)
		}
	}
}

func TestJSSyntaxErrorsAreErrors(t *testing.T) {
	rt := newTestRuntime(t)
	for _, c := range []struct{ path, src string }{
		{"m.js", "function f( {\n  return 1;\n}\n"},
		{"m.js", "const = 3;\n"},
		{"m.js", "if (a {}\n"},
		{"m.js", "let s = 'abc\n"},
		{"m.jsx", "const a = <div><span></div>;\n"},
		{"m.mjs", "export default function f() {\n"},
		{"m.cjs", "module.exports = {;\n"},
		{"m.ts", "function f(a: ): void {}\n"},
		{"m.ts", "interface I { a: }\n"},
		{"m.mts", "enum E { A = }\n"},
		{"m.cts", "class C { private }\n x(\n"},
		{"m.tsx", "export const A = () => <div>;\n"},
		{"m.tsx", "type T = ;\n"},
	} {
		if _, err := NewJavaScript(rt).Analyze(context.Background(), c.path, []byte(c.src)); !errors.Is(err, ErrSyntax) {
			t.Errorf("%s %q: want a syntax error, got %v", c.path, c.src, err)
		}
	}
}

func TestJSTestAndGeneratedDetection(t *testing.T) {
	rt := newTestRuntime(t)
	for p, want := range map[string]bool{
		"src/cart.test.js": true, "src/cart.spec.ts": true, "src/__tests__/cart.tsx": true,
		"Cart.test.jsx": true, "src/cart.ts": false, "src/testing.js": false, "test/cart.js": false,
		"src/contest.ts": false,
	} {
		a := analyzeJS(t, rt, p, "export function f() {}\n")
		if a.Test != want || a.Units[1].Test != want {
			t.Errorf("%s: test=%v want %v", p, a.Test, want)
		}
	}
	for src, want := range map[string]bool{
		"// @generated\nexport const a = 1;\n":                                        true,
		"#!/usr/bin/env node\n/* Code generated by protoc. DO NOT EDIT. */\nlet a;\n": true,
		"/**\n * @fileoverview generated by tsc; do not edit\n */\nlet a;\n":          true,
		"let a;\n// @generated\n":                                                     false,
		"// Generated docs live elsewhere.\nlet a;\n":                                 false,
	} {
		if got := analyzeJS(t, rt, "m.ts", src).Generated; got != want {
			t.Errorf("%q: generated=%v want %v", src, got, want)
		}
	}
}

func TestJSSupports(t *testing.T) {
	js := NewJavaScript(nil)
	for _, p := range []string{"a.js", "a/b.jsx", "x.mjs", "x.cjs", "a.ts", "a/b.tsx", "x.mts", "x.cts", "types/index.d.ts"} {
		if !js.Supports(p) {
			t.Errorf("%s is JavaScript or TypeScript", p)
		}
	}
	for _, p := range []string{"a.json", "a.js.map", "a.vue", "js", ".js", "dir/.ts", "a.tsx.snap", "a.d", "a.JS"} {
		if js.Supports(p) {
			t.Errorf("%s is neither", p)
		}
	}
	if _, err := js.Analyze(context.Background(), "a.json", []byte("{}")); err == nil {
		t.Error("an unsupported file is refused")
	}
}

// One declaration binding many names must not copy it once per name, or a small file could
// need gigabytes.
func TestJSManyNamesInOneDeclarationShareItsText(t *testing.T) {
	rt := newTestRuntime(t)
	var b strings.Builder
	b.WriteString("const {")
	for i := 0; i < 16000; i++ {
		fmt.Fprintf(&b, "a%d,", i)
	}
	src := []byte(b.String() + "} = o;\n")
	var before, after goruntime.MemStats
	goruntime.GC()
	goruntime.ReadMemStats(&before)
	a, err := NewJavaScript(rt).Analyze(context.Background(), "m.js", src)
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
