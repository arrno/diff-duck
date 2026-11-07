package cli

import (
	"errors"
	"io"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
)

func TestCLIProvidersAreAvailableOnlyWhenTheirBinaryIsOnPath(t *testing.T) {
	onPath := map[string]bool{"codex": true}
	a := pathAvailability{lookPath: func(file string) (string, error) {
		if onPath[file] {
			return "/bin/" + file, nil
		}
		return "", errors.New("not found")
	}}
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

func TestOnlyAnExplicitAdapterPinsTheProvider(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default":               {nil, ""},
		"explicit":              {[]string{"--adapter", "scripted:normal"}, "scripted:normal"},
		"explicit default name": {[]string{"--adapter=claude-cli"}, "claude-cli"},
		"other flags only":      {[]string{"--port", "0", "--model", "m"}, ""},
	} {
		var opts serveOptions
		fs := serveFlags(&opts, io.Discard, "serve")
		if _, ok := parseServe(fs, tc.args, &opts); !ok {
			t.Fatalf("%s: did not parse", name)
		}
		if got := pinnedAdapter(fs); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}
