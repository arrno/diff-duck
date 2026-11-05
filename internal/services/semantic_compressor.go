package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/arrno/diff-duck/internal/domain"
)

const (
	maxBatchItems    = 12
	maxBatchChars    = 60000
	maxFieldChars    = 16000
	maxDocChars      = 2000
	maxChildrenChars = 48000
	maxSummaryChars  = 600
	maxAliasChars    = 120
)

// Prompt names a PromptSource must provide.
const (
	promptLeaf     = "leaf"
	promptParent   = "parent"
	promptOutcomes = "outcomes"
)

const truncatedNote = "This summary is based on a truncated view of a very large change."

var summariesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "summaries": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string"},
          "semantic_name": {"type": "string"},
          "summary": {"type": "string"},
          "confidence": {"type": "string", "enum": ["high", "low"]},
          "uncertainty": {"type": "string"}
        },
        "required": ["id", "semantic_name", "summary", "confidence", "uncertainty"],
        "additionalProperties": false
      }
    }
  },
  "required": ["summaries"],
  "additionalProperties": false
}`)

// semanticCompressor explains the tree bottom-up: leaves from their source changes, then each
// parent from its direct changes and its children's explanations. It writes only the Summary
// of nodes, so structural facts are out of a model's reach.
type semanticCompressor struct {
	runner       *modelRunner
	leafPrompt   string
	parentPrompt string
	concurrency  int
}

type leafItem struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	Op                string `json:"op"`
	Name              string `json:"name"`
	PreviousName      string `json:"previous_name,omitempty"`
	Signature         string `json:"signature,omitempty"`
	PreviousSignature string `json:"previous_signature,omitempty"`
	Doc               string `json:"doc,omitempty"`
	Source            string `json:"source,omitempty"`
	Diff              string `json:"diff,omitempty"`
	Truncated         bool   `json:"truncated,omitempty"`
	Uncertainty       string `json:"known_limitation,omitempty"`
}

type childSummary struct {
	Kind         string `json:"kind"`
	Op           string `json:"op"`
	Name         string `json:"name"`
	Signature    string `json:"signature,omitempty"`
	SemanticName string `json:"semantic_name,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Test         bool   `json:"test,omitempty"`
	Unexplained  bool   `json:"unexplained,omitempty"`
}

type parentItem struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind"`
	Op              string         `json:"op"`
	Name            string         `json:"name"`
	Signature       string         `json:"signature,omitempty"`
	Direct          bool           `json:"directly_changed"`
	Doc             string         `json:"doc,omitempty"`
	OwnDiff         string         `json:"own_diff,omitempty"`
	Children        []childSummary `json:"children"`
	ChildrenOmitted int            `json:"children_omitted,omitempty"`
	Note            string         `json:"note,omitempty"`
	Truncated       bool           `json:"truncated,omitempty"`
}

type modelSummary struct {
	ID           string `json:"id"`
	SemanticName string `json:"semantic_name"`
	Summary      string `json:"summary"`
	Confidence   string `json:"confidence"`
	Uncertainty  string `json:"uncertainty"`
}

type summaryBatch struct {
	Items any `json:"items"`
}

// Compress fills in summaries across the tree and returns warnings about what it could not
// explain. Failures leave nodes visibly unexplained; they never produce invented text (I10).
func (c *semanticCompressor) Compress(ctx context.Context, roots []*domain.Node) []string {
	var warnings []string
	var mu sync.Mutex
	warn := func(format string, args ...any) {
		mu.Lock()
		warnings = append(warnings, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	// Leaves first, gathered depth-first so that a batch tends to share a package.
	var leaves []*domain.Node
	levels := map[int][]*domain.Node{}
	maxDepth := 0
	var walk func(n *domain.Node, depth int)
	walk = func(n *domain.Node, depth int) {
		if needsModel(n) {
			if len(n.Children) == 0 {
				leaves = append(leaves, n)
			} else {
				levels[depth] = append(levels[depth], n)
				if depth > maxDepth {
					maxDepth = depth
				}
			}
		}
		for _, child := range n.Children {
			walk(child, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}

	c.run(ctx, leaves, c.leafPrompt, func(n *domain.Node) (any, int, bool) {
		item := newLeafItem(n)
		return item, len(item.Source) + len(item.Diff) + len(item.Signature) + len(item.Doc), item.Truncated
	}, warn)

	// Parents strictly from the deepest level up, so each sees finished children.
	for depth := maxDepth; depth >= 0; depth-- {
		c.run(ctx, levels[depth], c.parentPrompt, func(n *domain.Node) (any, int, bool) {
			item := newParentItem(n)
			size := len(item.OwnDiff)
			for _, ch := range item.Children {
				size += len(ch.Name) + len(ch.Signature) + len(ch.Summary) + 40
			}
			return item, size, item.Truncated
		}, warn)
	}
	return warnings
}

func needsModel(n *domain.Node) bool {
	return n.Summary.Origin == domain.OriginNone
}

type preparedItem struct {
	node      *domain.Node
	payload   any
	truncated bool
}

// run groups nodes into batches, and calls the model with bounded concurrency.
func (c *semanticCompressor) run(ctx context.Context, nodes []*domain.Node, prompt string, prepare func(*domain.Node) (any, int, bool), warn func(string, ...any)) {
	var batches [][]preparedItem
	var current []preparedItem
	size := 0
	for _, n := range nodes {
		payload, itemSize, truncated := prepare(n)
		if len(current) > 0 && (len(current) >= maxBatchItems || size+itemSize > maxBatchChars) {
			batches = append(batches, current)
			current, size = nil, 0
		}
		current = append(current, preparedItem{n, payload, truncated})
		size += itemSize
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}

	sem := make(chan struct{}, max(c.concurrency, 1))
	var wg sync.WaitGroup
	for _, batch := range batches {
		wg.Add(1)
		sem <- struct{}{}
		go func(batch []preparedItem) {
			defer wg.Done()
			defer func() { <-sem }()
			c.summarizeBatch(ctx, batch, prompt, warn)
		}(batch)
	}
	wg.Wait()
}

func (c *semanticCompressor) summarizeBatch(ctx context.Context, batch []preparedItem, prompt string, warn func(string, ...any)) {
	items := make([]any, len(batch))
	byID := make(map[string]preparedItem, len(batch))
	for i, it := range batch {
		items[i] = it.payload
		byID[it.node.ID] = it
	}

	raw, err := c.runner.complete(ctx, prompt, summariesSchema, summaryBatch{Items: items})
	if err != nil {
		warn("model call failed for %d unit(s) starting at %s: %v", len(batch), batch[0].node.ID, err)
		for _, it := range batch {
			markUnexplained(it.node, "No explanation is available because the model call failed.")
		}
		return
	}
	var parsed struct {
		Summaries []modelSummary `json:"summaries"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		warn("model returned unusable output for %d unit(s) starting at %s: %v", len(batch), batch[0].node.ID, err)
		for _, it := range batch {
			markUnexplained(it.node, "No explanation is available because the model output was unusable.")
		}
		return
	}

	done := make(map[string]bool, len(batch))
	for _, s := range parsed.Summaries {
		it, ok := byID[s.ID]
		// IDs come from the model, so only those we asked about count (I11).
		if !ok || done[s.ID] || strings.TrimSpace(s.Summary) == "" {
			continue
		}
		done[s.ID] = true
		applySummary(it, s)
	}
	for _, it := range batch {
		if !done[it.node.ID] {
			warn("model returned no summary for %s", it.node.ID)
			markUnexplained(it.node, "The model did not return an explanation for this unit.")
		}
	}
}

func applySummary(it preparedItem, s modelSummary) {
	n := it.node
	limitation := n.Summary.Uncertainty
	summary := domain.Summary{
		Origin:      domain.OriginModel,
		Text:        clip(strings.TrimSpace(s.Summary), maxSummaryChars),
		Confidence:  domain.ConfidenceHigh,
		Uncertainty: clip(strings.TrimSpace(s.Uncertainty), maxSummaryChars),
	}
	if s.Confidence == string(domain.ConfidenceLow) || limitation != "" {
		summary.Confidence = domain.ConfidenceLow
	}
	if alias := strings.TrimSpace(s.SemanticName); alias != "" && alias != n.Name {
		summary.SemanticName = clip(alias, maxAliasChars)
	}
	notes := []string{}
	for _, note := range []string{limitation, summary.Uncertainty} {
		if note != "" {
			notes = append(notes, note)
		}
	}
	if it.truncated {
		summary.Confidence = domain.ConfidenceLow
		notes = append(notes, truncatedNote)
	}
	summary.Uncertainty = strings.Join(notes, " ")
	n.Summary = summary
}

func markUnexplained(n *domain.Node, why string) {
	note := why
	if n.Summary.Uncertainty != "" {
		note = n.Summary.Uncertainty + " " + why
	}
	n.Summary = domain.Summary{Origin: domain.OriginNone, Confidence: domain.ConfidenceLow, Uncertainty: note}
}

func newLeafItem(n *domain.Node) leafItem {
	item := leafItem{
		ID:                n.ID,
		Kind:              string(n.Kind),
		Op:                string(n.Op),
		Name:              n.Name,
		PreviousName:      n.PreviousName,
		Signature:         n.Signature,
		PreviousSignature: n.PreviousSignature,
		Doc:               n.Doc,
		Uncertainty:       n.Summary.Uncertainty,
	}
	var cut bool
	item.Doc, cut = boundedTo(n.Doc, maxDocChars)
	item.Truncated = cut
	item.Source, cut = bounded(n.Source)
	item.Truncated = item.Truncated || cut
	// An addition or removal's diff repeats its source. File nodes have only their diff.
	if n.Op == domain.OpChange || n.Source == "" {
		item.Diff, cut = bounded(renderDiff(n.Diff))
		item.Truncated = item.Truncated || cut
	}
	return item
}

func newParentItem(n *domain.Node) parentItem {
	item := parentItem{
		ID:        n.ID,
		Kind:      string(n.Kind),
		Op:        string(n.Op),
		Name:      n.Name,
		Signature: n.Signature,
		Direct:    n.Direct,
		Note:      n.Note,
	}
	var cut bool
	item.Doc, cut = boundedTo(n.Doc, maxDocChars)
	item.Truncated = cut
	if n.Direct {
		item.OwnDiff, cut = bounded(renderDiff(n.Diff))
		item.Truncated = item.Truncated || cut
	}
	size := 0
	for i, c := range n.Children {
		size += len(c.Name) + len(c.Signature) + len(c.Summary.Text) + 40
		if size > maxChildrenChars && i > 0 {
			item.ChildrenOmitted = len(n.Children) - i
			item.Truncated = true
			break
		}
		item.Children = append(item.Children, childSummary{
			Kind:         string(c.Kind),
			Op:           string(c.Op),
			Name:         c.Name,
			Signature:    c.Signature,
			SemanticName: c.Summary.SemanticName,
			Summary:      c.Summary.Text,
			Test:         c.Test,
			Unexplained:  c.Summary.Origin == domain.OriginNone,
		})
	}
	return item
}

func bounded(s string) (string, bool) { return boundedTo(s, maxFieldChars) }

func boundedTo(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return strings.ToValidUTF8(s[:limit], ""), true
}

func clip(s string, limit int) string {
	if cut, truncated := boundedTo(s, limit); truncated {
		return cut + "…"
	}
	return s
}

// renderDiff writes a node's diff as plain text for model input.
func renderDiff(diffs []domain.FileDiff) string {
	var b strings.Builder
	for _, d := range diffs {
		for _, l := range d.Lines {
			switch l.Kind {
			case domain.LineAdded:
				b.WriteString("+" + l.Text + "\n")
			case domain.LineRemoved:
				b.WriteString("-" + l.Text + "\n")
			case domain.LineContext:
				b.WriteString(" " + l.Text + "\n")
			case domain.LineGap:
				b.WriteString("...\n")
			}
		}
	}
	return b.String()
}
