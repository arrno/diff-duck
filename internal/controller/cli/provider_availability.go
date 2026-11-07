package cli

import (
	"os/exec"

	"github.com/arrno/diff-duck/internal/domain"
)

// pathAvailability implements ports.ProviderAvailability: a CLI provider is available when its
// binary is on PATH, and an API provider always is (its key is a separate question).
//
// Interim: S5 replaces this with the availability its ModelOpener shares, exposed by the
// model adapters.
type pathAvailability struct {
	lookPath func(file string) (string, error)
}

func newPathAvailability() pathAvailability {
	return pathAvailability{lookPath: exec.LookPath}
}

// Available reports whether provider can be used on this machine.
func (a pathAvailability) Available(provider domain.ModelProvider) bool {
	switch provider {
	case domain.ProviderClaudeCode:
		return a.onPath("claude")
	case domain.ProviderCodex:
		return a.onPath("codex")
	case domain.ProviderAnthropicAPI, domain.ProviderOpenAIAPI:
		return true
	}
	return false
}

func (a pathAvailability) onPath(binary string) bool {
	_, err := a.lookPath(binary)
	return err == nil
}
