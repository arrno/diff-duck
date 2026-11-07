// Package ports defines what the application needs from the outside world.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/arrno/diff-duck/internal/domain"
)

// RepositoryReader reads a repository's history. Implementations must treat revisions and
// paths as data, never as options.
type RepositoryReader interface {
	// ResolveCommit returns the full SHA of a revision, or ErrUnknownRevision.
	ResolveCommit(ctx context.Context, revision string) (string, error)
	// ChangedFiles returns the diff between two commits with renames detected.
	ChangedFiles(ctx context.Context, baseSHA, headSHA string) ([]domain.FileChange, error)
	// ReadFile returns a file's contents at a commit.
	ReadFile(ctx context.Context, sha, path string) ([]byte, error)
	// ListFiles returns the paths of the files directly inside a directory at a commit.
	ListFiles(ctx context.Context, sha, dir string) ([]string, error)
}

// ErrUnknownRevision means a revision does not resolve to a commit.
var ErrUnknownRevision = errors.New("unknown revision")

// OpenRepository is a repository in use. Close releases anything held for it.
type OpenRepository interface {
	RepositoryReader
	// ListCommits returns the most recent commits reachable from HEAD, newest first.
	ListCommits(ctx context.Context, limit int) ([]domain.Commit, error)
	Close()
}

// RepositorySource opens a repository from a local path or a clone URL.
type RepositorySource interface {
	Open(ctx context.Context, target string) (OpenRepository, error)
}

// DiffStore keeps saved diffs across restarts.
type DiffStore interface {
	Create(ctx context.Context, diff *domain.SavedDiff) error
	// Update replaces a saved diff. Updating one that was deleted does nothing.
	Update(ctx context.Context, diff *domain.SavedDiff) error
	// Get returns a saved diff with its result, or ErrDiffNotFound.
	Get(ctx context.Context, id string) (*domain.SavedDiff, error)
	// List returns saved diffs without their results, newest first.
	List(ctx context.Context) ([]domain.SavedDiff, error)
	// Delete removes a saved diff, or returns ErrDiffNotFound.
	Delete(ctx context.Context, id string) error
	// FailRunning marks every running diff as failed with this message.
	FailRunning(ctx context.Context, message string, at time.Time) error
}

// ErrDiffNotFound means no saved diff has that ID.
var ErrDiffNotFound = errors.New("diff not found")

// SourceAnalyzer extracts code units from one language's source files.
type SourceAnalyzer interface {
	Supports(path string) bool
	// Analyze returns the file's units. An error means the file could not be understood, and
	// callers must represent it without structural claims. Cancelling ctx abandons the work.
	Analyze(ctx context.Context, path string, src []byte) (domain.FileAnalysis, error)
}

// ModelRequest is one generation. System holds instructions. User holds untrusted data from
// the analyzed repository, which implementations must not allow to trigger any capability
// beyond producing text. Schema is the JSON Schema the response must satisfy.
type ModelRequest struct {
	System string
	User   string
	Schema json.RawMessage
}

// ModelResponse is a generation result. JSON satisfies the request's Schema. Implementations
// must not report a truncated or refused response as success.
type ModelResponse struct {
	JSON         json.RawMessage
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
	CacheHit     bool
}

// LanguageModel generates structured text.
type LanguageModel interface {
	Complete(ctx context.Context, req ModelRequest) (ModelResponse, error)
	// Describe identifies the adapter and configuration; it distinguishes cached responses.
	Describe() string
}

// ErrModelRefused means the model declined to answer this input. Retrying will not help.
var ErrModelRefused = errors.New("model refused the request")

// PromptSource loads prompts by name.
type PromptSource interface {
	// Load returns the named prompt's text and identity.
	Load(name string) (text string, info domain.PromptInfo, err error)
	// Variant names the set of prompts this source provides.
	Variant() string
}
