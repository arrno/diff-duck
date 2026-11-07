package treesitter

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

//go:embed wasm/java.wasm
var javaWasm []byte

var javaGrammar = Grammar{name: "java", wasm: javaWasm}

// Java implements ports.SourceAnalyzer for Java.
//
// Structure: Java is a package language, like Go, so there is no MODULE unit per file. A file's
// package is PackageRef{Dir: its directory, Name: its declared package, such as
// "com.example.app"}; a file with no package declaration takes its directory's name, or "" at
// the repository root. Top-level types are named by their simple name ("Repo"), and members are
// qualified by their enclosing type: "Repo.size", "Repo.Inner", "Repo.Inner.close()".
//
//   - class -> CLASS; interface and @interface (annotation type) -> INTERFACE; enum -> ENUM;
//     record -> RECORD.
//   - Methods and constructors -> METHOD. A constructor is named after its class, "Repo.Repo()";
//     a record's compact constructor is its canonical constructor.
//   - In an interface, a method without a body -> INTERFACE_METHOD; a default, static, or
//     private method has a body, so it is a METHOD. An annotation type's elements ->
//     INTERFACE_METHOD.
//   - Fields -> FIELD, except `static final` fields and every interface or annotation-type
//     field (implicitly static final) -> CONST. One declaration of several variables yields
//     one unit per variable; they share the declaration's lines.
//   - Enum constants -> ENUM_MEMBER; a constant's class body belongs to the constant. Methods,
//     fields, and constructors after the constants are members of the enum.
//   - Record components -> FIELD, spanning the component. Their order is the record's own code,
//     since it is the canonical constructor's parameter order.
//   - Every method, constructor, and annotation element is named with its parameter types, as
//     Java identifies methods: "Repo.find(String)", "Repo.find(int,int)", "Repo.size()". So
//     overloads have unique names, a method never shares a name with a field, constant, or
//     nested type of the same name ("Repo.size" beside "Repo.size()"), and no name depends on
//     its siblings, so adding or removing an overload leaves the others alone. Changing a
//     parameter's type changes the name; when the body is identical, the differ follows it
//     like a rename, since BodyHash leaves the parameter types out.
//   - Static and instance initializer blocks are not units: they are part of their type's own
//     code. Anonymous classes, local classes, and lambdas are part of the enclosing member.
//
// A unit's lines start at its Javadoc, when it has one, and the Javadoc is its Doc. Annotations
// are part of the declaration, so they are in the unit's code and hash. A type's hash covers
// only its own code (header, initializers), not its members': a member's change belongs to that
// member alone.
type Java struct{ rt *Runtime }

// NewJava returns a Java analyzer running on rt.
func NewJava(rt *Runtime) *Java { return &Java{rt: rt} }

// Supports reports whether the path is a Java source file.
func (*Java) Supports(p string) bool { return strings.HasSuffix(p, ".java") }

// Analyze parses one Java file into units.
func (a *Java) Analyze(ctx context.Context, p string, src []byte) (domain.FileAnalysis, error) {
	tree, err := a.rt.Parse(ctx, javaGrammar, src)
	if err != nil {
		return domain.FileAnalysis{}, fmt.Errorf("parse %s: %w", p, err)
	}
	root := tree.Root()
	if root.HasError() {
		return domain.FileAnalysis{}, fmt.Errorf("parse %s: %w near line %d", p, ErrSyntax, root.FirstError().StartLine())
	}
	dir := path.Dir(p)
	pkgName := javaPackageName(root)
	if pkgName == "" && dir != "." {
		pkgName = path.Base(dir)
	}
	e := &javaExtractor{
		text: string(src),
		path: p,
		pkg:  domain.PackageRef{Dir: dir, Name: pkgName},
		test: isJavaTest(p),
	}
	// A file is generated when its header says so, or when every type it declares is annotated
	// @Generated. One annotated type beside hand-written ones does not hide the others.
	types, annotated := 0, 0
	javaDeclarations(root, func(n, doc Node) {
		if javaTypeKinds[n.Type()] != "" {
			e.typeDecl(n, doc, "")
			types++
			if javaHasGeneratedAnnotation(n) {
				annotated++
			}
		}
	})
	return domain.FileAnalysis{
		Package:   e.pkg,
		Units:     e.units,
		Generated: isJavaGeneratedHeader(root) || types > 0 && annotated == types,
		Test:      e.test,
	}, nil
}

// javaTypeKinds maps a type declaration to its unit kind.
var javaTypeKinds = map[string]domain.UnitKind{
	"class_declaration":           domain.KindClass,
	"interface_declaration":       domain.KindInterface,
	"annotation_type_declaration": domain.KindInterface,
	"enum_declaration":            domain.KindEnum,
	"record_declaration":          domain.KindRecord,
}

type javaExtractor struct {
	text  string // the file's source, copied once
	path  string
	pkg   domain.PackageRef
	test  bool
	units []domain.Unit
}

func (e *javaExtractor) finish(u domain.Unit) domain.Unit {
	u.Package = e.pkg
	u.File = e.path
	u.Test = e.test
	u.Signature = compact(u.Signature)
	return u
}

// span sets a unit's lines and code: the declaration, from its Javadoc when it has one.
func (e *javaExtractor) span(u *domain.Unit, n, doc Node) {
	from := n
	if !doc.IsNull() {
		from = doc
	}
	u.StartLine, u.EndLine = from.StartLine(), n.EndLine()
	// A slice of the one copy of the file, so nested types share their text rather than
	// copying it once per level of nesting.
	u.Source = e.text[from.StartByte():n.EndByte()]
	u.Doc = javaDocText(doc)
}

// typeDecl extracts a class, interface, annotation type, enum, or record and its members.
func (e *javaExtractor) typeDecl(n, doc Node, scope string) {
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	name := javaQualify(scope, nameNode.Text())
	kind := javaTypeKinds[n.Type()]
	body := n.ChildByField("body")
	slot := len(e.units)
	e.units = append(e.units, domain.Unit{})
	owned := map[Node]bool{}
	var order []string
	var canonical string // a record's component types, the compact constructor's parameters
	if kind == domain.KindRecord {
		params := n.ChildByField("parameters")
		for _, c := range params.NamedChildren() {
			if cn := javaComponentName(c); cn != "" {
				e.component(c, cn, name)
				owned[c] = true
				order = append(order, cn)
			}
		}
		canonical = javaParamTypes(params)
	}
	iface := n.Type() == "interface_declaration" || n.Type() == "annotation_type_declaration"
	e.members(body, name, iface, canonical, owned)
	u := domain.Unit{
		Kind:      kind,
		Name:      name,
		Parent:    scope,
		Signature: javaHeader(n, body),
		Hash:      hashStrings(javaHash(owned).hash(n), strings.Join(order, ",")),
	}
	e.span(&u, n, doc)
	e.units[slot] = e.finish(u)
}

// members extracts the members a type body declares. Every member that became a unit is
// recorded in owned, so the type's hash leaves it out.
func (e *javaExtractor) members(body Node, scope string, iface bool, canonical string, owned map[Node]bool) {
	javaDeclarations(body, func(n, doc Node) {
		switch t := n.Type(); {
		case javaTypeKinds[t] != "":
			e.typeDecl(n, doc, scope)
		case javaCallableName(n) != "":
			e.callable(n, doc, scope, iface, canonical)
		case t == "field_declaration" || t == "constant_declaration":
			e.fields(n, doc, scope, iface || t == "constant_declaration")
		case t == "enum_constant":
			e.enumConstant(n, doc, scope)
		default:
			// Initializer blocks and stray semicolons are the type's own code.
			return
		}
		owned[n] = true
	})
}

// callable extracts a method, constructor, or annotation-type element.
func (e *javaExtractor) callable(n, doc Node, scope string, iface bool, canonical string) {
	body := n.ChildByField("body")
	kind := domain.KindMethod
	if n.Type() == "annotation_type_element_declaration" || (iface && body.IsNull()) {
		kind = domain.KindInterfaceMethod
	}
	paramList := n.ChildByField("parameters")
	params := javaParamTypes(paramList)
	if n.Type() == "compact_constructor_declaration" {
		params = canonical
	}
	u := domain.Unit{
		Kind:      kind,
		Name:      javaQualify(scope, javaCallableName(n)+"("+params+")"),
		Parent:    scope,
		Signature: javaHeader(n, body),
		Hash:      javaHash(nil).hash(n),
	}
	// Everything but the name and the parameter types, which make up the unit's name:
	// modifiers, annotations, type parameters, result, parameter names, throws, and body. So
	// renaming a method, or changing a parameter's type, is followed when the body is
	// identical. A body of one statement, such as "return null;", is too common to prove that.
	if len(body.NamedChildren()) >= 2 {
		var rest []Node
		for _, c := range n.NamedChildren() {
			if c.Field() != "name" && c.Field() != "parameters" {
				rest = append(rest, c)
			}
		}
		u.BodyHash = hashStrings(n.Type(), javaParamNames(paramList), javaHash(nil).hash(rest...))
	}
	e.span(&u, n, doc)
	e.units = append(e.units, e.finish(u))
}

// fields extracts one unit per variable a field declaration declares. The declaration's text,
// lines, and Javadoc are shared by every variable; each variable's hash and signature cover the
// shared modifiers and type plus its own declarator only, so the work stays linear in the size
// of the declaration.
func (e *javaExtractor) fields(n, doc Node, scope string, constant bool) {
	var mods Node
	for _, c := range n.NamedChildren() {
		if c.Type() == "modifiers" {
			mods = c
		}
	}
	kind := domain.KindField
	if constant || (javaHasModifier(mods, "static") && javaHasModifier(mods, "final")) {
		kind = domain.KindConst
	}
	typ := n.ChildByField("type")
	shared := javaHash(nil).hash(mods, typ)
	prefix := compact(strings.TrimSpace(mods.Text() + " " + typ.Text()))
	var base domain.Unit
	e.span(&base, n, doc)
	for _, d := range n.ChildrenByField("declarator") {
		nameNode := d.ChildByField("name")
		if nameNode.IsNull() {
			continue
		}
		u := base
		u.Kind = kind
		u.Name = javaQualify(scope, nameNode.Text())
		u.Parent = scope
		u.Signature = prefix + " " + nameNode.Text() + javaCompactText(d.ChildByField("dimensions"))
		u.Hash = hashStrings(shared, javaHash(nil).hash(d))
		e.units = append(e.units, e.finish(u))
	}
}

// component extracts one record component as a field.
func (e *javaExtractor) component(c Node, name, scope string) {
	u := domain.Unit{
		Kind:      domain.KindField,
		Name:      javaQualify(scope, name),
		Parent:    scope,
		Signature: c.Text(),
		Hash:      javaHash(nil).hash(c),
	}
	e.span(&u, c, Node{})
	e.units = append(e.units, e.finish(u))
}

func (e *javaExtractor) enumConstant(n, doc Node, scope string) {
	nameNode := n.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	u := domain.Unit{
		Kind:      domain.KindEnumMember,
		Name:      javaQualify(scope, nameNode.Text()),
		Parent:    scope,
		Signature: javaHeader(n, n.ChildByField("body")),
		Hash:      javaHash(nil).hash(n),
	}
	e.span(&u, n, doc)
	e.units = append(e.units, e.finish(u))
}

func javaQualify(scope, name string) string {
	if scope == "" {
		return name
	}
	return domain.QualifiedName(scope, name)
}

// javaDeclarations calls visit for each declaration in a program or type body, with the Javadoc
// comment just before it, or a null node. An enum's members after its constants are visited
// like any other body's.
func javaDeclarations(body Node, visit func(n, doc Node)) {
	var doc Node
	for _, c := range body.Children() {
		switch {
		case c.IsExtra():
			if c.Type() == "block_comment" && strings.HasPrefix(c.Text(), "/**") {
				doc = c
			}
		case c.Type() == "enum_body_declarations":
			javaDeclarations(c, visit)
			doc = Node{}
		case c.IsNamed():
			visit(c, doc)
			doc = Node{}
		default:
			doc = Node{}
		}
	}
}

// javaParamNames returns a parameter list's names in order, such as "key,rest".
func javaParamNames(params Node) string {
	var names []string
	for _, p := range params.NamedChildren() {
		if n := javaComponentName(p); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ",")
}

// javaComponentName returns the name of a parameter or record component: "x" in "int x" or
// "rest" in "String... rest".
func javaComponentName(c Node) string {
	switch c.Type() {
	case "formal_parameter":
		return c.ChildByField("name").Text()
	case "spread_parameter":
		for _, d := range c.NamedChildren() {
			if d.Type() == "variable_declarator" {
				return d.ChildByField("name").Text()
			}
		}
	}
	return ""
}

// javaCallableName returns the simple name of a method, constructor, or annotation-type
// element, or empty for anything else.
func javaCallableName(n Node) string {
	switch n.Type() {
	case "method_declaration", "constructor_declaration", "compact_constructor_declaration", "annotation_type_element_declaration":
		return n.ChildByField("name").Text()
	}
	return ""
}

// javaParamTypes returns a parameter list's types, such as "String,int[],Object...". The
// receiver parameter ("Repo this") is not a parameter.
func javaParamTypes(params Node) string {
	var types []string
	for _, p := range params.NamedChildren() {
		switch p.Type() {
		case "formal_parameter":
			types = append(types, javaCompactText(p.ChildByField("type"))+javaCompactText(p.ChildByField("dimensions")))
		case "spread_parameter":
			for _, c := range p.NamedChildren() {
				if c.Type() != "modifiers" && c.Type() != "variable_declarator" {
					types = append(types, javaCompactText(c)+"...")
					break
				}
			}
		}
	}
	return strings.Join(types, ",")
}

// javaCompactText returns a node's tokens with no whitespace except a single space between two
// words, so formatting never changes it: "Map<String,? extends T>".
func javaCompactText(n Node) string {
	var b strings.Builder
	word := false // whether the text so far ends with a word
	var walk func(Node)
	walk = func(n Node) {
		if n.IsExtra() {
			return
		}
		kids := n.Children()
		if len(kids) == 0 {
			t := n.Text()
			if t == "" {
				return
			}
			if b.Len() > 0 && (word && javaWordByte(t[0]) || t == "extends" || t == "super") {
				b.WriteByte(' ')
			}
			b.WriteString(t)
			word = javaWordByte(t[len(t)-1])
			return
		}
		for _, c := range kids {
			walk(c)
		}
	}
	if !n.IsNull() {
		walk(n)
	}
	return b.String()
}

func javaWordByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// javaHeader returns a declaration's text up to its body: annotations, modifiers, name, type
// parameters, parameters, and so on. A declaration without a body is all header.
func javaHeader(n, body Node) string {
	text := n.Text()
	if !body.IsNull() {
		text = string(n.t.src[n.StartByte():body.StartByte()])
	}
	return strings.TrimSuffix(strings.TrimSpace(text), ";")
}

func javaHasModifier(mods Node, keyword string) bool {
	for _, c := range mods.Children() {
		if c.Type() == keyword {
			return true
		}
	}
	return false
}

// javaPackageName returns the declared package, such as "com.example.app", or empty.
func javaPackageName(root Node) string {
	for _, c := range root.NamedChildren() {
		if c.Type() != "package_declaration" {
			continue
		}
		for _, n := range c.NamedChildren() {
			if n.Type() == "identifier" || n.Type() == "scoped_identifier" {
				return javaCompactText(n)
			}
		}
	}
	return ""
}

// javaHash fingerprints Java syntax. Units in owned are left out, so a type hashes only its own
// code. Comments, whitespace, trailing commas, and the case of number literals do not count;
// a string, text block, or character literal counts by its exact text.
func javaHash(owned map[Node]bool) fingerprint {
	return fingerprint{
		skip:   func(n Node) bool { return owned[n] },
		opaque: func(n Node) bool { return n.Type() == "string_literal" || n.Type() == "character_literal" },
		token: func(n Node) (string, bool) {
			switch n.Type() {
			case ",":
				return "", false
			case "decimal_integer_literal", "hex_integer_literal", "octal_integer_literal", "binary_integer_literal",
				"decimal_floating_point_literal", "hex_floating_point_literal":
				return strings.ToLower(n.Text()), true
			}
			return n.Text(), true
		},
	}
}

// javaDocText returns a Javadoc comment's text without its delimiters and leading asterisks.
func javaDocText(doc Node) string {
	if doc.IsNull() {
		return ""
	}
	text := strings.TrimSuffix(strings.TrimPrefix(doc.Text(), "/**"), "*/")
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = strings.TrimLeft(l, " \t")
		if strings.HasPrefix(l, "*") {
			l = strings.TrimPrefix(l[1:], " ")
		}
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// isJavaTest reports whether the file holds tests by Maven and Gradle conventions: anything
// under src/test/, or a file named *Test.java, *Tests.java, or *IT.java.
func isJavaTest(p string) bool {
	if strings.HasPrefix(p, "src/test/") || strings.Contains(p, "/src/test/") {
		return true
	}
	base := path.Base(p)
	for _, suffix := range []string{"Test.java", "Tests.java", "IT.java"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// isJavaGeneratedHeader reports whether the comments before the first declaration mark the file
// generated: "Generated ... DO NOT EDIT" (protoc and most generators) or an @generated tag.
func isJavaGeneratedHeader(root Node) bool {
	var header strings.Builder
	for _, c := range root.Children() {
		if !c.IsExtra() {
			break
		}
		header.WriteString(c.Text() + "\n")
	}
	lower := strings.ToLower(header.String())
	return strings.Contains(lower, "@generated") ||
		strings.Contains(lower, "generated") && (strings.Contains(lower, "do not edit") || strings.Contains(lower, "do not modify"))
}

// javaHasGeneratedAnnotation reports whether a type declaration is annotated @Generated
// (javax.annotation, javax.annotation.processing, jakarta.annotation, or lombok), which code
// generators put on the types they emit.
func javaHasGeneratedAnnotation(n Node) bool {
	for _, c := range n.NamedChildren() {
		if c.Type() != "modifiers" {
			continue
		}
		for _, a := range c.NamedChildren() {
			if a.Type() != "annotation" && a.Type() != "marker_annotation" {
				continue
			}
			name := javaCompactText(a.ChildByField("name"))
			if name == "Generated" || strings.HasSuffix(name, ".Generated") {
				return true
			}
		}
	}
	return false
}
