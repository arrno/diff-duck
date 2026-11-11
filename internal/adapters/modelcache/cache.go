// Package modelcache stores model responses on disk, so re-running a diff while tuning only
// pays for the calls whose inputs changed. A key covers the model configuration and the full
// prompt and input, so a new prompt variant can never read another variant's answers.
package modelcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arrno/diffduck/internal/services/ports"
)

// Model adds a disk cache in front of another LanguageModel.
type Model struct {
	next ports.LanguageModel
	dir  string
}

// New returns a caching model that stores responses in dir.
func New(next ports.LanguageModel, dir string) (*Model, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create model cache: %w", err)
	}
	return &Model{next: next, dir: dir}, nil
}

// Describe identifies the wrapped model.
func (m *Model) Describe() string { return m.next.Describe() }

// Complete returns a stored response, or calls the wrapped model and stores its success. A
// corrupt or unwritable entry only costs a call; it never fails the run.
func (m *Model) Complete(ctx context.Context, req ports.ModelRequest) (ports.ModelResponse, error) {
	path := filepath.Join(m.dir, m.key(req)+".json")
	if data, err := os.ReadFile(path); err == nil && json.Valid(data) && len(data) > 0 {
		return ports.ModelResponse{JSON: data, CacheHit: true}, nil
	}
	resp, err := m.next.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	// Write through a temporary file so concurrent runs never read a partial entry.
	if tmp, err := os.CreateTemp(m.dir, "entry-*"); err == nil {
		_, writeErr := tmp.Write(resp.JSON)
		closeErr := tmp.Close()
		if writeErr != nil || closeErr != nil || os.Rename(tmp.Name(), path) != nil {
			_ = os.Remove(tmp.Name())
		}
	}
	return resp, nil
}

func (m *Model) key(req ports.ModelRequest) string {
	h := sha256.New()
	for _, part := range []string{m.next.Describe(), req.System, string(req.Schema), req.User} {
		fmt.Fprintf(h, "%d:%s;", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
