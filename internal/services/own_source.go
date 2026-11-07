package services

import (
	"fmt"
	"sort"
	"strings"

	"github.com/arrno/diff-duck/internal/domain"
)

// Own source: what a container unit is explained from when it is a leaf. Its members are units
// of their own, so their code is not its change; reduced to signatures, they still show its
// outline.

// reducesMembers reports whether a kind is a container whose members are reduced to their
// signatures in its own source. Functions keep nested code, which is part of their logic.
func reducesMembers(k domain.UnitKind) bool {
	switch k {
	case domain.KindModule, domain.KindStruct, domain.KindInterface, domain.KindType,
		domain.KindClass, domain.KindEnum, domain.KindTrait, domain.KindRecord:
		return true
	}
	return false
}

// linkMembers records each container's direct members among one side of a file's units. A
// unit whose lines a container's strictly include is inside it; one with the very same lines
// is beside it, as the fields of "a, b int" are, except in a module, which holds everything.
// This is the same rule paintOwners uses to share a line between units; the two must agree.
func linkMembers(units []*keyedUnit) {
	sorted := append([]*keyedUnit(nil), units...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, c := sorted[i], sorted[j]
		if a.StartLine != c.StartLine {
			return a.StartLine < c.StartLine
		}
		if a.EndLine != c.EndLine {
			return a.EndLine > c.EndLine
		}
		return a.Kind == domain.KindModule && c.Kind != domain.KindModule
	})
	contains := func(outer, u *keyedUnit) bool {
		if outer.StartLine > u.StartLine || u.EndLine > outer.EndLine {
			return false
		}
		return outer.Kind == domain.KindModule || outer.StartLine != u.StartLine || outer.EndLine != u.EndLine
	}
	parent := map[*keyedUnit]*keyedUnit{}
	var open []*keyedUnit // the chain of containers around the current line
	for _, u := range sorted {
		u.members = nil
		for len(open) > 0 && open[len(open)-1].EndLine < u.StartLine {
			open = open[:len(open)-1]
		}
		if len(open) == 0 {
			open = append(open, u)
			continue
		}
		top := open[len(open)-1]
		switch {
		case contains(top, u):
			parent[u] = top
			top.members = append(top.members, u)
			open = append(open, u)
		case top.StartLine == u.StartLine && top.EndLine == u.EndLine:
			// Beside top, so inside whatever holds top. It is not pushed: top stands for it.
			if p := parent[top]; p != nil {
				parent[u] = p
				p.members = append(p.members, u)
			}
		default:
			open = append(open, u)
		}
	}
}

// ownSource returns a unit's code for explaining it on its own. In a container, each member
// whose lines hold nothing else is reduced to its signature, so a module whose only change is
// an import is explained from its own code rather than from every function in the file. A
// member sharing a line with other code, such as a method on a class's opening line or two
// functions on one line, stays as it is, so no code but members' is ever left out.
func ownSource(u *keyedUnit) string {
	if len(u.members) == 0 || !reducesMembers(u.Kind) {
		return u.Source
	}
	lines := strings.SplitAfter(u.Source, "\n")
	if len(lines) < u.EndLine-u.StartLine+1 {
		return u.Source // lines and code disagree; never guess
	}
	line := func(n int) string { return strings.TrimRight(lines[n-u.StartLine], "\r\n") }
	var b strings.Builder
	at := u.StartLine // the next line of u still to write
	for _, m := range u.members {
		if m.StartLine < at || !ownsItsLines(m, line(m.StartLine), line(m.EndLine)) {
			continue
		}
		for ; at < m.StartLine; at++ {
			b.WriteString(lines[at-u.StartLine])
		}
		first := lines[m.StartLine-u.StartLine]
		indent := first[:len(first)-len(strings.TrimLeft(first, " \t"))]
		summary := m.Signature
		if summary == "" {
			summary = m.Name
		}
		plural := "lines"
		if m.EndLine == m.StartLine {
			plural = "line"
		}
		fmt.Fprintf(&b, "%s%s … (%d %s)\n", indent, summary, m.EndLine-m.StartLine+1, plural)
		at = m.EndLine + 1
	}
	for ; at-u.StartLine < len(lines); at++ {
		b.WriteString(lines[at-u.StartLine])
	}
	return b.String()
}

// ownsItsLines reports whether a member's first and last lines, as they stand in its
// container, hold nothing but the member's own code and whitespace.
func ownsItsLines(m *keyedUnit, first, last string) bool {
	code := strings.TrimRight(m.Source, "\r\n")
	head, _, _ := strings.Cut(code, "\n")
	tail := code[strings.LastIndex(code, "\n")+1:]
	before, ok := strings.CutSuffix(strings.TrimRight(first, " \t"), strings.TrimRight(head, " \t\r"))
	if !ok || strings.TrimSpace(before) != "" {
		return false
	}
	after, ok := strings.CutPrefix(strings.TrimLeft(last, " \t"), strings.TrimLeft(tail, " \t"))
	return ok && strings.TrimSpace(after) == ""
}
