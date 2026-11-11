package sqlitestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

// LoadSettings returns the saved model settings, ports.ErrSettingsNotSaved when none have been
// saved, or ports.ErrSavedSettingsInvalid when the saved row no longer passes domain
// validation (for example, it was edited by hand).
func (s *Store) LoadSettings(ctx context.Context) (domain.ModelSettings, error) {
	var provider, level string
	err := s.db.QueryRowContext(ctx, `SELECT provider, reading_level FROM settings WHERE id = 1`).Scan(&provider, &level)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ModelSettings{}, ports.ErrSettingsNotSaved
	}
	if err != nil {
		return domain.ModelSettings{}, fmt.Errorf("read settings: %w", err)
	}
	settings, err := domain.ParseModelSettings(provider, level)
	if err != nil {
		return domain.ModelSettings{}, fmt.Errorf("%w: %w", ports.ErrSavedSettingsInvalid, err)
	}
	return settings, nil
}

// SaveSettings replaces the saved model settings. Settings that fail domain validation are
// not written.
func (s *Store) SaveSettings(ctx context.Context, settings domain.ModelSettings) error {
	if err := settings.Validate(); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO settings (id, provider, reading_level) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET provider = excluded.provider, reading_level = excluded.reading_level`,
		string(settings.Provider), string(settings.ReadingLevel))
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}
