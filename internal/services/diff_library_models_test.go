package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

// openRepo is a fake repository that, when gate is set, waits for it before opening.
type openRepo struct {
	fakeRepo
	gate   chan struct{}
	opened chan struct{}
}

func (r *openRepo) ListCommits(context.Context, int) ([]domain.Commit, error) { return nil, nil }
func (r *openRepo) Close()                                                    {}

type gatedSource struct{ repo *openRepo }

func (s gatedSource) Open(ctx context.Context, _ string) (ports.OpenRepository, error) {
	if s.repo.opened != nil {
		s.repo.opened <- struct{}{}
	}
	if s.repo.gate != nil {
		select {
		case <-s.repo.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.repo, nil
}

// recordingOpener opens a fake model and records the settings of every open.
type recordingOpener struct {
	mu       sync.Mutex
	settings []domain.ModelSettings
	closed   int
	err      error
	model    *fakeModel
	// pinned, when set, is the provider used whatever the settings say.
	pinned *domain.ModelProvider
}

func (o *recordingOpener) Open(_ context.Context, s domain.ModelSettings) (ports.OpenedModel, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.settings = append(o.settings, s)
	if o.err != nil {
		return ports.OpenedModel{}, o.err
	}
	release := func() {
		o.mu.Lock()
		o.closed++
		o.mu.Unlock()
	}
	provider := s.Provider
	if o.pinned != nil {
		provider = *o.pinned
	}
	return ports.OpenedModel{Model: o.model, Provider: provider, Close: release}, nil
}

func (o *recordingOpener) opened() ([]domain.ModelSettings, int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]domain.ModelSettings(nil), o.settings...), o.closed
}

func answerAll(req ports.ModelRequest) (string, error) {
	if strings.HasPrefix(req.System, promptOutcomes) {
		return `{"overview":"o","outcomes":[]}`, nil
	}
	return summarizeAll(req)
}

// noAnalyzer understands no file, so every change is a plain file node.
type noAnalyzer struct{}

func (noAnalyzer) Supports(string) bool { return false }
func (noAnalyzer) Analyze(context.Context, string, []byte) (domain.FileAnalysis, error) {
	return domain.FileAnalysis{}, errors.New("unsupported")
}

func modelLibrary(store ports.DiffStore, repo *openRepo, opener *recordingOpener, settings *settingsStore) *DiffLibrary {
	keeper := NewSettingsKeeper(SettingsDeps{Store: settings, Secrets: newSecretStore(), Availability: allAvailable()})
	return NewDiffLibrary(LibraryDeps{Store: store, Repositories: gatedSource{repo}, Analyzers: []ports.SourceAnalyzer{noAnalyzer{}}, Models: opener, Settings: keeper, Prompts: fakePrompts{}, Concurrency: 1})
}

func finished(t *testing.T, store *memoryStore, id string) *domain.SavedDiff {
	t.Helper()
	waitFor(t, func() bool { s := status(store, id); return s != "" && s != domain.SavedDiffRunning })
	d, _ := store.Get(context.Background(), id)
	return d
}

func TestEachRunUsesTheSavedSettingsAndRecordsThem(t *testing.T) {
	store := newMemoryStore()
	settings := &settingsStore{saved: &domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingCollege}}
	model := &fakeModel{answer: answerAll}
	opener := &recordingOpener{model: model}
	l := modelLibrary(store, &openRepo{fakeRepo: fakeRepo{changes: []domain.FileChange{textChange("a.txt")}}}, opener, settings)
	defer l.Shutdown()

	first, err := l.StartDiff(context.Background(), "/repo", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	d := finished(t, store, first.ID)
	if d.Status != domain.SavedDiffDone {
		t.Fatalf("%+v", d)
	}
	if g := d.Result.Generation; g.Provider != domain.ProviderCodex || g.ReadingLevel != domain.ReadingCollege {
		t.Fatalf("generation must record the run's settings: %+v", g)
	}
	for _, req := range model.requests {
		if !strings.HasSuffix(req.System, "LEVEL "+string(domain.ReadingCollege)) {
			t.Fatalf("a prompt is missing the run's reading level: %q", req.System)
		}
	}

	settings.saved = &domain.ModelSettings{Provider: domain.ProviderClaudeCode, ReadingLevel: domain.ReadingPhD}
	second, _ := l.StartDiff(context.Background(), "/repo", "a", "b")
	d = finished(t, store, second.ID)
	if g := d.Result.Generation; g.Provider != domain.ProviderClaudeCode || g.ReadingLevel != domain.ReadingPhD {
		t.Fatalf("the next run must use the newly saved settings: %+v", g)
	}
	got, _ := opener.opened()
	if len(got) != 2 || got[0].Provider != domain.ProviderCodex || got[1].ReadingLevel != domain.ReadingPhD {
		t.Fatalf("one open per run, with that run's settings: %+v", got)
	}
	waitFor(t, func() bool { _, closed := opener.opened(); return closed == 2 })
}

func TestTheRecordedProviderIsTheOneTheModelCameFrom(t *testing.T) {
	store := newMemoryStore()
	settings := &settingsStore{saved: &domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingCollege}}
	// As when the app is pinned: the opener uses another provider than the one saved.
	used := domain.ProviderAnthropicAPI
	opener := &recordingOpener{model: &fakeModel{answer: answerAll}, pinned: &used}
	l := modelLibrary(store, &openRepo{fakeRepo: fakeRepo{changes: []domain.FileChange{textChange("a.txt")}}}, opener, settings)
	defer l.Shutdown()
	started, _ := l.StartDiff(context.Background(), "/repo", "a", "b")
	d := finished(t, store, started.ID)
	if g := d.Result.Generation; g.Provider != used || g.ReadingLevel != domain.ReadingCollege {
		t.Fatalf("the provider must be the one used, and the level the one saved: %+v", g)
	}
}

func TestARequestedDiffKeepsTheSettingsItWasRequestedWith(t *testing.T) {
	store := newMemoryStore()
	settings := &settingsStore{saved: &domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingSecondGrade}}
	opener := &recordingOpener{model: &fakeModel{answer: answerAll}}
	repo := &openRepo{gate: make(chan struct{}), opened: make(chan struct{}, 2)}
	l := modelLibrary(store, repo, opener, settings)
	defer l.Shutdown()

	running, _ := l.StartDiff(context.Background(), "/repo", "a", "b")
	<-repo.opened
	queued, _ := l.StartDiff(context.Background(), "/repo", "a", "b")
	// Saved while the second diff waits its turn, which must not pick it up.
	settings.saved = &domain.ModelSettings{Provider: domain.ProviderOpenAIAPI, ReadingLevel: domain.ReadingPhD}
	close(repo.gate)
	finished(t, store, running.ID)
	d := finished(t, store, queued.ID)
	if g := d.Result.Generation; g.Provider != domain.ProviderCodex || g.ReadingLevel != domain.ReadingSecondGrade {
		t.Fatalf("the queued diff took later settings: %+v", g)
	}
}

func TestAModelThatCannotOpenFailsTheDiffWithTheReason(t *testing.T) {
	store := newMemoryStore()
	settings := &settingsStore{saved: &domain.ModelSettings{Provider: domain.ProviderAnthropicAPI, ReadingLevel: domain.ReadingSixthGrade}}
	opener := &recordingOpener{err: fmt.Errorf("%w for Anthropic API: save one in Model settings", ports.ErrKeyNotSaved)}
	repo := &openRepo{opened: make(chan struct{}, 1)}
	l := modelLibrary(store, repo, opener, settings)
	defer l.Shutdown()
	started, _ := l.StartDiff(context.Background(), "/repo", "a", "b")
	d := finished(t, store, started.ID)
	if d.Status != domain.SavedDiffFailed || !strings.Contains(d.Error, "no API key saved for Anthropic API") {
		t.Fatalf("the diff must fail with the reason: %+v", d)
	}
	if diffs, _ := store.List(context.Background()); len(diffs) != 1 {
		t.Fatalf("the failed diff must stay listed: %d", len(diffs))
	}
	if len(repo.opened) != 0 {
		t.Fatal("nothing more should be done once the model cannot open")
	}
}

func TestUnreadableSettingsRefuseTheDiff(t *testing.T) {
	store := newMemoryStore()
	settings := &settingsStore{loadErr: errors.New("disk on fire")}
	l := modelLibrary(store, &openRepo{}, &recordingOpener{}, settings)
	defer l.Shutdown()
	if _, err := l.StartDiff(context.Background(), "/repo", "a", "b"); err == nil {
		t.Fatal("expected the settings error")
	}
	if diffs, _ := store.List(context.Background()); len(diffs) != 0 {
		t.Fatalf("a refused diff must not be saved: %+v", diffs)
	}
}

// --- reading level in prompts

func TestTheReadingLevelReachesEveryPromptAndChangesItsIdentity(t *testing.T) {
	repo := &fakeRepo{}
	c := NewDiffCompressor(repo, NewStructuralDiffer(repo), &fakeModel{}, fakePrompts{}, 1)
	plain, plainInfos, err := c.loadPrompts(domain.ReadingSecondGrade)
	if err != nil {
		t.Fatal(err)
	}
	expert, _, err := c.loadPrompts(domain.ReadingPhD)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{promptLeaf, promptParent, promptOutcomes} {
		if plain[name] != name+"\n\nLEVEL 2nd-grade" {
			t.Errorf("%s: %q", name, plain[name])
		}
		// The system prompt is part of every cached answer's key, so it must differ per level.
		if plain[name] == expert[name] {
			t.Errorf("%s is the same at every level", name)
		}
	}
	if last := plainInfos[len(plainInfos)-1]; len(plainInfos) != 4 || last.Name != "reading-levels/2nd-grade" {
		t.Fatalf("the reading level's prompt must be recorded: %+v", plainInfos)
	}
}

func TestAModelRunNeedsAValidReadingLevel(t *testing.T) {
	for _, level := range []domain.ReadingLevel{"../leaf", "PhD"} {
		m := &fakeModel{answer: answerAll}
		if _, err := pipeline(t, m, CompressRequest{ReadingLevel: level}, textChange("a.txt")); !errors.Is(err, domain.ErrUnknownReadingLevel) {
			t.Errorf("%q: got %v", level, err)
		}
		if len(m.requests) != 0 {
			t.Errorf("%q: the run spent before failing", level)
		}
	}
	// pipeline fills in an empty level, so check that case directly.
	repo := &fakeRepo{changes: []domain.FileChange{textChange("a.txt")}}
	c := NewDiffCompressor(repo, NewStructuralDiffer(repo), &fakeModel{answer: answerAll}, fakePrompts{}, 1)
	if _, err := c.Compress(context.Background(), CompressRequest{}); !errors.Is(err, domain.ErrUnknownReadingLevel) {
		t.Errorf("empty level: got %v", err)
	}
}

func TestProviderAndReadingLevelSurviveTheModelPath(t *testing.T) {
	result, err := pipeline(t, &fakeModel{answer: answerAll}, CompressRequest{Provider: domain.ProviderOpenAIAPI, ReadingLevel: domain.ReadingHighSchool}, textChange("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	g := result.Generation
	if g.Provider != domain.ProviderOpenAIAPI || g.ReadingLevel != domain.ReadingHighSchool || g.ModelCalls == 0 {
		t.Fatalf("%+v", g)
	}

	structural, err := pipeline(t, nil, CompressRequest{StructureOnly: true, Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingPhD}, textChange("a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if g := structural.Generation; g.Provider != "" || g.ReadingLevel != "" {
		t.Fatalf("a structure-only run used no model or level: %+v", g)
	}
}
