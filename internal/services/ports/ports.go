// Package ports defines what the application needs from the outside world.
package ports

import (
	"context"
	"encoding/json"
	"errors"

	"duck-diff/internal/domain"
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

// SourceAnalyzer extracts code units from one language's source files.
type SourceAnalyzer interface {
	Supports(path string) bool
	// Analyze returns the file's units. An error means the file could not be understood, and
	// callers must represent it without structural claims.
	Analyze(path string, src []byte) (domain.FileAnalysis, error)
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
