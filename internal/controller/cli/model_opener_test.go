package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/adapters/modelcache"
	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const secret = "sk-test-secret-0123456789"

type keys map[domain.ModelProvider]string

func (k keys) LoadKey(p domain.ModelProvider) (string, error) {
	if key, ok := k[p]; ok {
		return key, nil
	}
	return "", ports.ErrKeyNotSaved
}

type availableOnly map[domain.ModelProvider]bool

func (a availableOnly) Available(p domain.ModelProvider) bool { return a[p] }

func everyProvider() availableOnly {
	return availableOnly{domain.ProviderClaudeCode: true, domain.ProviderCodex: true, domain.ProviderAnthropicAPI: true, domain.ProviderOpenAIAPI: true}
}

func settingsFor(p domain.ModelProvider) domain.ModelSettings {
	return domain.ModelSettings{Provider: p, ReadingLevel: domain.ReadingSixthGrade}
}

func defaultOptions() options {
	return options{adapter: adapterClaudeCLI, effort: "medium", noCache: true}
}

func TestEveryProviderHasAnExplicitDefaultModel(t *testing.T) {
	for _, p := range domain.ModelProviders() {
		if defaultModelID(p) == "" {
			t.Errorf("%s has no default model", p)
		}
	}
}

func TestModelFlagAppliesOnlyToTheAdaptersProvider(t *testing.T) {
	opts := defaultOptions()
	opts.model = "claude-haiku-5-5"
	if got := opts.modelID(domain.ProviderClaudeCode); got != "claude-haiku-5-5" {
		t.Errorf("--model with the default adapter must set Claude Code's model: %s", got)
	}
	for _, p := range []domain.ModelProvider{domain.ProviderCodex, domain.ProviderAnthropicAPI, domain.ProviderOpenAIAPI} {
		if got := opts.modelID(p); got != defaultModelID(p) {
			t.Errorf("%s took another provider's --model: %s", p, got)
		}
	}
	opts.adapter, opts.model = adapterOpenAIAPI, "gpt-6-luna"
	if opts.modelID(domain.ProviderOpenAIAPI) != "gpt-6-luna" || opts.modelID(domain.ProviderClaudeCode) != defaultClaudeCodeModel {
		t.Error("--model must follow --adapter")
	}
}

func TestTheSavedAPIProviderIsOpenedWithItsKeychainKey(t *testing.T) {
	o := modelOpener{opts: defaultOptions(), keys: keys{domain.ProviderOpenAIAPI: secret, domain.ProviderAnthropicAPI: secret}, available: everyProvider()}
	for p, want := range map[domain.ModelProvider]string{
		domain.ProviderOpenAIAPI:    "openai-api/" + defaultOpenAIAPIModel + "/medium",
		domain.ProviderAnthropicAPI: "anthropic-api/" + defaultAnthropicAPIModel + "/medium",
	} {
		opened, err := o.Open(context.Background(), settingsFor(p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if opened.Provider != p || opened.Model.Describe() != want || strings.Contains(opened.Model.Describe(), secret) {
			t.Errorf("%s: %s %s", p, opened.Provider, opened.Model.Describe())
		}
		opened.Close()
	}
}

// recordingKeys is a keychain that records which provider each key was read for.
type recordingKeys struct {
	keys
	asked []domain.ModelProvider
}

func (k *recordingKeys) LoadKey(p domain.ModelProvider) (string, error) {
	k.asked = append(k.asked, p)
	return k.keys.LoadKey(p)
}

func TestEachAPIProviderUsesOnlyItsOwnKey(t *testing.T) {
	k := &recordingKeys{keys: keys{domain.ProviderAnthropicAPI: secret}}
	o := modelOpener{opts: defaultOptions(), keys: k, available: everyProvider()}
	opened, err := o.Open(context.Background(), settingsFor(domain.ProviderAnthropicAPI))
	if err != nil {
		t.Fatal(err)
	}
	opened.Close()
	// Another provider's saved key never stands in for a missing one.
	if _, err := o.Open(context.Background(), settingsFor(domain.ProviderOpenAIAPI)); !errors.Is(err, ports.ErrKeyNotSaved) || !strings.Contains(err.Error(), "OpenAI API") {
		t.Fatalf("got %v", err)
	}
	if len(k.asked) != 2 || k.asked[0] != domain.ProviderAnthropicAPI || k.asked[1] != domain.ProviderOpenAIAPI {
		t.Fatalf("keys read for %v", k.asked)
	}
}

func TestTheSavedCLIProviderRunsItsOwnCLI(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	o := modelOpener{opts: defaultOptions(), keys: keys{}, available: installedProviders{}}
	for p, want := range map[domain.ModelProvider]string{
		domain.ProviderClaudeCode: "claude-cli/" + defaultClaudeCodeModel + "/medium",
		domain.ProviderCodex:      "codex-cli/" + defaultCodexModel + "/medium",
	} {
		opened, err := o.Open(context.Background(), settingsFor(p))
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if opened.Provider != p || opened.Model.Describe() != want {
			t.Errorf("%s: %s %s", p, opened.Provider, opened.Model.Describe())
		}
		opened.Close()
	}
}

func TestEachAdapterNameRecordsItsProvider(t *testing.T) {
	for name, want := range map[string]domain.ModelProvider{
		adapterClaudeCLI: domain.ProviderClaudeCode, adapterCodexCLI: domain.ProviderCodex,
		adapterAnthropicAPI: domain.ProviderAnthropicAPI, adapterOpenAIAPI: domain.ProviderOpenAIAPI,
		"scripted:normal": "",
	} {
		if got := adapterProvider(name); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

func TestAMissingKeyIsAClearErrorWithoutTheKey(t *testing.T) {
	o := modelOpener{opts: defaultOptions(), keys: keys{}, available: everyProvider()}
	_, err := o.Open(context.Background(), settingsFor(domain.ProviderAnthropicAPI))
	if !errors.Is(err, ports.ErrKeyNotSaved) || !strings.Contains(err.Error(), "Anthropic API") {
		t.Fatalf("got %v", err)
	}
}

type brokenKeychain struct{}

func (brokenKeychain) LoadKey(domain.ModelProvider) (string, error) {
	return "", errors.New("could not read the OS keychain")
}

func TestAnUnreadableKeychainIsReported(t *testing.T) {
	o := modelOpener{opts: defaultOptions(), keys: brokenKeychain{}, available: everyProvider()}
	_, err := o.Open(context.Background(), settingsFor(domain.ProviderOpenAIAPI))
	if err == nil || errors.Is(err, ports.ErrKeyNotSaved) || !strings.Contains(err.Error(), "keychain") {
		t.Fatalf("got %v", err)
	}
}

func TestAnUnavailableProviderIsNotOpened(t *testing.T) {
	o := modelOpener{opts: defaultOptions(), keys: keys{}, available: availableOnly{}}
	for _, p := range []domain.ModelProvider{domain.ProviderClaudeCode, domain.ProviderCodex} {
		if _, err := o.Open(context.Background(), settingsFor(p)); err == nil || !strings.Contains(err.Error(), p.Label()+" is not available") {
			t.Errorf("%s: got %v", p, err)
		}
	}
	if _, err := o.Open(context.Background(), domain.ModelSettings{Provider: "google"}); !errors.Is(err, domain.ErrUnknownProvider) {
		t.Errorf("unknown provider: got %v", err)
	}
}

func TestAPinnedAdapterOverridesTheSavedProvider(t *testing.T) {
	opts := defaultOptions()
	opts.adapter, opts.model = adapterAnthropicAPI, "claude-haiku-5-5"
	// Nothing is available and no key is saved: a pinned adapter needs neither.
	o := modelOpener{opts: opts, pinned: true, keys: keys{}, available: availableOnly{}}
	opened, err := o.Open(context.Background(), settingsFor(domain.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	if opened.Provider != domain.ProviderAnthropicAPI || opened.Model.Describe() != "anthropic-api/claude-haiku-5-5/medium" {
		t.Fatalf("%s %s", opened.Provider, opened.Model.Describe())
	}
}

func TestAPinnedUnknownAdapterFails(t *testing.T) {
	opts := defaultOptions()
	opts.adapter = "nope"
	if _, err := openAdapter(opts); err == nil || !strings.Contains(err.Error(), adapterNames) {
		t.Fatalf("got %v", err)
	}
}

func TestTheOpenAIAdapterByFlagNeedsItsEnvironmentKey(t *testing.T) {
	t.Setenv(openAIKeyEnv, "")
	opts := defaultOptions()
	opts.adapter = adapterOpenAIAPI
	if _, err := openAdapter(opts); err == nil || !strings.Contains(err.Error(), openAIKeyEnv) {
		t.Fatalf("got %v", err)
	}
	t.Setenv(openAIKeyEnv, secret)
	opened, err := openAdapter(opts)
	if err != nil || opened.Provider != domain.ProviderOpenAIAPI {
		t.Fatalf("%v %+v", err, opened)
	}
}

func TestOpenedModelsAreCachedUnlessDisabled(t *testing.T) {
	opts := defaultOptions()
	opts.noCache, opts.cacheDir = false, t.TempDir()
	o := modelOpener{opts: opts, keys: keys{domain.ProviderOpenAIAPI: secret}, available: everyProvider()}
	opened, err := o.Open(context.Background(), settingsFor(domain.ProviderOpenAIAPI))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := opened.Model.(*modelcache.Model); !ok {
		t.Fatalf("not cached: %T", opened.Model)
	}
	o.opts.noCache = true
	opened, _ = o.Open(context.Background(), settingsFor(domain.ProviderOpenAIAPI))
	if _, ok := opened.Model.(*modelcache.Model); ok {
		t.Fatal("--no-cache must skip the cache")
	}
}

func TestOneShotReadingLevelIsValidated(t *testing.T) {
	if code := Run(context.Background(), []string{"--reading-level", "../leaf", "repo", "a", "b"}, io.Discard, io.Discard); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestCLIProvidersAreAvailableOnlyWhenTheirBinaryIsOnPath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "codex"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	a := installedProviders{}
	for provider, want := range map[domain.ModelProvider]bool{
		domain.ProviderClaudeCode:   false,
		domain.ProviderCodex:        true,
		domain.ProviderAnthropicAPI: true,
		domain.ProviderOpenAIAPI:    true,
		"gemini":                    false,
	} {
		if got := a.Available(provider); got != want {
			t.Errorf("%s: got %t, want %t", provider, got, want)
		}
	}
}
