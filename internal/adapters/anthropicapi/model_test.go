package anthropicapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2/option"

	"github.com/arrno/diff-duck/internal/services/ports"
)

func server(t *testing.T, status int, reply string, seen *map[string]any, beta *string) *Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if seen != nil {
			_ = json.Unmarshal(body, seen)
		}
		if beta != nil {
			*beta = r.Header.Get("anthropic-beta")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return newWithOptions("claude-opus-5-5", "medium", option.WithBaseURL(srv.URL), option.WithAPIKey("test"), option.WithMaxRetries(0))
}

func message(stop, text string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "m", "type": "message", "role": "assistant", "model": "claude-opus-5-5", "stop_reason": stop,
		"content": []map[string]any{{"type": "text", "text": text}},
		"usage":   map[string]any{"input_tokens": 5, "cache_read_input_tokens": 2, "output_tokens": 7},
	})
	return string(b)
}

func TestRequestShapeAndResponse(t *testing.T) {
	var seen map[string]any
	var beta string
	m := server(t, 200, message("end_turn", `{"ok":true}`), &seen, &beta)
	resp, err := m.Complete(context.Background(), ports.ModelRequest{System: "SYS", User: "DATA", Schema: []byte(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` || resp.InputTokens != 7 || resp.OutputTokens != 7 {
		t.Fatalf("response: %+v", resp)
	}
	if _, hasTools := seen["tools"]; hasTools {
		t.Fatal("no tools may be declared: analyzed code is untrusted")
	}
	if seen["fallbacks"] != "default" || !strings.Contains(beta, "server-side-fallback-2026-07-01") {
		t.Fatalf("fallbacks=%v beta=%q", seen["fallbacks"], beta)
	}
	output := seen["output_config"].(map[string]any)
	if output["effort"] != "medium" || output["format"].(map[string]any)["schema"] == nil {
		t.Fatalf("output_config: %v", output)
	}
	if !strings.Contains(mustJSON(seen["messages"]), "DATA") || !strings.Contains(mustJSON(seen["system"]), "SYS") {
		t.Fatalf("instructions and data must be separated: %v", seen)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestFailuresAreNotReportedAsSuccess(t *testing.T) {
	ctx := context.Background()
	req := ports.ModelRequest{Schema: []byte(`{}`)}
	if _, err := server(t, 200, message("refusal", ""), nil, nil).Complete(ctx, req); !errors.Is(err, ports.ErrModelRefused) {
		t.Errorf("refusal: %v", err)
	}
	if _, err := server(t, 200, message("max_tokens", `{"a"`), nil, nil).Complete(ctx, req); err == nil {
		t.Error("a truncated response must fail")
	}
	if _, err := server(t, 200, message("end_turn", `not json`), nil, nil).Complete(ctx, req); err == nil {
		t.Error("invalid JSON must fail")
	}
	if _, err := server(t, 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow"}}`, nil, nil).Complete(ctx, req); err == nil || !strings.Contains(err.Error(), "429") {
		t.Errorf("status errors must surface: %v", err)
	}
}

func TestExplicitKeyAuthenticatesAndNeverLeaks(t *testing.T) {
	const key = "sk-ant-test-SECRET-0123456789"
	t.Setenv("ANTHROPIC_API_KEY", "env-key-must-not-be-used")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token-must-not-be-used")
	var headers http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		// A server that echoes the key back must not get it into our error.
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad key ` + key + `"}}`))
	}))
	t.Cleanup(srv.Close)

	m, err := newWithKey(key, "claude-opus-5-5", "medium", option.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Complete(context.Background(), ports.ModelRequest{Schema: []byte(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected a status error: %v", err)
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaks the key: %v", err)
	}
	if strings.Contains(m.Describe(), key) || m.Describe() != "anthropic-api/claude-opus-5-5/medium" {
		t.Fatalf("describe: %s", m.Describe())
	}
	sent := headers.Get("X-Api-Key") + headers.Get("Authorization")
	if !strings.Contains(sent, key) || strings.Contains(sent, "env-") {
		t.Fatalf("only the explicit key may authenticate: x-api-key=%q authorization=%q", headers.Get("X-Api-Key"), headers.Get("Authorization"))
	}

	if _, err := NewWithKey("  ", "claude-opus-5-5", "medium"); err == nil {
		t.Fatal("an empty key must be rejected")
	}

	// A transport failure must not echo the key or expose the SDK error to unwrapping.
	m, _ = newWithKey(key, "claude-opus-5-5", "medium", option.WithBaseURL("http://127.0.0.1:1/"+key+"/"))
	_, err = m.Complete(context.Background(), ports.ModelRequest{Schema: []byte(`{}`)})
	if err == nil || strings.Contains(err.Error(), key) || errors.Unwrap(err) != nil {
		t.Fatalf("transport error: %v", err)
	}
}

// roundTripper answers every request itself, so a test sees where a request was headed
// without anything reaching the network.
type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A keychain key goes only to the Anthropic API, whatever the environment says (K1): a
// gateway another tool set up through ANTHROPIC_BASE_URL or ANTHROPIC_CUSTOM_HEADERS must
// not receive it.
func TestKeychainKeyIgnoresEnvironmentGatewayAndHeaders(t *testing.T) {
	const key = "sk-ant-test-KEYCHAIN-0123456789"
	gatewayHits := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits++
		w.WriteHeader(500)
	}))
	t.Cleanup(gateway.Close)
	t.Setenv("ANTHROPIC_BASE_URL", gateway.URL)
	t.Setenv("ANTHROPIC_CUSTOM_HEADERS", "X-Gateway-Tenant: other-tool")
	t.Setenv("ANTHROPIC_API_KEY", "env-key-must-not-be-used")

	var sent *http.Request
	stand := &http.Client{Transport: roundTripper(func(r *http.Request) (*http.Response, error) {
		sent = r
		return &http.Response{
			StatusCode: 400,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"stand-in"}}`)),
			Request:    r,
		}, nil
	})}
	m, err := newWithKey(key, "claude-opus-5-5", "medium", option.WithHTTPClient(stand))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: []byte(`{}`)}); err == nil {
		t.Fatal("the stand-in answers 400")
	}
	if gatewayHits != 0 {
		t.Fatalf("the environment's gateway received %d requests", gatewayHits)
	}
	if sent == nil || sent.URL.Scheme != "https" || sent.URL.Host != "api.anthropic.com" {
		t.Fatalf("the key must go only to https://api.anthropic.com, got %v", sent)
	}
	if v := sent.Header.Get("X-Gateway-Tenant"); v != "" {
		t.Fatalf("ANTHROPIC_CUSTOM_HEADERS must be ignored, sent %q", v)
	}
	if auth := sent.Header.Get("X-Api-Key") + sent.Header.Get("Authorization"); !strings.Contains(auth, key) || strings.Contains(auth, "env-") {
		t.Fatalf("only the keychain key may authenticate: %q", auth)
	}
}

// The keychain client replaces the SDK's default client, so it must keep its guard against a
// server that accepts the connection and never answers.
func TestKeychainClientBoundsTheWaitForResponseHeaders(t *testing.T) {
	transport, ok := httpClient().Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != responseHeaderTimeout || responseHeaderTimeout <= 0 {
		t.Fatalf("response header timeout: %v", transport)
	}
}

func TestErrorTextIsBounded(t *testing.T) {
	long := strings.Repeat("x", 5000)
	_, err := server(t, 400, `{"type":"error","error":{"type":"invalid_request_error","message":"`+long+`"}}`, nil, nil).Complete(context.Background(), ports.ModelRequest{Schema: []byte(`{}`)})
	if err == nil || len(err.Error()) > 600 || !strings.Contains(err.Error(), "invalid_request_error") {
		t.Fatalf("error must be bounded and keep the type: %d bytes", len(err.Error()))
	}
}

func TestCancellationIsReportedAsSuch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := server(t, 200, message("end_turn", `{}`), nil, nil).Complete(ctx, ports.ModelRequest{Schema: []byte(`{}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
