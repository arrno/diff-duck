//go:build verify

package cli

import (
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

// Run with: go test -tags verify ./internal/controller/cli
func TestTheVerifyKeychainIsSharedAcrossTheProcess(t *testing.T) {
	first := secretStore()
	if err := first.SaveKey(domain.ProviderOpenAIAPI, "sk-test-shared"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.ClearKey(domain.ProviderOpenAIAPI) })
	got, err := secretStore().LoadKey(domain.ProviderOpenAIAPI)
	if err != nil || got != "sk-test-shared" {
		t.Errorf("a second secretStore lost the key saved through the first: %q, %v", got, err)
	}
}
