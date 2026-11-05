package domain

import (
	"fmt"
	"sort"
)

// MeasureCoverage checks, independently of how the tree was built, that every changed file
// and line appears in some node's diff.
func MeasureCoverage(changes []FileChange, roots []*Node) Coverage {
	type lineKey struct {
		path string
		kind LineKind
		line int
	}
	traced := map[lineKey]bool{}
	tracedFile := map[string]bool{}
	for _, root := range roots {
		root.Walk(func(n *Node) {
			for _, loc := range n.Locations {
				tracedFile[loc.Path] = true
			}
			for _, d := range n.Diff {
				for _, l := range d.Lines {
					switch l.Kind {
					case LineAdded:
						traced[lineKey{d.NewPath, LineAdded, l.NewLine}] = true
					case LineRemoved:
						traced[lineKey{d.OldPath, LineRemoved, l.OldLine}] = true
					}
				}
			}
		})
	}

	var c Coverage
	c.ChangedFiles = len(changes)
	for _, f := range changes {
		if tracedFile[f.Path()] || (f.OldPath != "" && tracedFile[f.OldPath]) {
			c.TracedFiles++
		} else {
			c.Untraced = append(c.Untraced, f.Path())
		}
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				var k lineKey
				switch l.Kind {
				case LineAdded:
					k = lineKey{f.NewPath, LineAdded, l.NewLine}
				case LineRemoved:
					k = lineKey{f.OldPath, LineRemoved, l.OldLine}
				default:
					continue
				}
				c.ChangedLines++
				if traced[k] {
					c.TracedLines++
				} else {
					c.Untraced = append(c.Untraced, fmt.Sprintf("%s:%d (%s)", k.path, k.line, k.kind))
				}
			}
		}
	}
	return c
}

// RollUpStats sets each node's added and removed counts to the distinct lines in its subtree.
// Fields declared together, as in "a, b int", share one line, which must count once.
func RollUpStats(n *Node) {
	rollUp(n)
}

type statLine struct {
	path string
	kind LineKind
	line int
}

func rollUp(n *Node) map[statLine]bool {
	lines := map[statLine]bool{}
	for _, d := range n.Diff {
		for _, l := range d.Lines {
			switch l.Kind {
			case LineAdded:
				lines[statLine{d.NewPath, LineAdded, l.NewLine}] = true
			case LineRemoved:
				lines[statLine{d.OldPath, LineRemoved, l.OldLine}] = true
			}
		}
	}
	for _, c := range n.Children {
		child := rollUp(c)
		if len(child) > len(lines) {
			lines, child = child, lines
		}
		for k := range child {
			lines[k] = true
		}
	}
	n.Added, n.Removed = 0, 0
	for k := range lines {
		if k.kind == LineAdded {
			n.Added++
		} else {
			n.Removed++
		}
	}
	return lines
}

// AssociateOutcomes links outcomes to nodes and returns the changes no outcome reaches: leaves,
// and parents that changed directly. A
// reference to a parent covers its whole subtree. Node IDs the tree does not contain are
// dropped, and an outcome left with no real node is removed, since every outcome must point
// at changed code. The rejected references are returned so the caller can report them.
func AssociateOutcomes(roots []*Node, outcomes []Outcome) (kept []Outcome, unassigned []string, rejected []string) {
	index := map[string]*Node{}
	for _, root := range roots {
		root.Walk(func(n *Node) {
			n.Outcomes = nil
			index[n.ID] = n
		})
	}

	kept = []Outcome{}
	for _, o := range outcomes {
		valid := []string{}
		seen := map[string]bool{}
		for _, id := range o.NodeIDs {
			n, ok := index[id]
			if !ok {
				rejected = append(rejected, fmt.Sprintf("outcome %q referenced unknown node %q", o.Title, id))
				continue
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			valid = append(valid, id)
			n.Walk(func(d *Node) {
				if !contains(d.Outcomes, o.ID) {
					d.Outcomes = append(d.Outcomes, o.ID)
				}
			})
		}
		if len(valid) == 0 {
			rejected = append(rejected, fmt.Sprintf("outcome %q had no valid nodes and was dropped", o.Title))
			continue
		}
		o.NodeIDs = valid
		kept = append(kept, o)
	}

	unassigned = []string{}
	for _, root := range roots {
		root.Walk(func(n *Node) {
			if (len(n.Children) == 0 || n.Direct) && len(n.Outcomes) == 0 {
				unassigned = append(unassigned, n.ID)
			}
		})
	}
	sort.Strings(unassigned)
	return kept, unassigned, rejected
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
