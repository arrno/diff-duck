package treesitter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Shared, language-independent pieces of unit extraction.

const maxSignature = 240

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
		writeGap(b, n.t.src, at, c.StartByte())
		f.write(b, c)
		at = c.EndByte()
	}
	writeGap(b, n.t.src, at, n.EndByte())
	b.WriteByte(')')
}

// writeGap records source between children that no node covers, ignoring whitespace.
func writeGap(b *strings.Builder, src []byte, from, to int) {
	if from >= to || from < 0 || to > len(src) {
		return
	}
	if gap := strings.Join(strings.Fields(string(src[from:to])), ""); gap != "" {
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

// compact collapses text to one bounded line.
func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxSignature {
		return s
	}
	return strings.ToValidUTF8(s[:maxSignature], "") + "…"
}

// lineCount returns how many lines a file has, counting a final line without a newline.
func lineCount(src []byte) int {
	n := bytes.Count(src, []byte("\n"))
	if len(src) == 0 || src[len(src)-1] != '\n' {
		n++
	}
	return n
}

// spanText returns the source from the start of one node to the end of another.
func spanText(src []byte, from, to Node) string {
	s, e := from.StartByte(), to.EndByte()
	if s < 0 || e > len(src) || s > e {
		return ""
	}
	return string(src[s:e])
}
