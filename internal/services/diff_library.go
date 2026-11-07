package services

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

var (
	// ErrInvalidDiffRequest means the request can never succeed as written.
	ErrInvalidDiffRequest = errors.New("invalid diff request")
	// ErrTooManyDiffs means too many diffs are already running or waiting.
	ErrTooManyDiffs = errors.New("too many diffs are already running")
)

const (
	maxCommitsListed = 200
	// One at a time, because each run can make many model calls.
	concurrentDiffs = 1
	maxQueuedDiffs  = 8
	stoppedMessage  = "DiffDuck stopped before this diff finished."
)

// LibraryDeps are what a DiffLibrary needs from the outside world.
type LibraryDeps struct {
	Store        ports.DiffStore
	Repositories ports.RepositorySource
	Analyzer     ports.SourceAnalyzer
	// Models opens the model each run uses, from the settings read when the run was requested.
	Models ports.ModelOpener
	// Settings supplies those settings. When nil, every run uses the default settings.
	Settings      *SettingsKeeper
	Prompts       ports.PromptSource
	Concurrency   int
	MaxModelCalls int
}

// DiffLibrary creates diffs in the background and keeps them. Every run is saved as soon as it
// is requested, so its outcome stays visible even if the process stops partway.
type DiffLibrary struct {
	deps   LibraryDeps
	now    func() time.Time
	ctx    context.Context
	stop   context.CancelFunc
	slots  chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

// NewDiffLibrary returns a library. Call Recover before serving and Shutdown when finished.
func NewDiffLibrary(deps LibraryDeps) *DiffLibrary {
	ctx, stop := context.WithCancel(context.Background())
	return &DiffLibrary{
		deps:   deps,
		now:    time.Now,
		ctx:    ctx,
		stop:   stop,
		slots:  make(chan struct{}, concurrentDiffs),
		active: map[string]context.CancelFunc{},
	}
}

// Recover marks diffs left running by a previous process as failed, since nothing is working
// on them anymore.
func (l *DiffLibrary) Recover(ctx context.Context) error {
	return l.deps.Store.FailRunning(ctx, stoppedMessage, l.now())
}

// Shutdown stops running diffs and waits for them to record that they stopped.
func (l *DiffLibrary) Shutdown() {
	l.stop()
	l.wg.Wait()
}

// Commits lists a repository's recent commits, newest first.
func (l *DiffLibrary) Commits(ctx context.Context, target string, limit int) ([]domain.Commit, error) {
	if err := validateField("repository", target); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxCommitsListed {
		limit = maxCommitsListed
	}
	repo, err := l.deps.Repositories.Open(ctx, target)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	return repo.ListCommits(ctx, limit)
}

// StartDiff saves a new diff as running and works on it in the background.
func (l *DiffLibrary) StartDiff(ctx context.Context, target, base, head string) (*domain.SavedDiff, error) {
	for _, field := range []struct{ name, value string }{{"repository", target}, {"from commit", base}, {"to commit", head}} {
		if err := validateField(field.name, field.value); err != nil {
			return nil, err
		}
	}
	// Read once, so a diff keeps the settings it was requested with even while it waits.
	settings := domain.DefaultModelSettings()
	if l.deps.Settings != nil {
		var err error
		if settings, err = l.deps.Settings.Settings(ctx); err != nil {
			return nil, err
		}
	}
	diff := &domain.SavedDiff{
		ID:         rand.Text(),
		Repository: target,
		Base:       base,
		Head:       head,
		Status:     domain.SavedDiffRunning,
		Stage:      "Waiting to start",
		CreatedAt:  l.now(),
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.active) >= maxQueuedDiffs {
		return nil, ErrTooManyDiffs
	}
	if err := l.deps.Store.Create(ctx, diff); err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(l.ctx)
	l.active[diff.ID] = cancel
	l.wg.Add(1)
	work := *diff
	go func() {
		defer l.wg.Done()
		defer l.finish(work.ID, cancel)
		l.run(runCtx, &work, settings)
	}()
	return diff, nil
}

// List returns saved diffs, newest first.
func (l *DiffLibrary) List(ctx context.Context) ([]domain.SavedDiff, error) {
	return l.deps.Store.List(ctx)
}

// Get returns one saved diff with its result.
func (l *DiffLibrary) Get(ctx context.Context, id string) (*domain.SavedDiff, error) {
	return l.deps.Store.Get(ctx, id)
}

// Delete removes a saved diff, stopping it first if it is still running.
func (l *DiffLibrary) Delete(ctx context.Context, id string) error {
	l.mu.Lock()
	cancel := l.active[id]
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return l.deps.Store.Delete(ctx, id)
}

func (l *DiffLibrary) finish(id string, cancel context.CancelFunc) {
	cancel()
	l.mu.Lock()
	delete(l.active, id)
	l.mu.Unlock()
}

func (l *DiffLibrary) run(ctx context.Context, diff *domain.SavedDiff, settings domain.ModelSettings) {
	result, err := l.compress(ctx, diff, settings)
	finished := l.now()
	diff.FinishedAt, diff.Stage = &finished, ""
	switch {
	case result != nil:
		diff.Status = domain.SavedDiffDone
		diff.BaseSHA, diff.HeadSHA = result.BaseSHA, result.HeadSHA
		diff.Overview, diff.Files, diff.Added, diff.Removed = result.Overview, result.Files, result.Added, result.Removed
		diff.Result = result
	case ctx.Err() != nil:
		diff.Status = domain.SavedDiffFailed
		err = errors.New(stoppedMessage)
	default:
		diff.Status = domain.SavedDiffFailed
	}
	if err != nil {
		diff.Error = err.Error()
	}
	// Recording the end must still work when the run itself was cancelled.
	save, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// If this write fails, the row stays running until the next start marks it as stopped.
	_ = l.deps.Store.Update(save, diff)
}

func (l *DiffLibrary) compress(ctx context.Context, diff *domain.SavedDiff, settings domain.ModelSettings) (*domain.CompressedDiff, error) {
	select {
	case l.slots <- struct{}{}:
		defer func() { <-l.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	progress := func(stage string) {
		diff.Stage = stage
		// Progress is a courtesy, so a failed write must not stop the work.
		_ = l.deps.Store.Update(ctx, diff)
	}
	progress("Preparing the model")
	if l.deps.Models == nil {
		return nil, errors.New("no model is configured")
	}
	opened, err := l.deps.Models.Open(ctx, settings)
	if err != nil {
		return nil, err
	}
	if opened.Close != nil {
		defer opened.Close()
	}
	progress("Opening the repository")
	repo, err := l.deps.Repositories.Open(ctx, diff.Repository)
	if err != nil {
		return nil, err
	}
	defer repo.Close()
	differ := NewStructuralDiffer(repo, l.deps.Analyzer)
	compressor := NewDiffCompressor(repo, differ, opened.Model, l.deps.Prompts, l.deps.Concurrency)
	return compressor.Compress(ctx, CompressRequest{
		Repository:    diff.Repository,
		Base:          diff.Base,
		Head:          diff.Head,
		MaxModelCalls: l.deps.MaxModelCalls,
		Progress:      progress,
		ReadingLevel:  settings.ReadingLevel,
		Provider:      opened.Provider,
	})
}

// validateField rejects what no repository or revision can be. A leading dash would read as a
// command-line option, so it is refused here as well as at the Git boundary.
func validateField(name, value string) error {
	switch {
	case strings.TrimSpace(value) == "":
		return fmt.Errorf("%w: the %s is required", ErrInvalidDiffRequest, name)
	case strings.HasPrefix(value, "-"):
		return fmt.Errorf("%w: the %s cannot start with a dash", ErrInvalidDiffRequest, name)
	case len(value) > 4096:
		return fmt.Errorf("%w: the %s is too long", ErrInvalidDiffRequest, name)
	}
	return nil
}
