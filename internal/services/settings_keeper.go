package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services/ports"
)

var (
	// ErrInvalidSettings means the request names an unknown provider or reading level, or
	// carries a key that cannot be used.
	ErrInvalidSettings = errors.New("invalid settings")
	// ErrProviderUnavailable means the chosen provider cannot be used on this machine.
	ErrProviderUnavailable = errors.New("model provider is not available")
	// ErrKeyRequired means an API provider was chosen with no key saved and none given.
	ErrKeyRequired = errors.New("an API key is required")
)

// maxKeyBytes keeps a key well inside every OS keychain's size limit. Keep it: go-keyring on
// macOS leaves a `security` process behind when a key exceeds its limit.
const maxKeyBytes = 1024

// SettingsDeps are what a SettingsKeeper needs from the outside world.
type SettingsDeps struct {
	Store        ports.SettingsStore
	Secrets      ports.SecretStore
	Availability ports.ProviderAvailability
	// Pinned names the adapter set explicitly when the app started, or is empty.
	Pinned string
	// PinnedProvider is the provider the pinned adapter belongs to. It is empty when nothing
	// is pinned or the pinned adapter is no provider a person can choose (verification).
	PinnedProvider domain.ModelProvider
}

// SettingsView is what a person may know about the model settings. It never holds a key.
type SettingsView struct {
	Provider     domain.ModelProvider `json:"provider"`
	ReadingLevel domain.ReadingLevel  `json:"readingLevel"`
	Pinned       string               `json:"pinned"`
	// PinnedProvider is the provider every run uses while pinned, which may differ from the
	// saved Provider; empty when nothing is pinned or the pin is no choosable provider.
	PinnedProvider domain.ModelProvider `json:"pinnedProvider"`
	Providers      []ProviderStatus     `json:"providers"`
}

// ProviderStatus says whether a provider can be chosen.
type ProviderStatus struct {
	ID        domain.ModelProvider `json:"id"`
	Label     string               `json:"label"`
	Available bool                 `json:"available"`
	NeedsKey  bool                 `json:"needsKey"`
	KeySaved  bool                 `json:"keySaved"`
}

// SettingsKeeper reads and saves the app-wide model settings and the API keys they rely on.
//
// Clearing the key of the selected provider leaves the settings unchanged: the selection is
// still what the person chose, and the next run fails with a clear "no API key saved" error
// until a key is saved or another provider is chosen.
type SettingsKeeper struct {
	deps SettingsDeps
}

// NewSettingsKeeper returns a keeper.
func NewSettingsKeeper(deps SettingsDeps) *SettingsKeeper {
	return &SettingsKeeper{deps: deps}
}

// Settings returns the settings to apply to the next run: the saved ones, or the defaults
// when none are saved or what was saved is no longer valid.
func (k *SettingsKeeper) Settings(ctx context.Context) (domain.ModelSettings, error) {
	settings, err := k.deps.Store.LoadSettings(ctx)
	if errors.Is(err, ports.ErrSettingsNotSaved) || errors.Is(err, ports.ErrSavedSettingsInvalid) {
		return domain.DefaultModelSettings(), nil
	}
	if err != nil {
		return domain.ModelSettings{}, err
	}
	return settings, nil
}

// Current returns the effective settings and the state of every provider. A keychain that
// cannot be read reports no key saved rather than failing, so the CLI providers stay usable
// on a machine without a working keychain; saving a key there still fails with the reason.
func (k *SettingsKeeper) Current(ctx context.Context) (SettingsView, error) {
	settings, err := k.Settings(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	view := SettingsView{
		Provider:       settings.Provider,
		ReadingLevel:   settings.ReadingLevel,
		Pinned:         k.deps.Pinned,
		PinnedProvider: k.deps.PinnedProvider,
	}
	for _, p := range domain.ModelProviders() {
		status := ProviderStatus{ID: p, Label: p.Label(), Available: k.deps.Availability.Available(p), NeedsKey: p.NeedsKey()}
		if p.NeedsKey() {
			saved, err := k.deps.Secrets.HasKey(p)
			status.KeySaved = err == nil && saved
		}
		view.Providers = append(view.Providers, status)
	}
	return view, nil
}

// Save makes provider and readingLevel the settings for the next run. A non-empty apiKey is
// saved for the provider first. An API provider cannot be saved without a key, and an
// unavailable provider cannot be chosen. The key is saved before the settings, so if saving
// the settings then fails, the key stays saved and retrying succeeds.
//
// While the app is pinned, runs ignore the saved provider, so it is saved without the
// availability and key checks; that keeps the reading level savable. Unknown values and a
// key given for a provider that takes none are still rejected.
func (k *SettingsKeeper) Save(ctx context.Context, provider, readingLevel, apiKey string) (SettingsView, error) {
	settings, err := domain.ParseModelSettings(provider, readingLevel)
	if err != nil {
		return SettingsView{}, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	p := settings.Provider
	key := strings.TrimSpace(apiKey)
	if key != "" && !p.NeedsKey() {
		return SettingsView{}, fmt.Errorf("%w: %s does not use an API key", ErrInvalidSettings, p.Label())
	}
	pinned := k.deps.Pinned != ""
	if !pinned && !k.deps.Availability.Available(p) {
		return SettingsView{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, p.Label())
	}
	if p.NeedsKey() && (!pinned || key != "") {
		if err := k.keep(p, key); err != nil {
			return SettingsView{}, err
		}
	}
	if err := k.deps.Store.SaveSettings(ctx, settings); err != nil {
		return SettingsView{}, err
	}
	return k.Current(ctx)
}

// keep saves key for p when one is given, and otherwise requires one to be saved already.
func (k *SettingsKeeper) keep(p domain.ModelProvider, key string) error {
	if key == "" {
		has, err := k.deps.Secrets.HasKey(p)
		if err != nil {
			return fmt.Errorf("check API key for %s: %w", p.Label(), err)
		}
		if !has {
			return fmt.Errorf("%w: save an API key for %s", ErrKeyRequired, p.Label())
		}
		return nil
	}
	if err := checkKey(key); err != nil {
		return err
	}
	if err := k.deps.Secrets.SaveKey(p, key); err != nil {
		return fmt.Errorf("save API key for %s: %w", p.Label(), err)
	}
	return nil
}

// checkKey rejects a key no provider issues. Its errors never repeat the key.
func checkKey(key string) error {
	if len(key) > maxKeyBytes {
		return fmt.Errorf("%w: the API key is longer than %d characters", ErrInvalidSettings, maxKeyBytes)
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("%w: the API key may hold only printable ASCII without spaces", ErrInvalidSettings)
		}
	}
	return nil
}

// ClearKey removes the provider's saved key. The settings stay as they are.
func (k *SettingsKeeper) ClearKey(ctx context.Context, provider string) (SettingsView, error) {
	p, err := domain.ParseModelProvider(provider)
	if err != nil {
		return SettingsView{}, fmt.Errorf("%w: %w", ErrInvalidSettings, err)
	}
	if !p.NeedsKey() {
		return SettingsView{}, fmt.Errorf("%w: %s does not use an API key", ErrInvalidSettings, p.Label())
	}
	if err := k.deps.Secrets.ClearKey(p); err != nil {
		return SettingsView{}, fmt.Errorf("clear API key for %s: %w", p.Label(), err)
	}
	return k.Current(ctx)
}
