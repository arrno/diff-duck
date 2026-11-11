//go:build !keychain

package keyring

import (
	"errors"
	"strings"
	"testing"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
	gokeyring "github.com/zalando/go-keyring"
)

const secret = "sk-test-0123456789abcdef"

// Tests replace the OS keychain with go-keyring's in-memory mock, so they never touch the
// real keychain. They share that package-level mock, so none may run in parallel.

func TestAKeyIsSavedReadAndClearedPerProvider(t *testing.T) {
	gokeyring.MockInit()
	k := New()
	if err := k.SaveKey(domain.ProviderAnthropicAPI, secret); err != nil {
		t.Fatal(err)
	}
	if has, err := k.HasKey(domain.ProviderAnthropicAPI); err != nil || !has {
		t.Errorf("anthropic key not reported saved: %v %v", has, err)
	}
	if has, err := k.HasKey(domain.ProviderOpenAIAPI); err != nil || has {
		t.Errorf("openai key reported saved: %v %v", has, err)
	}
	if got, err := k.LoadKey(domain.ProviderAnthropicAPI); err != nil || got != secret {
		t.Errorf("loaded %q, %v", got, err)
	}
	if got, err := gokeyring.Get(Service, "anthropic-api"); err != nil || got != secret {
		t.Errorf("key is not filed under service %q and the provider ID: %q, %v", Service, got, err)
	}

	if err := k.SaveKey(domain.ProviderAnthropicAPI, "sk-replaced"); err != nil {
		t.Fatal(err)
	}
	if got, _ := k.LoadKey(domain.ProviderAnthropicAPI); got != "sk-replaced" {
		t.Errorf("saving did not replace the key: %q", got)
	}

	if err := k.ClearKey(domain.ProviderAnthropicAPI); err != nil {
		t.Fatal(err)
	}
	if has, err := k.HasKey(domain.ProviderAnthropicAPI); err != nil || has {
		t.Errorf("cleared key still reported saved: %v %v", has, err)
	}
	if _, err := k.LoadKey(domain.ProviderAnthropicAPI); !errors.Is(err, ports.ErrKeyNotSaved) {
		t.Errorf("want ErrKeyNotSaved, got %v", err)
	}
}

func TestClearingAnAbsentKeyIsNotAnError(t *testing.T) {
	gokeyring.MockInit()
	if err := New().ClearKey(domain.ProviderOpenAIAPI); err != nil {
		t.Errorf("clearing an absent key: %v", err)
	}
}

func TestOnlyKeyedProvidersHaveKeys(t *testing.T) {
	gokeyring.MockInit()
	k := New()
	for _, p := range []domain.ModelProvider{domain.ProviderClaudeCode, domain.ProviderCodex, "gemini", ""} {
		if err := k.SaveKey(p, secret); err == nil {
			t.Errorf("saved a key for %q", p)
		}
		if _, err := k.HasKey(p); err == nil {
			t.Errorf("checked a key for %q", p)
		}
		if _, err := k.LoadKey(p); err == nil {
			t.Errorf("loaded a key for %q", p)
		}
		if err := k.ClearKey(p); err == nil {
			t.Errorf("cleared a key for %q", p)
		}
	}
	if err := k.SaveKey(domain.ProviderOpenAIAPI, ""); err == nil {
		t.Error("saved an empty key")
	}
}

var (
	_ ports.SecretStore = (*Keychain)(nil)
	_ ports.KeySource   = (*Keychain)(nil)
)

func TestKeychainErrorsNeverIncludeTheKeyOrLibraryErrors(t *testing.T) {
	vendor := errors.New("security: add-generic-password -w " + secret + " failed")
	gokeyring.MockInitWithError(vendor)
	k := New()
	errs := map[string]error{"save": k.SaveKey(domain.ProviderAnthropicAPI, secret), "clear": k.ClearKey(domain.ProviderAnthropicAPI)}
	_, errs["load"] = k.LoadKey(domain.ProviderAnthropicAPI)
	_, errs["has"] = k.HasKey(domain.ProviderAnthropicAPI)
	for op, err := range errs {
		if err == nil {
			t.Errorf("%s: a keychain failure was not reported", op)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "security:") || errors.Is(err, vendor) {
			t.Errorf("%s: error carries the key or the library's error: %v", op, err)
		}
		if errors.Is(err, ports.ErrKeyNotSaved) {
			t.Errorf("%s: a keychain failure was reported as no key: %v", op, err)
		}
	}
}

func TestAKeyTooLongForTheKeychainSaysSo(t *testing.T) {
	gokeyring.MockInitWithError(gokeyring.ErrSetDataTooBig)
	err := New().SaveKey(domain.ProviderAnthropicAPI, secret)
	if err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("got %v", err)
	}
}

func TestAKeySavedUnderTheOldNameIsMovedOnFirstRead(t *testing.T) {
	gokeyring.MockInit()
	if err := gokeyring.Set(LegacyService, "anthropic-api", secret); err != nil {
		t.Fatal(err)
	}
	k := New()
	if has, err := k.HasKey(domain.ProviderAnthropicAPI); err != nil || !has {
		t.Fatalf("legacy key not found: %v %v", has, err)
	}
	if got, err := gokeyring.Get(Service, "anthropic-api"); err != nil || got != secret {
		t.Errorf("key was not moved to %q: %q, %v", Service, got, err)
	}
	if _, err := gokeyring.Get(LegacyService, "anthropic-api"); !errors.Is(err, gokeyring.ErrNotFound) {
		t.Errorf("key still under %q: %v", LegacyService, err)
	}
	if got, err := k.LoadKey(domain.ProviderAnthropicAPI); err != nil || got != secret {
		t.Errorf("loaded %q, %v", got, err)
	}
}

func TestClearingAKeyAlsoClearsItUnderTheOldName(t *testing.T) {
	gokeyring.MockInit()
	if err := gokeyring.Set(LegacyService, "openai-api", secret); err != nil {
		t.Fatal(err)
	}
	k := New()
	if err := k.ClearKey(domain.ProviderOpenAIAPI); err != nil {
		t.Fatal(err)
	}
	if has, err := k.HasKey(domain.ProviderOpenAIAPI); err != nil || has {
		t.Errorf("a cleared legacy key came back: %v %v", has, err)
	}
}
