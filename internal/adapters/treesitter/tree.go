package treesitter

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// recordSize is sizeof(dd_node) in shim.c.
const recordSize = 36

// Flags in a dd_node record, from shim.c.
const (
	flagError    = 1
	flagMissing  = 2
	flagHasError = 4
	flagExtra    = 8
)

// symbolError is tree-sitter's built-in ERROR symbol (ts_builtin_sym_error). It is outside the
// grammar's symbol table.
const symbolError = 0xFFFF

const noParent = 0xFFFFFFFF

type record struct {
	typ                string
	field              string
	parent             int32
	firstChild, next   int32 // -1 when none
	startByte, endByte uint32
	startRow, endRow   uint32
	endCol             uint32
	named              bool
	flags              uint8
}

// Tree is a parsed file. It holds a copy of every node, so it outlives the WASM instance, and
// one copy of the file's text, which every node's text is a slice of. Units keep slices of it
// too, so however deeply declarations nest, the file is held in memory once.
type Tree struct {
	text  string
	nodes []record
}

// decode validates and converts the shim's records. Nothing read from WASM memory is trusted:
// every index and offset is checked before use.
func decode(raw []byte, count uint64, src []byte, symbols, fields []string) (*Tree, error) {
	if count == 0 || count*recordSize > uint64(len(raw)) || count > 1<<31-1 {
		return nil, errors.New("malformed parse result")
	}
	le := binary.LittleEndian
	t := &Tree{text: string(src), nodes: make([]record, count)}
	last := make([]int32, count) // last child seen, per node
	for i := range t.nodes {
		b := raw[i*recordSize : (i+1)*recordSize]
		sym, parent := le.Uint32(b[0:]), le.Uint32(b[4:])
		r := record{
			parent: -1, firstChild: -1, next: -1,
			startByte: le.Uint32(b[8:]), endByte: le.Uint32(b[12:]),
			startRow: le.Uint32(b[16:]), endRow: le.Uint32(b[24:]), endCol: le.Uint32(b[28:]),
			named: b[34] != 0, flags: b[35],
		}
		switch {
		case sym == symbolError:
			r.typ = "ERROR"
			r.flags |= flagError | flagHasError
		case int(sym) < len(symbols):
			r.typ = symbols[sym]
		default:
			return nil, fmt.Errorf("malformed parse result: node %d has unknown symbol %d", i, sym)
		}
		field := le.Uint16(b[32:])
		if int(field) >= len(fields) {
			return nil, fmt.Errorf("malformed parse result: node %d has unknown field %d", i, field)
		}
		r.field = fields[field]
		if r.startByte > r.endByte || int(r.endByte) > len(src) || r.startRow > r.endRow {
			return nil, fmt.Errorf("malformed parse result: node %d has an invalid range", i)
		}
		// Records are in pre-order, so a parent always comes first.
		switch {
		case i == 0 && parent == noParent:
		case i > 0 && parent < uint32(i):
			r.parent = int32(parent)
			p := &t.nodes[parent]
			if p.firstChild == -1 {
				p.firstChild = int32(i)
			} else {
				t.nodes[last[parent]].next = int32(i)
			}
			last[parent] = int32(i)
		default:
			return nil, fmt.Errorf("malformed parse result: node %d has parent %d", i, parent)
		}
		t.nodes[i] = r
	}
	return t, nil
}

// Root returns the tree's root node.
func (t *Tree) Root() Node { return Node{t: t, i: 0} }

// Text returns the whole file's text.
func (t *Tree) Text() string { return t.text }

// Any reports whether some node in the tree satisfies match. Nodes are visited in pre-order
// without recursion, so the walk's depth does not depend on how deeply the source nests.
func (t *Tree) Any(match func(Node) bool) bool {
	for i := range t.nodes {
		if match(Node{t: t, i: int32(i)}) {
			return true
		}
	}
	return false
}

// Node is one syntax node. The zero Node is null: it is what a missing child returns.
type Node struct {
	t *Tree
	i int32
}

// IsNull reports whether the node does not exist.
func (n Node) IsNull() bool { return n.t == nil }

func (n Node) rec() *record { return &n.t.nodes[n.i] }

// Type is the grammar's name for the node, such as "class_definition", or the token text for an
// anonymous node, such as "(".
func (n Node) Type() string {
	if n.IsNull() {
		return ""
	}
	return n.rec().typ
}

// Field is the node's field name within its parent, or empty.
func (n Node) Field() string {
	if n.IsNull() {
		return ""
	}
	return n.rec().field
}

// IsNamed reports whether the node is a named grammar rule rather than an anonymous token.
func (n Node) IsNamed() bool { return !n.IsNull() && n.rec().named }

// IsExtra reports whether the node may appear anywhere, such as a comment.
func (n Node) IsExtra() bool { return !n.IsNull() && n.rec().flags&flagExtra != 0 }

// HasError reports whether the node or anything inside it is an error or a missing token.
func (n Node) HasError() bool {
	return !n.IsNull() && n.rec().flags&(flagError|flagMissing|flagHasError) != 0
}

// IsError reports whether the node itself is an error or a missing token.
func (n Node) IsError() bool { return !n.IsNull() && n.rec().flags&(flagError|flagMissing) != 0 }

// Parent returns the enclosing node, or a null node for the root.
func (n Node) Parent() Node {
	if n.IsNull() || n.rec().parent < 0 {
		return Node{}
	}
	return Node{t: n.t, i: n.rec().parent}
}

// Children returns every child, including anonymous tokens and extras.
func (n Node) Children() []Node {
	if n.IsNull() {
		return nil
	}
	var out []Node
	for c := n.rec().firstChild; c != -1; c = n.t.nodes[c].next {
		out = append(out, Node{t: n.t, i: c})
	}
	return out
}

// NamedChildren returns the named children that are not extras.
func (n Node) NamedChildren() []Node {
	var out []Node
	for _, c := range n.Children() {
		if c.IsNamed() && !c.IsExtra() {
			out = append(out, c)
		}
	}
	return out
}

// ChildByField returns the first child with the field name, or a null node.
func (n Node) ChildByField(name string) Node {
	for _, c := range n.Children() {
		if c.Field() == name {
			return c
		}
	}
	return Node{}
}

// ChildrenByField returns every child with the field name.
func (n Node) ChildrenByField(name string) []Node {
	var out []Node
	for _, c := range n.Children() {
		if c.Field() == name {
			out = append(out, c)
		}
	}
	return out
}

// Text returns the node's source text: a slice of the file's one copy, never a new copy.
func (n Node) Text() string {
	if n.IsNull() {
		return ""
	}
	r := n.rec()
	return n.t.text[r.startByte:r.endByte]
}

// TextUntil returns the source from the node's start up to the byte offset end, such as up to
// a body's start or to a later node's end. Like Text, it slices the file's one copy. It is
// empty when end lies before the node's start or past the end of the file.
func (n Node) TextUntil(end int) string {
	if n.IsNull() {
		return ""
	}
	start := int(n.rec().startByte)
	if end < start || end > len(n.t.text) {
		return ""
	}
	return n.t.text[start:end]
}

// NextSibling returns the next sibling that is not an extra, such as a comment, or a null node.
// It costs constant time apart from the extras it skips.
func (n Node) NextSibling() Node {
	if n.IsNull() {
		return Node{}
	}
	for i := n.rec().next; i != -1; i = n.t.nodes[i].next {
		if n.t.nodes[i].flags&flagExtra == 0 {
			return Node{t: n.t, i: i}
		}
	}
	return Node{}
}

// StartByte returns the node's first byte offset.
func (n Node) StartByte() int {
	if n.IsNull() {
		return 0
	}
	return int(n.rec().startByte)
}

// EndByte returns the offset just past the node's last byte.
func (n Node) EndByte() int {
	if n.IsNull() {
		return 0
	}
	return int(n.rec().endByte)
}

// StartLine returns the 1-based line the node starts on.
func (n Node) StartLine() int {
	if n.IsNull() {
		return 0
	}
	return int(n.rec().startRow) + 1
}

// EndLine returns the 1-based line holding the node's last character. A node that ends at the
// start of a line, just after a newline, ends on the line before.
func (n Node) EndLine() int {
	if n.IsNull() {
		return 0
	}
	r := n.rec()
	if r.endCol == 0 && r.endRow > r.startRow {
		return int(r.endRow)
	}
	return int(r.endRow) + 1
}

// FirstError returns the first node, in source order, that is an error or a missing token.
func (n Node) FirstError() Node {
	if n.IsNull() || !n.HasError() {
		return Node{}
	}
	if n.IsError() {
		return n
	}
	for _, c := range n.Children() {
		if e := c.FirstError(); !e.IsNull() {
			return e
		}
	}
	return n
}
