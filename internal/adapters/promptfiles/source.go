// Package promptfiles loads prompts from Markdown files, one directory per variant. Trying a new
// prompt means copying a directory and editing Markdown, with no code change.
package promptfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arrno/diffduck/internal/domain"
)

// Source implements ports.PromptSource.
type Source struct {
	files   fs.FS
	variant string
}

// FromFS reads a variant from a file system, such as the embedded default.
func FromFS(files fs.FS, variant string) *Source { return &Source{files: files, variant: variant} }

// FromDir reads a variant from a directory, and names it after that directory.
func FromDir(dir string) (*Source, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("prompt variant: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("prompt variant %s is not a directory", dir)
	}
	return &Source{files: os.DirFS(abs), variant: filepath.Base(abs)}, nil
}

// Variant names this set of prompts.
func (s *Source) Variant() string { return s.variant }

// Load returns the prompt in <name>.md and its content hash.
func (s *Source) Load(name string) (string, domain.PromptInfo, error) {
	return s.read(name)
}

// LoadReadingLevel returns the instruction in reading-levels/<level>.md and its content hash.
// Only a level that passes domain validation names a file (K3).
func (s *Source) LoadReadingLevel(level domain.ReadingLevel) (string, domain.PromptInfo, error) {
	if !level.Valid() {
		return "", domain.PromptInfo{}, domain.ErrUnknownReadingLevel
	}
	return s.read("reading-levels/" + string(level))
}

func (s *Source) read(name string) (string, domain.PromptInfo, error) {
	data, err := fs.ReadFile(s.files, name+".md")
	if err != nil {
		return "", domain.PromptInfo{}, fmt.Errorf("prompt variant %q: %w", s.variant, err)
	}
	sum := sha256.Sum256(data)
	return string(data), domain.PromptInfo{Name: name, Hash: hex.EncodeToString(sum[:])[:12]}, nil
}
