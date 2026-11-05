package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"duck-diff/internal/domain"
	"duck-diff/internal/services/ports"
)

func TestModelInputsStayWithinBoundsAndCutsAreFlagged(t *testing.T) {
	leaf := newLeafItem(&domain.Node{ID: "l", Kind: domain.KindFunction, Op: domain.OpAdd, Doc: strings.Repeat("d", maxDocChars*5)})
	if len(leaf.Doc) > maxDocChars || !leaf.Truncated {
		t.Fatalf("doc %d truncated=%v", len(leaf.Doc), leaf.Truncated)
	}

	parent := &domain.Node{ID: "p", Kind: domain.KindPackage, Op: domain.OpChange}
	for i := 0; i < 5000; i++ {
		parent.Children = append(parent.Children, &domain.Node{Kind: domain.KindFunction, Op: domain.OpAdd, Name: strings.Repeat("n", 40), Signature: strings.Repeat("s", 100)})
	}
	item := newParentItem(parent)
	raw, _ := json.Marshal(item)
	if len(raw) > 2*maxChildrenChars || !item.Truncated || item.ChildrenOmitted == 0 {
		t.Fatalf("size %d truncated=%v omitted=%d", len(raw), item.Truncated, item.ChildrenOmitted)
	}
	if item.ChildrenOmitted+len(item.Children) != 5000 {
		t.Fatalf("omitted %d + listed %d", item.ChildrenOmitted, len(item.Children))
	}

	small := newParentItem(&domain.Node{Kind: domain.KindStruct, Op: domain.OpChange, Children: []*domain.Node{{Name: "a"}}})
	if small.Truncated || small.ChildrenOmitted != 0 {
		t.Fatalf("nothing was cut: %+v", small)
	}
}

func TestTruncatedInputForcesLowConfidence(t *testing.T) {
	n := &domain.Node{ID: "x"}
	applySummary(preparedItem{node: n, truncated: true}, modelSummary{ID: "x", Summary: "Does a thing.", Confidence: "high"})
	if n.Summary.Confidence != domain.ConfidenceLow || !strings.Contains(n.Summary.Uncertainty, "truncated") {
		t.Fatalf("%+v", n.Summary)
	}
}

func TestModelTextIsBounded(t *testing.T) {
	n := &domain.Node{ID: "x", Name: "real"}
	applySummary(preparedItem{node: n}, modelSummary{ID: "x", Summary: strings.Repeat("s", 5000), SemanticName: strings.Repeat("a", 5000), Confidence: "high"})
	if len(n.Summary.Text) > maxSummaryChars+4 || len(n.Summary.SemanticName) > maxAliasChars+4 {
		t.Fatalf("text %d alias %d", len(n.Summary.Text), len(n.Summary.SemanticName))
	}
	if n.Name != "real" {
		t.Fatal("a model must never alter the source name")
	}
}

type countingModel struct {
	calls int
	err   error
}

func (m *countingModel) Describe() string { return "counting" }
func (m *countingModel) Complete(context.Context, ports.ModelRequest) (ports.ModelResponse, error) {
	m.calls++
	return ports.ModelResponse{JSON: []byte(`{}`)}, m.err
}

func TestRetriesOnlyWhenARetryCouldHelp(t *testing.T) {
	ctx := context.Background()
	transient := &countingModel{err: errors.New("boom")}
	r := &modelRunner{model: transient}
	if _, err := r.complete(ctx, "", nil, 1); err == nil || transient.calls != modelAttempts || r.stats.FailedCalls != modelAttempts {
		t.Fatalf("calls %d, stats %+v, err %v", transient.calls, r.stats, err)
	}
	refused := &countingModel{err: ports.ErrModelRefused}
	r = &modelRunner{model: refused}
	if _, err := r.complete(ctx, "", nil, 1); !errors.Is(err, ports.ErrModelRefused) || refused.calls != 1 {
		t.Fatalf("a refusal must not retry: calls %d, %v", refused.calls, err)
	}
	limited := &countingModel{}
	r = &modelRunner{model: limited, limit: 1}
	if _, err := r.complete(ctx, "", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.complete(ctx, "", nil, 1); !errors.Is(err, errCallLimit) || limited.calls != 1 {
		t.Fatalf("limit: calls %d, %v", limited.calls, err)
	}
}
