package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/arrno/diffduck/internal/adapters/anthropicapi"
	"github.com/arrno/diffduck/internal/adapters/claudecli"
	"github.com/arrno/diffduck/internal/adapters/codexcli"
	"github.com/arrno/diffduck/internal/adapters/modelcache"
	"github.com/arrno/diffduck/internal/adapters/openaiapi"
	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

// Adapters --adapter accepts in every build. Verification builds also accept scripted:<behavior>.
const (
	adapterClaudeCLI    = "claude-cli"
	adapterCodexCLI     = "codex-cli"
	adapterAnthropicAPI = "anthropic-api"
	adapterOpenAIAPI    = "openai-api"
	adapterNames        = "claude-cli, codex-cli, anthropic-api, or openai-api"
)

// Default model IDs, one per provider. Each is explicit, so a provider changing its own
// default changes neither the output nor the response cache identity silently.
const (
	defaultClaudeCodeModel   = "claude-sonnet-5-5"
	defaultCodexModel        = "gpt-6.1-sol"
	defaultAnthropicAPIModel = "claude-sonnet-5-5"
	defaultOpenAIAPIModel    = "gpt-6.1-sol"
)

// openAIKeyEnv holds the key the openai-api adapter uses when chosen by --adapter. The
// anthropic-api adapter chosen that way resolves its key the Anthropic SDK's usual way.
const openAIKeyEnv = "OPENAI_API_KEY"

func defaultModelID(p domain.ModelProvider) string {
	switch p {
	case domain.ProviderClaudeCode:
		return defaultClaudeCodeModel
	case domain.ProviderCodex:
		return defaultCodexModel
	case domain.ProviderAnthropicAPI:
		return defaultAnthropicAPIModel
	case domain.ProviderOpenAIAPI:
		return defaultOpenAIAPIModel
	}
	return ""
}

// adapterProvider is the provider an --adapter name belongs to, or empty for an adapter no
// person can choose in the app, such as a verification adapter.
func adapterProvider(adapter string) domain.ModelProvider {
	switch adapter {
	case adapterClaudeCLI:
		return domain.ProviderClaudeCode
	case adapterCodexCLI:
		return domain.ProviderCodex
	case adapterAnthropicAPI:
		return domain.ProviderAnthropicAPI
	case adapterOpenAIAPI:
		return domain.ProviderOpenAIAPI
	}
	return ""
}

// modelID is the model to use for p. --model belongs to --adapter: it replaces the default
// only for the provider --adapter names (claude-cli, so Claude Code, when not given). Every
// other provider keeps its default, so a Claude model ID never reaches OpenAI.
func (o options) modelID(p domain.ModelProvider) string {
	if o.model != "" && adapterProvider(o.adapter) == p {
		return o.model
	}
	return defaultModelID(p)
}

// installedProviders implements ports.ProviderAvailability: a CLI provider is available
// when its binary is on PATH, and an API provider always is (its key is checked separately).
type installedProviders struct{}

func (installedProviders) Available(p domain.ModelProvider) bool {
	switch p {
	case domain.ProviderClaudeCode:
		return claudecli.Available("")
	case domain.ProviderCodex:
		return codexcli.Available("")
	case domain.ProviderAnthropicAPI, domain.ProviderOpenAIAPI:
		return true
	}
	return false
}

// modelOpener implements ports.ModelOpener for the app. When --adapter was given explicitly
// (pinned), every run uses that adapter whatever provider is saved; otherwise each run uses
// the saved provider, with API keys from the keychain. Either way the reading level still
// comes from the settings, through the run.
type modelOpener struct {
	opts      options
	pinned    bool
	keys      ports.KeySource
	available ports.ProviderAvailability
}

// Open returns the model for one run.
func (o modelOpener) Open(_ context.Context, settings domain.ModelSettings) (ports.OpenedModel, error) {
	if o.pinned {
		return openAdapter(o.opts)
	}
	p := settings.Provider
	if !p.Valid() {
		return ports.OpenedModel{}, domain.ErrUnknownProvider
	}
	if !o.available.Available(p) {
		return ports.OpenedModel{}, fmt.Errorf("%s is not available on this machine: install it and log in, or choose another provider in Model settings", p.Label())
	}
	model, closeModel, err := o.openProvider(p)
	if err != nil {
		return ports.OpenedModel{}, err
	}
	return withCache(o.opts, ports.OpenedModel{Model: model, Provider: p, Close: closeModel})
}

func (o modelOpener) openProvider(p domain.ModelProvider) (ports.LanguageModel, func(), error) {
	id, effort := o.opts.modelID(p), o.opts.effort
	switch p {
	case domain.ProviderClaudeCode, domain.ProviderCodex:
		return cliModel(p, id, effort)
	case domain.ProviderAnthropicAPI, domain.ProviderOpenAIAPI:
		// K1: the key lives only in this frame and the client built from it.
		key, err := o.keys.LoadKey(p)
		if errors.Is(err, ports.ErrKeyNotSaved) {
			return nil, nil, fmt.Errorf("%w for %s: save one in Model settings, or choose another provider", ports.ErrKeyNotSaved, p.Label())
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read the API key for %s: %w", p.Label(), err)
		}
		var m ports.LanguageModel
		if p == domain.ProviderAnthropicAPI {
			m, err = anthropicapi.NewWithKey(key, id, effort)
		} else {
			m, err = openaiapi.New(key, id, effort)
		}
		if err != nil {
			return nil, nil, err
		}
		return m, func() {}, nil
	}
	return nil, nil, domain.ErrUnknownProvider
}

// cliModel builds a CLI provider's model, which needs no key, and the func that releases it.
func cliModel(p domain.ModelProvider, id, effort string) (ports.LanguageModel, func(), error) {
	if p == domain.ProviderCodex {
		m, err := codexcli.New("", id, effort)
		if err != nil {
			return nil, nil, err
		}
		return m, m.Close, nil
	}
	m, err := claudecli.New("", id, effort)
	if err != nil {
		return nil, nil, err
	}
	return m, m.Close, nil
}

// openAdapter opens the adapter --adapter names. The one-shot command always uses it, and the
// app uses it when pinned. API keys come from the environment here, not the keychain.
func openAdapter(opts options) (ports.OpenedModel, error) {
	p := adapterProvider(opts.adapter)
	id, effort := opts.modelID(p), opts.effort
	opened := ports.OpenedModel{Provider: p, Close: func() {}}
	switch opts.adapter {
	case adapterClaudeCLI, adapterCodexCLI:
		m, release, err := cliModel(p, id, effort)
		if err != nil {
			return ports.OpenedModel{}, err
		}
		opened.Model, opened.Close = m, release
	case adapterAnthropicAPI:
		opened.Model = anthropicapi.New(id, effort)
	case adapterOpenAIAPI:
		key := os.Getenv(openAIKeyEnv)
		if key == "" {
			return ports.OpenedModel{}, fmt.Errorf("the openai-api adapter reads its key from %s, which is not set", openAIKeyEnv)
		}
		m, err := openaiapi.New(key, id, effort)
		if err != nil {
			return ports.OpenedModel{}, err
		}
		opened.Model = m
	default:
		m, err := verificationModel(opts.adapter)
		if err != nil {
			return ports.OpenedModel{}, err
		}
		opened.Model = m
	}
	return withCache(opts, opened)
}

// withCache wraps the model with the response cache unless --no-cache is set. On failure it
// releases the model.
func withCache(opts options, opened ports.OpenedModel) (ports.OpenedModel, error) {
	if opts.noCache {
		return opened, nil
	}
	dir := opts.cacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			opened.Close()
			return ports.OpenedModel{}, fmt.Errorf("locate cache directory (use --cache-dir or --no-cache): %w", err)
		}
		own, err := ownDir(base)
		if err != nil {
			opened.Close()
			return ports.OpenedModel{}, err
		}
		dir = filepath.Join(own, "responses")
	}
	cached, err := modelcache.New(opened.Model, dir)
	if err != nil {
		opened.Close()
		return ports.OpenedModel{}, err
	}
	opened.Model = cached
	return opened, nil
}
