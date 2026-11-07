package treesitter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

// Shared, language-independent pieces of unit extraction.

const maxSignature = 240

// parseClean parses a file and refuses it when it has a syntax error, so a broken file
// degrades to a file node rather than to invented structure.
func parseClean(ctx context.Context, rt *Runtime, g Grammar, p string, src []byte) (*Tree, error) {
	tree, err := rt.Parse(ctx, g, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	if root := tree.Root(); root.HasError() {
		return nil, fmt.Errorf("parse %s: %w near line %d", p, ErrSyntax, root.FirstError().StartLine())
	}
	return tree, nil
}

// dirPackage is the package of a language whose packages are directories: the file's
// directory, named by its last element, or unnamed at the repository root.
func dirPackage(p string) domain.PackageRef {
	dir := path.Dir(p)
	if dir == "." {
		return domain.PackageRef{Dir: dir}
	}
	return domain.PackageRef{Dir: dir, Name: path.Base(dir)}
}

// fileUnits collects one file's units, in source order, and stamps each with what every unit
// of the file shares.
type fileUnits struct {
	ctx   context.Context
	path  string
	pkg   domain.PackageRef
	test  bool // the whole file holds tests
	units []domain.Unit
}

// stopped reports whether the caller has given up on the analysis. Extraction checks it once
// per declaration it visits, so a cancelled diff stops promptly even on a large file.
func (f *fileUnits) stopped() bool { return f.ctx.Err() != nil }

// reserve holds a place for a unit whose hash depends on its members, which come first.
func (f *fileUnits) reserve() int {
	f.units = append(f.units, domain.Unit{})
	return len(f.units) - 1
}

// finish stamps a unit with its file, package, and test status, and bounds its signature.
func (f *fileUnits) finish(u domain.Unit) domain.Unit {
	u.Package = f.pkg
	u.File = f.path
	u.Test = u.Test || f.test
	u.Signature = compact(u.Signature)
	return u
}

// analysis returns the file's analysis, or the context's error when extraction was cancelled
// part way, since a partial list of units would misreport the file.
func (f *fileUnits) analysis(a domain.FileAnalysis) (domain.FileAnalysis, error) {
	if err := f.ctx.Err(); err != nil {
		return domain.FileAnalysis{}, fmt.Errorf("analyze %s: %w", f.path, err)
	}
	a.Units = f.units
	a.Test = f.test
	return a, nil
}

// leadingComments returns the comments before a file's first statement, lower-cased, one per
// line; a #! line before them is skipped. Generators mark their output there.
func leadingComments(root Node) string {
	var header strings.Builder
	for _, c := range root.Children() {
		if c.Type() == "hash_bang_line" {
			continue
		}
		if !c.IsExtra() {
			break
		}
		header.WriteString(c.Text() + "\n")
	}
	return strings.ToLower(header.String())
}

// markedGenerated reports whether a file's leading comments carry the common generated-file
// markers: "@generated", or "generated" with "do not edit".
func markedGenerated(header string) bool {
	return strings.Contains(header, "@generated") ||
		(strings.Contains(header, "generated") && strings.Contains(header, "do not edit"))
}

// fingerprint describes how one language turns syntax into a formatting-independent hash.
// Extras such as comments never count.
type fingerprint struct {
	// skip leaves a node out entirely, such as a member that is its own unit.
	skip func(Node) bool
	// transparent replaces a node by its named children, such as redundant parentheses.
	transparent func(Node) bool
	// token returns a leaf's text as it should count, and false to drop the leaf.
	token func(Node) (string, bool)
	// opaque counts a node by its exact source text, such as a string's contents, where
	// whitespace is meaning rather than formatting.
	opaque func(Node) bool
}

// hash fingerprints syntax. It records tree shape and token text, never whitespace, so
// reformatting does not change it. Text a grammar keeps in hidden tokens, which appear in no
// node, still counts: whatever lies between a node's children, less whitespace, is recorded.
func (f fingerprint) hash(nodes ...Node) string {
	var b strings.Builder
	for _, n := range nodes {
		if n.IsNull() {
			b.WriteString("nil;")
			continue
		}
		f.write(&b, n)
		b.WriteByte(';')
	}
	return hashStrings(b.String())
}

func (f fingerprint) write(b *strings.Builder, n Node) {
	if n.IsExtra() || (f.skip != nil && f.skip(n)) {
		return
	}
	if f.transparent != nil && f.transparent(n) {
		for _, c := range n.NamedChildren() {
			f.write(b, c)
		}
		return
	}
	if f.opaque != nil && f.opaque(n) {
		fmt.Fprintf(b, "(%s %q)", n.Type(), n.Text())
		return
	}
	children := n.Children()
	if len(children) == 0 {
		text := n.Text()
		if f.token != nil {
			var keep bool
			if text, keep = f.token(n); !keep {
				return
			}
		}
		fmt.Fprintf(b, "(%s %q)", n.Type(), text)
		return
	}
	b.WriteString("(" + n.Type())
	at := n.StartByte()
	for _, c := range children {
		writeGap(b, n.t.text, at, c.StartByte())
		f.write(b, c)
		at = c.EndByte()
	}
	writeGap(b, n.t.text, at, n.EndByte())
	b.WriteByte(')')
}

// writeGap records source between children that no node covers, ignoring whitespace.
func writeGap(b *strings.Builder, text string, from, to int) {
	if from >= to || from < 0 || to > len(text) {
		return
	}
	if gap := strings.Join(strings.Fields(text[from:to]), ""); gap != "" {
		fmt.Fprintf(b, "(gap %q)", gap)
	}
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s;", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// compact collapses text to one bounded line. Text that already is one, the usual case for a
// signature, is returned as it is, without the work or a copy.
func compact(s string) string {
	if isCompact(s) {
		return s
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxSignature {
		return s
	}
	return strings.ToValidUTF8(s[:maxSignature], "") + "…"
}

// isCompact reports whether compact would return s unchanged: it is short and ASCII, and its
// words are separated by single spaces.
func isCompact(s string) bool {
	if len(s) > maxSignature || s == "" || s[0] == ' ' || s[len(s)-1] == ' ' {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == ' ' && s[i+1] == ' ', c == '\t', c == '\n', c == '\r', c == '\v', c == '\f', c >= 0x80:
			return false
		}
	}
	return true
}

// lineCount returns how many lines a file has, counting a final line without a newline.
func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if len(text) == 0 || text[len(text)-1] != '\n' {
		n++
	}
	return n
}
