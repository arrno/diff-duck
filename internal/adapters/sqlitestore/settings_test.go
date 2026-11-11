package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

func TestSettingsAreNotSavedUntilSaved(t *testing.T) {
	_, err := open(t, filepath.Join(t.TempDir(), "diffs.db")).LoadSettings(context.Background())
	if !errors.Is(err, ports.ErrSettingsNotSaved) {
		t.Fatalf("want ErrSettingsNotSaved, got %v", err)
	}
}

func TestSavedSettingsSurviveReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "diffs.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SaveSettings(ctx, domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingCollege}); err != nil {
		t.Fatal(err)
	}
	want := domain.ModelSettings{Provider: domain.ProviderAnthropicAPI, ReadingLevel: domain.ReadingPhD}
	if err := first.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	first.Close()

	got, err := open(t, path).LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestInvalidSettingsAreNotWritten(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "diffs.db"))
	for _, bad := range []domain.ModelSettings{
		{Provider: "gemini", ReadingLevel: domain.ReadingPhD},
		{Provider: domain.ProviderCodex, ReadingLevel: "kindergarten"},
		{},
	} {
		if err := s.SaveSettings(ctx, bad); err == nil {
			t.Errorf("saved invalid settings %+v", bad)
		}
	}
	if _, err := s.LoadSettings(ctx); !errors.Is(err, ports.ErrSettingsNotSaved) {
		t.Errorf("invalid settings reached the database: %v", err)
	}
}

func TestATamperedSettingsRowIsReportedInvalid(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "diffs.db")
	s := open(t, path)
	if err := s.SaveSettings(ctx, domain.DefaultModelSettings()); err != nil {
		t.Fatal(err)
	}
	for _, tamper := range []string{
		`UPDATE settings SET provider = '../../etc/passwd'`,
		`UPDATE settings SET provider = 'claude-code', reading_level = 'genius'`,
	} {
		if _, err := s.db.ExecContext(ctx, tamper); err != nil {
			t.Fatal(err)
		}
		got, err := s.LoadSettings(ctx)
		if !errors.Is(err, ports.ErrSavedSettingsInvalid) {
			t.Errorf("%s: want ErrSavedSettingsInvalid, got %+v, %v", tamper, got, err)
		}
		if got != (domain.ModelSettings{}) {
			t.Errorf("%s: returned settings %+v with the error", tamper, got)
		}
	}
}

func TestADatabaseFromBeforeSettingsGainsTheTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "diffs.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.ExecContext(ctx, `CREATE TABLE diffs (id TEXT PRIMARY KEY, repository TEXT NOT NULL, base TEXT NOT NULL,
		head TEXT NOT NULL, status TEXT NOT NULL, created_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := open(t, path)
	if _, err := s.LoadSettings(ctx); !errors.Is(err, ports.ErrSettingsNotSaved) {
		t.Fatalf("want ErrSettingsNotSaved, got %v", err)
	}
	want := domain.ModelSettings{Provider: domain.ProviderOpenAIAPI, ReadingLevel: domain.ReadingSecondGrade}
	if err := s.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LoadSettings(ctx); err != nil || got != want {
		t.Errorf("got %+v, %v", got, err)
	}
}
