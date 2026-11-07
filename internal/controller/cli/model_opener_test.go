package cli

import (
	"context"
	"errors"
	"io"
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

func TestOnlyAnExplicitAdapterPinsTheApp(t *testing.T) {
	for args, want := range map[string]bool{"": false, "--model x": false, "--adapter claude-cli": true, "--adapter=scripted:normal": true} {
		var opts serveOptions
		fs := serveFlags(&opts, io.Discard, "test")
		if _, ok := parseServe(fs, strings.Fields(args), &opts); !ok {
			t.Fatalf("%q did not parse", args)
		}
		if opts.adapterPinned != want {
			t.Errorf("%q: pinned %v", args, opts.adapterPinned)
		}
	}
}

func TestOneShotReadingLevelIsValidated(t *testing.T) {
	if code := Run(context.Background(), []string{"--reading-level", "../leaf", "repo", "a", "b"}, io.Discard, io.Discard); code != exitUsage {
		t.Fatalf("exit %d", code)
	}
}

func TestInstalledProvidersAlwaysOfferAPIProviders(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	a := installedProviders{}
	if a.Available(domain.ProviderClaudeCode) || a.Available(domain.ProviderCodex) {
		t.Error("a CLI provider without its binary on PATH must be unavailable")
	}
	if !a.Available(domain.ProviderAnthropicAPI) || !a.Available(domain.ProviderOpenAIAPI) || a.Available("google") {
		t.Error("API providers are available; unknown ones are not")
	}
}
