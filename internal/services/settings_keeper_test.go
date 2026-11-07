package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const testKey = "sk-ant-secret-0123456789"

type settingsStore struct {
	saved   *domain.ModelSettings
	loadErr error
	saveErr error
	saves   int
}

func (s *settingsStore) LoadSettings(context.Context) (domain.ModelSettings, error) {
	if s.loadErr != nil {
		return domain.ModelSettings{}, s.loadErr
	}
	if s.saved == nil {
		return domain.ModelSettings{}, ports.ErrSettingsNotSaved
	}
	return *s.saved, nil
}

func (s *settingsStore) SaveSettings(_ context.Context, settings domain.ModelSettings) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saves++
	s.saved = &settings
	return nil
}

type secretStore struct {
	keys map[domain.ModelProvider]string
	err  error
}

func newSecretStore() *secretStore { return &secretStore{keys: map[domain.ModelProvider]string{}} }

func (s *secretStore) SaveKey(p domain.ModelProvider, key string) error {
	if s.err != nil {
		return s.err
	}
	s.keys[p] = key
	return nil
}
func (s *secretStore) HasKey(p domain.ModelProvider) (bool, error) {
	_, ok := s.keys[p]
	return ok, s.err
}
func (s *secretStore) LoadKey(p domain.ModelProvider) (string, error) {
	key, ok := s.keys[p]
	if !ok {
		return "", ports.ErrKeyNotSaved
	}
	return key, nil
}
func (s *secretStore) ClearKey(p domain.ModelProvider) error {
	if s.err != nil {
		return s.err
	}
	delete(s.keys, p)
	return nil
}

type availability map[domain.ModelProvider]bool

func (a availability) Available(p domain.ModelProvider) bool { return a[p] }

func allAvailable() availability {
	return availability{domain.ProviderClaudeCode: true, domain.ProviderCodex: true, domain.ProviderAnthropicAPI: true, domain.ProviderOpenAIAPI: true}
}

func newKeeper(store *settingsStore, secrets *secretStore, avail availability) *SettingsKeeper {
	return NewSettingsKeeper(SettingsDeps{Store: store, Secrets: secrets, Availability: avail})
}

// noKey fails the test if the key appears anywhere in the view or error.
func noKey(t *testing.T, view SettingsView, err error) {
	t.Helper()
	encoded, _ := json.Marshal(view)
	if strings.Contains(string(encoded), testKey) || strings.Contains(fmt.Sprintf("%+v", view), testKey) {
		t.Errorf("view holds the key: %s", encoded)
	}
	if err != nil && strings.Contains(err.Error(), testKey) {
		t.Errorf("error holds the key: %v", err)
	}
}

func TestUnsavedSettingsAreTheDefaults(t *testing.T) {
	k := newKeeper(&settingsStore{}, newSecretStore(), allAvailable())
	got, err := k.Settings(context.Background())
	if err != nil || got != domain.DefaultModelSettings() {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestInvalidSavedSettingsFallBackToTheDefaults(t *testing.T) {
	store := &settingsStore{loadErr: fmt.Errorf("%w: %w", ports.ErrSavedSettingsInvalid, domain.ErrUnknownProvider)}
	k := newKeeper(store, newSecretStore(), allAvailable())
	got, err := k.Settings(context.Background())
	if err != nil || got != domain.DefaultModelSettings() {
		t.Errorf("got %+v, %v", got, err)
	}
	view, err := k.Current(context.Background())
	if err != nil || view.Provider != domain.ProviderClaudeCode || view.ReadingLevel != domain.ReadingSixthGrade {
		t.Errorf("view %+v, %v", view, err)
	}
}

func TestAStoreFailureIsReportedNotHidden(t *testing.T) {
	failure := errors.New("disk on fire")
	k := newKeeper(&settingsStore{loadErr: failure}, newSecretStore(), allAvailable())
	if _, err := k.Settings(context.Background()); !errors.Is(err, failure) {
		t.Errorf("Settings: %v", err)
	}
	if _, err := k.Current(context.Background()); !errors.Is(err, failure) {
		t.Errorf("Current: %v", err)
	}
}

func TestCurrentListsEveryProviderInOrder(t *testing.T) {
	secrets := newSecretStore()
	secrets.keys[domain.ProviderOpenAIAPI] = testKey
	saved := domain.ModelSettings{Provider: domain.ProviderOpenAIAPI, ReadingLevel: domain.ReadingPhD}
	avail := availability{domain.ProviderClaudeCode: true, domain.ProviderAnthropicAPI: true, domain.ProviderOpenAIAPI: true}
	k := NewSettingsKeeper(SettingsDeps{Store: &settingsStore{saved: &saved}, Secrets: secrets, Availability: avail, Pinned: "scripted:ok"})
	view, err := k.Current(context.Background())
	noKey(t, view, err)
	if err != nil {
		t.Fatal(err)
	}
	if view.Provider != saved.Provider || view.ReadingLevel != saved.ReadingLevel || view.Pinned != "scripted:ok" {
		t.Errorf("view %+v", view)
	}
	want := []ProviderStatus{
		{ID: domain.ProviderClaudeCode, Label: "Claude Code", Available: true},
		{ID: domain.ProviderCodex, Label: "Codex"},
		{ID: domain.ProviderAnthropicAPI, Label: "Anthropic API", Available: true, NeedsKey: true},
		{ID: domain.ProviderOpenAIAPI, Label: "OpenAI API", Available: true, NeedsKey: true, KeySaved: true},
	}
	if len(view.Providers) != len(want) {
		t.Fatalf("providers %+v", view.Providers)
	}
	for i := range want {
		if view.Providers[i] != want[i] {
			t.Errorf("provider %d: got %+v, want %+v", i, view.Providers[i], want[i])
		}
	}
}

func TestTheViewEncodesToTheSettingsContract(t *testing.T) {
	k := newKeeper(&settingsStore{}, newSecretStore(), allAvailable())
	view, err := k.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(view)
	for _, field := range []string{`"provider":"claude-code"`, `"readingLevel":"6th-grade"`, `"pinned":""`,
		`"id":"codex"`, `"label":"Codex"`, `"available":true`, `"needsKey":false`, `"keySaved":false`} {
		if !strings.Contains(string(encoded), field) {
			t.Errorf("missing %s in %s", field, encoded)
		}
	}
}

func TestSavingPersistsTheChoice(t *testing.T) {
	store := &settingsStore{}
	k := newKeeper(store, newSecretStore(), allAvailable())
	view, err := k.Save(context.Background(), "codex", "college", "")
	if err != nil {
		t.Fatal(err)
	}
	want := domain.ModelSettings{Provider: domain.ProviderCodex, ReadingLevel: domain.ReadingCollege}
	if store.saved == nil || *store.saved != want {
		t.Errorf("saved %+v", store.saved)
	}
	if view.Provider != want.Provider || view.ReadingLevel != want.ReadingLevel {
		t.Errorf("view %+v", view)
	}
}

func TestSavingRejectsUnknownValues(t *testing.T) {
	for _, c := range []struct{ provider, level string }{{"gemini", "phd"}, {"codex", "genius"}, {"", ""}} {
		store := &settingsStore{}
		_, err := newKeeper(store, newSecretStore(), allAvailable()).Save(context.Background(), c.provider, c.level, "")
		if !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%+v: want ErrInvalidSettings, got %v", c, err)
		}
		if store.saves != 0 {
			t.Errorf("%+v: invalid settings were saved", c)
		}
	}
	_, err := newKeeper(&settingsStore{}, newSecretStore(), allAvailable()).Save(context.Background(), "gemini", "phd", "")
	if !errors.Is(err, domain.ErrUnknownProvider) {
		t.Errorf("the domain reason is lost: %v", err)
	}
}

func TestAnUnavailableProviderCannotBeChosen(t *testing.T) {
	store := &settingsStore{}
	secrets := newSecretStore()
	avail := allAvailable()
	avail[domain.ProviderCodex] = false
	avail[domain.ProviderAnthropicAPI] = false
	k := newKeeper(store, secrets, avail)
	if _, err := k.Save(context.Background(), "codex", "phd", ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("codex: %v", err)
	}
	if _, err := k.Save(context.Background(), "anthropic-api", "phd", testKey); !errors.Is(err, ErrProviderUnavailable) {
		t.Errorf("anthropic: %v", err)
	}
	if store.saves != 0 || len(secrets.keys) != 0 {
		t.Errorf("something was saved: %d settings, %d keys", store.saves, len(secrets.keys))
	}
}

func TestAnAPIProviderNeedsAKey(t *testing.T) {
	store := &settingsStore{}
	k := newKeeper(store, newSecretStore(), allAvailable())
	for _, key := range []string{"", "   \n"} {
		_, err := k.Save(context.Background(), "anthropic-api", "phd", key)
		if !errors.Is(err, ErrKeyRequired) {
			t.Errorf("key %q: want ErrKeyRequired, got %v", key, err)
		}
	}
	if store.saves != 0 {
		t.Error("settings were saved without a key")
	}
}

func TestAGivenKeyIsSavedBeforeTheSettings(t *testing.T) {
	store := &settingsStore{}
	secrets := newSecretStore()
	k := newKeeper(store, secrets, allAvailable())
	view, err := k.Save(context.Background(), "anthropic-api", "phd", "  "+testKey+"\n")
	noKey(t, view, err)
	if err != nil {
		t.Fatal(err)
	}
	if secrets.keys[domain.ProviderAnthropicAPI] != testKey {
		t.Errorf("key saved as %q", secrets.keys[domain.ProviderAnthropicAPI])
	}
	if store.saved == nil || store.saved.Provider != domain.ProviderAnthropicAPI {
		t.Errorf("settings %+v", store.saved)
	}
	if !view.Providers[2].KeySaved {
		t.Errorf("view does not report the key saved: %+v", view.Providers[2])
	}

	// With the key saved, the provider can be saved again without one.
	if _, err := k.Save(context.Background(), "anthropic-api", "college", ""); err != nil {
		t.Errorf("resaving with a saved key: %v", err)
	}
}

func TestAKeyThatCannotBeSavedStopsTheSave(t *testing.T) {
	store := &settingsStore{}
	secrets := newSecretStore()
	secrets.err = errors.New("keychain locked")
	view, err := newKeeper(store, secrets, allAvailable()).Save(context.Background(), "openai-api", "phd", testKey)
	noKey(t, view, err)
	if err == nil || store.saves != 0 {
		t.Errorf("settings saved after the key failed: %v", err)
	}
}

func TestMalformedKeysAreRejectedWithoutBeingRepeated(t *testing.T) {
	for name, key := range map[string]string{
		"space":     "sk-ant secret",
		"control":   testKey + "\x00",
		"non-ascii": testKey + "é",
		"too long":  testKey + strings.Repeat("a", maxKeyBytes),
	} {
		secrets := newSecretStore()
		view, err := newKeeper(&settingsStore{}, secrets, allAvailable()).Save(context.Background(), "openai-api", "phd", key)
		noKey(t, view, err)
		if !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%s: want ErrInvalidSettings, got %v", name, err)
		}
		if len(secrets.keys) != 0 {
			t.Errorf("%s: key was saved", name)
		}
	}
}

func TestACLIProviderTakesNoKey(t *testing.T) {
	store := &settingsStore{}
	secrets := newSecretStore()
	k := newKeeper(store, secrets, allAvailable())
	view, err := k.Save(context.Background(), "claude-code", "phd", testKey)
	noKey(t, view, err)
	if !errors.Is(err, ErrInvalidSettings) || store.saves != 0 || len(secrets.keys) != 0 {
		t.Errorf("got %v, %d saves, %d keys", err, store.saves, len(secrets.keys))
	}
	if _, err := k.ClearKey(context.Background(), "codex"); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("clearing a CLI provider's key: %v", err)
	}
}

func TestClearingTheSelectedProvidersKeyKeepsTheSettings(t *testing.T) {
	store := &settingsStore{}
	secrets := newSecretStore()
	k := newKeeper(store, secrets, allAvailable())
	if _, err := k.Save(context.Background(), "openai-api", "phd", testKey); err != nil {
		t.Fatal(err)
	}
	view, err := k.ClearKey(context.Background(), "openai-api")
	noKey(t, view, err)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets.keys[domain.ProviderOpenAIAPI]; ok {
		t.Error("key was not cleared")
	}
	if view.Provider != domain.ProviderOpenAIAPI || view.Providers[3].KeySaved {
		t.Errorf("view %+v", view)
	}
	if store.saved.Provider != domain.ProviderOpenAIAPI {
		t.Errorf("settings changed: %+v", store.saved)
	}
	// Clearing again is not an error.
	if _, err := k.ClearKey(context.Background(), "openai-api"); err != nil {
		t.Errorf("clearing an absent key: %v", err)
	}
}

func TestClearingAnUnknownProvidersKeyIsInvalid(t *testing.T) {
	if _, err := newKeeper(&settingsStore{}, newSecretStore(), allAvailable()).ClearKey(context.Background(), "../gemini"); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("got %v", err)
	}
}

func TestAKeychainFailureIsReported(t *testing.T) {
	secrets := newSecretStore()
	secrets.err = errors.New("keychain locked")
	k := newKeeper(&settingsStore{}, secrets, allAvailable())
	if _, err := k.Current(context.Background()); !errors.Is(err, secrets.err) {
		t.Errorf("Current: %v", err)
	}
	if _, err := k.Save(context.Background(), "openai-api", "phd", ""); !errors.Is(err, secrets.err) {
		t.Errorf("Save: %v", err)
	}
	if _, err := k.ClearKey(context.Background(), "openai-api"); !errors.Is(err, secrets.err) {
		t.Errorf("ClearKey: %v", err)
	}
}
