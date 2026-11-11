package services

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/arrno/diffduck/internal/domain"
)

const maxOutcomeInputChars = 1500000

var outcomesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "overview": {"type": "string"},
    "outcomes": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "op": {"type": "string", "enum": ["ADD", "REMOVE", "CHANGE"]},
          "title": {"type": "string"},
          "description": {"type": "string"},
          "node_ids": {"type": "array", "items": {"type": "string"}},
          "confidence": {"type": "string", "enum": ["high", "low"]},
          "uncertainty": {"type": "string"}
        },
        "required": ["op", "title", "description", "node_ids", "confidence", "uncertainty"],
        "additionalProperties": false
      }
    }
  },
  "required": ["overview", "outcomes"],
  "additionalProperties": false
}`)

type outcomeUnit struct {
	ID           string `json:"id"`
	Parent       string `json:"parent,omitempty"`
	Kind         string `json:"kind"`
	Op           string `json:"op"`
	Name         string `json:"name"`
	Signature    string `json:"signature,omitempty"`
	SemanticName string `json:"semantic_name,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Test         bool   `json:"test,omitempty"`
}

type outcomeInput struct {
	Files   int           `json:"changed_files"`
	Added   int           `json:"lines_added"`
	Removed int           `json:"lines_removed"`
	Units   []outcomeUnit `json:"units"`
}

// identifyOutcomes runs after compression, working from explanations rather than source. Its
// failure leaves every change visible and unassigned, which the product allows (I8).
func identifyOutcomes(ctx context.Context, runner *modelRunner, prompt string, result *domain.CompressedDiff) {
	input := outcomeInput{Files: result.Files, Added: result.Added, Removed: result.Removed}
	// On a change too large to send whole, keep the upper levels of the tree and say so.
	maxDepth := 8
	var collect func(n *domain.Node, parent string, depth int)
	collect = func(n *domain.Node, parent string, depth int) {
		if depth > maxDepth {
			return
		}
		input.Units = append(input.Units, outcomeUnit{
			ID:           n.ID,
			Parent:       parent,
			Kind:         string(n.Kind),
			Op:           string(n.Op),
			Name:         n.Name,
			Signature:    n.Signature,
			SemanticName: n.Summary.SemanticName,
			Summary:      n.Summary.Text,
			Test:         n.Test,
		})
		for _, child := range n.Children {
			collect(child, n.ID, depth+1)
		}
	}
	gather := func() int {
		input.Units = nil
		for _, r := range result.Nodes {
			collect(r, "", 0)
		}
		size := 0
		for _, u := range input.Units {
			size += len(u.ID) + len(u.Name) + len(u.Signature) + len(u.Summary) + len(u.SemanticName) + 60
		}
		return size
	}
	size := gather()
	for size > maxOutcomeInputChars && maxDepth > 0 {
		if maxDepth == 8 {
			maxDepth = 2
		}
		maxDepth--
		size = gather()
		result.Warnings = append(result.Warnings, fmt.Sprintf("this change is too large to describe in full, so outcomes were identified from the top %d level(s) of the tree", maxDepth+1))
	}
	if len(input.Units) == 0 {
		return
	}

	raw, err := runner.complete(ctx, prompt, outcomesSchema, input)
	if err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("outcome identification failed: %v", err))
		return
	}
	var parsed struct {
		Overview string `json:"overview"`
		Outcomes []struct {
			Op          string   `json:"op"`
			Title       string   `json:"title"`
			Description string   `json:"description"`
			NodeIDs     []string `json:"node_ids"`
			Confidence  string   `json:"confidence"`
			Uncertainty string   `json:"uncertainty"`
		} `json:"outcomes"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		result.Warnings = append(result.Warnings, fmt.Sprintf("outcome identification returned unusable output: %v", err))
		return
	}
	result.Overview = strings.TrimSpace(parsed.Overview)
	for i, o := range parsed.Outcomes {
		op := domain.ChangeOp(o.Op)
		if !op.Valid() || strings.TrimSpace(o.Title) == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("dropped a malformed outcome %q", o.Title))
			continue
		}
		confidence := domain.ConfidenceHigh
		if o.Confidence == string(domain.ConfidenceLow) {
			confidence = domain.ConfidenceLow
		}
		result.Outcomes = append(result.Outcomes, domain.Outcome{
			ID:          fmt.Sprintf("o%d", i+1),
			Op:          op,
			Title:       strings.TrimSpace(o.Title),
			Description: strings.TrimSpace(o.Description),
			NodeIDs:     o.NodeIDs,
			Confidence:  confidence,
			Uncertainty: strings.TrimSpace(o.Uncertainty),
		})
	}
}
