package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/arrno/duck-diff/internal/domain"
	"github.com/arrno/duck-diff/internal/services/ports"
)

func TestParseDiffPairsPathsAndLineNumbers(t *testing.T) {
	names := []byte("M\x00a file.go\x00R087\x00old.txt\x00new.txt\x00A\x00bin.png\x00D\x00gone.go\x00")
	patch := []byte(`diff --git a/a file.go b/a file.go
--- a/a file.go
+++ b/a file.go
@@ -10,3 +10,4 @@ func x() {
 keep
-old
+new
+diff --git looks like a header but is content
 keep2
\ No newline at end of file
diff --git a/old.txt b/new.txt
similarity index 87%
@@ -1 +1 @@
-a
+b
diff --git a/bin.png b/bin.png
Binary files /dev/null and b/bin.png differ
diff --git a/gone.go b/gone.go
@@ -1 +0,0 @@
-x
`)
	changes, err := ParseDiff(names, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 4 {
		t.Fatalf("got %d changes", len(changes))
	}
	first := changes[0]
	if first.NewPath != "a file.go" || first.OldPath != "a file.go" {
		t.Fatalf("paths: %+v", first)
	}
	lines := first.Hunks[0].Lines
	if len(lines) != 5 {
		t.Fatalf("got %d lines: %+v", len(lines), lines)
	}
	if lines[1].Kind != domain.LineRemoved || lines[1].OldLine != 11 {
		t.Errorf("removed line: %+v", lines[1])
	}
	if lines[2].Kind != domain.LineAdded || lines[2].NewLine != 11 {
		t.Errorf("added line: %+v", lines[2])
	}
	if lines[3].Text != "diff --git looks like a header but is content" || lines[3].NewLine != 12 {
		t.Errorf("content that resembles a header: %+v", lines[3])
	}
	if lines[4].OldLine != 12 || lines[4].NewLine != 13 {
		t.Errorf("context numbering: %+v", lines[4])
	}
	if changes[1].Status != domain.FileRenamed || changes[1].OldPath != "old.txt" || changes[1].NewPath != "new.txt" {
		t.Errorf("rename: %+v", changes[1])
	}
	if !changes[2].Binary || changes[2].Status != domain.FileAdded {
		t.Errorf("binary: %+v", changes[2])
	}
	if changes[3].Status != domain.FileDeleted || changes[3].Path() != "gone.go" {
		t.Errorf("deleted: %+v", changes[3])
	}
}

func TestParseDiffRejectsMismatchedOutputs(t *testing.T) {
	if _, err := ParseDiff([]byte("M\x00a\x00M\x00b\x00"), []byte("diff --git a/a b/a\n")); err == nil {
		t.Fatal("expected an error when the listing and patch disagree")
	}
}

func TestOnlyNetworkTransportsCountAsRemote(t *testing.T) {
	for target, want := range map[string]bool{
		"https://github.com/a/b":  true,
		"git@github.com:a/b.git":  true,
		"ssh://git@host/a":        true,
		"github.com/a/b":          true,
		"ext::sh -c touch /tmp/x": false,
		"file:///tmp/x":           false,
		"--upload-pack=touch":     false,
		"./local":                 false,
	} {
		if got := IsRemote(target); got != want {
			t.Errorf("%q: got %v", target, got)
		}
	}
}

func TestClonedRepositoryResolvesBranchesThroughItsRemote(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	run("commit", "-q", "--allow-empty", "-m", "c")
	sha := run("rev-parse", "HEAD")
	run("update-ref", "refs/remotes/origin/feature", sha)
	ctx := context.Background()

	cloned := &Repository{dir: dir, cloned: true}
	if got, err := cloned.ResolveCommit(ctx, "feature"); err != nil || got != sha {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := cloned.ResolveCommit(ctx, "missing"); !errors.Is(err, ports.ErrUnknownRevision) {
		t.Fatalf("missing branch: %v", err)
	}
	local := &Repository{dir: dir}
	if _, err := local.ResolveCommit(ctx, "feature"); !errors.Is(err, ports.ErrUnknownRevision) {
		t.Fatalf("a local repository uses the name as given: %v", err)
	}
}

func TestRevisionsCannotBecomeOptions(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r := &Repository{dir: dir}
	for _, rev := range []string{"--help", "--git-dir", "-h"} {
		if _, err := r.ResolveCommit(context.Background(), rev); !errors.Is(err, ports.ErrUnknownRevision) {
			t.Errorf("%q: %v", rev, err)
		}
	}
}

func TestOutputLimitStopsAnOversizedRead(t *testing.T) {
	b := &limitedBuffer{limit: 4}
	if _, err := b.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("5")); !errors.Is(err, errOutputTooLarge) {
		t.Fatalf("got %v", err)
	}
}
