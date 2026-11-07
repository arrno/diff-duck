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

// secretStore is the OS keychain.
func secretStore() ports.SecretStore { return keyring.New() }
