package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arrno/diff-duck/internal/adapters/sqlitestore"
	"github.com/arrno/diff-duck/internal/services"
	"github.com/arrno/diff-duck/internal/services/ports"
)

type noRepositories struct{}

func (noRepositories) Open(context.Context, string) (ports.OpenRepository, error) {
	return nil, errors.New("not a repository")
}

const testPort = 7474

func handler(t *testing.T) http.Handler {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), filepath.Join(t.TempDir(), "diffs.db"))
	if err != nil {
		t.Fatal(err)
	}
	library := services.NewDiffLibrary(services.LibraryDeps{Store: store, Repositories: noRepositories{}, Concurrency: 1})
	t.Cleanup(func() {
		library.Shutdown()
		store.Close()
	})
	return Handler(library, []byte("<!doctype html>the page"), testPort)
}

const ownHost = "127.0.0.1:7474"

func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = ownHost
	for k, v := range headers {
		if k == "Host" {
			r.Host = v
			continue
		}
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestRequestsNotMeantForThisServerAreRefused(t *testing.T) {
	h := handler(t)
	create := `{"repository":"/r","base":"a","head":"b"}`
	jsonType := map[string]string{"Content-Type": "application/json"}
	with := func(k, v string) map[string]string {
		return map[string]string{"Content-Type": "application/json", k: v}
	}
	for name, tc := range map[string]struct {
		method, path, body string
		headers            map[string]string
	}{
		"DNS rebinding to the list":     {"GET", "/api/diffs", "", map[string]string{"Host": "evil.example:7474"}},
		"DNS rebinding to the page":     {"GET", "/", "", map[string]string{"Host": "evil.example"}},
		"wrong port in the host":        {"GET", "/api/diffs", "", map[string]string{"Host": "127.0.0.1:9999"}},
		"another site creating a diff":  {"POST", "/api/diffs", create, with("Origin", "https://evil.example")},
		"another site deleting a diff":  {"DELETE", "/api/diffs/x", "", map[string]string{"Origin": "https://evil.example"}},
		"another local port":            {"POST", "/api/diffs", create, with("Origin", "http://127.0.0.1:3000")},
		"a sandboxed page":              {"POST", "/api/diffs", create, with("Origin", "null")},
		"https look-alike of this host": {"POST", "/api/diffs", create, with("Origin", "https://"+ownHost)},
		"cross-site GET with no origin": {"GET", "/api/commits?repo=/r", "", map[string]string{"Sec-Fetch-Site": "cross-site"}},
		"sibling-site GET":              {"GET", "/api/commits?repo=/r", "", map[string]string{"Sec-Fetch-Site": "same-site"}},
	} {
		if w := do(h, tc.method, tc.path, tc.body, tc.headers); w.Code != http.StatusForbidden {
			t.Errorf("%s: got %d", name, w.Code)
		}
	}
	// Prove the refusals above come from the guard and not from something else being wrong.
	if w := do(h, "POST", "/api/diffs", create, with("Origin", "http://"+ownHost)); w.Code != http.StatusAccepted {
		t.Errorf("own origin: got %d: %s", w.Code, w.Body)
	}
	if w := do(h, "POST", "/api/diffs", create, jsonType); w.Code != http.StatusAccepted {
		t.Errorf("no origin: got %d", w.Code)
	}
	if w := do(h, "GET", "/api/diffs", "", map[string]string{"Host": "localhost:" + strconv.Itoa(testPort)}); w.Code != http.StatusOK {
		t.Errorf("localhost: got %d", w.Code)
	}
}

func TestNoResponseGrantsOtherSitesAccess(t *testing.T) {
	h := handler(t)
	w := do(h, "GET", "/api/diffs", "", nil)
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("CORS header present: %q", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("saved source code could be cached: %q", got)
	}
}

func TestCreatingADiffNeedsAWellFormedJSONRequest(t *testing.T) {
	h := handler(t)
	valid := `{"repository":"/r","base":"a","head":"b"}`
	jsonType := map[string]string{"Content-Type": "application/json"}
	for name, tc := range map[string]struct {
		body    string
		headers map[string]string
		want    int
	}{
		"a form post, which needs no permission to send cross-site": {valid, map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		"no content type":  {valid, nil, http.StatusUnsupportedMediaType},
		"not JSON":         {"nope", jsonType, http.StatusBadRequest},
		"unknown field":    {`{"repository":"/r","base":"a","head":"b","x":1}`, jsonType, http.StatusBadRequest},
		"trailing data":    {valid + valid, jsonType, http.StatusBadRequest},
		"too large":        {`{"repository":"` + strings.Repeat("x", maxBodyBytes) + `"}`, jsonType, http.StatusBadRequest},
		"missing commit":   {`{"repository":"/r","base":"","head":"b"}`, jsonType, http.StatusBadRequest},
		"option as commit": {`{"repository":"/r","base":"--output=/tmp/x","head":"b"}`, jsonType, http.StatusBadRequest},
	} {
		if w := do(h, "POST", "/api/diffs", tc.body, tc.headers); w.Code != tc.want {
			t.Errorf("%s: got %d, want %d: %s", name, w.Code, tc.want, w.Body)
		}
	}
	if w := do(h, "GET", "/api/diffs", "", nil); w.Body.String() != "[]\n" {
		t.Errorf("refused requests were saved: %s", w.Body)
	}
}

func TestPageIsServedWithItsProtections(t *testing.T) {
	h := handler(t)
	w := do(h, "GET", "/", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "the page") {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
	policy := w.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(policy, want) {
			t.Errorf("policy lacks %q: %s", want, policy)
		}
	}
	if w := do(h, "GET", "/nothing", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", w.Code)
	}
}

func TestMissingDiffAndUnreadableRepositoryMessages(t *testing.T) {
	h := handler(t)
	if w := do(h, "GET", "/api/diffs/nope", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("get: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/diffs/nope", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("delete: %d", w.Code)
	}
	w := do(h, "GET", "/api/commits?repo=/nowhere", "", nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not a repository") {
		t.Errorf("commits: %d %s", w.Code, w.Body)
	}
}

func TestPeopleCanTypePathsTheWayTheyThinkOfThem(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	for in, want := range map[string]string{
		"  /abs/repo  ":               "/abs/repo",
		"~/projects/app":              "/home/someone/projects/app",
		"~":                           "/home/someone",
		"https://github.com/org/repo": "https://github.com/org/repo",
		"git@github.com:org/repo.git": "git@github.com:org/repo.git",
		"github.com/org/repo":         "github.com/org/repo",
		"":                            "",
		"--upload-pack=x":             "--upload-pack=x",
	} {
		if got := repositoryTarget(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	if got := repositoryTarget("relative/repo"); !filepath.IsAbs(got) {
		t.Errorf("relative path stayed relative: %q", got)
	}
}
