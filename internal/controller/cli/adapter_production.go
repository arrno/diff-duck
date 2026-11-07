//go:build !verify

package cli

import (
	"fmt"

	"github.com/arrno/diff-duck/internal/adapters/keyring"
	"github.com/arrno/diff-duck/internal/services/ports"
)

// verificationModel rejects every name: canned output is not reachable from a release build.
func verificationModel(adapter string) (ports.LanguageModel, error) {
	return nil, fmt.Errorf("unknown adapter %q: use claude-cli or anthropic-api", adapter)
}

// secretStore is the OS keychain. It serves as both ports.SecretStore and ports.KeySource;
// build it once and share it.
func secretStore() *keyring.Keychain { return keyring.New() }
