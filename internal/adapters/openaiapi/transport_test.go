package openaiapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/arrno/diff-duck/internal/services/ports"
)

// reply is one scripted server answer.
type reply struct {
	status  int
	header  map[string]string
	body    string
	onServe func()
}

// scripted serves replies in order, repeating the last, and counts requests.
func scripted(t *testing.T, replies ...reply) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		i := int(n.Add(1)) - 1
		if i >= len(replies) {
			i = len(replies) - 1
		}
		rp := replies[i]
		if rp.onServe != nil {
			rp.onServe()
		}
		for k, v := range rp.header {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(rp.status)
		_, _ = w.Write([]byte(rp.body))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func ok() reply { return reply{status: 200, body: response("completed", "", text(`{"ok":true}`))} }

func TestTheWireRequestIsExactlyWhatDiffDuckMeans(t *testing.T) {
	var body map[string]any
	var hdr http.Header
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		hdr, path = r.Header.Clone(), r.URL.Path
		_, _ = w.Write([]byte(response("completed", "", text(`{"ok":true}`))))
	}))
	t.Cleanup(srv.Close)
	m, err := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL+"/v1/"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(context.Background(), ports.ModelRequest{System: "S", User: "U", Schema: schema}); err != nil {
		t.Fatal(err)
	}
	if path != "/v1/responses" {
		t.Fatalf("path: %s", path)
	}
	if hdr.Get("Authorization") != "Bearer "+key || hdr.Get("User-Agent") != "diff-duck" || hdr.Get("Content-Type") != "application/json" {
		t.Fatalf("headers: %v", hdr)
	}
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// No tools, tool_choice, previous_response_id, or reasoning when effort is empty.
	if strings.Join(keys, ",") != "input,instructions,max_output_tokens,model,store,text" {
		t.Fatalf("top-level fields: %v", keys)
	}
	if body["store"] != false {
		t.Fatalf("store must be false: %v", body["store"])
	}
	format := body["text"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["name"] != "response" || format["strict"] != true {
		t.Fatalf("format: %v", format)
	}
	if got, _ := json.Marshal(format["schema"]); string(got) != mustCompact(t, schema) {
		t.Fatalf("schema sent: %s", got)
	}
	if defaultBaseURL+"/responses" != "https://api.openai.com/v1/responses" {
		t.Fatalf("default endpoint: %s", defaultBaseURL)
	}
	if newClient(key).http.Timeout <= 0 {
		t.Fatal("the default client must have a timeout")
	}
}

func mustCompact(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func TestRetriesHonorRetryAfterThenSucceed(t *testing.T) {
	srv, n := scripted(t,
		reply{status: 429, header: map[string]string{"Retry-After": "0.02"}, body: `{"error":{"code":"rate_limit_exceeded","message":"slow"}}`},
		reply{status: 500, header: map[string]string{"Retry-After-Ms": "20"}, body: `{"error":{"type":"server_error","message":"oops"}}`},
		ok(),
	)
	// An hour of default backoff: only the server's Retry-After keeps this test fast.
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Hour))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := m.Complete(ctx, ports.ModelRequest{Schema: schema})
	if err != nil {
		t.Fatalf("after retries: %v", err)
	}
	if string(resp.JSON) != `{"ok":true}` || n.Load() != 3 {
		t.Fatalf("resp=%s attempts=%d", resp.JSON, n.Load())
	}
}

func TestRetriesAreBounded(t *testing.T) {
	srv, n := scripted(t, reply{status: 503, body: `{"error":{"type":"server_error","message":"down"}}`})
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Millisecond))
	_, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema})
	if err == nil || !strings.Contains(err.Error(), "503") || n.Load() != maxAttempts {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 409, 422} {
		srv, n := scripted(t, reply{status: status, body: `{"error":{"code":"nope","message":"no"}}`}, ok())
		m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Millisecond))
		if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || n.Load() != 1 {
			t.Fatalf("%d: err=%v attempts=%d", status, err, n.Load())
		}
	}
}

func TestCancellationDuringBackoffStopsAtOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv, n := scripted(t, reply{status: 429, body: `{}`, onServe: func() {
		time.AfterFunc(20*time.Millisecond, cancel)
	}}, ok())
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Hour))
	start := time.Now()
	_, err := m.Complete(ctx, ports.ModelRequest{Schema: schema})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second || n.Load() != 1 {
		t.Fatalf("err=%v elapsed=%v attempts=%d", err, time.Since(start), n.Load())
	}
}

func TestOversizedBodiesAreBounded(t *testing.T) {
	huge := response("completed", "", text(`{"ok":"`+strings.Repeat("x", maxBodyBytes)+`"}`))
	srv, _ := scripted(t, reply{status: 200, body: huge})
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL))
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("an oversized success body must fail: %v", err)
	}
	// An error body is read only up to its own bound and reported briefly.
	srv, _ = scripted(t, reply{status: 400, body: `{"error":{"message":"` + strings.Repeat("y", 4<<20) + `"}}`})
	m, _ = newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL))
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || len(err.Error()) > 600 {
		t.Fatalf("an oversized error body must be bounded: %.200v", err)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte(response("completed", "", text(`{"ok":true}`))))
	}))
	t.Cleanup(other.Close)
	srv, _ := scripted(t, reply{status: 307, header: map[string]string{"Location": other.URL + "/responses"}})
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL))
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || elsewhere.Load() != 0 {
		t.Fatalf("a redirect must fail without resending the request: err=%v forwarded=%d", err, elsewhere.Load())
	}
}

func TestTheKeyIsAbsentFromEveryError(t *testing.T) {
	masked := "sk-test-SE**********6789"
	echo := `{"error":{"type":"` + key + `","code":"` + key + `","message":"key ` + key + ` aka ` + masked + `"}}`
	for _, status := range []int{400, 401, 403, 404, 409, 422, 429, 500, 502, 503} {
		srv, _ := scripted(t, reply{status: status, body: echo})
		m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Millisecond))
		_, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema})
		if err == nil || strings.Contains(err.Error(), key) || errors.Unwrap(err) != nil {
			t.Fatalf("%d: %v", status, err)
		}
		if (status == 401 || status == 403) && strings.Contains(err.Error(), "6789") {
			t.Fatalf("%d: an auth error must drop the message: %v", status, err)
		}
	}
	// Server text inside a 200 reply is scrubbed too.
	for _, body := range []string{
		`{"status":"failed","error":{"code":"` + key + `"}}`,
		`{"status":"incomplete","incomplete_details":{"reason":"` + key + `"}}`,
		`{"status":"` + key + `"}`,
	} {
		srv, _ := scripted(t, reply{status: 200, body: body})
		m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL))
		_, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema})
		if err == nil || strings.Contains(err.Error(), key) {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		h    http.Header
		want time.Duration
	}{
		{http.Header{"Retry-After": {"2"}}, 2 * time.Second},
		{http.Header{"Retry-After": {now.Add(3 * time.Second).Format(http.TimeFormat)}}, 3 * time.Second},
		{http.Header{"Retry-After-Ms": {"150"}, "Retry-After": {"9"}}, 150 * time.Millisecond},
		{http.Header{"Retry-After": {"3600"}}, 0},
		{http.Header{"Retry-After": {"-1"}}, 0},
		{http.Header{"Retry-After": {"soon"}}, 0},
		{http.Header{}, 0},
	} {
		if got := retryAfter(c.h, now); got != c.want {
			t.Errorf("%v: got %v want %v", c.h, got, c.want)
		}
	}
	c := newClient(key)
	for n := 1; n <= 10; n++ {
		if d := c.backoffFor(n); d <= 0 || d > maxBackoff {
			t.Fatalf("backoff %d: %v", n, d)
		}
	}
}

func TestABrokenSuccessBodyIsNotResent(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		// Promise more than is sent, then drop the connection mid-body.
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"completed"`))
		conn, _, _ := w.(http.Hijacker).Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Millisecond))
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || n.Load() != 1 {
		t.Fatalf("err=%v attempts=%d", err, n.Load())
	}
}

func TestAnErrorBodyIsReadOnlyUpToItsBound(t *testing.T) {
	// The error object starts past the bound, so a bounded read never sees it.
	body := strings.Repeat(" ", maxErrorBodyBytes) + `{"error":{"code":"marker"}}`
	srv, _ := scripted(t, reply{status: 400, body: body})
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL))
	if _, err := m.Complete(context.Background(), ports.ModelRequest{Schema: schema}); err == nil || strings.Contains(err.Error(), "marker") {
		t.Fatalf("the error body must be read only up to its bound: %v", err)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestATimeoutIsNotRetried(t *testing.T) {
	if retryableTransport(timeoutErr{}) {
		t.Fatal("a timeout spent the whole request budget and must not be retried")
	}
	if !retryableTransport(errors.New("connection reset by peer")) {
		t.Fatal("a connection failure is worth another attempt")
	}
}

func TestADeadlineDuringARequestIsReportedAsSuch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server notices the client hanging up only once the body is consumed.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	m, _ := newWithOptions(key, "gpt-test", "", withBaseURL(srv.URL), withBackoff(time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := m.Complete(ctx, ports.ModelRequest{Schema: schema}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
}
