// Package sqlitestore implements ports.DiffStore and ports.SettingsStore with a single SQLite
// file.
package sqlitestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"

	"modernc.org/sqlite" // Also registers the database driver.
	sqlite3 "modernc.org/sqlite/lib"
)

const schema = `
CREATE TABLE IF NOT EXISTS diffs (
	id          TEXT PRIMARY KEY,
	repository  TEXT NOT NULL,
	base        TEXT NOT NULL,
	head        TEXT NOT NULL,
	base_sha    TEXT NOT NULL DEFAULT '',
	head_sha    TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	stage       TEXT NOT NULL DEFAULT '',
	error       TEXT NOT NULL DEFAULT '',
	overview    TEXT NOT NULL DEFAULT '',
	files       INTEGER NOT NULL DEFAULT 0,
	added       INTEGER NOT NULL DEFAULT 0,
	removed     INTEGER NOT NULL DEFAULT 0,
	created_at  INTEGER NOT NULL,
	finished_at INTEGER,
	result      BLOB
);
CREATE INDEX IF NOT EXISTS diffs_created_at ON diffs (created_at DESC);
CREATE TABLE IF NOT EXISTS settings (
	id            INTEGER PRIMARY KEY CHECK (id = 1),
	provider      TEXT NOT NULL,
	reading_level TEXT NOT NULL
);`

const summaryColumns = `id, repository, base, head, base_sha, head_sha, status, stage, error, overview, files, added, removed, created_at, finished_at`

// Store is a SQLite database of saved diffs.
type Store struct {
	db *sql.DB
}

// Open opens the database at path, creating it if needed. Saved diffs hold source code from
// the analyzed repositories, so the file and its directory are private to this user.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := errors.Join(file.Chmod(0o600), file.Close()); err != nil {
		return nil, fmt.Errorf("secure database: %w", err)
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare database %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// ClearDiffs deletes every saved diff in the database at path and keeps everything else, such
// as the model settings. Nothing else may have it open. A database that does not exist is not
// an error, and none is created; side files a crash left behind are removed. A file SQLite
// cannot read as a database is removed whole, since nothing in it can be kept.
func ClearDiffs(ctx context.Context, path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return remove(path)
	}
	s, err := Open(ctx, path)
	if unreadable(err) {
		return remove(path)
	}
	if err != nil {
		return err
	}
	return errors.Join(s.clearDiffs(ctx), s.Close())
}

// unreadable reports whether err says the file is not a database or is corrupt.
func unreadable(err error) bool {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	code := e.Code() & 0xff // the primary result code
	return code == sqlite3.SQLITE_NOTADB || code == sqlite3.SQLITE_CORRUPT
}

// remove deletes the database at path along with SQLite's side files.
func remove(path string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove saved diffs: %w", err)
		}
	}
	return nil
}

// ErrDatabaseInUse means saved diffs were deleted, but another process has the database open,
// so copies of their text stay in the write-ahead log until it closes the database.
var ErrDatabaseInUse = errors.New("another process has the database open")

// clearDiffs deletes every diff row. The rows held source code, so the file is then rebuilt
// without their freed pages and the write-ahead log is emptied, leaving no copy on disk. A
// checkpoint another process blocks is reported, never treated as success.
func (s *Store) clearDiffs(ctx context.Context) error {
	for _, stmt := range []string{`DELETE FROM diffs`, `VACUUM`} {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("delete saved diffs: %w", err)
		}
	}
	var busy, logFrames, checkpointed int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("delete saved diffs: %w", err)
	}
	if busy != 0 || logFrames != 0 {
		return fmt.Errorf("saved diffs were deleted, but %w: stop any other diffduck and run reset again to remove their last copies", ErrDatabaseInUse)
	}
	return nil
}

func columns(d *domain.SavedDiff) ([]any, error) {
	var result []byte
	if d.Result != nil {
		var err error
		if result, err = json.Marshal(d.Result); err != nil {
			return nil, fmt.Errorf("encode result: %w", err)
		}
	}
	var finished any
	if d.FinishedAt != nil {
		finished = d.FinishedAt.UnixMilli()
	}
	return []any{d.Repository, d.Base, d.Head, d.BaseSHA, d.HeadSHA, string(d.Status), d.Stage, d.Error, d.Overview,
		d.Files, d.Added, d.Removed, d.CreatedAt.UnixMilli(), finished, result, d.ID}, nil
}

// Create saves a new diff.
func (s *Store) Create(ctx context.Context, d *domain.SavedDiff) error {
	values, err := columns(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO diffs (repository, base, head, base_sha, head_sha, status, stage, error, overview,
		files, added, removed, created_at, finished_at, result, id) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, values...)
	if err != nil {
		return fmt.Errorf("save diff: %w", err)
	}
	return nil
}

// Update replaces a saved diff. Updating one that was deleted does nothing.
func (s *Store) Update(ctx context.Context, d *domain.SavedDiff) error {
	values, err := columns(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE diffs SET repository=?, base=?, head=?, base_sha=?, head_sha=?, status=?, stage=?, error=?, overview=?,
		files=?, added=?, removed=?, created_at=?, finished_at=?, result=? WHERE id=?`, values...)
	if err != nil {
		return fmt.Errorf("update diff: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanSummary(row scanner, extra ...any) (*domain.SavedDiff, error) {
	var d domain.SavedDiff
	var status string
	var created int64
	var finished sql.NullInt64
	dest := append([]any{&d.ID, &d.Repository, &d.Base, &d.Head, &d.BaseSHA, &d.HeadSHA, &status, &d.Stage, &d.Error, &d.Overview,
		&d.Files, &d.Added, &d.Removed, &created, &finished}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	d.Status = domain.SavedDiffStatus(status)
	d.CreatedAt = time.UnixMilli(created)
	if finished.Valid {
		at := time.UnixMilli(finished.Int64)
		d.FinishedAt = &at
	}
	return &d, nil
}

// Get returns a saved diff with its result, or ports.ErrDiffNotFound.
func (s *Store) Get(ctx context.Context, id string) (*domain.SavedDiff, error) {
	var result []byte
	row := s.db.QueryRowContext(ctx, `SELECT `+summaryColumns+`, result FROM diffs WHERE id=?`, id)
	d, err := scanSummary(row, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ports.ErrDiffNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read diff: %w", err)
	}
	if result != nil {
		d.Result = &domain.CompressedDiff{}
		if err := json.Unmarshal(result, d.Result); err != nil {
			return nil, fmt.Errorf("decode result: %w", err)
		}
	}
	return d, nil
}

// List returns saved diffs without their results, newest first.
func (s *Store) List(ctx context.Context) ([]domain.SavedDiff, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+summaryColumns+` FROM diffs ORDER BY created_at DESC, rowid DESC`)
	if err != nil {
		return nil, fmt.Errorf("list diffs: %w", err)
	}
	defer rows.Close()
	diffs := []domain.SavedDiff{}
	for rows.Next() {
		d, err := scanSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("list diffs: %w", err)
		}
		diffs = append(diffs, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list diffs: %w", err)
	}
	return diffs, nil
}

// Delete removes a saved diff, or returns ports.ErrDiffNotFound.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM diffs WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete diff: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ports.ErrDiffNotFound
	}
	return nil
}

// FailRunning marks every running diff as failed with this message.
func (s *Store) FailRunning(ctx context.Context, message string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE diffs SET status=?, stage='', error=?, finished_at=? WHERE status=?`,
		string(domain.SavedDiffFailed), message, at.UnixMilli(), string(domain.SavedDiffRunning))
	if err != nil {
		return fmt.Errorf("mark stopped diffs: %w", err)
	}
	return nil
}
