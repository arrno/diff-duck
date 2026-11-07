//go:build !unix

package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where Codex's process tree cannot be stopped, the adapter fails closed (K2): Codex is
// unavailable even when its binary is installed, and New refuses to build a model.
func TestCodexFailsClosedWhereTheProcessTreeCannotBeStopped(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex.exe")
	if err := os.WriteFile(bin, []byte("stand-in"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	if Available("") || Available(bin) {
		t.Fatal("codex must be unavailable where its process tree cannot be stopped")
	}
	if _, err := New(bin, "gpt-test", "low"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("New must refuse clearly: %v", err)
	}
}
