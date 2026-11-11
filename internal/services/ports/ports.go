// Package ports defines what the application needs from the outside world.
package ports

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/arrno/diffduck/internal/domain"
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
	// LoadReadingLevel returns the instruction for writing at a reading level, and its
	// identity. Implementations reject a level that fails domain validation, so a raw string
	// never selects a file (K3).
	LoadReadingLevel(level domain.ReadingLevel) (text string, info domain.PromptInfo, err error)
	// Variant names the set of prompts this source provides.
	Variant() string
}

// SettingsStore keeps the app-wide model settings across restarts.
type SettingsStore interface {
	// LoadSettings returns the saved settings, ErrSettingsNotSaved when none have been saved,
	// or ErrSavedSettingsInvalid when what was saved no longer passes domain validation.
	// It never returns settings that fail domain validation.
	LoadSettings(ctx context.Context) (domain.ModelSettings, error)
	// SaveSettings replaces the saved settings. It rejects settings that fail domain validation.
	SaveSettings(ctx context.Context, settings domain.ModelSettings) error
}

var (
	// ErrSettingsNotSaved means no settings have been saved yet.
	ErrSettingsNotSaved = errors.New("settings not saved")
	// ErrSavedSettingsInvalid means the saved settings name an unknown provider or reading level.
	ErrSavedSettingsInvalid = errors.New("saved settings are not valid")
)

// SecretStore keeps one API key per model provider in the OS keychain. It never hands a key
// back; KeySource does, for model construction only.
//
// K1: a key lives only in the keychain and in process memory while a model is built from it.
// Implementations must never write a key anywhere else or include it in an error. They reject
// providers that do not use a key (domain.ModelProvider.NeedsKey).
type SecretStore interface {
	// SaveKey stores the provider's key, replacing any saved one.
	SaveKey(provider domain.ModelProvider, key string) error
	// HasKey reports whether a key is saved for the provider.
	HasKey(provider domain.ModelProvider) (bool, error)
	// ClearKey removes the provider's key. Clearing a key that is not saved is not an error.
	ClearKey(provider domain.ModelProvider) error
}

// KeySource reads a saved API key so a model can be built from it. Only model construction
// depends on it, and the key must not outlive that use (K1).
type KeySource interface {
	// LoadKey returns the provider's key, or ErrKeyNotSaved.
	LoadKey(provider domain.ModelProvider) (string, error)
}

// ErrKeyNotSaved means no API key is saved for the provider.
var ErrKeyNotSaved = errors.New("no API key saved")

// ProviderAvailability reports whether a provider can be used on this machine, such as whether
// a CLI provider's binary is on PATH. It says nothing about saved keys.
type ProviderAvailability interface {
	Available(provider domain.ModelProvider) bool
}

// ModelOpener opens the language model one run uses, chosen from the effective settings.
// An implementation may override the settings' provider, such as when the app was started
// with an explicit adapter, and reports the provider it actually used.
//
// Open re-checks availability and the API key at run time on purpose, although
// SettingsKeeper already checked both when the settings were saved: settings can change, a
// CLI binary can disappear, and a key can be cleared between saving and running. The
// keeper's checks are save-time validation that gives the person early feedback; these are
// what keep a run from starting with a provider that cannot work.
type ModelOpener interface {
	// Open returns the model for a run with these settings. A provider that cannot be used
	// returns an error a person can act on; a missing API key wraps ErrKeyNotSaved. Errors
	// never include a key (K1).
	Open(ctx context.Context, settings domain.ModelSettings) (OpenedModel, error)
}

// OpenedModel is a model ready for one run. Close releases it when the run ends; it is never
// nil.
type OpenedModel struct {
	Model LanguageModel
	// Provider is the provider the model belongs to. It is empty for a model no person can
	// choose, such as a verification adapter.
	Provider domain.ModelProvider
	Close    func()
}
