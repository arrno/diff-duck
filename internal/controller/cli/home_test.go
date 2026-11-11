package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTheDirectoryFromBeforeTheRenameIsMovedOnce(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, legacyName)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "diffs.db"), []byte("saved"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err := ownDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(base, "diffduck") {
		t.Errorf("dir = %s", dir)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "diffs.db")); err != nil || string(got) != "saved" {
		t.Errorf("saved diffs did not carry over: %q, %v", got, err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("the old directory is still there: %v", err)
	}
}

func TestAnExistingDirectoryIsNeverReplacedByTheOldOne(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{legacyName, "diffduck"} {
		if err := os.MkdirAll(filepath.Join(base, name), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(base, name, "diffs.db"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := ownDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "diffs.db")); string(got) != "diffduck" {
		t.Errorf("the current directory was replaced: %q", got)
	}
	if _, err := os.Stat(filepath.Join(base, legacyName)); err != nil {
		t.Errorf("the old directory was touched: %v", err)
	}
}

func TestNoOldDirectoryLeavesNothingToMove(t *testing.T) {
	base := t.TempDir()
	dir, err := ownDir(base)
	if err != nil || dir != filepath.Join(base, "diffduck") {
		t.Errorf("dir = %s, %v", dir, err)
	}
}
