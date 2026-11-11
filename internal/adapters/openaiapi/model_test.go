package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arrno/diffduck/internal/services/ports"
)

const key = "sk-test-SECRET-0123456789"

type seen struct {
	body map[string]any
	auth string
}

func server(t *testing.T, status int, reply string, got *seen) *Model {
	t.Helper()
	return serverWithEffort(t, "low", status, reply, got)
}

func serverWithEffort(t *testing.T, effort string, status int, reply string, got *seen) *Model {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got != nil {
			_ = json.Unmarshal(body, &got.body)
			got.auth = r.Header.Get("Authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	m, err := newWithOptions(key, "gpt-test", effort, withBaseURL(srv.URL), withBackoff(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func response(status, incomplete string, content ...map[string]any) string {
	r := map[string]any{
		"id": "resp_1", "object": "response", "created_at": 1, "model": "gpt-test", "status": status,
		"output": []map[string]any{{"id": "msg_1", "type": "message", "role": "assistant", "status": "completed", "content": content}},
		"usage": map[string]any{"input_tokens": 11, "output_tokens": 13, "total_tokens": 24,
			"input_tokens_details": map[string]any{"cached_tokens": 4}, "output_tokens_details": map[string]any{"reasoning_tokens": 2}},
	}
	if incomplete != "" {
		r["incomplete_details"] = map[string]any{"reason": incomplete}
	}
	b, _ := json.Marshal(r)
	return string(b)
}

func text(s string) map[string]any {
	return map[string]any{"type": "output_text", "text": s, "annotations": []any{}}
}

var schema = []byte(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)

func TestRequestShapeAndResponse(t *testing.T) {
	var got seen
	m := server(t, 200, response("completed", "", text(`{"ok":true}`)), &got)
	resp, err := m.Complete(context.Background(), ports.ModelRequest{System: "SYS", User: "DATA", Schema: schema})
	if err != nil {
		t.Fatal(err)
	}
	if string(resp.JSON) != `{"ok":true}` || resp.InputTokens != 11 || resp.OutputTokens != 13 {
		t.Fatalf("response: %+v", resp)
	}
	if got.auth != "Bearer "+key {
		t.Fatalf("the explicit key must authenticate the request: %q", got.auth)
	}
	if _, hasTools := got.body["tools"]; hasTools {
		t.Fatal("no tools may be declared: analyzed code is untrusted")
	}
	if got.body["instructions"] != "SYS" || got.body["input"] != "DATA" || got.body["model"] != "gpt-test" || got.body["store"] != false {
		t.Fatalf("instructions and data must be separated: %v", got.body)
	}
	if got.body["max_output_tokens"] != float64(maxOutputTokens) {
		t.Fatalf("max_output_tokens: %v", got.body["max_output_tokens"])
	}
	if got.body["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("reasoning: %v", got.body["reasoning"])
	}
	format := got.body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["strict"] != true || format["schema"].(map[string]any)["additionalProperties"] != false {
		t.Fatalf("format: %v", format)
	}

	var plain seen
	m = serverWithEffort(t, "", 200, response("completed", "", text(`{"ok":true}`)), &plain)
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err != nil {
		t.Fatal(err)
	}
	if _, has := plain.body["reasoning"]; has {
		t.Fatalf("an empty effort must leave the model's default: %v", plain.body["reasoning"])
	}
}

func TestFailuresAreNotReportedAsSuccess(t *testing.T) {
	ctx := context.Background()
	req := ports.ModelRequest{Schema: schema}
	refusal := map[string]any{"type": "refusal", "refusal": "I can't help with that."}
	if _, err := server(t, 200, response("completed", "", refusal), nil).Complete(ctx, req); !errors.Is(err, ports.ErrModelRefused) {
		t.Errorf("refusal: %v", err)
	}
	if _, err := server(t, 200, response("incomplete", "content_filter", text(`{"o`)), nil).Complete(ctx, req); !errors.Is(err, ports.ErrModelRefused) {
		t.Errorf("content filter: %v", err)
	}
	if _, err := server(t, 200, response("incomplete", "max_output_tokens", text(`{"ok":`)), nil).Complete(ctx, req); err == nil || errors.Is(err, ports.ErrModelRefused) {
		t.Errorf("a truncated response must fail: %v", err)
	}
	if _, err := server(t, 200, response("completed", "", text(`not json`)), nil).Complete(ctx, req); err == nil {
		t.Error("invalid JSON must fail")
	}
	if _, err := server(t, 200, response("completed", ""), nil).Complete(ctx, req); err == nil {
		t.Error("empty output must fail")
	}
	if _, err := server(t, 200, response("failed", "", text(`{"ok":true}`)), nil).Complete(ctx, req); err == nil {
		t.Error("a failed response must fail")
	}
	if _, err := server(t, 200, response("cancelled", "", text(`{"ok":true}`)), nil).Complete(ctx, req); err == nil {
		t.Error("a cancelled response must fail")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := server(t, 200, response("completed", "", text(`{"ok":true}`)), nil).Complete(cancelled, req); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation: %v", err)
	}
	long := strings.Repeat("x", 5000)
	if _, err := server(t, 400, `{"error":{"type":"invalid_request_error","code":"bad","message":"`+long+`"}}`, nil).Complete(ctx, req); err == nil || len(err.Error()) > 600 {
		t.Errorf("error text must be bounded: %v", err)
	}
	if _, err := server(t, 429, `{"error":{"type":"requests","code":"rate_limit_exceeded","message":"slow down"}}`, nil).Complete(ctx, req); err == nil || !strings.Contains(err.Error(), "429") || !strings.Contains(err.Error(), "slow down") {
		t.Errorf("status errors must surface: %v", err)
	}
	if _, err := server(t, 200, response("completed", "", text(`{}`)), nil).Complete(ctx, ports.ModelRequest{Schema: []byte(`nope`)}); err == nil {
		t.Error("an unusable schema must fail")
	}
}

func TestTheKeyNeverLeaks(t *testing.T) {
	ctx := context.Background()
	req := ports.ModelRequest{Schema: schema}
	for name, reply := range map[int]string{
		401: `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"Incorrect API key provided: sk-test-SE**********6789."}}`,
		403: `{"error":{"type":"invalid_request_error","code":"forbidden","message":"Key sk-test-SE**********6789 lacks access."}}`,
		400: `{"error":{"type":"invalid_request_error","code":"bad","message":"echo ` + key + `"}}`,
	} {
		m := server(t, name, reply, nil)
		_, err := m.Complete(ctx, req)
		if err == nil {
			t.Fatalf("%d must fail", name)
		}
		if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "6789") || strings.Contains(err.Error(), "sk-test") {
			t.Fatalf("%d: error leaks the key: %v", name, err)
		}
		if strings.Contains(m.Describe(), key) || strings.Contains(m.Describe(), "sk-") {
			t.Fatalf("describe leaks the key: %s", m.Describe())
		}
	}
	// A transport failure must not echo the key either.
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL("http://127.0.0.1:1/"+key+"/"), withBackoff(time.Millisecond))
	if _, err := m.Complete(ctx, req); err == nil || strings.Contains(err.Error(), key) || errors.Unwrap(err) != nil {
		t.Fatalf("transport error: %v", err)
	}
	if _, err := New("", "gpt-test", ""); err == nil {
		t.Fatal("an empty key must be rejected")
	}
	if _, err := New(key, "", ""); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("an empty model must be rejected without echoing the key: %v", err)
	}
}
