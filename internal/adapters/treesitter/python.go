package treesitter

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

//go:embed wasm/python.wasm
var pythonWasm []byte

var pythonGrammar = Grammar{name: "python", wasm: pythonWasm}

// Python implements ports.SourceAnalyzer for Python.
//
// Structure: a directory is a package (PackageRef{Dir: dir, Name: the directory's own name,
// empty at the repository root}). Each file is a MODULE unit spanning the whole file, named by
// its base name ("util" for util.py). Classes, functions, and module variables sit under the
// module, with names qualified by it: "util.Repo", "util.Repo.get", "util.Repo.Inner". A
// class's methods, class attributes, and nested classes sit under it. Functions declared inside
// functions are part of their enclosing function. Definitions inside module- or class-level
// if/try/with blocks count, since they are what the module or class defines.
type Python struct{ rt *Runtime }

// NewPython returns a Python analyzer running on rt.
func NewPython(rt *Runtime) *Python { return &Python{rt: rt} }

// Supports reports whether the path is a Python source file.
func (*Python) Supports(p string) bool { return strings.HasSuffix(p, ".py") }

// Analyze parses one Python file into units.
func (a *Python) Analyze(ctx context.Context, p string, src []byte) (domain.FileAnalysis, error) {
	tree, err := a.rt.Parse(ctx, pythonGrammar, src)
	if err != nil {
		return domain.FileAnalysis{}, fmt.Errorf("parse %s: %w", p, err)
	}
	root := tree.Root()
	if root.HasError() {
		return domain.FileAnalysis{}, fmt.Errorf("parse %s: %w near line %d", p, ErrSyntax, root.FirstError().StartLine())
	}
	dir := path.Dir(p)
	pkgName := path.Base(dir)
	if dir == "." {
		pkgName = ""
	}
	module := strings.TrimSuffix(path.Base(p), ".py")
	e := &pyExtractor{
		src:  src,
		path: p,
		pkg:  domain.PackageRef{Dir: dir, Name: pkgName},
		test: isPythonTest(p),
	}
	owned := map[Node]bool{}
	slot := e.reserve()
	e.statements(root, module, false, owned)
	e.units[slot] = e.finish(domain.Unit{
		Kind:      domain.KindModule,
		Name:      module,
		Signature: "module " + strings.TrimSuffix(strings.ReplaceAll(p, "/", "."), ".py"),
		Hash:      pyHash(owned).hash(root),
		Doc:       pyDocstring(root),
		StartLine: 1,
		EndLine:   lineCount(src),
		Source:    string(src),
	})
	return domain.FileAnalysis{
		Package:   e.pkg,
		Units:     e.units,
		Generated: isPythonGenerated(root),
		Test:      e.test,
	}, nil
}

type pyExtractor struct {
	src   []byte
	path  string
	pkg   domain.PackageRef
	test  bool
	units []domain.Unit
}

// reserve holds a place for a unit whose hash depends on its members, which come first.
func (e *pyExtractor) reserve() int {
	e.units = append(e.units, domain.Unit{})
	return len(e.units) - 1
}

func (e *pyExtractor) finish(u domain.Unit) domain.Unit {
	u.Package = e.pkg
	u.File = e.path
	u.Test = e.test
	u.Signature = compact(u.Signature)
	return u
}

func (e *pyExtractor) add(u domain.Unit, n Node) {
	u.StartLine, u.EndLine = n.StartLine(), n.EndLine()
	u.Source = n.Text()
	e.units = append(e.units, e.finish(u))
}

// statements extracts the units a block declares. scope is the enclosing unit's name; inClass
// says whether functions are methods and assignments are class attributes. Statements that
// became units are recorded in owned, so the enclosing unit's hash leaves them out.
func (e *pyExtractor) statements(block Node, scope string, inClass bool, owned map[Node]bool) {
	for _, s := range block.NamedChildren() {
		switch s.Type() {
		case "decorated_definition":
			e.definition(s, s.ChildByField("definition"), scope, inClass)
			owned[s] = true
		case "class_definition", "function_definition":
			e.definition(s, s, scope, inClass)
			owned[s] = true
		case "expression_statement":
			if names := pyAssignedNames(s); len(names) > 0 {
				e.assignment(s, names, scope, inClass)
				owned[s] = true
			}
		case "type_alias_statement":
			if name := pyTypeAliasName(s); name != "" {
				e.add(domain.Unit{
					Kind:      domain.KindType,
					Name:      domain.QualifiedName(scope, name),
					Parent:    scope,
					Signature: s.Text(),
					Hash:      pyHash(nil).hash(s),
				}, s)
				owned[s] = true
			}
		case "if_statement", "try_statement", "with_statement":
			e.nested(s, scope, inClass, owned)
		}
	}
}

// nested extracts definitions from the blocks of a compound statement.
func (e *pyExtractor) nested(s Node, scope string, inClass bool, owned map[Node]bool) {
	for _, c := range s.NamedChildren() {
		switch c.Type() {
		case "block":
			e.statements(c, scope, inClass, owned)
		case "elif_clause", "else_clause", "except_clause", "except_group_clause", "finally_clause":
			e.nested(c, scope, inClass, owned)
		}
	}
}

// definition extracts a class or function. outer is the definition with its decorators.
func (e *pyExtractor) definition(outer, def Node, scope string, inClass bool) {
	nameNode := def.ChildByField("name")
	if nameNode.IsNull() {
		return
	}
	name := domain.QualifiedName(scope, nameNode.Text())
	body := def.ChildByField("body")
	decorators := pyDecoratorText(outer)
	switch def.Type() {
	case "class_definition":
		slot := e.reserve()
		members := map[Node]bool{}
		e.statements(body, name, true, members)
		u := domain.Unit{
			Kind:      domain.KindClass,
			Name:      name,
			Parent:    scope,
			Signature: decorators + "class " + nameNode.Text() + def.ChildByField("type_parameters").Text() + def.ChildByField("superclasses").Text(),
			// The class's own code: its header, docstring, and any statements that are not
			// members. A member's change belongs to that member alone.
			Hash: pyHash(members).hash(outer),
			Doc:  pyDocstring(body),
		}
		u.StartLine, u.EndLine = outer.StartLine(), outer.EndLine()
		u.Source = outer.Text()
		e.units[slot] = e.finish(u)
	case "function_definition":
		kind := domain.KindFunction
		if inClass {
			kind = domain.KindMethod
		}
		async := ""
		if first := def.Children(); len(first) > 0 && first[0].Type() == "async" {
			async = "async "
		}
		params, returns := def.ChildByField("parameters"), def.ChildByField("return_type")
		sig := decorators + async + "def " + nameNode.Text() + def.ChildByField("type_parameters").Text() + params.Text()
		if !returns.IsNull() {
			sig += " -> " + returns.Text()
		}
		u := domain.Unit{
			Kind:      kind,
			Name:      name,
			Parent:    scope,
			Signature: sig,
			Hash:      pyHash(nil).hash(outer),
			Doc:       pyDocstring(body),
		}
		// A body of one statement, such as "return None", is too common to prove a rename.
		if len(body.NamedChildren()) >= 2 {
			u.BodyHash = hashStrings(async, pyDecoratorsHash(outer), pyHash(nil).hash(def.ChildByField("type_parameters"), params, returns, body))
		}
		e.add(u, outer)
	}
}

// assignment extracts module variables or class attributes. Every name assigned by one
// statement shares its range and code.
func (e *pyExtractor) assignment(s Node, names []string, scope string, inClass bool) {
	kind := domain.KindVar
	if inClass {
		kind = domain.KindField
	}
	code := pyHash(nil).hash(s)
	// The statement's text is read and compacted once and shared by every name. A copy per
	// name would grow with the square of the statement's length.
	source := s.Text()
	signature := compact(source)
	for _, n := range names {
		e.units = append(e.units, e.finish(domain.Unit{
			Kind:      kind,
			Name:      domain.QualifiedName(scope, n),
			Parent:    scope,
			Signature: signature,
			Hash:      hashStrings(n, code),
			StartLine: s.StartLine(),
			EndLine:   s.EndLine(),
			Source:    source,
		}))
	}
}

// pyAssignedNames returns the plain names an assignment statement binds, such as a and b in
// "a = b = 1" or "a, b = f()". Assignments to attributes or subscripts declare nothing.
func pyAssignedNames(stmt Node) []string {
	kids := stmt.NamedChildren()
	if len(kids) != 1 || kids[0].Type() != "assignment" {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for a := kids[0]; a.Type() == "assignment"; a = a.ChildByField("right") {
		for _, n := range pyTargetNames(a.ChildByField("left")) {
			if !seen[n] {
				seen[n] = true
				names = append(names, n)
			}
		}
	}
	return names
}

func pyTargetNames(n Node) []string {
	switch n.Type() {
	case "identifier":
		return []string{n.Text()}
	case "pattern_list", "tuple_pattern", "list_pattern", "list_splat_pattern":
		var out []string
		for _, c := range n.NamedChildren() {
			out = append(out, pyTargetNames(c)...)
		}
		return out
	}
	return nil
}

func pyTypeAliasName(s Node) string {
	left := s.ChildByField("left")
	if left.Type() == "type" {
		if kids := left.NamedChildren(); len(kids) > 0 {
			left = kids[0]
		}
	}
	if left.Type() == "generic_type" {
		if kids := left.NamedChildren(); len(kids) > 0 {
			left = kids[0]
		}
	}
	if left.Type() != "identifier" {
		return ""
	}
	return left.Text()
}

// pyDecoratorText returns a definition's decorators as signature text, such as "@property ".
func pyDecoratorText(outer Node) string {
	var b strings.Builder
	for _, c := range outer.NamedChildren() {
		if c.Type() == "decorator" {
			b.WriteString(c.Text() + " ")
		}
	}
	return b.String()
}

func pyDecoratorsHash(outer Node) string {
	var ds []Node
	for _, c := range outer.NamedChildren() {
		if c.Type() == "decorator" {
			ds = append(ds, c)
		}
	}
	return pyHash(nil).hash(ds...)
}

// pyHash fingerprints Python syntax. Units in owned are left out, so a class or module hashes
// only its own code. Differences that Python formatters make without changing meaning do not
// count: quote style, redundant parentheses, trailing commas, and the case of number prefixes.
func pyHash(owned map[Node]bool) fingerprint {
	return fingerprint{
		skip: func(n Node) bool { return owned[n] },
		// The tree already encodes precedence, so parentheses that only group are formatting.
		transparent: func(n Node) bool { return n.Type() == "parenthesized_expression" },
		// Inside a string every character counts, including spaces and escapes.
		opaque: func(n Node) bool { return n.Type() == "string_content" || n.Type() == "format_specifier" },
		token: func(n Node) (string, bool) {
			switch n.Type() {
			case ",":
				return "", false
			case "string_start", "string_end":
				return pyNormalizeQuote(n.Text()), true
			case "integer", "float":
				return strings.ToLower(n.Text()), true
			}
			return n.Text(), true
		},
	}
}

// pyNormalizeQuote keeps what a string delimiter means, its prefix and whether it is triple
// quoted, and drops which quote character was used.
func pyNormalizeQuote(s string) string {
	prefix := strings.ToLower(strings.TrimRight(s, `'"`))
	quotes := len(s) - len(strings.TrimRight(s, `'"`))
	if quotes == 0 {
		// string_end has no prefix.
		quotes = len(s)
	}
	return fmt.Sprintf("%s/%d", prefix, quotes)
}

// pyDocstring returns the cleaned docstring that starts a module, class, or function body.
func pyDocstring(body Node) string {
	kids := body.NamedChildren()
	if len(kids) == 0 || kids[0].Type() != "expression_statement" {
		return ""
	}
	inner := kids[0].NamedChildren()
	if len(inner) != 1 || inner[0].Type() != "string" {
		return ""
	}
	text := inner[0].Text()
	text = strings.TrimLeft(text, "rRuUbBfF")
	for _, q := range []string{`"""`, `'''`, `"`, `'`} {
		if strings.HasPrefix(text, q) && strings.HasSuffix(text, q) && len(text) >= 2*len(q) {
			text = text[len(q) : len(text)-len(q)]
			break
		}
	}
	return pyCleanDoc(text)
}

// pyCleanDoc removes a docstring's indentation the way inspect.cleandoc does.
func pyCleanDoc(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\t", "        "), "\n")
	indent := -1
	for _, l := range lines[1:] {
		if t := strings.TrimLeft(l, " "); t != "" {
			if d := len(l) - len(t); indent < 0 || d < indent {
				indent = d
			}
		}
	}
	lines[0] = strings.TrimSpace(lines[0])
	for i := 1; i < len(lines); i++ {
		if indent > 0 && len(lines[i]) >= indent {
			lines[i] = lines[i][indent:]
		} else {
			lines[i] = strings.TrimLeft(lines[i], " ")
		}
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// isPythonTest reports whether the file holds tests by the conventions pytest and unittest
// discover: test_*.py, *_test.py, conftest.py, or anything under a tests or test directory.
func isPythonTest(p string) bool {
	base := path.Base(p)
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") || base == "conftest.py" {
		return true
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "tests" || dir == "test" {
			return true
		}
	}
	return false
}

// isPythonGenerated reports whether the comments before the first statement declare the file
// generated, as protoc and similar tools do ("Generated by ... DO NOT EDIT!").
func isPythonGenerated(root Node) bool {
	var header strings.Builder
	for _, c := range root.Children() {
		if !c.IsExtra() {
			break
		}
		header.WriteString(c.Text() + "\n")
	}
	lower := strings.ToLower(header.String())
	return strings.Contains(lower, "generated") && strings.Contains(lower, "do not edit")
}
