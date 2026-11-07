package sqlitestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func saved(id string, created time.Time) *domain.SavedDiff {
	return &domain.SavedDiff{ID: id, Repository: "/repo", Base: "a", Head: "b", Status: domain.SavedDiffRunning, Stage: "Starting", CreatedAt: created}
}

func TestSavedDiffsSurviveReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nested", "diffs.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	d := saved("one", time.UnixMilli(1000))
	if err := first.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	finished := time.UnixMilli(2000)
	d.Status, d.Stage, d.FinishedAt, d.Overview, d.Files, d.Added, d.Removed = domain.SavedDiffDone, "", &finished, "Users can log in", 3, 4, 5
	d.BaseSHA, d.HeadSHA = "aaa", "bbb"
	d.Result = &domain.CompressedDiff{Overview: "Users can log in", Outcomes: []domain.Outcome{{ID: "o1", Title: "Login"}}, Nodes: []*domain.Node{{ID: "n1", Name: "users"}}}
	if err := first.Update(ctx, d); err != nil {
		t.Fatal(err)
	}
	first.Close()

	got, err := open(t, path).Get(ctx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.SavedDiffDone || got.Overview != "Users can log in" || got.Files != 3 || got.Added != 4 || got.Removed != 5 || got.HeadSHA != "bbb" {
		t.Errorf("fields were not kept: %+v", got)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) || !got.CreatedAt.Equal(d.CreatedAt) {
		t.Errorf("times were not kept: %v %v", got.CreatedAt, got.FinishedAt)
	}
	if got.Result == nil || got.Result.Outcomes[0].Title != "Login" || got.Result.Nodes[0].ID != "n1" {
		t.Errorf("result was not kept: %+v", got.Result)
	}
}

func TestSavedDiffsAreOnlyReadableByTheirOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "diffs.db")
	open(t, path)
	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s has mode %o, want %o", p, got, want)
		}
	}
}

func TestAnExistingLooseDatabaseIsTightened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "diffs.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	open(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode is %o, want 600", got)
	}
}

func TestListIsNewestFirstAndOmitsResults(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "diffs.db"))
	if diffs, err := s.List(ctx); err != nil || diffs == nil || len(diffs) != 0 {
		t.Fatalf("an empty list must be empty, not null: %v %v", diffs, err)
	}
	older, newer := saved("older", time.UnixMilli(1000)), saved("newer", time.UnixMilli(2000))
	older.Result = &domain.CompressedDiff{Overview: "heavy"}
	for _, d := range []*domain.SavedDiff{older, newer} {
		if err := s.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	diffs, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 || diffs[0].ID != "newer" || diffs[1].ID != "older" {
		t.Fatalf("wrong order: %+v", diffs)
	}
	if diffs[1].Result != nil {
		t.Error("a list must not carry results")
	}
}

func TestMissingAndDeletedDiffs(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "diffs.db"))
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ports.ErrDiffNotFound) {
		t.Errorf("get: %v", err)
	}
	if err := s.Delete(ctx, "nope"); !errors.Is(err, ports.ErrDiffNotFound) {
		t.Errorf("delete: %v", err)
	}
	d := saved("one", time.UnixMilli(1000))
	if err := s.Create(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	// A background run finishing after its diff was deleted must not bring it back or fail.
	if err := s.Update(ctx, d); err != nil {
		t.Errorf("update after delete: %v", err)
	}
	if _, err := s.Get(ctx, "one"); !errors.Is(err, ports.ErrDiffNotFound) {
		t.Errorf("deleted diff came back: %v", err)
	}
}

func TestFailRunningOnlyTouchesRunningDiffs(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "diffs.db"))
	running, done := saved("running", time.UnixMilli(1000)), saved("done", time.UnixMilli(1000))
	done.Status, done.Stage = domain.SavedDiffDone, ""
	for _, d := range []*domain.SavedDiff{running, done} {
		if err := s.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.FailRunning(ctx, "stopped", time.UnixMilli(5000)); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx, "running")
	if got.Status != domain.SavedDiffFailed || got.Error != "stopped" || got.Stage != "" || got.FinishedAt == nil {
		t.Errorf("running diff: %+v", got)
	}
	if got, _ := s.Get(ctx, "done"); got.Status != domain.SavedDiffDone || got.Error != "" {
		t.Errorf("finished diff was changed: %+v", got)
	}
}

func TestClearDiffsDeletesEverySavedDiffAndKeepsSettings(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "diffs.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Saved diffs hold the analyzed code; none of it may stay on disk once cleared.
	const code = "func SourceCodeThatMustNotLinger() {}"
	for i, id := range []string{"one", "two"} {
		d := saved(id, time.UnixMilli(int64(1000+i)))
		d.Overview = code
		if err := s.Create(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	settings := domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingPhD}
	if err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if err := ClearDiffs(ctx, path); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal"} {
		data, _ := os.ReadFile(path + suffix)
		if strings.Contains(string(data), code) {
			t.Fatalf("a cleared diff's text is still in %s", filepath.Base(path+suffix))
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the database must stay private: %v %v", info.Mode(), err)
	}
	reopened := open(t, path)
	if list, err := reopened.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("got %v, %v; want an empty library", list, err)
	}
	if got, err := reopened.LoadSettings(ctx); err != nil || got != settings {
		t.Fatalf("settings must survive: %+v %v", got, err)
	}

	// Another process holding the database open (here, a second store reading from it) keeps
	// the write-ahead log; reset must say so rather than claim nothing is left.
	held := filepath.Join(t.TempDir(), "held.db")
	h := open(t, held)
	if err := h.Create(ctx, saved("one", time.UnixMilli(1000))); err != nil {
		t.Fatal(err)
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.QueryContext(ctx, `SELECT id FROM diffs`); err != nil {
		t.Fatal(err)
	}
	if err := ClearDiffs(ctx, held); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("clearing a database another process reads must report it: %v", err)
	}
	_ = tx.Rollback()

	// A file SQLite cannot read is removed whole, so reset still gets the app working again.
	corrupt := filepath.Join(t.TempDir(), "corrupt.db")
	if err := os.WriteFile(corrupt, []byte(strings.Repeat("not a database ", 600)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearDiffs(ctx, corrupt); err != nil {
		t.Fatalf("clearing an unreadable database must remove it: %v", err)
	}
	if _, err := os.Stat(corrupt); !os.IsNotExist(err) {
		t.Fatal("an unreadable database must be removed")
	}

	missing := filepath.Join(t.TempDir(), "none.db")
	// A log a crash left beside a missing database may hold diff text.
	if err := os.WriteFile(missing+"-wal", []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ClearDiffs(ctx, missing); err != nil {
		t.Fatalf("clearing a missing database must succeed: %v", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("clearing a missing database must not create one")
	}
	if _, err := os.Stat(missing + "-wal"); !os.IsNotExist(err) {
		t.Fatal("a leftover log must be removed")
	}
}
