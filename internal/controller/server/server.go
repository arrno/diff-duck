// Package server is the local web interface. It only ever listens on the loopback address.
//
// Loopback alone does not protect it: any website the user visits can make their browser send
// requests here, and this API runs git and spends model calls. So every request must name this
// server in its Host header, which stops DNS rebinding, and must not come from another site.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/arrno/diff-duck/internal/adapters/gitrepo"
	"github.com/arrno/diff-duck/internal/services"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const (
	maxBodyBytes = 64 << 10
	// Clones can be slow, but a stuck one must not hold a request forever.
	commitsTimeout = 2 * time.Minute
	contentPolicy  = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// Handler returns the interface for a server reachable at port on the loopback address. page
// is the application page.
func Handler(library *services.DiffLibrary, page []byte, port int) http.Handler {
	api := &api{library: library}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", contentPolicy)
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /api/commits", api.commits)
	mux.HandleFunc("GET /api/diffs", api.listDiffs)
	mux.HandleFunc("POST /api/diffs", api.createDiff)
	mux.HandleFunc("GET /api/diffs/{id}", api.getDiff)
	mux.HandleFunc("DELETE /api/diffs/{id}", api.deleteDiff)
	return guard(mux, port)
}

// guard refuses requests that did not come from this server's own page.
func guard(next http.Handler, port int) http.Handler {
	hosts := map[string]bool{
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)): true,
		net.JoinHostPort("localhost", strconv.Itoa(port)): true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if !hosts[r.Host] {
			writeError(w, http.StatusForbidden, "This server only answers on its local address.")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			writeError(w, http.StatusForbidden, "Requests from other sites are not allowed.")
			return
		}
		// Browsers omit Origin on simple cross-site GETs, such as an image tag, but do send this.
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			writeError(w, http.StatusForbidden, "Requests from other sites are not allowed.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type api struct {
	library *services.DiffLibrary
}

func (a *api) commits(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), commitsTimeout)
	defer cancel()
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	commits, err := a.library.Commits(ctx, repositoryTarget(r.URL.Query().Get("repo")), limit)
	if err != nil {
		// A repository that cannot be read is the expected failure here, and the user can fix it.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, commits)
}

func (a *api) listDiffs(w http.ResponseWriter, r *http.Request) {
	diffs, err := a.library.List(r.Context())
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diffs)
}

type createDiffRequest struct {
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Head       string `json:"head"`
}

func (a *api) createDiff(w http.ResponseWriter, r *http.Request) {
	// A JSON content type cannot be sent cross-site without the browser asking permission first.
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Send JSON.")
		return
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	var req createDiffRequest
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "The request could not be read.")
		return
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "The request could not be read.")
		return
	}
	diff, err := a.library.StartDiff(r.Context(), repositoryTarget(req.Repository), strings.TrimSpace(req.Base), strings.TrimSpace(req.Head))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, diff)
}

func (a *api) getDiff(w http.ResponseWriter, r *http.Request) {
	diff, err := a.library.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

func (a *api) deleteDiff(w http.ResponseWriter, r *http.Request) {
	if err := a.library.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// repositoryTarget turns what a person typed into an absolute path or a clone URL, because the
// server's working directory means nothing to them.
func repositoryTarget(input string) string {
	target := strings.TrimSpace(input)
	if target == "" || strings.HasPrefix(target, "-") || gitrepo.IsRemote(target) {
		return target
	}
	if target == "~" || strings.HasPrefix(target, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			target = filepath.Join(home, strings.TrimPrefix(target, "~"))
		}
	}
	if abs, err := filepath.Abs(target); err == nil {
		return abs
	}
	return target
}

func writeFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ports.ErrDiffNotFound):
		writeError(w, http.StatusNotFound, "That diff no longer exists.")
	case errors.Is(err, services.ErrInvalidDiffRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, services.ErrTooManyDiffs):
		writeError(w, http.StatusTooManyRequests, "Too many diffs are already running. Try again when one finishes.")
	default:
		fmt.Fprintln(os.Stderr, "error:", err)
		writeError(w, http.StatusInternalServerError, "Something went wrong on the server.")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
