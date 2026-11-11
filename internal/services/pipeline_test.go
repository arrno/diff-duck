package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

// answerFunc scripts a model from each request's decoded input.
type answerFunc func(req ports.ModelRequest) (string, error)

type fakeModel struct {
	mu       sync.Mutex
	requests []ports.ModelRequest
	answer   answerFunc
}

func (m *fakeModel) Describe() string { return "fake" }
func (m *fakeModel) Complete(_ context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	text, err := m.answer(req)
	if err != nil {
		return ports.ModelResponse{}, err
	}
	return ports.ModelResponse{JSON: []byte(text)}, nil
}

type requestInput struct {
	Items []struct {
		ID       string `json:"id"`
		OwnDiff  string `json:"own_diff"`
		Children []struct {
			Name    string `json:"name"`
			Summary string `json:"summary"`
		} `json:"children"`
	} `json:"items"`
}

func decode(t *testing.T, req ports.ModelRequest) requestInput {
	t.Helper()
	var in requestInput
	if err := json.Unmarshal([]byte(req.User), &in); err != nil {
		t.Fatal(err)
	}
	return in
}

func summarizeAll(req ports.ModelRequest) (string, error) {
	var in requestInput
	if err := json.Unmarshal([]byte(req.User), &in); err != nil {
		return "", err
	}
	var entries []string
	for _, item := range in.Items {
		entries = append(entries, fmt.Sprintf(`{"id":%q,"semantic_name":"","summary":"explained %s","confidence":"high","uncertainty":""}`, item.ID, item.ID))
	}
	return `{"summaries":[` + strings.Join(entries, ",") + `]}`, nil
}

func tree() (pkg, typ, leaf *domain.Node) {
	leaf = &domain.Node{ID: "leaf", Name: "leaf", Kind: domain.KindMethod, Op: domain.OpChange, Direct: true, Summary: domain.Summary{Origin: domain.OriginNone}}
	typ = &domain.Node{ID: "type", Name: "type", Kind: domain.KindStruct, Op: domain.OpChange, Summary: domain.Summary{Origin: domain.OriginNone}, Children: []*domain.Node{leaf}}
	pkg = &domain.Node{ID: "pkg", Name: "pkg", Kind: domain.KindPackage, Op: domain.OpChange, Summary: domain.Summary{Origin: domain.OriginNone}, Children: []*domain.Node{typ}}
	return pkg, typ, leaf
}

func compress(t *testing.T, answer answerFunc, roots ...*domain.Node) (*fakeModel, []string) {
	t.Helper()
	m := &fakeModel{answer: answer}
	c := &semanticCompressor{runner: &modelRunner{model: m}, leafPrompt: "LEAF", parentPrompt: "PARENT", concurrency: 1}
	return m, c.Compress(context.Background(), roots)
}

func TestCompressionIsBottomUpAndParentsSeeChildExplanations(t *testing.T) {
	pkg, typ, leaf := tree()
	m, warnings := compress(t, summarizeAll, pkg)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	if len(m.requests) != 3 {
		t.Fatalf("calls: %d", len(m.requests))
	}
	if m.requests[0].System != "LEAF" || m.requests[1].System != "PARENT" {
		t.Fatalf("leaves go first with the leaf prompt: %q %q", m.requests[0].System, m.requests[1].System)
	}
	for i, want := range []string{"leaf", "type", "pkg"} {
		if got := decode(t, m.requests[i]).Items[0].ID; got != want {
			t.Fatalf("request %d summarized %q, want %q", i, got, want)
		}
	}
	if got := decode(t, m.requests[1]).Items[0].Children[0].Summary; got != "explained leaf" {
		t.Fatalf("the type must see its child's explanation: %q", got)
	}
	if got := decode(t, m.requests[2]).Items[0].Children[0].Summary; got != "explained type" {
		t.Fatalf("the package must see the type's finished explanation: %q", got)
	}
	if typ.Summary.Text != "explained type" || leaf.Summary.Origin != domain.OriginModel {
		t.Fatalf("%+v %+v", typ.Summary, leaf.Summary)
	}
}

func TestParentSeesItsOwnDiffOnlyWhenItChangedDirectly(t *testing.T) {
	diff := []domain.FileDiff{{Lines: []domain.DiffLine{{Kind: domain.LineAdded, Text: "OWNLINE"}}}}
	child := &domain.Node{ID: "c", Name: "c", Kind: domain.KindField, Summary: domain.Summary{Origin: domain.OriginStructural}}
	direct := &domain.Node{ID: "direct", Kind: domain.KindStruct, Direct: true, Diff: diff, Children: []*domain.Node{child}, Summary: domain.Summary{Origin: domain.OriginNone}}
	inherited := &domain.Node{ID: "inherited", Kind: domain.KindStruct, Diff: diff, Children: []*domain.Node{child}, Summary: domain.Summary{Origin: domain.OriginNone}}
	if !strings.Contains(newParentItem(direct).OwnDiff, "OWNLINE") {
		t.Error("a directly changed parent must see its own change")
	}
	if newParentItem(inherited).OwnDiff != "" {
		t.Error("an inherited parent has no change of its own to describe")
	}
}

func TestStructuralNodesCostNoModelCall(t *testing.T) {
	field := &domain.Node{ID: "f", Kind: domain.KindField, Summary: domain.Summary{Origin: domain.OriginStructural}}
	m, _ := compress(t, summarizeAll, field)
	if len(m.requests) != 0 {
		t.Fatalf("calls: %d", len(m.requests))
	}
	// Leaves whose signature says everything are explained by the differ, never the model.
	for _, kind := range []domain.UnitKind{domain.KindField, domain.KindConst, domain.KindEnumMember, domain.KindStruct} {
		leaf := &domain.Node{ID: "leaf", Kind: kind, Op: domain.OpAdd, Direct: true, Signature: "X = 1"}
		explainUnit(leaf)
		if m, _ := compress(t, summarizeAll, leaf); len(m.requests) != 0 {
			t.Errorf("%s: calls: %d", kind, len(m.requests))
		}
	}
}

func TestUnusableModelOutputLeavesNodesVisiblyUnexplained(t *testing.T) {
	for name, tc := range map[string]struct {
		answer  string
		warning string
	}{
		"invalid JSON":  {`{not json`, "unusable"},
		"missing entry": {`{"summaries":[]}`, "no summary"},
		"blank summary": {`{"summaries":[{"id":"leaf","summary":"   ","confidence":"high"}]}`, "no summary"},
		"unknown ID":    {`{"summaries":[{"id":"other","summary":"x","confidence":"high"}]}`, "no summary"},
	} {
		t.Run(name, func(t *testing.T) {
			leaf := &domain.Node{ID: "leaf", Kind: domain.KindMethod, Summary: domain.Summary{Origin: domain.OriginNone}}
			_, warnings := compress(t, func(ports.ModelRequest) (string, error) { return tc.answer, nil }, leaf)
			if leaf.Summary.Origin != domain.OriginNone || leaf.Summary.Text != "" || leaf.Summary.Uncertainty == "" || leaf.Summary.Confidence != domain.ConfidenceLow {
				t.Fatalf("%+v", leaf.Summary)
			}
			if !strings.Contains(strings.Join(warnings, "\n"), tc.warning) {
				t.Fatalf("warnings: %v", warnings)
			}
		})
	}
}

func TestFirstAnswerWinsWhenAModelRepeatsAnID(t *testing.T) {
	leaf := &domain.Node{ID: "leaf", Kind: domain.KindMethod, Summary: domain.Summary{Origin: domain.OriginNone}}
	compress(t, func(ports.ModelRequest) (string, error) {
		return `{"summaries":[{"id":"leaf","summary":"first","confidence":"high"},{"id":"leaf","summary":"second","confidence":"high"}]}`, nil
	}, leaf)
	if leaf.Summary.Text != "first" {
		t.Fatalf("%q", leaf.Summary.Text)
	}
}

func TestLowConfidenceAndKnownLimitationsAreKept(t *testing.T) {
	limited := &domain.Node{ID: "a", Kind: domain.KindFile, Summary: domain.Summary{Origin: domain.OriginNone, Uncertainty: "Could not be parsed."}}
	unsure := &domain.Node{ID: "b", Kind: domain.KindMethod, Summary: domain.Summary{Origin: domain.OriginNone}}
	compress(t, func(ports.ModelRequest) (string, error) {
		return `{"summaries":[{"id":"a","summary":"x","confidence":"high"},{"id":"b","summary":"y","confidence":"low","uncertainty":"unclear"}]}`, nil
	}, limited, unsure)
	if limited.Summary.Confidence != domain.ConfidenceLow || !strings.Contains(limited.Summary.Uncertainty, "Could not be parsed") {
		t.Errorf("a known limitation must survive a confident answer: %+v", limited.Summary)
	}
	if unsure.Summary.Confidence != domain.ConfidenceLow || unsure.Summary.Uncertainty != "unclear" {
		t.Errorf("%+v", unsure.Summary)
	}
}

func TestAliasEqualToTheSourceNameIsDropped(t *testing.T) {
	n := &domain.Node{ID: "x", Name: "Same"}
	applySummary(preparedItem{node: n}, modelSummary{ID: "x", Summary: "s", SemanticName: "Same", Confidence: "high"})
	if n.Summary.SemanticName != "" {
		t.Fatal(n.Summary.SemanticName)
	}
}

func TestCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	m := &fakeModel{answer: func(ports.ModelRequest) (string, error) {
		calls++
		cancel()
		return "", errors.New("canceled")
	}}
	r := &modelRunner{model: m}
	if _, err := r.complete(ctx, "", nil, 1); err == nil || calls != 1 {
		t.Fatalf("calls %d err %v", calls, err)
	}
}

func TestATransientFailureRecoversOnRetry(t *testing.T) {
	calls := 0
	r := &modelRunner{model: &fakeModel{answer: func(ports.ModelRequest) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("transient")
		}
		return `{}`, nil
	}}}
	if _, err := r.complete(context.Background(), "", nil, 1); err != nil || calls != 2 || r.stats.FailedCalls != 1 {
		t.Fatalf("calls %d, err %v, %+v", calls, err, r.stats)
	}
}

func TestOverlargeBatchesSplit(t *testing.T) {
	var nodes []*domain.Node
	for i := 0; i < maxBatchItems+1; i++ {
		nodes = append(nodes, &domain.Node{ID: fmt.Sprint(i), Kind: domain.KindMethod, Summary: domain.Summary{Origin: domain.OriginNone}})
	}
	m, _ := compress(t, summarizeAll, nodes...)
	if len(m.requests) != 2 {
		t.Fatalf("calls: %d", len(m.requests))
	}
	big := []*domain.Node{
		{ID: "a", Kind: domain.KindMethod, Source: strings.Repeat("x", maxFieldChars), Summary: domain.Summary{Origin: domain.OriginNone}},
		{ID: "b", Kind: domain.KindMethod, Source: strings.Repeat("x", maxFieldChars), Summary: domain.Summary{Origin: domain.OriginNone}},
		{ID: "c", Kind: domain.KindMethod, Source: strings.Repeat("x", maxFieldChars), Summary: domain.Summary{Origin: domain.OriginNone}},
		{ID: "d", Kind: domain.KindMethod, Source: strings.Repeat("x", maxFieldChars), Summary: domain.Summary{Origin: domain.OriginNone}},
	}
	m, _ = compress(t, summarizeAll, big...)
	if len(m.requests) != 2 {
		t.Fatalf("a batch must stay near %d chars: %d calls", maxBatchChars, len(m.requests))
	}
}

// --- the whole pipeline over fake ports

type fakeRepo struct{ changes []domain.FileChange }

func (r *fakeRepo) ResolveCommit(_ context.Context, rev string) (string, error) { return rev, nil }
func (r *fakeRepo) ChangedFiles(context.Context, string, string) ([]domain.FileChange, error) {
	return r.changes, nil
}
func (r *fakeRepo) ReadFile(context.Context, string, string) ([]byte, error) { return nil, nil }
func (r *fakeRepo) ListFiles(context.Context, string, string) ([]string, error) {
	return nil, nil
}

type fakePrompts struct{}

func (fakePrompts) Variant() string { return "fake" }
func (fakePrompts) Load(name string) (string, domain.PromptInfo, error) {
	return name, domain.PromptInfo{Name: name, Hash: name}, nil
}
func (fakePrompts) LoadReadingLevel(level domain.ReadingLevel) (string, domain.PromptInfo, error) {
	return "LEVEL " + string(level), domain.PromptInfo{Name: "reading-levels/" + string(level), Hash: string(level)}, nil
}

func textChange(path string) domain.FileChange {
	return domain.FileChange{Status: domain.FileModified, OldPath: path, NewPath: path, Hunks: []domain.Hunk{{Lines: []domain.DiffLine{{Kind: domain.LineAdded, NewLine: 1, Text: "x"}}}}}
}

func pipeline(t *testing.T, m ports.LanguageModel, req CompressRequest, changes ...domain.FileChange) (*domain.CompressedDiff, error) {
	t.Helper()
	repo := &fakeRepo{changes: changes}
	c := NewDiffCompressor(repo, NewStructuralDiffer(repo), m, fakePrompts{}, 1)
	req.Base, req.Head = "b", "h"
	if req.ReadingLevel == "" {
		req.ReadingLevel = domain.ReadingSixthGrade
	}
	return c.Compress(context.Background(), req)
}

func TestNoExplanationsErrorOnlyWhenEveryCallFailed(t *testing.T) {
	boom := errors.New("boom")
	all := &fakeModel{answer: func(ports.ModelRequest) (string, error) { return "", boom }}
	result, err := pipeline(t, all, CompressRequest{}, textChange("a.txt"))
	if !errors.Is(err, ErrNoExplanations) || result == nil || len(result.Nodes) != 1 {
		t.Fatalf("total failure: %v %+v", err, result)
	}

	// Summaries work and outcomes fail: a partial result is not a total failure.
	mixed := &fakeModel{answer: func(req ports.ModelRequest) (string, error) {
		if strings.HasPrefix(req.System, promptOutcomes) {
			return "", boom
		}
		return summarizeAll(req)
	}}
	result, err = pipeline(t, mixed, CompressRequest{}, textChange("a.txt"))
	if err != nil {
		t.Fatalf("mixed: %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "outcome identification failed") || len(result.Unassigned) != 1 {
		t.Fatalf("an outcome failure must be visible and leave the change unassigned: %+v", result)
	}

	// An empty diff makes no calls, which is success.
	none := &fakeModel{answer: func(ports.ModelRequest) (string, error) { return "", boom }}
	if _, err := pipeline(t, none, CompressRequest{}); err != nil || len(none.requests) != 0 {
		t.Fatalf("empty diff: %v, %d calls", err, len(none.requests))
	}
}

func TestCachedAnswersCountAsExplanations(t *testing.T) {
	m := cachedThenFailing{}
	if _, err := pipeline(t, &m, CompressRequest{}, textChange("a.txt")); err != nil {
		t.Fatalf("one cached answer means the run has explanations: %v", err)
	}
}

type cachedThenFailing struct{ calls int }

func (m *cachedThenFailing) Describe() string { return "x" }
func (m *cachedThenFailing) Complete(_ context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	m.calls++
	if m.calls == 1 {
		text, _ := summarizeAll(req)
		return ports.ModelResponse{JSON: []byte(text), CacheHit: true}, nil
	}
	return ports.ModelResponse{}, errors.New("boom")
}

func TestMalformedOutcomesAreDroppedAndReported(t *testing.T) {
	m := &fakeModel{answer: func(req ports.ModelRequest) (string, error) {
		if strings.HasPrefix(req.System, promptOutcomes) {
			return `{"overview":" Sum. ","outcomes":[
				{"op":"EXPLODE","title":"bad op","node_ids":["file:a.txt"]},
				{"op":"ADD","title":"  ","node_ids":["file:a.txt"]},
				{"op":"ADD","title":"Good","description":"d","node_ids":["file:a.txt"],"confidence":"low","uncertainty":"u"}]}`, nil
		}
		return summarizeAll(req)
	}}
	result, err := pipeline(t, m, CompressRequest{}, textChange("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Title != "Good" || result.Outcomes[0].Confidence != domain.ConfidenceLow || result.Overview != "Sum." {
		t.Fatalf("%+v", result)
	}
	if strings.Count(strings.Join(result.Warnings, "\n"), "malformed outcome") != 2 {
		t.Fatalf("%v", result.Warnings)
	}
	if len(result.Nodes[0].Outcomes) != 1 {
		t.Fatalf("%+v", result.Nodes[0])
	}

	unusable := &fakeModel{answer: func(req ports.ModelRequest) (string, error) {
		if strings.HasPrefix(req.System, promptOutcomes) {
			return `{broken`, nil
		}
		return summarizeAll(req)
	}}
	result, err = pipeline(t, unusable, CompressRequest{}, textChange("a.txt"))
	if err != nil || !strings.Contains(strings.Join(result.Warnings, "\n"), "unusable") {
		t.Fatalf("%v %v", err, result.Warnings)
	}
}

func TestOutcomeCallIsReservedWithinTheLimit(t *testing.T) {
	m := &fakeModel{answer: func(req ports.ModelRequest) (string, error) {
		if strings.HasPrefix(req.System, promptOutcomes) {
			return `{"overview":"o","outcomes":[]}`, nil
		}
		return summarizeAll(req)
	}}
	var changes []domain.FileChange
	for i := 0; i < maxBatchItems*3; i++ {
		changes = append(changes, textChange(fmt.Sprintf("f%d.txt", i)))
	}
	result, err := pipeline(t, m, CompressRequest{MaxModelCalls: 2}, changes...)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.requests) != 2 || result.Overview != "o" {
		t.Fatalf("calls %d, overview %q", len(m.requests), result.Overview)
	}
	if result.Metrics.MissingSummaries == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "limit") {
		t.Fatalf("%+v %v", result.Metrics, result.Warnings)
	}
}

func TestMissingPortsAreAnErrorNotAPanic(t *testing.T) {
	repo := &fakeRepo{}
	c := NewDiffCompressor(repo, NewStructuralDiffer(repo), nil, nil, 1)
	if _, err := c.Compress(context.Background(), CompressRequest{}); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := c.Compress(context.Background(), CompressRequest{StructureOnly: true}); err != nil {
		t.Fatal(err)
	}
}
