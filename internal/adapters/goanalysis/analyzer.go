// Package goanalysis extracts code units from Go source with the standard library parser. It
// needs no type checking, so it works on any revision without installing dependencies.
package goanalysis

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"strings"

	"duck-diff/internal/domain"
)

const maxSignature = 240

// Analyzer implements ports.SourceAnalyzer for Go.
type Analyzer struct{}

// New returns a Go analyzer.
func New() *Analyzer { return &Analyzer{} }

// Supports reports whether the path is a Go source file.
func (*Analyzer) Supports(p string) bool { return strings.HasSuffix(p, ".go") }

// Analyze parses one Go file into units.
func (*Analyzer) Analyze(p string, src []byte) (domain.FileAnalysis, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return domain.FileAnalysis{}, fmt.Errorf("parse %s: %w", p, err)
	}
	e := extractor{
		fset: fset,
		src:  src,
		path: p,
		pkg:  domain.PackageRef{Dir: path.Dir(p), Name: file.Name.Name},
		test: strings.HasSuffix(p, "_test.go"),
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			e.function(d)
		case *ast.GenDecl:
			e.genDecl(d)
		}
	}
	return domain.FileAnalysis{
		Package:     e.pkg,
		Units:       e.units,
		Generated:   ast.IsGenerated(file),
		Test:        e.test,
		Constraints: buildConstraints(file),
	}, nil
}

// buildConstraints returns the constraint lines above the package clause.
func buildConstraints(file *ast.File) string {
	var lines []string
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, c := range group.List {
			if constraint.IsGoBuild(c.Text) || constraint.IsPlusBuild(c.Text) {
				lines = append(lines, c.Text)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// compilerDirectives returns a doc comment's //go: lines. They look like comments but change
// what the compiler does, so they count as part of the code.
func compilerDirectives(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	var lines []string
	for _, c := range doc.List {
		if strings.HasPrefix(c.Text, "//go:") {
			lines = append(lines, c.Text)
		}
	}
	return strings.Join(lines, "\n")
}

type extractor struct {
	fset  *token.FileSet
	src   []byte
	path  string
	pkg   domain.PackageRef
	test  bool
	units []domain.Unit
}

func (e *extractor) add(u domain.Unit, start, end token.Pos) {
	u.Package = e.pkg
	u.File = e.path
	u.Test = e.test
	// Real file positions: a //line comment must not be able to relocate code.
	u.StartLine = e.fset.PositionFor(start, false).Line
	u.EndLine = e.fset.PositionFor(end, false).Line
	u.Source = e.text(start, end)
	u.Signature = compact(u.Signature)
	e.units = append(e.units, u)
}

func (e *extractor) text(start, end token.Pos) string {
	s, t := e.fset.PositionFor(start, false).Offset, e.fset.PositionFor(end, false).Offset
	if s < 0 || t > len(e.src) || s > t {
		return ""
	}
	return string(e.src[s:t])
}

func (e *extractor) print(node any) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, e.fset, node); err != nil {
		return ""
	}
	return b.String()
}

func (e *extractor) function(d *ast.FuncDecl) {
	start := d.Pos()
	if d.Doc != nil {
		start = d.Doc.Pos()
	}
	u := domain.Unit{Kind: domain.KindFunction, Name: d.Name.Name, Hash: hashStrings(hashNodes(d), compilerDirectives(d.Doc)), Doc: docText(d.Doc)}
	if d.Recv != nil && len(d.Recv.List) > 0 {
		recv := receiverName(d.Recv.List[0].Type)
		u.Kind = domain.KindMethod
		u.Parent = recv
		u.Name = recv + "." + d.Name.Name
	}
	bare := *d
	bare.Doc = nil
	bare.Body = nil
	u.Signature = e.print(&bare)
	// A body of one statement, such as "return nil", is too common to prove a rename.
	if d.Body != nil && len(d.Body.List) >= 2 {
		u.BodyHash = hashNodes(d.Type, d.Body)
	}
	e.add(u, start, d.End())
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.ParenExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func (e *extractor) genDecl(d *ast.GenDecl) {
	switch d.Tok {
	case token.TYPE:
		for _, s := range d.Specs {
			e.typeSpec(d, s.(*ast.TypeSpec))
		}
	case token.CONST, token.VAR:
		e.valueDecl(d)
	}
}

// specStart returns where a spec begins: its doc comment, or for an ungrouped declaration the
// declaration itself, so the keyword and its doc belong to the unit.
func specStart(d *ast.GenDecl, spec ast.Spec, doc *ast.CommentGroup) token.Pos {
	if !d.Lparen.IsValid() {
		if d.Doc != nil {
			return d.Doc.Pos()
		}
		return d.Pos()
	}
	if doc != nil {
		return doc.Pos()
	}
	return spec.Pos()
}

func (e *extractor) typeSpec(d *ast.GenDecl, s *ast.TypeSpec) {
	doc := s.Doc
	if doc == nil && !d.Lparen.IsValid() {
		doc = d.Doc
	}
	u := domain.Unit{Name: s.Name.Name, Doc: docText(doc), Kind: domain.KindType}
	head := "type " + s.Name.Name
	if s.TypeParams != nil {
		head += e.typeParams(s.TypeParams)
	}
	switch t := s.Type.(type) {
	case *ast.StructType:
		u.Kind = domain.KindStruct
		u.Signature = head + " struct"
		u.Hash = hashStrings("struct", hashNodes(s.TypeParams))
		e.add(u, specStart(d, s, s.Doc), s.End())
		e.fields(s.Name.Name, t.Fields)
	case *ast.InterfaceType:
		u.Kind = domain.KindInterface
		u.Signature = head + " interface"
		u.Hash = hashStrings("interface", hashNodes(s.TypeParams))
		e.add(u, specStart(d, s, s.Doc), s.End())
		e.interfaceElements(s.Name.Name, t.Methods)
	default:
		assign := " "
		if s.Assign.IsValid() {
			assign = " = "
		}
		u.Signature = head + assign + e.print(s.Type)
		u.Hash = hashNodes(s)
		e.add(u, specStart(d, s, s.Doc), s.End())
	}
}

func (e *extractor) typeParams(list *ast.FieldList) string {
	parts := make([]string, 0, len(list.List))
	for _, f := range list.List {
		names := make([]string, 0, len(f.Names))
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.Join(names, ", ")+" "+e.print(f.Type))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (e *extractor) fields(owner string, list *ast.FieldList) {
	if list == nil {
		return
	}
	for _, f := range list.List {
		typeText := e.print(f.Type)
		tag := ""
		if f.Tag != nil {
			tag = " " + f.Tag.Value
		}
		start := f.Pos()
		if f.Doc != nil {
			start = f.Doc.Pos()
		}
		names := []string{receiverNameOr(f.Type, typeText)}
		embedded := len(f.Names) == 0
		if !embedded {
			names = names[:0]
			for _, n := range f.Names {
				names = append(names, n.Name)
			}
		}
		for _, name := range names {
			sig := name + " " + typeText + tag
			if embedded {
				sig = typeText + tag
			}
			e.add(domain.Unit{
				Kind:      domain.KindField,
				Name:      owner + "." + name,
				Parent:    owner,
				Signature: sig,
				Hash:      hashStrings(name, hashNodes(f.Type, f.Tag)),
				Doc:       docText(f.Doc),
			}, start, f.End())
		}
	}
}

func receiverNameOr(expr ast.Expr, fallback string) string {
	if n := receiverName(expr); n != "?" {
		return n
	}
	if sel, ok := unstar(expr).(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return fallback
}

func unstar(expr ast.Expr) ast.Expr {
	if s, ok := expr.(*ast.StarExpr); ok {
		return s.X
	}
	return expr
}

func (e *extractor) interfaceElements(owner string, list *ast.FieldList) {
	if list == nil {
		return
	}
	for _, f := range list.List {
		start := f.Pos()
		if f.Doc != nil {
			start = f.Doc.Pos()
		}
		if len(f.Names) == 0 {
			text := e.print(f.Type)
			e.add(domain.Unit{
				Kind:      domain.KindField,
				Name:      owner + "." + compact(text),
				Parent:    owner,
				Signature: text,
				Hash:      hashNodes(f.Type),
				Doc:       docText(f.Doc),
			}, start, f.End())
			continue
		}
		for _, n := range f.Names {
			e.add(domain.Unit{
				Kind:      domain.KindInterfaceMethod,
				Name:      owner + "." + n.Name,
				Parent:    owner,
				Signature: n.Name + strings.TrimPrefix(e.print(f.Type), "func"),
				Hash:      hashStrings(n.Name, hashNodes(f.Type)),
				Doc:       docText(f.Doc),
			}, start, f.End())
		}
	}
}

func (e *extractor) valueDecl(d *ast.GenDecl) {
	kind, keyword := domain.KindVar, "var"
	if d.Tok == token.CONST {
		kind, keyword = domain.KindConst, "const"
	}
	var inherited []ast.Expr
	var inheritedType ast.Expr
	for ordinal, spec := range d.Specs {
		s := spec.(*ast.ValueSpec)
		if d.Tok == token.CONST {
			// A const spec with no values repeats the previous expressions.
			if len(s.Values) > 0 || s.Type != nil {
				inherited, inheritedType = s.Values, s.Type
			}
		}
		values, typ := s.Values, s.Type
		if d.Tok == token.CONST {
			values, typ = inherited, inheritedType
		}

		doc := s.Doc
		if doc == nil && !d.Lparen.IsValid() {
			doc = d.Doc
		}
		for i, name := range s.Names {
			head := keyword + " " + name.Name
			if s.Type != nil {
				head += " " + e.print(s.Type)
			}
			value := ""
			if len(s.Values) == len(s.Names) {
				value = " = " + e.print(s.Values[i])
			} else if len(s.Values) > 0 {
				value = " = " + e.print(s.Values[0])
			}
			nodes := []any{typ}
			if len(values) == len(s.Names) {
				nodes = append(nodes, values[i])
			} else {
				for _, v := range values {
					nodes = append(nodes, v)
				}
			}
			parts := []string{keyword, name.Name, hashNodes(nodes...), compilerDirectives(doc)}
			// iota makes a constant's value depend on its position even when its text does
			// not change, so inserting a constant really does change those after it.
			if d.Tok == token.CONST && usesIota(values) {
				parts = append(parts, fmt.Sprintf("ordinal:%d:%d", ordinal, i))
			}
			e.add(domain.Unit{
				Kind:       kind,
				Name:       name.Name,
				Signature:  head + value,
				Hash:       hashStrings(parts...),
				Doc:        docText(doc),
				ChangeNote: implicitNote(d.Tok, s),
			}, specStart(d, s, s.Doc), s.End())
		}
	}
}

// implicitNote explains a constant whose value can change while its own text does not.
func implicitNote(tok token.Token, s *ast.ValueSpec) string {
	if tok != token.CONST || len(s.Values) > 0 {
		return ""
	}
	return "Implicit value changed: its iota position or inherited expression differs."
}

func usesIota(values []ast.Expr) bool {
	found := false
	for _, v := range values {
		ast.Inspect(v, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
				found = true
			}
			return !found
		})
	}
	return found
}

func docText(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	return strings.TrimSpace(doc.Text())
}

// compact collapses a printed signature to one bounded line.
func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxSignature {
		return s
	}
	return strings.ToValidUTF8(s[:maxSignature], "") + "…"
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// hashNodes fingerprints syntax so that formatting and comments do not affect the result.
// Printing cannot do this, because the printer preserves blank lines. The walk records tree
// shape, plus every field that distinguishes syntax yet is not a child node.
func hashNodes(nodes ...any) string {
	var b strings.Builder
	for _, node := range nodes {
		n, ok := node.(ast.Node)
		if !ok || isNilNode(n) {
			b.WriteString("nil;")
			continue
		}
		ast.Inspect(n, func(c ast.Node) bool {
			switch v := c.(type) {
			case nil:
				b.WriteByte(')')
				return true
			case *ast.CommentGroup, *ast.Comment:
				return false
			case *ast.Ident:
				fmt.Fprintf(&b, "(id %q", v.Name)
			case *ast.BasicLit:
				fmt.Fprintf(&b, "(lit %d %q", v.Kind, v.Value)
			case *ast.BinaryExpr:
				fmt.Fprintf(&b, "(bin %d", v.Op)
			case *ast.UnaryExpr:
				fmt.Fprintf(&b, "(un %d", v.Op)
			case *ast.AssignStmt:
				fmt.Fprintf(&b, "(assign %d %d", v.Tok, len(v.Lhs))
			case *ast.IncDecStmt:
				fmt.Fprintf(&b, "(incdec %d", v.Tok)
			case *ast.BranchStmt:
				fmt.Fprintf(&b, "(branch %d", v.Tok)
			case *ast.RangeStmt:
				fmt.Fprintf(&b, "(range %d %t %t", v.Tok, v.Key != nil, v.Value != nil)
			case *ast.ChanType:
				fmt.Fprintf(&b, "(chan %d", v.Dir)
			case *ast.GenDecl:
				fmt.Fprintf(&b, "(gen %d", v.Tok)
			case *ast.ForStmt:
				fmt.Fprintf(&b, "(for %t %t %t", v.Init != nil, v.Cond != nil, v.Post != nil)
			case *ast.SliceExpr:
				fmt.Fprintf(&b, "(slice %t %t %t %t", v.Low != nil, v.High != nil, v.Max != nil, v.Slice3)
			case *ast.CompositeLit:
				fmt.Fprintf(&b, "(comp %t", v.Type != nil)
			case *ast.ValueSpec:
				fmt.Fprintf(&b, "(value %t %d", v.Type != nil, len(v.Names))
			case *ast.TypeSpec:
				fmt.Fprintf(&b, "(type %t", v.Assign.IsValid())
			case *ast.CallExpr:
				fmt.Fprintf(&b, "(call %t %d", v.Ellipsis.IsValid(), len(v.Args))
			case *ast.IfStmt:
				fmt.Fprintf(&b, "(if %t %t", v.Init != nil, v.Else != nil)
			case *ast.SwitchStmt:
				fmt.Fprintf(&b, "(switch %t %t", v.Init != nil, v.Tag != nil)
			case *ast.TypeSwitchStmt:
				fmt.Fprintf(&b, "(typeswitch %t", v.Init != nil)
			case *ast.ArrayType:
				fmt.Fprintf(&b, "(array %t", v.Len != nil)
			case *ast.CaseClause:
				fmt.Fprintf(&b, "(case %d", len(v.List))
			case *ast.CommClause:
				fmt.Fprintf(&b, "(comm %t", v.Comm != nil)
			case *ast.TypeAssertExpr:
				fmt.Fprintf(&b, "(assert %t", v.Type != nil)
			case *ast.ReturnStmt:
				fmt.Fprintf(&b, "(ret %d", len(v.Results))
			case *ast.Field:
				fmt.Fprintf(&b, "(field %d %t", len(v.Names), v.Tag != nil)
			case *ast.FuncType:
				fmt.Fprintf(&b, "(functype %t", v.TypeParams != nil)
			case *ast.FuncDecl:
				fmt.Fprintf(&b, "(funcdecl %t %t", v.Recv != nil, v.Body != nil)
			default:
				fmt.Fprintf(&b, "(%T", c)
			}
			return true
		})
		b.WriteByte(';')
	}
	return hashStrings(b.String())
}

// isNilNode detects a typed nil held in an interface, such as a nil *ast.FieldList.
func isNilNode(n ast.Node) bool {
	switch v := n.(type) {
	case *ast.FieldList:
		return v == nil
	case *ast.BasicLit:
		return v == nil
	case *ast.BlockStmt:
		return v == nil
	case *ast.CommentGroup:
		return v == nil
	}
	return false
}
