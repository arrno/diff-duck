package treesitter

import (
	"context"
	_ "embed"
	"path"
	"strconv"
	"strings"

	"github.com/arrno/diffduck/internal/domain"
)

//go:embed wasm/rust.wasm
var rustWasm []byte

var rustGrammar = Grammar{name: "rust", wasm: rustWasm}

// Rust implements ports.SourceAnalyzer for Rust.
//
// Packages. A directory is a package (PackageRef{Dir: dir, Name: the directory's own name,
// empty at the repository root}). The crate-relative module path (crate::a::b) is not used: it
// depends on the crate graph (lib.rs/main.rs, mod declarations, #[path]), which one file does
// not show.
//
// Modules. Each file is the one MODULE unit spanning the whole file, named by its base name
// ("parser" for parser.rs, "mod" for mod.rs, "lib" for lib.rs). Every other unit is named
// QualifiedName(module, ...), as the domain naming rule requires. An inline `mod x { ... }`
// is flattened: its items keep the module path in their names ("lib.x.Foo") but their Parent is
// the enclosing real unit (the file module), because a file has exactly one MODULE unit and an
// inline module is not a type. The `mod x {` header, and anything in it that is not a unit
// (use declarations, macro invocations), is the file module's own code.
//
// Units:
//   - struct and union: STRUCT; named fields are FIELD units ("lib.S.a"), tuple-struct fields
//     are FIELD units named by position ("lib.P.0").
//   - enum: ENUM; each variant is an ENUM_MEMBER ("lib.E.A").
//   - trait: TRAIT; required methods are INTERFACE_METHOD, provided (default) methods METHOD,
//     associated types TYPE, associated consts CONST.
//   - impl blocks are not units. Their items sit under the implemented type: "lib.Foo.new()"
//     for `impl Foo`, and "lib.Foo.<fmt::Display>.fmt()" for `impl fmt::Display for Foo`, so
//     same-named methods of different trait impls stay distinct. The trait segment is the trait
//     with its generics (<From<u8>>), written in one canonical form so reformatting the impl
//     header never renames its items. A method's Parent is the type's unit when the type is
//     declared in the same scope of the same file (and is not the impl's own type parameter);
//     otherwise it is the enclosing scope's unit, since no type unit exists to attach to. The
//     self type's generic arguments are not part of the name, so same-named items of impls for
//     Foo<u8> and Foo<u16> share a name, which the differ pairs by content and position. The
//     impl header (generics, where clause, attributes) is the module's own code.
//   - free functions FUNCTION; const CONST; static VAR; type alias TYPE; macro_rules! m is a
//     FUNCTION named "lib.m!".
//   - use, extern crate, `mod x;`, extern blocks, and item-position macro invocations are the
//     module's own code. Items inside function bodies are part of the function.
//
// Every function and method name ends in "()" and every macro name in "!". Rust keeps fields,
// functions, and macros in separate namespaces, so a getter shares its field's name
// (struct User { name } with fn name()); the suffix keeps "lib.User.name" and
// "lib.User.name()" distinct units. An inherent associated const named like one of its enum's
// variants still shares that variant's name.
//
// Every unit's code, range, and hash include its outer attributes (#[derive], #[cfg], ...);
// its range also includes its outer doc comments, which become Doc. Comments never change a
// hash, except the fenced code blocks of doc comments: cargo test runs those as doctests, so
// they count as the unit's code (the file's //! doctests as the module's). A file's
// #![cfg(...)] attributes are its Constraints.
//
// Known limitation: the pinned grammar (tree-sitter-rust v0.24.2, the newest release) rejects
// a few valid forms, such as a closure parameter list with a trailing comma and Rust 2024
// `unsafe extern` blocks with `safe fn`. Such a file degrades to a file node with a warning.
//
// Tests: files under a tests directory, files with #![cfg(test)], items inside a #[cfg(test)]
// module or impl, and functions with a #[test]-style attribute (#[test], #[tokio::test], ...).
type Rust struct{ rt *Runtime }

// NewRust returns a Rust analyzer running on rt.
func NewRust(rt *Runtime) *Rust { return &Rust{rt: rt} }

// Supports reports whether the path is a Rust source file.
func (*Rust) Supports(p string) bool { return strings.HasSuffix(p, ".rs") }

// Analyze parses one Rust file into units.
func (a *Rust) Analyze(ctx context.Context, p string, src []byte) (domain.FileAnalysis, error) {
	tree, err := parseClean(ctx, a.rt, rustGrammar, p, src)
	if err != nil {
		return domain.FileAnalysis{}, err
	}
	return extractRust(ctx, tree, p)
}

// extractRust extracts the units of a cleanly parsed Rust file.
func extractRust(ctx context.Context, tree *Tree, p string) (domain.FileAnalysis, error) {
	root := tree.Root()
	module := strings.TrimSuffix(path.Base(p), ".rs")
	constraints, constraintNodes := rsConstraints(root)
	e := &rsExtractor{newFileUnits(ctx, tree, p, dirPackage(p), rsIsTestPath(p) || rsHasCfgTest(constraintNodes))}
	owned := map[Node]bool{}
	for _, c := range constraintNodes {
		owned[c] = true
	}
	slot := e.reserve()
	e.items(root, rsScope{name: module, parent: module, test: e.test}, owned)
	if e.stopped() { // the module's hash covers the whole file
		return e.analysis(domain.FileAnalysis{})
	}
	e.units[slot] = e.finish(domain.Unit{
		Kind:      domain.KindModule,
		Name:      module,
		Signature: domain.ModuleSignature(p),
		Hash:      hashStrings(rsHash(owned).hash(root), rsDocCode(rsInnerDocs(root))),
		Doc:       rsDoc(rsInnerDocs(root)),
		StartLine: 1,
		EndLine:   lineCount(tree.Text()),
		Source:    tree.Text(),
	})
	return e.analysis(domain.FileAnalysis{
		Package:     e.pkg,
		Generated:   rsIsGenerated(root),
		Constraints: constraints,
	})
}

type rsExtractor struct{ fileUnits }

// rsScope is where items are declared. name qualifies their names; parent is the unit they sit
// under, which differs from name inside a flattened inline module.
type rsScope struct {
	name   string
	parent string
	test   bool
}

// rsItem is one declaration with the attributes and doc comments written before it.
type rsItem struct {
	node  Node
	attrs []Node // attribute_item nodes
	docs  []Node // outer doc comments
	first Node   // the first attribute or doc comment, or node itself
}

func (it rsItem) test() bool {
	for _, a := range it.attrs {
		if rsIsTestAttr(a) {
			return true
		}
	}
	return false
}

// hash fingerprints an item with its attributes, leaving out owned members. The code blocks
// in its doc comments count too: cargo test compiles and runs them as doctests.
func (it rsItem) hash(owned map[Node]bool, nodes ...Node) string {
	all := append(append([]Node{}, it.attrs...), it.node)
	if len(nodes) > 0 {
		all = append(append([]Node{}, it.attrs...), nodes...)
	}
	return hashStrings(rsHash(owned).hash(all...), rsDocCode(it.docs))
}

// place sets a unit's range and code to the item with its attributes and doc comments.
func (e *rsExtractor) place(u domain.Unit, it rsItem) domain.Unit {
	u.StartLine, u.EndLine = it.first.StartLine(), it.node.EndLine()
	u.Source = it.first.TextUntil(it.node.EndByte())
	if u.Doc == "" {
		u.Doc = rsDoc(it.docs)
	}
	return e.finish(u)
}

// rsLeading groups a container's children into items, each with the attributes and outer doc
// comments directly before it. Plain comments between them do not break the group.
func rsLeading(container Node) []rsItem {
	var out []rsItem
	var attrs, docs []Node
	var first Node
	for _, c := range container.Children() {
		switch {
		case c.IsExtra():
			if rsIsOuterDoc(c) {
				docs = append(docs, c)
				if first.IsNull() {
					first = c
				}
			}
			continue
		case !c.IsNamed():
			continue
		case c.Type() == "attribute_item":
			attrs = append(attrs, c)
			if first.IsNull() {
				first = c
			}
			continue
		}
		it := rsItem{node: c, attrs: attrs, docs: docs, first: first}
		if it.first.IsNull() {
			it.first = c
		}
		out = append(out, it)
		attrs, docs, first = nil, nil, Node{}
	}
	return out
}

// items extracts the units a module-level container declares. Nodes that became units, with
// their attributes, are recorded in owned so the enclosing unit's hash leaves them out.
func (e *rsExtractor) items(container Node, scope rsScope, owned map[Node]bool) {
	list := rsLeading(container)
	local := map[string]bool{}
	for _, it := range list {
		switch it.node.Type() {
		case "struct_item", "union_item", "enum_item":
			local[it.node.ChildByField("name").Text()] = true
		}
	}
	for _, it := range list {
		if e.stopped() {
			return
		}
		n := it.node
		name := n.ChildByField("name")
		test := scope.test || it.test()
		own := func() {
			owned[n] = true
			for _, a := range it.attrs {
				owned[a] = true
			}
		}
		switch n.Type() {
		case "function_item":
			e.function(it, scope.name, scope.parent, domain.KindFunction, test)
			own()
		case "struct_item", "union_item":
			e.structure(it, scope, test)
			own()
		case "enum_item":
			e.enum(it, scope, test)
			own()
		case "trait_item":
			e.trait(it, scope, test)
			own()
		case "impl_item":
			e.impl(it, scope, local, test, owned)
		case "const_item":
			e.simple(it, scope.name, scope.parent, domain.KindConst, test)
			own()
		case "static_item":
			e.simple(it, scope.name, scope.parent, domain.KindVar, test)
			own()
		case "type_item":
			e.simple(it, scope.name, scope.parent, domain.KindType, test)
			own()
		case "macro_definition":
			if name.IsNull() {
				continue
			}
			e.units = append(e.units, e.place(domain.Unit{
				Kind:      domain.KindFunction,
				Name:      e.qualify(scope.name, name.Text()+"!"),
				Parent:    scope.parent,
				Signature: rsAttrText(it) + "macro_rules! " + name.Text(),
				Hash:      it.hash(nil),
				Test:      test,
			}, it))
			own()
		case "mod_item":
			body := n.ChildByField("body")
			if body.IsNull() || name.IsNull() {
				continue
			}
			e.items(body, rsScope{name: e.qualify(scope.name, name.Text()), parent: scope.parent, test: test}, owned)
		}
	}
}

// function extracts a function or method with a body, or a trait's required method.
func (e *rsExtractor) function(it rsItem, scope, parent string, kind domain.UnitKind, test bool) {
	n := it.node
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	body := n.ChildByField("body")
	u := domain.Unit{
		Kind:      kind,
		Name:      e.qualify(scope, nameNode.Text()+"()"),
		Parent:    parent,
		Signature: rsAttrText(it) + rsHeader(n, body),
		Hash:      it.hash(nil),
		Test:      test,
	}
	// A body of one statement, such as "Ok(())", is too common to prove a rename.
	if !body.IsNull() && len(body.NamedChildren()) >= 2 {
		u.BodyHash = it.hash(map[Node]bool{nameNode: true})
	}
	e.units = append(e.units, e.place(u, it))
}

// simple extracts a declaration that is one unit with no members: const, static, type alias,
// associated type.
func (e *rsExtractor) simple(it rsItem, scope, parent string, kind domain.UnitKind, test bool) {
	nameNode := it.node.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	e.units = append(e.units, e.place(domain.Unit{
		Kind:      kind,
		Name:      e.qualify(scope, nameNode.Text()),
		Parent:    parent,
		Signature: rsAttrText(it) + strings.TrimSuffix(it.node.Text(), ";"),
		Hash:      it.hash(nil),
		Test:      test,
	}, it))
}

// container records a type-like unit whose members were extracted first, hashing only its own
// code.
func (e *rsExtractor) container(slot int, it rsItem, u domain.Unit, members map[Node]bool) {
	u.Hash = it.hash(members)
	e.units[slot] = e.place(u, it)
}

func (e *rsExtractor) structure(it rsItem, scope rsScope, test bool) {
	n := it.node
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	name := e.qualify(scope.name, nameNode.Text())
	body := n.ChildByField("body")
	slot := e.reserve()
	members := map[Node]bool{}
	switch body.Type() {
	case "field_declaration_list":
		for _, f := range rsLeading(body) {
			if e.stopped() {
				break
			}
			fname := f.node.ChildByField("name")
			if f.node.Type() != "field_declaration" || fname.IsNull() {
				continue
			}
			e.field(f, name, fname.Text(), []Node{f.node}, test || f.test())
			members[f.node] = true
			for _, a := range f.attrs {
				members[a] = true
			}
		}
	case "ordered_field_declaration_list":
		e.tupleFields(body, name, test, members)
	}
	header := rsHeader(n, body)
	if body.Type() != "field_declaration_list" {
		header = strings.TrimSuffix(n.Text(), ";")
	}
	e.container(slot, it, domain.Unit{
		Kind:      domain.KindStruct,
		Name:      name,
		Parent:    scope.parent,
		Signature: rsAttrText(it) + header,
		Test:      test,
	}, members)
}

func (e *rsExtractor) field(it rsItem, parent, short string, nodes []Node, test bool) {
	last := nodes[len(nodes)-1]
	u := domain.Unit{
		Kind:      domain.KindField,
		Name:      e.qualify(parent, short),
		Parent:    parent,
		Signature: rsAttrText(it) + nodes[0].TextUntil(last.EndByte()),
		Hash:      it.hash(nil, nodes...),
		Test:      test,
	}
	it.node = last
	e.units = append(e.units, e.place(u, it))
}

// tupleFields extracts a tuple struct's fields, named by position. A field is its type with the
// attributes and visibility written before it.
func (e *rsExtractor) tupleFields(body Node, parent string, test bool, members map[Node]bool) {
	index := 0
	var attrs, docs, lead []Node
	var first Node
	for _, c := range body.Children() {
		if e.stopped() {
			break
		}
		switch {
		case c.IsExtra():
			if rsIsOuterDoc(c) {
				docs = append(docs, c)
				if first.IsNull() {
					first = c
				}
			}
			continue
		case !c.IsNamed():
			continue
		case c.Type() == "attribute_item":
			attrs = append(attrs, c)
		case c.Field() == "type":
			it := rsItem{node: c, attrs: attrs, docs: docs, first: first}
			nodes := append(lead, c)
			if it.first.IsNull() {
				it.first = nodes[0]
			}
			e.field(it, parent, strconv.Itoa(index), nodes, test || it.test())
			for _, m := range append(attrs, nodes...) {
				members[m] = true
			}
			index++
			attrs, docs, lead, first = nil, nil, nil, Node{}
			continue
		default:
			lead = append(lead, c) // visibility
		}
		if first.IsNull() {
			first = c
		}
	}
}

func (e *rsExtractor) enum(it rsItem, scope rsScope, test bool) {
	n := it.node
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	name := e.qualify(scope.name, nameNode.Text())
	body := n.ChildByField("body")
	slot := e.reserve()
	members := map[Node]bool{}
	for _, v := range rsLeading(body) {
		if e.stopped() {
			break
		}
		vname := v.node.ChildByField("name")
		if v.node.Type() != "enum_variant" || vname.IsNull() {
			continue
		}
		e.units = append(e.units, e.place(domain.Unit{
			Kind:      domain.KindEnumMember,
			Name:      e.qualify(name, vname.Text()),
			Parent:    name,
			Signature: rsAttrText(v) + v.node.Text(),
			Hash:      v.hash(nil),
			Test:      test || v.test(),
		}, v))
		members[v.node] = true
		for _, a := range v.attrs {
			members[a] = true
		}
	}
	e.container(slot, it, domain.Unit{
		Kind:      domain.KindEnum,
		Name:      name,
		Parent:    scope.parent,
		Signature: rsAttrText(it) + rsHeader(n, body),
		Test:      test,
	}, members)
}

func (e *rsExtractor) trait(it rsItem, scope rsScope, test bool) {
	n := it.node
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	name := e.qualify(scope.name, nameNode.Text())
	body := n.ChildByField("body")
	slot := e.reserve()
	members := map[Node]bool{}
	e.associated(body, name, name, test, members, domain.KindInterfaceMethod)
	e.container(slot, it, domain.Unit{
		Kind:      domain.KindTrait,
		Name:      name,
		Parent:    scope.parent,
		Signature: rsAttrText(it) + rsHeader(n, body),
		Test:      test,
	}, members)
}

// impl extracts an impl block's items under the implemented type. The block itself stays the
// enclosing module's own code.
func (e *rsExtractor) impl(it rsItem, scope rsScope, local map[string]bool, test bool, owned map[Node]bool) {
	n := it.node
	body := n.ChildByField("body")
	typeName, plain := rsSelfType(n.ChildByField("type"))
	if body.IsNull() || typeName == "" {
		return
	}
	typeScope := e.qualify(scope.name, typeName)
	parent := scope.parent
	// impl<T> Show for T is a blanket impl, even beside a struct named T.
	if plain && local[typeName] && !rsDeclaresTypeParam(n.ChildByField("type_parameters"), typeName) {
		parent = typeScope
	}
	itemScope := typeScope
	if trait := n.ChildByField("trait"); !trait.IsNull() {
		itemScope = e.qualify(typeScope, "<"+rsTypeName(trait)+">")
	}
	e.associated(body, itemScope, parent, test, owned, domain.KindMethod)
}

// associated extracts a trait's or impl's items. signatureKind is the kind of a function
// without a body: a trait's required method.
func (e *rsExtractor) associated(body Node, scope, parent string, test bool, owned map[Node]bool, signatureKind domain.UnitKind) {
	for _, it := range rsLeading(body) {
		if e.stopped() {
			break
		}
		t := test || it.test()
		switch it.node.Type() {
		case "function_item":
			e.function(it, scope, parent, domain.KindMethod, t)
		case "function_signature_item":
			e.function(it, scope, parent, signatureKind, t)
		case "const_item":
			e.simple(it, scope, parent, domain.KindConst, t)
		case "type_item", "associated_type":
			e.simple(it, scope, parent, domain.KindType, t)
		default:
			continue
		}
		owned[it.node] = true
		for _, a := range it.attrs {
			owned[a] = true
		}
	}
}

// rsSelfType names an impl's type: the type's own name for a plain or generic path ("Foo" for
// Foo<T>), else the type in canonical form ("&'a str", "[T]"). plain reports a bare identifier,
// which may name a type declared beside the impl.
func rsSelfType(t Node) (name string, plain bool) {
	if t.Type() == "generic_type" {
		t = t.ChildByField("type")
	}
	switch t.Type() {
	case "":
		return "", false
	case "type_identifier":
		return t.Text(), true
	}
	return rsTypeName(t), false
}

// rsTypeName writes a type the same way however it is formatted: its tokens, without comments
// or ignorable commas, with one space only between two words and after each comma. A rewrapped
// impl header therefore keeps its items' names.
func rsTypeName(t Node) string {
	var b strings.Builder
	var walk func(Node)
	walk = func(n Node) {
		if n.IsExtra() {
			return
		}
		kids := n.Children()
		if len(kids) > 0 {
			for _, c := range kids {
				walk(c)
			}
			return
		}
		tok := n.Text()
		if tok == "," {
			if !rsIgnoredComma(n) {
				b.WriteString(", ")
			}
			return
		}
		if s := b.String(); s != "" && rsWordEnd(s[len(s)-1]) && tok != "" && rsWordEnd(tok[0]) {
			b.WriteByte(' ')
		}
		b.WriteString(tok)
	}
	walk(t)
	return strings.TrimSpace(b.String())
}

func rsWordEnd(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// rsDeclaresTypeParam reports whether an impl's generics declare a type parameter named name.
func rsDeclaresTypeParam(params Node, name string) bool {
	for _, p := range params.NamedChildren() {
		if p.Type() == "type_parameter" && p.ChildByField("name").Text() == name {
			return true
		}
	}
	return false
}

// rsHeader returns an item's text before its body, or the whole item without a trailing
// semicolon when it has none.
func rsHeader(n, body Node) string {
	if body.IsNull() {
		return strings.TrimSuffix(strings.TrimSpace(n.Text()), ";")
	}
	return strings.TrimSpace(n.TextUntil(body.StartByte()))
}

// rsAttrText returns an item's attributes as signature text, such as "#[derive(Debug)] ".
func rsAttrText(it rsItem) string {
	var b strings.Builder
	for _, a := range it.attrs {
		b.WriteString(a.Text() + " ")
	}
	return b.String()
}

// rsHash fingerprints Rust syntax. Units in owned are left out, so a container hashes only its
// own code. Trailing commas, which rustfmt adds and removes, do not count; everything inside a
// string or character literal does.
func rsHash(owned map[Node]bool) fingerprint {
	return fingerprint{
		skip: func(n Node) bool { return owned[n] },
		// String contents can sit in hidden tokens around escapes, and their whitespace is
		// meaning, so a literal counts by its exact text.
		opaque: func(n Node) bool {
			switch n.Type() {
			case "string_literal", "raw_string_literal", "char_literal":
				return true
			}
			return false
		},
		token: func(n Node) (string, bool) {
			if n.Type() == "," && rsIgnoredComma(n) {
				return "", false
			}
			return n.Text(), true
		},
	}
}

// rsIgnoredComma reports whether a comma is formatting: one that only ends a list, with nothing
// but a closing delimiter after it, or one between the fields or variants of a struct or enum.
// Commas in macro token trees are macro input, and the comma of a one-element tuple makes it a
// tuple, so both always count.
func rsIgnoredComma(n Node) bool {
	parent := n.Parent()
	switch parent.Type() {
	case "field_declaration_list", "ordered_field_declaration_list", "enum_variant_list":
		// Commas there only separate members, which are units of their own: adding a field
		// must not change its struct.
		return true
	case "token_tree", "token_tree_pattern", "token_repetition", "token_repetition_pattern":
		return false
	}
	// Only the next sibling is looked at, so a list of n items costs O(n), not O(n^2).
	switch n.NextSibling().Type() {
	case "", ")", "]", "}", ">", "|":
	default:
		return false
	}
	switch parent.Type() {
	case "tuple_type", "tuple_pattern", "tuple_expression":
		// Reached once per tuple, at its last comma.
		return len(parent.NamedChildren()) >= 2
	}
	return true
}

// rsIsOuterDoc reports whether a comment is an outer doc comment (/// or /** */).
func rsIsOuterDoc(c Node) bool {
	return (c.Type() == "line_comment" || c.Type() == "block_comment") && !c.ChildByField("outer").IsNull()
}

// rsDoc joins doc comments' text, without their markers or the one space after them.
func rsDoc(comments []Node) string {
	var lines []string
	for _, c := range comments {
		text := c.ChildByField("doc").Text()
		if c.Type() == "block_comment" {
			for _, l := range strings.Split(text, "\n") {
				l = strings.TrimSpace(l)
				l = strings.TrimPrefix(strings.TrimPrefix(l, "*"), " ")
				lines = append(lines, l)
			}
			continue
		}
		lines = append(lines, strings.TrimPrefix(strings.TrimRight(text, "\r\n"), " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// rsInnerDocs returns a file's own documentation comments, its //! and /*! */ comments.
func rsInnerDocs(root Node) []Node {
	var docs []Node
	for _, c := range root.Children() {
		if (c.Type() == "line_comment" || c.Type() == "block_comment") && !c.ChildByField("inner").IsNull() {
			docs = append(docs, c)
		}
	}
	return docs
}

// rsDocCode returns the fenced code blocks in doc comments, which cargo test runs as doctests,
// so a change to them is a code change. Prose stays documentation.
func rsDocCode(comments []Node) string {
	var code strings.Builder
	in := false
	for _, l := range strings.Split(rsDoc(comments), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			in = !in
			code.WriteString("```\n")
			continue
		}
		if in {
			code.WriteString(strings.Join(strings.Fields(l), " ") + "\n")
		}
	}
	return code.String()
}

// rsConstraints returns the file's #![cfg(...)] attributes, which decide whether it compiles.
func rsConstraints(root Node) (string, []Node) {
	var texts []string
	var nodes []Node
	for _, c := range root.NamedChildren() {
		if c.Type() == "inner_attribute_item" && rsAttrPath(c) == "cfg" {
			texts = append(texts, compact(c.Text()))
			nodes = append(nodes, c)
		}
	}
	return strings.Join(texts, "\n"), nodes
}

// rsAttrPath returns an attribute item's path, such as "cfg" or "tokio::test".
func rsAttrPath(item Node) string {
	for _, a := range item.NamedChildren() {
		if a.Type() == "attribute" {
			if kids := a.NamedChildren(); len(kids) > 0 {
				return kids[0].Text()
			}
		}
	}
	return ""
}

// rsIsTestAttr reports whether an attribute marks test code: #[test] and its async or framework
// forms (#[tokio::test]), or #[cfg(test)] alone or in all(...). Under not(...) it is not test
// code, and under any(...) it is compiled outside tests too, so neither counts.
func rsIsTestAttr(item Node) bool {
	p := rsAttrPath(item)
	if p == "test" || strings.HasSuffix(p, "::test") {
		return true
	}
	if p != "cfg" {
		return false
	}
	return rsHasCfgTest([]Node{item})
}

func rsHasCfgTest(items []Node) bool {
	for _, item := range items {
		var args Node
		for _, a := range item.NamedChildren() {
			if a.Type() == "attribute" {
				args = a.ChildByField("arguments")
			}
		}
		idents := map[string]bool{}
		var walk func(Node)
		walk = func(n Node) {
			if n.Type() == "identifier" {
				idents[n.Text()] = true
			}
			for _, c := range n.NamedChildren() {
				walk(c)
			}
		}
		walk(args)
		if idents["test"] && !idents["not"] && !idents["any"] {
			return true
		}
	}
	return false
}

// rsIsTestPath reports whether a file is a Cargo integration test: anything under tests/.
func rsIsTestPath(p string) bool {
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "tests" {
			return true
		}
	}
	return false
}

// rsIsGenerated reports whether the comments before the first item declare the file generated:
// "@generated", the marker Rust tools use, or "generated ... do not edit".
func rsIsGenerated(root Node) bool { return markedGenerated(leadingComments(root)) }
