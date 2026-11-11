//go:build !verify

package cli

import (
	"fmt"

	"github.com/arrno/diffduck/internal/adapters/keyring"
	"github.com/arrno/diffduck/internal/services/ports"
)

// verificationModel rejects every name: canned output is not reachable from a release build.
func verificationModel(adapter string) (ports.LanguageModel, error) {
	return nil, fmt.Errorf("unknown adapter %q: use "+adapterNames, adapter)
}

// secretStore is the OS keychain. It serves as both ports.SecretStore and ports.KeySource;
// build it once and share it.
func secretStore() *keyring.Keychain { return keyring.New() }
