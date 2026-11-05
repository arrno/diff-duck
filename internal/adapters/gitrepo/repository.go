// Package gitrepo implements ports.RepositoryReader by running the git CLI.
package gitrepo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"duck-diff/internal/domain"
	"duck-diff/internal/services/ports"
)

// Repository is an opened Git repository.
type Repository struct {
	dir     string
	cleanup func()
	// cloned means this adapter created the repository, so its configuration is trusted.
	cloned bool
}

const (
	maxPatchBytes = 512 << 20
	maxBlobBytes  = 16 << 20
)

// binaryProbeBytes matches how far git looks for a NUL when deciding a file is binary.
const binaryProbeBytes = 8000

// errOutputTooLarge means git produced more output than this adapter will hold in memory.
var errOutputTooLarge = errors.New("output exceeds the size limit")

// limitedBuffer fails the write that crosses its limit, which also stops the git process.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errOutputTooLarge
	}
	return b.Buffer.Write(p)
}

// IsRemote reports whether the target is a URL to clone rather than a local path. Only
// network transports qualify, which keeps git's command-running ext:: transport unreachable.
func IsRemote(target string) bool {
	for _, prefix := range []string{"https://", "http://", "ssh://", "git@"} {
		if strings.HasPrefix(target, prefix) {
			return true
		}
	}
	return strings.HasPrefix(target, "github.com/")
}

// Open opens a local repository or clones a remote one into a temporary directory. Call Close
// when finished.
func Open(ctx context.Context, target string) (*Repository, error) {
	if IsRemote(target) {
		return clone(ctx, target)
	}
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory or a supported repository URL", target)
	}
	r := &Repository{dir: target}
	if _, err := r.git(ctx, maxBlobBytes, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("%q is not a Git repository", target)
	}
	return r, nil
}

func clone(ctx context.Context, target string) (*Repository, error) {
	url := target
	if strings.HasPrefix(url, "github.com/") {
		url = "https://" + url
	}
	dir, err := os.MkdirTemp("", "duck-diff-clone-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	// A blobless clone holds all commits, so any pair can be diffed, and fetches file
	// contents only as they are read.
	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--filter=blob:none", "--no-checkout", "--", url, dir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return nil, fmt.Errorf("clone %s: %w: %s", target, err, strings.TrimSpace(stderr.String()))
	}
	return &Repository{dir: dir, cleanup: cleanup, cloned: true}, nil
}

// Close releases any temporary clone.
func (r *Repository) Close() {
	if r.cleanup != nil {
		r.cleanup()
	}
}

// git runs a command. A local repository's configuration is untrusted, because it may have
// arrived with the repository in an archive or shared directory. So everything that could run
// code or change the output format is pinned here. Lazy fetching is off for local repositories
// because it would run commands named by that configuration. The flag needs git 2.45, and an
// older git fails on it rather than running without the protection.
func (r *Repository) git(ctx context.Context, limit int, args ...string) ([]byte, error) {
	base := []string{"-C", r.dir}
	if !r.cloned {
		base = append(base, "--no-lazy-fetch")
	}
	for _, setting := range []string{
		"core.fsmonitor=false",
		"core.quotePath=false",
		"color.ui=false",
		"diff.noprefix=false",
		"diff.mnemonicPrefix=false",
		"diff.renames=true",
		"diff.relative=false",
		"diff.suppressBlankEmpty=false",
		"diff.submodule=short",
		"diff.ignoreSubmodules=none",
		"diff.interHunkContext=0",
	} {
		base = append(base, "-c", setting)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	stdout := &limitedBuffer{limit: limit}
	var stderr bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stdout.Len()+65536 > limit {
			return nil, fmt.Errorf("git %s: %w", args[0], errOutputTooLarge)
		}
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// ResolveCommit returns the full SHA of a revision. A clone has no local branches beyond the
// default, so there a branch name also resolves through its remote-tracking ref.
func (r *Repository) ResolveCommit(ctx context.Context, revision string) (string, error) {
	candidates := []string{revision}
	if r.cloned {
		candidates = append(candidates, "refs/remotes/origin/"+revision)
	}
	for _, candidate := range candidates {
		out, err := r.git(ctx, maxBlobBytes, "rev-parse", "--verify", "--quiet", "--end-of-options", candidate+"^{commit}")
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", fmt.Errorf("%w: %q", ports.ErrUnknownRevision, revision)
}

// ReadFile returns a file's contents at a commit.
func (r *Repository) ReadFile(ctx context.Context, sha, path string) ([]byte, error) {
	return r.git(ctx, maxBlobBytes, "cat-file", "blob", sha+":"+path)
}

// ListFiles returns the paths directly inside a directory at a commit.
func (r *Repository) ListFiles(ctx context.Context, sha, dir string) ([]string, error) {
	args := []string{"--literal-pathspecs", "ls-tree", "-z", "--name-only", sha}
	if dir != "." {
		args = append(args, "--", dir+"/")
	}
	out, err := r.git(ctx, maxBlobBytes, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// ChangedFiles returns the parsed diff between two commits.
func (r *Repository) ChangedFiles(ctx context.Context, baseSHA, headSHA string) ([]domain.FileChange, error) {
	common := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-relative", "-M"}
	names, err := r.git(ctx, maxPatchBytes, append(common, "--name-status", "-z", baseSHA, headSHA, "--")...)
	if err != nil {
		return nil, err
	}
	patchArgs := append(common, "--unified=3", "--src-prefix=a/", "--dst-prefix=b/")
	patch, err := r.git(ctx, maxPatchBytes, append(patchArgs[:len(patchArgs):len(patchArgs)], baseSHA, headSHA, "--")...)
	if err != nil {
		return nil, err
	}
	changes, err := ParseDiff(names, patch)
	if err != nil {
		return nil, err
	}
	return r.exposeFalseBinaries(ctx, baseSHA, headSHA, names, patchArgs, changes)
}

// exposeFalseBinaries restores the lines of text files reported as binary. Git decides that
// partly from .gitattributes, which the analyzed repository controls, so a committed "*.go -diff"
// would otherwise hide code changes. Only the file's own bytes count here.
func (r *Repository) exposeFalseBinaries(ctx context.Context, baseSHA, headSHA string, names []byte, patchArgs []string, changes []domain.FileChange) ([]domain.FileChange, error) {
	var hidden []int
	for i, change := range changes {
		if !change.Binary {
			continue
		}
		real, err := r.isBinary(ctx, baseSHA, headSHA, change)
		if err != nil {
			return nil, err
		}
		if !real {
			hidden = append(hidden, i)
		}
	}
	if len(hidden) == 0 {
		return changes, nil
	}
	patch, err := r.git(ctx, maxPatchBytes, append(patchArgs[:len(patchArgs):len(patchArgs)], "--text", baseSHA, headSHA, "--")...)
	if err != nil {
		return nil, err
	}
	asText, err := ParseDiff(names, patch)
	if err != nil {
		return nil, err
	}
	for _, i := range hidden {
		changes[i] = asText[i]
	}
	return changes, nil
}

func (r *Repository) isBinary(ctx context.Context, baseSHA, headSHA string, change domain.FileChange) (bool, error) {
	check := func(sha, path string) (bool, error) {
		if path == "" {
			return false, nil
		}
		data, err := r.ReadFile(ctx, sha, path)
		if errors.Is(err, errOutputTooLarge) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(data) > binaryProbeBytes {
			data = data[:binaryProbeBytes]
		}
		return bytes.IndexByte(data, 0) >= 0, nil
	}
	if real, err := check(baseSHA, change.OldPath); real || err != nil {
		return real, err
	}
	return check(headSHA, change.NewPath)
}

// ParseDiff combines git's two outputs. Paths come from the NUL-separated listing, which is
// unambiguous for any file name, and hunks come from the patch. Git emits both in one order,
// so they pair by position.
func ParseDiff(nameStatus, patch []byte) ([]domain.FileChange, error) {
	changes, err := parseNameStatus(nameStatus)
	if err != nil {
		return nil, err
	}
	sections := splitSections(patch)
	next := 0
	for i := range changes {
		if next >= len(sections) {
			return nil, fmt.Errorf("git reported %d changed files but only %d patch sections", len(changes), len(sections))
		}
		first := sections[next]
		if err := parseSection(first, &changes[i]); err != nil {
			return nil, fmt.Errorf("%s: %w", changes[i].Path(), err)
		}
		next++
		// A type change, such as a file becoming a symlink, is one listed entry that git
		// patches as a deletion plus an addition under the same header.
		if next < len(sections) && firstLine(sections[next]) == firstLine(first) {
			if err := parseSection(sections[next], &changes[i]); err != nil {
				return nil, fmt.Errorf("%s: %w", changes[i].Path(), err)
			}
			next++
		}
	}
	if next != len(sections) {
		return nil, fmt.Errorf("git reported %d changed files but %d patch sections", len(changes), len(sections))
	}
	return changes, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func parseNameStatus(raw []byte) ([]domain.FileChange, error) {
	fields := strings.Split(string(raw), "\x00")
	var changes []domain.FileChange
	for i := 0; i < len(fields); {
		code := fields[i]
		if code == "" {
			i++
			continue
		}
		if i+1 >= len(fields) {
			return nil, fmt.Errorf("malformed name-status entry %q", code)
		}
		switch code[0] {
		case 'A':
			changes = append(changes, domain.FileChange{Status: domain.FileAdded, NewPath: fields[i+1]})
			i += 2
		case 'D':
			changes = append(changes, domain.FileChange{Status: domain.FileDeleted, OldPath: fields[i+1]})
			i += 2
		case 'M', 'T':
			changes = append(changes, domain.FileChange{Status: domain.FileModified, OldPath: fields[i+1], NewPath: fields[i+1]})
			i += 2
		case 'R', 'C':
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("malformed rename entry %q", code)
			}
			status := domain.FileRenamed
			oldPath := fields[i+1]
			if code[0] == 'C' {
				status, oldPath = domain.FileAdded, ""
			}
			changes = append(changes, domain.FileChange{Status: status, OldPath: oldPath, NewPath: fields[i+2]})
			i += 3
		default:
			return nil, fmt.Errorf("unsupported status %q", code)
		}
	}
	return changes, nil
}

// splitSections cuts a patch per file. Content lines always start with a diff marker, so only
// a real header can start with "diff --git ".
func splitSections(patch []byte) []string {
	var sections []string
	for _, chunk := range strings.SplitAfter(string(patch), "\n") {
		if strings.HasPrefix(chunk, "diff --git ") {
			sections = append(sections, chunk)
		} else if len(sections) > 0 {
			sections[len(sections)-1] += chunk
		}
	}
	return sections
}

func parseSection(section string, change *domain.FileChange) error {
	lines := strings.Split(strings.TrimSuffix(section, "\n"), "\n")
	var hunk *domain.Hunk
	var oldLine, newLine int
	flush := func() {
		if hunk != nil {
			change.Hunks = append(change.Hunks, *hunk)
			hunk = nil
		}
	}
	for _, line := range lines {
		if hunk == nil {
			if strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch" {
				change.Binary = true
				continue
			}
		}
		if strings.HasPrefix(line, "@@ ") {
			flush()
			var err error
			oldLine, newLine, err = parseHunkHeader(line)
			if err != nil {
				return err
			}
			hunk = &domain.Hunk{}
			continue
		}
		if hunk == nil {
			continue
		}
		if line == "" {
			// Only a blank context line can look like this. Counting it keeps every later
			// line number right, even if a config setting survives the pinning above.
			hunk.Lines = append(hunk.Lines, domain.DiffLine{Kind: domain.LineContext, OldLine: oldLine, NewLine: newLine})
			oldLine++
			newLine++
			continue
		}
		text := line[1:]
		switch line[0] {
		case ' ':
			hunk.Lines = append(hunk.Lines, domain.DiffLine{Kind: domain.LineContext, OldLine: oldLine, NewLine: newLine, Text: text})
			oldLine++
			newLine++
		case '+':
			hunk.Lines = append(hunk.Lines, domain.DiffLine{Kind: domain.LineAdded, NewLine: newLine, Text: text})
			newLine++
		case '-':
			hunk.Lines = append(hunk.Lines, domain.DiffLine{Kind: domain.LineRemoved, OldLine: oldLine, Text: text})
			oldLine++
		case '\\':
			// "\ No newline at end of file" is not a line of the file.
		default:
			return fmt.Errorf("unexpected diff line %q", line)
		}
	}
	flush()
	return nil
}

func parseHunkHeader(line string) (oldStart, newStart int, err error) {
	parts := strings.Fields(line)
	if len(parts) < 3 {
		return 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}
	if oldStart, err = hunkStart(parts[1], "-"); err != nil {
		return 0, 0, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	if newStart, err = hunkStart(parts[2], "+"); err != nil {
		return 0, 0, fmt.Errorf("malformed hunk header %q: %w", line, err)
	}
	return oldStart, newStart, nil
}

func hunkStart(field, marker string) (int, error) {
	value, ok := strings.CutPrefix(field, marker)
	if !ok {
		return 0, fmt.Errorf("expected %s in %q", marker, field)
	}
	value, _, _ = strings.Cut(value, ",")
	return strconv.Atoi(value)
}
