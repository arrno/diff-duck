package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services"
)

var _ SettingsKeeper = (*services.SettingsKeeper)(nil)

const testKey = "sk-ant-secret-0123456789"

// fakeSettings records what the server asked for and answers with err, or with view.
type fakeSettings struct {
	err   error
	calls []string
}

var view = services.SettingsView{
	Provider:     domain.ProviderAnthropicAPI,
	ReadingLevel: domain.ReadingCollege,
	Pinned:       "scripted:normal",
	Providers: []services.ProviderStatus{
		{ID: domain.ProviderClaudeCode, Label: "Claude Code", Available: true},
		{ID: domain.ProviderAnthropicAPI, Label: "Anthropic API", Available: true, NeedsKey: true, KeySaved: true},
	},
}

func (f *fakeSettings) Current(context.Context) (services.SettingsView, error) {
	f.calls = append(f.calls, "current")
	return f.answer()
}

func (f *fakeSettings) Save(_ context.Context, provider, readingLevel, apiKey string) (services.SettingsView, error) {
	f.calls = append(f.calls, fmt.Sprintf("save %s %s key=%t", provider, readingLevel, apiKey == testKey))
	return f.answer()
}

func (f *fakeSettings) ClearKey(_ context.Context, provider string) (services.SettingsView, error) {
	f.calls = append(f.calls, "clear "+provider)
	return f.answer()
}

func (f *fakeSettings) answer() (services.SettingsView, error) {
	if f.err != nil {
		return services.SettingsView{}, f.err
	}
	return view, nil
}

var jsonType = map[string]string{"Content-Type": "application/json"}

func saveBody(key string) string {
	return `{"provider":"anthropic-api","readingLevel":"college","apiKey":"` + key + `"}`
}

func TestSettingsAreReadSavedAndClearedThroughTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		method, path, body string
		want               string
	}{
		"read":                   {"GET", "/api/settings", "", "current"},
		"save with a key":        {"PUT", "/api/settings", saveBody(testKey), "save anthropic-api college key=true"},
		"save keeping the key":   {"PUT", "/api/settings", `{"provider":"anthropic-api","readingLevel":"college"}`, "save anthropic-api college key=false"},
		"clear a provider's key": {"DELETE", "/api/settings/keys/openai-api", "", "clear openai-api"},
	} {
		fake := &fakeSettings{}
		w := do(handlerWith(t, fake), tc.method, tc.path, tc.body, jsonType)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: got %d: %s", name, w.Code, w.Body)
		}
		var got services.SettingsView
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Provider != view.Provider || got.ReadingLevel != view.ReadingLevel || got.Pinned != view.Pinned || len(got.Providers) != len(view.Providers) || got.Providers[1] != view.Providers[1] {
			t.Errorf("%s: got %+v", name, got)
		}
		if strings.Join(fake.calls, ";") != tc.want {
			t.Errorf("%s: service saw %q, want %q", name, fake.calls, tc.want)
		}
	}
}

func TestSettingsFailuresTheUserCanFixAreExplainedAndOthersAreNot(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		want    int
		message string
	}{
		"invalid":     {fmt.Errorf("%w: unknown reading level", services.ErrInvalidSettings), http.StatusBadRequest, "invalid settings: unknown reading level"},
		"unavailable": {fmt.Errorf("%w: Codex", services.ErrProviderUnavailable), http.StatusBadRequest, "model provider is not available: Codex"},
		"no key":      {fmt.Errorf("%w: save an API key", services.ErrKeyRequired), http.StatusBadRequest, "an API key is required: save an API key"},
		"keychain":    {errors.New("could not read the OS keychain at /private/path"), http.StatusInternalServerError, "Something went wrong on the server."},
	} {
		for _, req := range []struct{ method, path, body string }{
			{"GET", "/api/settings", ""},
			{"PUT", "/api/settings", saveBody("")},
			{"DELETE", "/api/settings/keys/openai-api", ""},
		} {
			w := do(handlerWith(t, &fakeSettings{err: tc.err}), req.method, req.path, req.body, jsonType)
			var body struct{ Error string }
			_ = json.Unmarshal(w.Body.Bytes(), &body)
			if w.Code != tc.want || body.Error != tc.message {
				t.Errorf("%s %s: got %d %q, want %d %q", name, req.method, w.Code, body.Error, tc.want, tc.message)
			}
		}
	}
}

// captureStderr returns what fn writes to the process's standard error.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	return <-done
}

func TestASubmittedKeyIsNeverWrittenBackOrLogged(t *testing.T) {
	// Errors that quote the key stand for a service that someday breaks its promise not to.
	quoting := func(sentinel error) error { return fmt.Errorf("%w: key %s rejected", sentinel, testKey) }
	cases := map[string]struct {
		err     error
		body    string
		headers map[string]string
	}{
		"saved":                {nil, saveBody(testKey), jsonType},
		"saved, padded key":    {quoting(services.ErrInvalidSettings), saveBody("  " + testKey + " "), jsonType},
		"invalid":              {quoting(services.ErrInvalidSettings), saveBody(testKey), jsonType},
		"unavailable":          {quoting(services.ErrProviderUnavailable), saveBody(testKey), jsonType},
		"key required":         {quoting(services.ErrKeyRequired), saveBody(testKey), jsonType},
		"server failure":       {errors.New("keychain said " + testKey), saveBody(testKey), jsonType},
		"unknown field":        {nil, `{"apiKey":"` + testKey + `","extra":1}`, jsonType},
		"wrong type":           {nil, `{"provider":1,"apiKey":"` + testKey + `"}`, jsonType},
		"malformed JSON":       {nil, `{"apiKey":"` + testKey + `"`, jsonType},
		"trailing data":        {nil, saveBody(testKey) + `"` + testKey + `"`, jsonType},
		"not JSON":             {nil, saveBody(testKey), map[string]string{"Content-Type": "text/plain"}},
		"too large":            {nil, `{"apiKey":"` + testKey + strings.Repeat("x", maxBodyBytes) + `"}`, jsonType},
		"another site sent it": {nil, saveBody(testKey), map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}},
	}
	for name, tc := range cases {
		var body string
		logged := captureStderr(t, func() {
			body = do(handlerWith(t, &fakeSettings{err: tc.err}), "PUT", "/api/settings", tc.body, tc.headers).Body.String()
		})
		if strings.Contains(body, testKey) || strings.Contains(body, "sk-ant") {
			t.Errorf("%s: the response repeats the key: %s", name, body)
		}
		if strings.Contains(logged, testKey) {
			t.Errorf("%s: the key was logged: %s", name, logged)
		}
	}
}

func TestMalformedSettingsRequestsNeverReachTheService(t *testing.T) {
	for name, tc := range map[string]struct {
		body    string
		headers map[string]string
		want    int
	}{
		"a form post":   {saveBody(""), map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		"unknown field": {`{"provider":"codex","readingLevel":"phd","model":"x"}`, jsonType, http.StatusBadRequest},
		"too large":     {`{"provider":"` + strings.Repeat("x", maxBodyBytes) + `"}`, jsonType, http.StatusBadRequest},
		"trailing data": {saveBody("") + saveBody(""), jsonType, http.StatusBadRequest},
	} {
		fake := &fakeSettings{}
		if w := do(handlerWith(t, fake), "PUT", "/api/settings", tc.body, tc.headers); w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", name, w.Code, tc.want)
		}
		if len(fake.calls) != 0 {
			t.Errorf("%s: reached the service: %q", name, fake.calls)
		}
	}
}

func TestSettingsRequestsNotMeantForThisServerAreRefused(t *testing.T) {
	with := func(k, v string) map[string]string {
		return map[string]string{"Content-Type": "application/json", k: v}
	}
	for name, tc := range map[string]struct {
		method, path string
		headers      map[string]string
	}{
		"another site saving":            {"PUT", "/api/settings", with("Origin", "https://evil.example")},
		"another local port saving":      {"PUT", "/api/settings", with("Origin", "http://127.0.0.1:3000")},
		"a sandboxed page saving":        {"PUT", "/api/settings", with("Origin", "null")},
		"cross-site save with no origin": {"PUT", "/api/settings", with("Sec-Fetch-Site", "cross-site")},
		"DNS rebinding to save":          {"PUT", "/api/settings", with("Host", "evil.example:7474")},
		"another site clearing a key":    {"DELETE", "/api/settings/keys/openai-api", with("Origin", "https://evil.example")},
		"sibling site clearing a key":    {"DELETE", "/api/settings/keys/openai-api", with("Sec-Fetch-Site", "same-site")},
		"DNS rebinding to clear a key":   {"DELETE", "/api/settings/keys/openai-api", with("Host", "evil.example:7474")},
		"another site reading":           {"GET", "/api/settings", with("Sec-Fetch-Site", "cross-site")},
		"DNS rebinding to read":          {"GET", "/api/settings", with("Host", "evil.example")},
	} {
		fake := &fakeSettings{}
		if w := do(handlerWith(t, fake), tc.method, tc.path, saveBody(""), tc.headers); w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", name, w.Code)
		}
		if len(fake.calls) != 0 {
			t.Errorf("%s: reached the service: %q", name, fake.calls)
		}
	}
	// The same requests from this server's own page succeed, so the guard is what refused them.
	own := with("Origin", "http://"+ownHost)
	own["Sec-Fetch-Site"] = "same-origin"
	for _, req := range []struct{ method, path string }{{"PUT", "/api/settings"}, {"DELETE", "/api/settings/keys/openai-api"}, {"GET", "/api/settings"}} {
		if w := do(handlerWith(t, &fakeSettings{}), req.method, req.path, saveBody(""), own); w.Code != http.StatusOK {
			t.Errorf("own page %s: got %d: %s", req.method, w.Code, w.Body)
		}
	}
}
