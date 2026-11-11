//go:build keychain

package keyring

import (
	"testing"

	"github.com/arrno/diffduck/internal/domain"
)

const secret = "sk-test-0123456789abcdef"

// TestTheRealKeychain round-trips a key through this machine's keychain, under a service name
// of its own. It is built only with the keychain tag, apart from the tests that install
// go-keyring's package-level mock:
//
//	go test -tags keychain ./internal/adapters/keyring
func TestTheRealKeychain(t *testing.T) {
	k := &Keychain{service: Service + "-test"}
	t.Cleanup(func() { _ = k.ClearKey(domain.ProviderOpenAIAPI) })
	if err := k.SaveKey(domain.ProviderOpenAIAPI, secret); err != nil {
		t.Fatal(err)
	}
	if got, err := k.LoadKey(domain.ProviderOpenAIAPI); err != nil || got != secret {
		t.Fatalf("loaded %q, %v", got, err)
	}
	if err := k.ClearKey(domain.ProviderOpenAIAPI); err != nil {
		t.Fatal(err)
	}
	if has, err := k.HasKey(domain.ProviderOpenAIAPI); err != nil || has {
		t.Errorf("cleared key still saved: %v %v", has, err)
	}
	if err := k.ClearKey(domain.ProviderOpenAIAPI); err != nil {
		t.Errorf("clearing an absent key: %v", err)
	}
}
