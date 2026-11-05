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

	"duck-diff/internal/services/ports"
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
