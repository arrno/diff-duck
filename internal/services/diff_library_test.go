package services

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

type memoryStore struct {
	mu    sync.Mutex
	diffs map[string]domain.SavedDiff
}

func newMemoryStore() *memoryStore { return &memoryStore{diffs: map[string]domain.SavedDiff{}} }

func (s *memoryStore) Create(_ context.Context, d *domain.SavedDiff) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diffs[d.ID] = *d
	return nil
}
func (s *memoryStore) Update(_ context.Context, d *domain.SavedDiff) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.diffs[d.ID]; ok {
		s.diffs[d.ID] = *d
	}
	return nil
}
func (s *memoryStore) Get(_ context.Context, id string) (*domain.SavedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.diffs[id]
	if !ok {
		return nil, ports.ErrDiffNotFound
	}
	return &d, nil
}
func (s *memoryStore) List(context.Context) ([]domain.SavedDiff, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []domain.SavedDiff{}
	for _, d := range s.diffs {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *memoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.diffs[id]; !ok {
		return ports.ErrDiffNotFound
	}
	delete(s.diffs, id)
	return nil
}
func (s *memoryStore) FailRunning(_ context.Context, message string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, d := range s.diffs {
		if d.Status == domain.SavedDiffRunning {
			d.Status, d.Error, d.FinishedAt = domain.SavedDiffFailed, message, &at
			s.diffs[id] = d
		}
	}
	return nil
}

// repoSource fails every open, and with block set waits for the context first.
type repoSource struct {
	block   bool
	opening chan struct{}
}

var errNoRepo = errors.New("not a repository")

func (r *repoSource) Open(ctx context.Context, _ string) (ports.OpenRepository, error) {
	if r.block {
		r.opening <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return nil, errNoRepo
}

func newLibrary(store ports.DiffStore, source ports.RepositorySource) *DiffLibrary {
	return NewDiffLibrary(LibraryDeps{Store: store, Repositories: source, Models: &recordingOpener{model: &fakeModel{answer: answerAll}}, Prompts: fakePrompts{}, Concurrency: 1})
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func status(store *memoryStore, id string) domain.SavedDiffStatus {
	d, err := store.Get(context.Background(), id)
	if err != nil {
		return ""
	}
	return d.Status
}

func TestAFailedRunStaysVisibleWithItsReason(t *testing.T) {
	store := newMemoryStore()
	l := newLibrary(store, &repoSource{})
	defer l.Shutdown()
	d, err := l.StartDiff(context.Background(), "/repo", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != domain.SavedDiffRunning {
		t.Errorf("a new diff should be running, got %s", d.Status)
	}
	waitFor(t, func() bool { return status(store, d.ID) == domain.SavedDiffFailed })
	got, _ := store.Get(context.Background(), d.ID)
	if got.Error != errNoRepo.Error() || got.FinishedAt == nil || got.Stage != "" {
		t.Errorf("failed diff: %+v", got)
	}
}

func TestDeletingARunningDiffStopsItAndDoesNotBringItBack(t *testing.T) {
	store := newMemoryStore()
	source := &repoSource{block: true, opening: make(chan struct{}, 1)}
	l := newLibrary(store, source)
	d, err := l.StartDiff(context.Background(), "/repo", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	<-source.opening
	if err := l.Delete(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	// Shutdown waits for the run, so the run has finished by the time this returns.
	done := make(chan struct{})
	go func() { l.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deleting did not stop the run")
	}
	if got := status(store, d.ID); got != "" {
		t.Errorf("the deleted diff came back as %s", got)
	}
}

func TestShutdownRecordsThatARunStopped(t *testing.T) {
	store := newMemoryStore()
	source := &repoSource{block: true, opening: make(chan struct{}, 1)}
	l := newLibrary(store, source)
	d, err := l.StartDiff(context.Background(), "/repo", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	<-source.opening
	l.Shutdown()
	got, _ := store.Get(context.Background(), d.ID)
	if got.Status != domain.SavedDiffFailed || !strings.Contains(got.Error, "stopped") {
		t.Errorf("stopped diff: %+v", got)
	}
}

func TestRunningDiffsFromAnEarlierProcessAreMarkedStopped(t *testing.T) {
	store := newMemoryStore()
	_ = store.Create(context.Background(), &domain.SavedDiff{ID: "old", Status: domain.SavedDiffRunning})
	l := newLibrary(store, &repoSource{})
	defer l.Shutdown()
	if err := l.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := status(store, "old"); got != domain.SavedDiffFailed {
		t.Errorf("status %s", got)
	}
}

func TestTheQueueIsBounded(t *testing.T) {
	store := newMemoryStore()
	source := &repoSource{block: true, opening: make(chan struct{}, maxQueuedDiffs)}
	l := newLibrary(store, source)
	defer l.Shutdown()
	for range maxQueuedDiffs {
		if _, err := l.StartDiff(context.Background(), "/repo", "a", "b"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := l.StartDiff(context.Background(), "/repo", "a", "b"); !errors.Is(err, ErrTooManyDiffs) {
		t.Errorf("got %v", err)
	}
	diffs, _ := store.List(context.Background())
	if len(diffs) != maxQueuedDiffs {
		t.Errorf("a refused diff must not be saved: %d saved", len(diffs))
	}
}

func TestOnlyOneDiffRunsAtATime(t *testing.T) {
	store := newMemoryStore()
	source := &repoSource{block: true, opening: make(chan struct{}, maxQueuedDiffs)}
	l := newLibrary(store, source)
	defer l.Shutdown()
	for range 3 {
		if _, err := l.StartDiff(context.Background(), "/repo", "a", "b"); err != nil {
			t.Fatal(err)
		}
	}
	<-source.opening
	select {
	case <-source.opening:
		t.Error("a second diff started while the first was running")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRequestsThatCanNeverWorkAreRefused(t *testing.T) {
	store := newMemoryStore()
	l := newLibrary(store, &repoSource{})
	defer l.Shutdown()
	for _, tc := range [][3]string{{"", "a", "b"}, {"/r", " ", "b"}, {"/r", "a", ""}, {"--upload-pack=x", "a", "b"}, {"/r", "--output=x", "b"}, {"/r", "a", "-b"}, {strings.Repeat("x", 5000), "a", "b"}} {
		if _, err := l.StartDiff(context.Background(), tc[0], tc[1], tc[2]); !errors.Is(err, ErrInvalidDiffRequest) {
			t.Errorf("%q: got %v", tc, err)
		}
	}
	if _, err := l.Commits(context.Background(), "--upload-pack=x", 10); !errors.Is(err, ErrInvalidDiffRequest) {
		t.Errorf("commits: got %v", err)
	}
	if diffs, _ := store.List(context.Background()); len(diffs) != 0 {
		t.Errorf("refused requests were saved: %+v", diffs)
	}
}
