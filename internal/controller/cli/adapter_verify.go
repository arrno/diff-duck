//go:build verify

package cli

import (
	"fmt"
	"strings"
	"sync"

	"github.com/arrno/diff-duck/internal/adapters/keyring"
	"github.com/arrno/diff-duck/internal/adapters/scriptedmodel"
	"github.com/arrno/diff-duck/internal/services/ports"
	gokeyring "github.com/zalando/go-keyring"
)

// verificationModel provides the scripted model to verification builds.
func verificationModel(adapter string) (ports.LanguageModel, error) {
	behavior, ok := strings.CutPrefix(adapter, "scripted:")
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q: use claude-cli, anthropic-api, or scripted:<behavior>", adapter)
	}
	return scriptedmodel.New(scriptedmodel.Behavior(behavior))
}

// mockKeychain installs go-keyring's in-memory mock once per process. A second MockInit would
// wipe every key saved so far.
var mockKeychain sync.Once

// secretStore is the keychain adapter over go-keyring's in-memory mock, so verification never
// reads or writes the real OS keychain of the machine running it. It serves as both
// ports.SecretStore and ports.KeySource; build it once and share it.
func secretStore() *keyring.Keychain {
	mockKeychain.Do(gokeyring.MockInit)
	return keyring.New()
}
