package openaiapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	// requestTimeout bounds one attempt, response body included. A high-effort answer of
	// maxOutputTokens can take many minutes; this matches the Codex per-call limit.
	requestTimeout = 15 * time.Minute
	// maxAttempts includes the first try, as the SDK's default of two retries did.
	maxAttempts    = 3
	defaultBackoff = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
	// maxRetryAfter is the longest server-requested wait honored; longer ones fall back to
	// exponential backoff.
	maxRetryAfter = 60 * time.Second
	// maxBodyBytes bounds a success body; maxErrorBodyBytes bounds a failure body.
	maxBodyBytes      = 8 << 20
	maxErrorBodyBytes = 64 << 10
	// maxErrorText bounds any server-provided text placed in an error.
	maxErrorText = 400
)

// client sends Responses API requests. It is the only holder of the API key.
type client struct {
	key     string
	baseURL string
	http    *http.Client
	backoff time.Duration
}

// option adjusts a client; it exists for tests.
type option func(*client)

func withBaseURL(u string) option { return func(c *client) { c.baseURL = strings.TrimRight(u, "/") } }

func withBackoff(d time.Duration) option { return func(c *client) { c.backoff = d } }

func newClient(key string, opts ...option) *client {
	c := &client{
		key:     key,
		baseURL: defaultBaseURL,
		http: &http.Client{
			Timeout: requestTimeout,
			// Never follow a redirect: the API does not issue them, and following one would
			// resend the request somewhere else.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		backoff: defaultBackoff,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// apiError is the Responses API error object, both in error bodies and in failed responses.
type apiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// createResponse posts body to /responses and decodes a 2xx reply into out. 429 and 5xx
// replies and connection failures are retried with bounded exponential backoff. Every error
// it returns is newly built and redacted, never wrapping the request or a transport error,
// so nothing that might carry the key reaches a log or the browser (K1). A done ctx is
// returned as ctx.Err().
func (c *client) createResponse(ctx context.Context, body wireRequest, out *wireResponse) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return errors.New("openai api: could not encode the request")
	}
	for attempt := 1; ; attempt++ {
		retry, wait, err := c.attempt(ctx, payload, out)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err == nil || !retry || attempt == maxAttempts {
			return err
		}
		if wait <= 0 {
			wait = c.backoffFor(attempt)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// attempt makes one request. It reports whether a failure may be retried and how long the
// server asked to wait (zero when it did not say).
func (c *client) attempt(ctx context.Context, payload []byte, out *wireResponse) (retry bool, wait time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/responses", bytes.NewReader(payload))
	if err != nil {
		return false, 0, errors.New("openai api: could not build the request")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "diff-duck")

	resp, err := c.http.Do(req)
	if err != nil {
		return retryableTransport(err), 0, fmt.Errorf("openai api: %s", c.redact(transportText(err)))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
		retry = resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500
		return retry, retryAfter(resp.Header, time.Now()), c.statusError(resp.StatusCode, raw)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return retryableTransport(err), 0, fmt.Errorf("openai api: reading the response: %s", c.redact(transportText(err)))
	}
	if len(raw) > maxBodyBytes {
		return false, 0, fmt.Errorf("openai api: the response exceeded %d bytes", maxBodyBytes)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, 0, errors.New("openai api: the response was not a valid Responses API object")
	}
	return false, 0, nil
}

// statusError describes a non-2xx reply. Authentication failures echo part of the key in
// their message, so for 401 and 403 only the code is kept.
func (c *client) statusError(status int, raw []byte) error {
	var body struct {
		Error *apiError `json:"error"`
	}
	detail := ""
	if json.Unmarshal(raw, &body) == nil && body.Error != nil {
		detail = body.Error.Code
		if detail == "" {
			detail = body.Error.Type
		}
		if status != http.StatusUnauthorized && status != http.StatusForbidden && body.Error.Message != "" {
			detail += ": " + body.Error.Message
		}
	}
	if detail == "" {
		detail = strings.ToLower(http.StatusText(status))
	}
	return fmt.Errorf("openai api status %d: %s", status, c.redact(detail))
}

// backoffFor is the wait before retry n (1-based): exponential, capped, with jitter.
func (c *client) backoffFor(n int) time.Duration {
	d := c.backoff << (n - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d - time.Duration(rand.Float64()*0.25*float64(d))
}

// retryAfter reads retry-after-ms or Retry-After (seconds or an HTTP date). It returns zero
// when neither is present, usable, or within maxRetryAfter.
func retryAfter(h http.Header, now time.Time) time.Duration {
	var d time.Duration
	if ms, err := strconv.ParseFloat(h.Get("Retry-After-Ms"), 64); err == nil {
		d = time.Duration(ms * float64(time.Millisecond))
	} else if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			d = time.Duration(s * float64(time.Second))
		} else if at, err := http.ParseTime(v); err == nil {
			d = at.Sub(now)
		}
	}
	if d <= 0 || d > maxRetryAfter {
		return 0
	}
	return d
}

// retryableTransport reports whether a transport failure is worth another attempt. A
// timeout already spent the whole request budget, so it is not retried.
func retryableTransport(err error) bool {
	var ne net.Error
	return !(errors.As(err, &ne) && ne.Timeout())
}

// transportText describes a transport failure without its URL.
func transportText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// redact removes the key and bounds the length of text bound for an error.
func (c *client) redact(s string) string {
	if c.key != "" {
		s = strings.ReplaceAll(s, c.key, "[redacted]")
	}
	if len(s) > maxErrorText {
		s = strings.ToValidUTF8(s[:maxErrorText], "") + "…"
	}
	return s
}
