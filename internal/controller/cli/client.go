package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/arrno/diffduck/internal/controller/server"
	"github.com/arrno/diffduck/internal/domain"
	"github.com/arrno/diffduck/internal/services"
)

// appClient talks to a running diffduck server over its loopback API.
type appClient struct {
	base string
	http *http.Client
}

func newAppClient(r serverRecord) *appClient {
	return &appClient{base: strings.TrimSuffix(r.url(), "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

// health reports whether the server answering is the diffduck process recorded as pid.
func (c *appClient) health(ctx context.Context, pid int) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var h server.Health
	if err := c.do(ctx, http.MethodGet, "/api/health", nil, &h); err != nil {
		return false
	}
	return h.App == server.AppName && h.PID == pid
}

func (c *appClient) create(ctx context.Context, repo, base, head string) (*domain.SavedDiff, error) {
	body := map[string]string{"repository": repo, "base": base, "head": head}
	var d domain.SavedDiff
	return &d, c.do(ctx, http.MethodPost, "/api/diffs", body, &d)
}

func (c *appClient) get(ctx context.Context, id string) (*domain.SavedDiff, error) {
	var d domain.SavedDiff
	return &d, c.do(ctx, http.MethodGet, "/api/diffs/"+pathID(id), nil, &d)
}

func (c *appClient) list(ctx context.Context) ([]domain.SavedDiff, error) {
	var ds []domain.SavedDiff
	return ds, c.do(ctx, http.MethodGet, "/api/diffs", nil, &ds)
}

func (c *appClient) delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/diffs/"+pathID(id), nil, nil)
}

func (c *appClient) settings(ctx context.Context) (*services.SettingsView, error) {
	var v services.SettingsView
	return &v, c.do(ctx, http.MethodGet, "/api/settings", nil, &v)
}

// saveSettingsBody carries an API key, so it is never logged or put in an error.
type saveSettingsBody struct {
	Provider     string `json:"provider"`
	ReadingLevel string `json:"readingLevel"`
	APIKey       string `json:"apiKey,omitempty"`
}

func (c *appClient) saveSettings(ctx context.Context, provider, level, key string) (*services.SettingsView, error) {
	var v services.SettingsView
	body := saveSettingsBody{Provider: provider, ReadingLevel: level, APIKey: key}
	return &v, c.do(ctx, http.MethodPut, "/api/settings", body, &v)
}

func (c *appClient) clearKey(ctx context.Context, provider string) (*services.SettingsView, error) {
	var v services.SettingsView
	return &v, c.do(ctx, http.MethodDelete, "/api/settings/keys/"+pathID(provider), nil, &v)
}

// pathID keeps an ID from reaching another route; real IDs are letters and digits.
func pathID(id string) string {
	if id == "" || strings.ContainsAny(id, "/?#%. ") {
		return "-"
	}
	return id
}

func (c *appClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach diffduck at %s: %w", c.base, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("diffduck answered %s", resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
