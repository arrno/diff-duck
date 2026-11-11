package modelcache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arrno/diffduck/internal/services/ports"
)

type stub struct {
	calls int
	name  string
	err   error
}

func (s *stub) Describe() string { return s.name }
func (s *stub) Complete(context.Context, ports.ModelRequest) (ports.ModelResponse, error) {
	s.calls++
	return ports.ModelResponse{JSON: []byte(`{"n":1}`)}, s.err
}

func TestEveryPartOfTheRequestSeparatesEntries(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &stub{name: "a"}
	m, err := New(s, dir)
	if err != nil {
		t.Fatal(err)
	}
	base := ports.ModelRequest{System: "s", User: "u", Schema: []byte(`{}`)}
	if _, err := m.Complete(ctx, base); err != nil {
		t.Fatal(err)
	}
	if hit, _ := m.Complete(ctx, base); !hit.CacheHit || s.calls != 1 {
		t.Fatalf("an identical request should hit: %+v, %d calls", hit, s.calls)
	}
	for name, req := range map[string]ports.ModelRequest{
		"system":   {System: "other", User: "u", Schema: []byte(`{}`)},
		"user":     {System: "s", User: "other", Schema: []byte(`{}`)},
		"schema":   {System: "s", User: "u", Schema: []byte(`{"a":1}`)},
		"boundary": {System: "su", User: "", Schema: []byte(`{}`)},
	} {
		if resp, _ := m.Complete(ctx, req); resp.CacheHit {
			t.Errorf("%s must separate entries", name)
		}
	}
	other, _ := New(&stub{name: "b"}, dir)
	if resp, _ := other.Complete(ctx, base); resp.CacheHit {
		t.Error("a different model must not reuse an answer")
	}
}

func TestFailuresAreNotStoredAndCorruptEntriesAreIgnored(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := &stub{name: "a", err: errors.New("boom")}
	m, _ := New(s, dir)
	req := ports.ModelRequest{Schema: []byte(`{}`)}
	if _, err := m.Complete(ctx, req); err == nil {
		t.Fatal("expected the failure to surface")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("a failure was stored: %v", entries)
	}
	s.err = nil
	if _, err := m.Complete(ctx, req); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("entries: %v", entries)
	}
	if err := os.WriteFile(filepath.Join(dir, entries[0].Name()), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resp, err := m.Complete(ctx, req); err != nil || resp.CacheHit || s.calls != 3 {
		t.Fatalf("corrupt entry: %+v %v, %d calls", resp, err, s.calls)
	}
}
