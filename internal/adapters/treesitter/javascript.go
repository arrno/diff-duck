package treesitter

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

// JavaScript and TypeScript are one language family with one extractor. tree-sitter-javascript
// parses .js .jsx .mjs .cjs (JSX is part of that grammar); tree-sitter-typescript parses
// .ts .mts .cts with its typescript dialect and .tsx with its tsx dialect.
//
// Structure: a directory is a package (PackageRef{Dir: dir, Name: the directory's own name,
// empty at the repository root}), as for Python. Each file is a MODULE unit spanning the whole
// file, named by its base name less the last extension ("Button" for Button.tsx,
// "Button.test" for Button.test.tsx, "index.d" for index.d.ts). Every other unit is qualified
// by the module, and its Parent chain ends there. Under the module:
//
//   - function declarations, overload signatures, and `declare function` are FUNCTIONs;
//   - class and abstract class declarations are CLASSes. Their methods, getters, setters,
//     constructors, and method or abstract method signatures are METHODs; their fields are
//     FIELDs, except a field whose value is an arrow function or function expression, which
//     is a METHOD ("handleClick = () => {...}");
//   - each name bound by a top-level const, let, or var is a VAR, or a FUNCTION when its value
//     is an arrow function, function expression, or generator, or a CLASS (with members) when
//     its value is a class expression. Destructuring binds every name in the pattern;
//   - interfaces are INTERFACEs with property signatures as FIELDs and method signatures as
//     INTERFACE_METHODs; type aliases are TYPEs; enums are ENUMs with ENUM_MEMBERs;
//   - `export default` of an anonymous function or class is a FUNCTION or CLASS named
//     "default"; of any other expression, a VAR named "default". A named default
//     declaration keeps its own name.
//
// A unit's code includes `export`, `declare`, and its decorators, and its lines start at the
// first of them. Namespaces (`namespace A.B {}`, `module "x" {}`, `declare global {}`) are not
// units: they qualify the names of what they declare ("util.A.B.f", "util.global.Window"),
// whose Parent is the enclosing unit. Functions declared inside functions are part of them.
// CommonJS assignments (module.exports = ...), imports, and re-exports are module code.
//
// Hash ignores comments, formatting, quote style, semicolons, commas (except in arrays, where
// one can be a hole), redundant parentheses, and the case of number literals. String, template,
// and regular expression text counts exactly; JSX text counts as React renders it. Quoted
// member names are named by their value. BodyHash, for rename detection, covers a
// function's decorators, modifiers, parameters, types, and body but not its name, and is set
// only for a block body of two or more statements.
//
// Test files are *.test.*, *.spec.*, and anything under a __tests__ directory. A file is
// generated when the comments before its first statement say "@generated", or say both
// "generated" and "do not edit".

//go:embed wasm/javascript.wasm
var javascriptWasm []byte

//go:embed wasm/typescript.wasm
var typescriptWasm []byte

//go:embed wasm/tsx.wasm
var tsxWasm []byte

var (
	javascriptGrammar = Grammar{name: "javascript", wasm: javascriptWasm}
	typescriptGrammar = Grammar{name: "typescript", wasm: typescriptWasm}
	tsxGrammar        = Grammar{name: "tsx", wasm: tsxWasm}
)

// jsGrammars maps each supported extension to the grammar that parses it.
var jsGrammars = map[string]Grammar{
	".js": javascriptGrammar, ".jsx": javascriptGrammar, ".mjs": javascriptGrammar, ".cjs": javascriptGrammar,
	".ts": typescriptGrammar, ".mts": typescriptGrammar, ".cts": typescriptGrammar,
	".tsx": tsxGrammar,
}

// JavaScript implements ports.SourceAnalyzer for JavaScript, JSX, TypeScript, and TSX. The
// family is one analyzer so a file renamed between dialects, such as cart.js to cart.ts, keeps
// its units' identity.
type JavaScript struct{ rt *Runtime }

// NewJavaScript returns a JavaScript and TypeScript analyzer running on rt.
func NewJavaScript(rt *Runtime) *JavaScript { return &JavaScript{rt: rt} }

// Supports reports whether the path is a JavaScript or TypeScript source file.
func (*JavaScript) Supports(p string) bool {
	_, ok := jsGrammars[path.Ext(p)]
	return ok && len(path.Base(p)) > len(path.Ext(p))
}

// Analyze parses one file into units with the grammar for its extension.
func (a *JavaScript) Analyze(ctx context.Context, p string, src []byte) (domain.FileAnalysis, error) {
	g, ok := jsGrammars[path.Ext(p)]
	if !ok {
		return domain.FileAnalysis{}, fmt.Errorf("analyze %s: not a JavaScript or TypeScript file", p)
	}
	return jsAnalyze(ctx, a.rt, g, p, src)
}

func jsAnalyze(ctx context.Context, rt *Runtime, g Grammar, p string, src []byte) (domain.FileAnalysis, error) {
	tree, err := parseClean(ctx, rt, g, p, src)
	if err != nil {
		return domain.FileAnalysis{}, err
	}
	return extractJS(ctx, tree, p)
}

// extractJS extracts the units of a cleanly parsed JavaScript or TypeScript file.
func extractJS(ctx context.Context, tree *Tree, p string) (domain.FileAnalysis, error) {
	root := tree.Root()
	base := path.Base(p)
	module := strings.TrimSuffix(base, path.Ext(base))
	e := &jsExtractor{newFileUnits(ctx, tree, p, dirPackage(p), isJSTest(p))}
	owned := map[Node]bool{}
	slot := e.reserve()
	e.statements(root, jsScope{prefix: module, parent: module}, owned)
	if e.stopped() { // the module's hash covers the whole file
		return e.analysis(domain.FileAnalysis{})
	}
	e.units[slot] = e.finish(domain.Unit{
		Kind:      domain.KindModule,
		Name:      module,
		Signature: domain.ModuleSignature(p),
		Hash:      jsHash(owned).hash(root),
		StartLine: 1,
		EndLine:   lineCount(tree.Text()),
		Source:    tree.Text(),
	})
	return e.analysis(domain.FileAnalysis{Package: e.pkg, Generated: isJSGenerated(root)})
}

// jsScope is where a declaration sits: prefix qualifies its name, and parent is the enclosing
// unit. They differ inside a namespace, which qualifies names but is not a unit.
type jsScope struct{ prefix, parent string }

type jsExtractor struct{ fileUnits }

// spanned completes a unit that runs from the start of one node to the end of another.
func (e *jsExtractor) spanned(u domain.Unit, from, to Node) domain.Unit {
	u.StartLine, u.EndLine = from.StartLine(), to.EndLine()
	u.Source = from.TextUntil(to.EndByte())
	return e.finish(u)
}

func (e *jsExtractor) add(u domain.Unit, from, to Node) {
	e.units = append(e.units, e.spanned(u, from, to))
}

// header returns a declaration's text from its start up to its body, such as
// "export async function f(a: T): R", or all of it when it has no body.
func (e *jsExtractor) header(from, body, whole Node) string {
	if body.IsNull() {
		return from.TextUntil(whole.EndByte())
	}
	return from.TextUntil(body.StartByte())
}

// statements extracts the units a program, namespace, or block declares. Statements that became
// units are recorded in owned, so the enclosing module's hash leaves them out.
func (e *jsExtractor) statements(block Node, sc jsScope, owned map[Node]bool) {
	doc := ""
	for _, s := range block.Children() {
		if e.stopped() {
			return
		}
		if s.IsExtra() {
			doc = jsDocComment(s)
			continue
		}
		if s.IsNamed() {
			e.statement(s, s, doc, sc, owned)
		}
		doc = ""
	}
}

// statement extracts what one statement declares. outer is what its units span, such as the
// export or declare statement around the declaration s.
func (e *jsExtractor) statement(outer, s Node, doc string, sc jsScope, owned map[Node]bool) {
	switch s.Type() {
	case "export_statement":
		if d := s.ChildByField("declaration"); !d.IsNull() {
			e.statement(outer, d, doc, sc, owned)
		} else if v := s.ChildByField("value"); !v.IsNull() {
			e.defaultExport(outer, v, doc, sc)
			owned[outer] = true
		}
	case "ambient_declaration":
		for _, c := range s.NamedChildren() {
			if c.Type() == "statement_block" { // declare global { ... }
				e.statements(c, jsScope{prefix: e.qualify(sc.prefix, "global"), parent: sc.parent}, owned)
			} else {
				e.statement(outer, c, doc, sc, owned)
			}
		}
	case "expression_statement":
		// A namespace is parsed as an expression statement.
		if kids := s.NamedChildren(); len(kids) == 1 && (kids[0].Type() == "internal_module" || kids[0].Type() == "module") {
			e.namespace(kids[0], sc, owned)
		}
	case "internal_module", "module":
		e.namespace(s, sc, owned)
	case "function_declaration", "generator_function_declaration", "function_signature":
		e.function(outer, nil, s, s.ChildByField("name").Text(), domain.KindFunction, doc, sc)
		owned[outer] = true
	case "class_declaration", "abstract_class_declaration":
		e.class(outer, outer, s, s.ChildByField("name").Text(), "", doc, "", sc)
		owned[outer] = true
	case "interface_declaration":
		e.iface(outer, s, doc, sc)
		owned[outer] = true
	case "type_alias_declaration":
		e.add(domain.Unit{
			Kind:      domain.KindType,
			Name:      e.qualify(sc.prefix, s.ChildByField("name").Text()),
			Parent:    sc.parent,
			Signature: outer.Text(),
			Hash:      jsHash(nil).hash(outer),
			Doc:       doc,
		}, outer, outer)
		owned[outer] = true
	case "enum_declaration":
		e.enum(outer, s, doc, sc)
		owned[outer] = true
	case "lexical_declaration", "variable_declaration":
		e.variables(outer, s, doc, sc, owned)
	}
}

// namespace qualifies the names of what a namespace declares. Its own text stays module code.
func (e *jsExtractor) namespace(n Node, sc jsScope, owned map[Node]bool) {
	body := n.ChildByField("body")
	name := n.ChildByField("name")
	if body.IsNull() || name.IsNull() {
		return
	}
	e.statements(body, jsScope{prefix: e.qualify(sc.prefix, jsName(name)), parent: sc.parent}, owned)
}

// defaultExport extracts `export default <value>`.
func (e *jsExtractor) defaultExport(outer, v Node, doc string, sc jsScope) {
	name := "default"
	if n := v.ChildByField("name"); !n.IsNull() {
		name = n.Text()
	}
	switch {
	case jsIsFunction(v):
		e.function(outer, nil, v, name, domain.KindFunction, doc, sc)
	case v.Type() == "class":
		e.class(outer, outer, v, name, "", doc, "", sc)
	default:
		e.add(domain.Unit{
			Kind:      domain.KindVar,
			Name:      e.qualify(sc.prefix, "default"),
			Parent:    sc.parent,
			Signature: outer.Text(),
			Hash:      jsHash(nil).hash(outer),
			Doc:       doc,
		}, outer, outer)
	}
}

// function extracts a function or method. The unit spans decorators (when they are siblings,
// as TypeScript class members' are) through outer; fn is the function node itself.
func (e *jsExtractor) function(outer Node, decorators []Node, fn Node, name string, kind domain.UnitKind, doc string, sc jsScope) {
	from := outer
	if len(decorators) > 0 {
		from = decorators[0]
	}
	body := fn.ChildByField("body")
	u := domain.Unit{
		Kind:      kind,
		Name:      e.qualify(sc.prefix, name),
		Parent:    sc.parent,
		Signature: e.header(from, body, outer),
		Hash:      jsHash(nil).hash(append(decorators, outer)...),
		Doc:       doc,
	}
	if jsProvesRename(body) {
		nameNode := fn.ChildByField("name")
		withoutName := jsHash(map[Node]bool{nameNode: !nameNode.IsNull()})
		u.BodyHash = hashStrings(jsHash(nil).hash(decorators...), withoutName.hash(fn))
	}
	e.add(u, from, outer)
}

// class extracts a class with its members. cls is the class node and span is what the unit
// spans. hashed is the code that counts, which for `const C = class {...}` is the declarator,
// salted with its statement's own code.
func (e *jsExtractor) class(span, hashed, cls Node, name, sigPrefix, doc, salt string, sc jsScope) {
	slot := e.reserve()
	qualified := e.qualify(sc.prefix, name)
	members := map[Node]bool{}
	body := cls.ChildByField("body")
	e.classBody(body, jsScope{prefix: qualified, parent: qualified}, members)
	e.units[slot] = e.spanned(domain.Unit{
		Kind:      domain.KindClass,
		Name:      qualified,
		Parent:    sc.parent,
		Signature: sigPrefix + e.header(span, body, span),
		// The class's own code: its header, decorators, and anything that is not a member,
		// such as a static block. A member's change belongs to that member alone.
		Hash: hashStrings(salt, jsHash(members).hash(hashed)),
		Doc:  doc,
	}, span, span)
}

func (e *jsExtractor) classBody(body Node, sc jsScope, members map[Node]bool) {
	var decorators []Node
	doc := ""
	for _, c := range body.Children() {
		if e.stopped() {
			break
		}
		if c.IsExtra() {
			if len(decorators) == 0 {
				doc = jsDocComment(c)
			}
			continue
		}
		if !c.IsNamed() {
			continue
		}
		if c.Type() == "decorator" {
			// TypeScript parses a method's decorators as its siblings.
			decorators = append(decorators, c)
			continue
		}
		made := true
		switch c.Type() {
		case "method_definition", "method_signature", "abstract_method_signature":
			e.function(c, decorators, c, jsName(c.ChildByField("name")), domain.KindMethod, doc, sc)
		case "field_definition", "public_field_definition":
			name := c.ChildByField("property") // JavaScript
			if name.IsNull() {
				name = c.ChildByField("name") // TypeScript
			}
			e.field(c, name, decorators, doc, sc)
		default:
			made = false // static blocks and index signatures stay class code
		}
		if made {
			members[c] = true
			for _, d := range decorators {
				members[d] = true
			}
		}
		decorators, doc = nil, ""
	}
}

// field extracts a class field. One holding a function is a method.
func (e *jsExtractor) field(f, nameNode Node, decorators []Node, doc string, sc jsScope) {
	from := f
	if len(decorators) > 0 {
		from = decorators[0]
	}
	u := domain.Unit{
		Kind:      domain.KindField,
		Name:      e.qualify(sc.prefix, jsName(nameNode)),
		Parent:    sc.parent,
		Signature: from.TextUntil(f.EndByte()),
		Hash:      jsHash(nil).hash(append(decorators, f)...),
		Doc:       doc,
	}
	if v := f.ChildByField("value"); jsIsFunction(v) {
		body := v.ChildByField("body")
		u.Kind = domain.KindMethod
		u.Signature = e.header(from, body, f)
		if jsProvesRename(body) {
			u.BodyHash = hashStrings(jsHash(nil).hash(decorators...), jsHash(map[Node]bool{nameNode: true}).hash(f))
		}
	}
	e.add(u, from, f)
}

// jsProvesRename reports whether a function body is distinctive enough for an identical copy
// under a new name to count as a rename. A body of one statement, such as "return null", or an
// expression body is too common.
func jsProvesRename(body Node) bool {
	return body.Type() == "statement_block" && len(body.NamedChildren()) >= 2
}

// iface extracts an interface with its property and method signatures.
func (e *jsExtractor) iface(outer, s Node, doc string, sc jsScope) {
	slot := e.reserve()
	qualified := e.qualify(sc.prefix, s.ChildByField("name").Text())
	inner := jsScope{prefix: qualified, parent: qualified}
	members := map[Node]bool{}
	body := s.ChildByField("body")
	memberDoc := ""
	for _, c := range body.Children() {
		if e.stopped() {
			break
		}
		if c.IsExtra() {
			memberDoc = jsDocComment(c)
			continue
		}
		var kind domain.UnitKind
		switch c.Type() {
		case "property_signature":
			kind = domain.KindField
		case "method_signature":
			kind = domain.KindInterfaceMethod
		}
		if kind != "" {
			e.add(domain.Unit{
				Kind:      kind,
				Name:      e.qualify(inner.prefix, jsName(c.ChildByField("name"))),
				Parent:    qualified,
				Signature: c.Text(),
				Hash:      jsHash(nil).hash(c),
				Doc:       memberDoc,
			}, c, c)
			members[c] = true
		}
		if c.IsNamed() {
			memberDoc = ""
		}
	}
	e.units[slot] = e.spanned(domain.Unit{
		Kind:      domain.KindInterface,
		Name:      qualified,
		Parent:    sc.parent,
		Signature: e.header(outer, body, outer),
		Hash:      jsHash(members).hash(outer),
		Doc:       doc,
	}, outer, outer)
}

// enum extracts an enum and its members. A member without an initializer takes its value from
// the member before it, so its hash chains that member's hash: reordering, inserting, or
// re-initializing members changes every member whose value moves, and appending changes none.
func (e *jsExtractor) enum(outer, s Node, doc string, sc jsScope) {
	slot := e.reserve()
	qualified := e.qualify(sc.prefix, s.ChildByField("name").Text())
	members := map[Node]bool{}
	previous := ""
	body := s.ChildByField("body")
	for _, c := range body.NamedChildren() {
		if e.stopped() {
			break
		}
		nameNode := c
		if c.Type() == "enum_assignment" {
			nameNode = c.ChildByField("name")
		}
		name := jsName(nameNode)
		hash := jsHash(nil).hash(c)
		if c.Type() != "enum_assignment" {
			hash = hashStrings(hash, "after", previous)
		}
		previous = hash
		e.add(domain.Unit{
			Kind:      domain.KindEnumMember,
			Name:      e.qualify(qualified, name),
			Parent:    qualified,
			Signature: c.Text(),
			Hash:      hash,
		}, c, c)
		members[c] = true
	}
	e.units[slot] = e.spanned(domain.Unit{
		Kind:      domain.KindEnum,
		Name:      qualified,
		Parent:    sc.parent,
		Signature: e.header(outer, body, outer),
		Hash:      jsHash(members).hash(outer),
		Doc:       doc,
	}, outer, outer)
}

// variables extracts the names a const, let, or var statement binds. With one declarator the
// unit spans the whole statement; with several, each spans its own declarator. Every unit's
// hash includes the statement's own code, such as `export const`, so it changes with it.
func (e *jsExtractor) variables(outer, s Node, doc string, sc jsScope, owned map[Node]bool) {
	var decls []Node
	for _, c := range s.NamedChildren() {
		if c.Type() == "variable_declarator" {
			decls = append(decls, c)
		}
	}
	if len(decls) == 0 {
		return
	}
	skip := map[Node]bool{}
	for _, d := range decls {
		skip[d] = true
	}
	statement := jsHash(skip).hash(outer)
	// The statement's own text before its first declarator, such as "export const ", begins
	// every declarator's signature. It is bounded once here: a copy of the whole of it per
	// declarator would grow with the product of the two.
	prefix := compact(e.header(outer, decls[0], outer))
	if len(prefix) > jsMaxPrefix {
		prefix = strings.ToValidUTF8(prefix[:jsMaxPrefix], "") + "…"
	}
	prefix += " "
	for _, d := range decls {
		span, sigPrefix := d, prefix
		if len(decls) == 1 {
			span, sigPrefix = outer, ""
		}
		nameNode, value := d.ChildByField("name"), d.ChildByField("value")
		if nameNode.Type() == "identifier" && jsIsFunction(value) {
			u := domain.Unit{
				Kind:      domain.KindFunction,
				Name:      e.qualify(sc.prefix, nameNode.Text()),
				Parent:    sc.parent,
				Signature: sigPrefix + e.header(span, value.ChildByField("body"), span),
				Hash:      hashStrings(statement, jsHash(nil).hash(d)),
				Doc:       doc,
			}
			if jsProvesRename(value.ChildByField("body")) {
				u.BodyHash = jsHash(map[Node]bool{nameNode: true}).hash(d)
			}
			e.add(u, span, span)
		} else if nameNode.Type() == "identifier" && value.Type() == "class" {
			e.class(span, d, value, nameNode.Text(), sigPrefix, doc, statement, sc)
		} else if names := jsPatternNames(nameNode); len(names) > 0 {
			e.bindings(span, d, names, sigPrefix, doc, statement, sc)
		} else {
			continue // binds nothing, such as "const {} = a": it stays module code
		}
		owned[d] = true
		if len(decls) == 1 {
			owned[outer] = true
		}
	}
}

// bindings extracts every name one declarator binds. They share its range and code; the text
// is read and compacted once, since a copy per name would grow with the square of its length.
func (e *jsExtractor) bindings(span, decl Node, names []string, sigPrefix, doc, statement string, sc jsScope) {
	code := hashStrings(statement, jsHash(nil).hash(decl))
	source := span.Text()
	signature := compact(sigPrefix + source)
	for _, n := range names {
		e.units = append(e.units, e.finish(domain.Unit{
			Kind:      domain.KindVar,
			Name:      e.qualify(sc.prefix, n),
			Parent:    sc.parent,
			Signature: signature,
			Hash:      hashStrings(n, code),
			Doc:       doc,
			StartLine: span.StartLine(),
			EndLine:   span.EndLine(),
			Source:    source,
		}))
	}
}

// jsPatternNames returns the names a binding pattern declares, such as a, c, and d in
// "{ a, b: c, ...d }". Property keys and default values declare nothing.
func jsPatternNames(n Node) []string {
	var names []string
	seen := map[string]bool{}
	var walk func(Node)
	walk = func(n Node) {
		switch n.Type() {
		case "identifier", "shorthand_property_identifier_pattern":
			if !seen[n.Text()] {
				seen[n.Text()] = true
				names = append(names, n.Text())
			}
		case "pair_pattern":
			walk(n.ChildByField("value"))
		case "assignment_pattern", "object_assignment_pattern":
			walk(n.ChildByField("left"))
		case "object_pattern", "array_pattern", "rest_pattern":
			for _, c := range n.NamedChildren() {
				walk(c)
			}
		}
	}
	walk(n)
	return names
}

// jsMaxPrefix bounds the statement text, such as "export declare const ", that begins each of
// several declarators' signatures. Real prefixes are far shorter.
const jsMaxPrefix = 64

func jsIsFunction(n Node) bool {
	switch n.Type() {
	case "arrow_function", "function_expression", "function", "generator_function":
		return true
	}
	return false
}

// jsName returns a declared name: an identifier's text, or a quoted name's value, so quote
// style is not identity.
func jsName(n Node) string {
	if n.Type() == "string" {
		return jsStringValue(n)
	}
	return n.Text()
}

// jsStringValue returns a string literal's text without its quotes.
func jsStringValue(n Node) string {
	t := n.Text()
	if len(t) >= 2 {
		return t[1 : len(t)-1]
	}
	return t
}

// jsDocComment returns the text of a JSDoc comment ("/** ... */") without its delimiters and
// leading asterisks, or empty for any other comment.
func jsDocComment(c Node) string {
	t := c.Text()
	if c.Type() != "comment" || !strings.HasPrefix(t, "/**") || !strings.HasSuffix(t, "*/") || len(t) < 5 {
		return ""
	}
	lines := strings.Split(t[3:len(t)-2], "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(l, "*")
		lines[i] = strings.TrimSpace(l)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// jsHash fingerprints JavaScript and TypeScript syntax. Units in owned are left out, so a class
// or module hashes only its own code. Differences that formatters such as Prettier make without
// changing meaning do not count: quote style, semicolons, trailing commas, redundant
// parentheses, the case of number literals, and how JSX text is wrapped.
func jsHash(owned map[Node]bool) fingerprint {
	return fingerprint{
		skip: func(n Node) bool { return owned[n] },
		// The tree already encodes precedence, so parentheses that only group are formatting.
		transparent: func(n Node) bool { return n.Type() == "parenthesized_expression" },
		// Inside a string, template, or regular expression every character counts.
		opaque: func(n Node) bool {
			switch n.Type() {
			case "string_fragment", "escape_sequence", "regex_pattern", "regex_flags":
				return true
			}
			return false
		},
		token: func(n Node) (string, bool) {
			switch n.Type() {
			case ",":
				// In an array or array pattern a comma can be a hole, which moves every
				// element after it, so there it counts.
				if p := n.Parent().Type(); p == "array" || p == "array_pattern" {
					return ",", true
				}
				return "", false
			case ";":
				return "", false
			case "'", "\"":
				// The string node remains; which quote delimits it is style.
				return "", false
			case "number":
				return strings.ToLower(n.Text()), true
			case "jsx_text":
				return jsxText(n.Text()), true
			}
			return n.Text(), true
		},
	}
}

// jsxText returns JSX text as React renders it: whitespace that includes a line break is
// dropped where it starts or ends a line, and the remaining lines are joined by a space.
func jsxText(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var kept []string
	for i, l := range lines {
		if i > 0 {
			l = strings.TrimLeft(l, " \t")
		}
		if i < len(lines)-1 {
			l = strings.TrimRight(l, " \t")
		}
		if l != "" {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, " ")
}

// isJSTest reports whether the file holds tests by the conventions Jest, Vitest, and Mocha
// discover: *.test.*, *.spec.*, or anything under a __tests__ directory.
func isJSTest(p string) bool {
	base := path.Base(p)
	if strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") {
		return true
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "__tests__" {
			return true
		}
	}
	return false
}

// isJSGenerated reports whether the comments before the first statement declare the file
// generated, as "// @generated" or "Code generated ... DO NOT EDIT." do.
func isJSGenerated(root Node) bool { return markedGenerated(leadingComments(root)) }
