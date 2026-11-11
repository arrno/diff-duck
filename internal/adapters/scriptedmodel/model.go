// Package scriptedmodel is a deterministic LanguageModel for verification. It lets the real CLI
// run end to end with repeatable output and no spend, and it can simulate model failures.
package scriptedmodel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/arrno/diffduck/internal/services/ports"
)

// Behavior selects what the model does.
type Behavior string

const (
	// Normal explains every item and makes one outcome per package.
	Normal Behavior = "normal"
	// Fail makes every call fail.
	Fail Behavior = "fail"
	// Partial omits every second summary, invents a summary for an unknown ID, and references
	// an unknown node from an outcome.
	Partial Behavior = "partial"
	// Slow answers like Normal after a delay per call, so a diff stays visibly in progress.
	Slow Behavior = "slow"
)

// slowDelay is how long each Slow call takes.
const slowDelay = 2 * time.Second

// Model is the scripted model.
type Model struct{ behavior Behavior }

// New returns a scripted model, rejecting an unknown behavior so a mistyped scenario cannot
// silently run as another one.
func New(b Behavior) (*Model, error) {
	switch b {
	case Normal, Fail, Partial, Slow:
		return &Model{behavior: b}, nil
	}
	return nil, fmt.Errorf("unknown scripted behavior %q", b)
}

// Describe identifies this configuration.
func (m *Model) Describe() string { return "scripted/" + string(m.behavior) }

type incoming struct {
	Items []struct {
		ID       string            `json:"id"`
		Op       string            `json:"op"`
		Name     string            `json:"name"`
		Children []json.RawMessage `json:"children"`
	} `json:"items"`
	Units []struct {
		ID     string `json:"id"`
		Parent string `json:"parent"`
		Kind   string `json:"kind"`
		Name   string `json:"name"`
	} `json:"units"`
}

// Complete returns output derived from the request's input.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	if m.behavior == Slow {
		select {
		case <-ctx.Done():
			return ports.ModelResponse{}, ctx.Err()
		case <-time.After(slowDelay):
		}
	}
	if m.behavior == Fail {
		return ports.ModelResponse{}, errors.New("scripted failure")
	}
	var in incoming
	if err := json.Unmarshal([]byte(req.User), &in); err != nil {
		return ports.ModelResponse{}, fmt.Errorf("scripted model input: %w", err)
	}
	var out any
	if in.Units != nil {
		out = m.outcomes(in)
	} else {
		out = m.summaries(in)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ports.ModelResponse{}, err
	}
	return ports.ModelResponse{JSON: raw, InputTokens: int64(len(req.User) / 4), OutputTokens: int64(len(raw) / 4)}, nil
}

func (m *Model) summaries(in incoming) any {
	entries := []map[string]string{}
	for i, item := range in.Items {
		if m.behavior == Partial && i%2 == 1 {
			continue
		}
		kindOfInput := "leaf"
		if item.Children != nil {
			kindOfInput = fmt.Sprintf("parent of %d", len(item.Children))
		}
		alias := ""
		if item.Name == "Service.Create" {
			alias = "createValidatedUser"
		}
		entries = append(entries, map[string]string{
			"id":            item.ID,
			"semantic_name": alias,
			"summary":       fmt.Sprintf("Scripted %s summary of %s %s.", kindOfInput, item.Op, item.Name),
			"confidence":    "high",
			"uncertainty":   "",
		})
	}
	if m.behavior == Partial {
		entries = append(entries, map[string]string{
			"id": "invented::Unit", "semantic_name": "", "summary": "Invented.", "confidence": "high", "uncertainty": "",
		})
	}
	return map[string]any{"summaries": entries}
}

func (m *Model) outcomes(in incoming) any {
	outcomes := []map[string]any{}
	for _, u := range in.Units {
		if u.Kind != "PACKAGE" || strings.HasSuffix(u.Name, "unassigned") {
			continue
		}
		ids := []string{}
		for _, c := range in.Units {
			if c.Parent == u.ID && c.Kind != "REGION" {
				ids = append(ids, c.ID)
			}
		}
		if len(ids) == 0 {
			continue
		}
		if m.behavior == Partial {
			ids = append(ids, "invented::Unit")
		}
		outcomes = append(outcomes, map[string]any{
			"op": "CHANGE", "title": "Behavior of " + u.Name, "description": "Scripted outcome.",
			"node_ids": ids, "confidence": "high", "uncertainty": "",
		})
	}
	if m.behavior == Partial {
		outcomes = append(outcomes, map[string]any{
			"op": "ADD", "title": "Fabricated outcome", "description": "Points only at nothing.",
			"node_ids": []string{"invented::Other"}, "confidence": "high", "uncertainty": "",
		})
	}
	return map[string]any{"overview": "Scripted overview of the change.", "outcomes": outcomes}
}
