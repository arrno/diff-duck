// Package cli is the command-line entry point. It translates arguments, wires adapters to
// services, and renders the result.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/arrno/diff-duck/internal/adapters/anthropicapi"
	"github.com/arrno/diff-duck/internal/adapters/claudecli"
	"github.com/arrno/diff-duck/internal/adapters/gitrepo"
	"github.com/arrno/diff-duck/internal/adapters/goanalysis"
	"github.com/arrno/diff-duck/internal/adapters/modelcache"
	"github.com/arrno/diff-duck/internal/adapters/promptfiles"
	"github.com/arrno/diff-duck/internal/controller/htmlview"
	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const usage = `diff-duck turns the Git diff between two commits into a compressed diff.

Usage:
  diff-duck [flags] <repo-url-or-path> <base> <head>
  diff-duck serve [flags]

The first form writes compressed-diff.json and index.html to the output directory. The second
opens the local web app. To analyze a directory named serve, write it as ./serve.

Flags:
`

// Exit codes.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitNoSummaries = 3
)

type options struct {
	out           string
	adapter       string
	model         string
	effort        string
	promptsDir    string
	concurrency   int
	noCache       bool
	cacheDir      string
	structureOnly bool
	maxModelCalls int
}

// Run executes the command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "serve" {
		return runServe(ctx, args[1:], stdout, stderr)
	}
	opts := options{}
	fs := flag.NewFlagSet("diff-duck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.out, "out", "", "output directory (default: ./diff-duck-out/<base>..<head>)")
	fs.BoolVar(&opts.structureOnly, "structure-only", false, "skip the model and output only structural facts")
	addModelFlags(fs, &opts)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}

	// Allow flags before, between, or after the positional arguments. A revision that begins
	// with a dash is therefore read as a flag and never reaches git.
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return exitOK
			}
			return exitUsage
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	if len(positional) != 3 {
		fs.Usage()
		return exitUsage
	}
	if opts.concurrency < 1 {
		fmt.Fprintln(stderr, "error: --concurrency must be at least 1")
		return exitUsage
	}

	if err := execute(ctx, opts, positional[0], positional[1], positional[2], stdout); err != nil {
		fmt.Fprintln(stderr, "error:", printable(err.Error()))
		if errors.Is(err, services.ErrNoExplanations) {
			return exitNoSummaries
		}
		return exitFailure
	}
	return exitOK
}

// addModelFlags registers the settings shared by every way of generating a diff.
func addModelFlags(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.adapter, "adapter", "claude-cli", "model adapter: claude-cli or anthropic-api")
	fs.StringVar(&opts.model, "model", "claude-sonnet-5-5", "model ID")
	fs.StringVar(&opts.effort, "effort", "medium", "model effort: low, medium, high, xhigh, or max")
	fs.StringVar(&opts.promptsDir, "prompts", "", "prompt variant directory holding leaf.md, parent.md, outcomes.md (default: built in)")
	fs.IntVar(&opts.concurrency, "concurrency", 4, "maximum concurrent model calls")
	fs.BoolVar(&opts.noCache, "no-cache", false, "ignore and do not write the model response cache")
	fs.StringVar(&opts.cacheDir, "cache-dir", "", "model response cache directory (default: the user cache directory)")
	fs.IntVar(&opts.maxModelCalls, "max-model-calls", 200, "most model calls in one run, leaving the rest visibly unexplained; 0 means no limit")
}

func execute(ctx context.Context, opts options, target, base, head string, stdout io.Writer) error {
	repo, err := gitrepo.Open(ctx, target)
	if err != nil {
		return err
	}
	defer repo.Close()

	var model ports.LanguageModel
	var source ports.PromptSource
	if !opts.structureOnly {
		var closeModel func()
		model, closeModel, err = buildModel(opts)
		if err != nil {
			return err
		}
		defer closeModel()
		if source, err = buildPrompts(opts); err != nil {
			return err
		}
	}

	differ := services.NewStructuralDiffer(repo, goanalysis.New())
	compressor := services.NewDiffCompressor(repo, differ, model, source, opts.concurrency)
	result, compressErr := compressor.Compress(ctx, services.CompressRequest{
		Repository:    target,
		Base:          base,
		Head:          head,
		StructureOnly: opts.structureOnly,
		MaxModelCalls: opts.maxModelCalls,
	})
	if result == nil {
		return compressErr
	}

	dir := opts.out
	if dir == "" {
		dir = filepath.Join("diff-duck-out", shortSHA(result.BaseSHA)+".."+shortSHA(result.HeadSHA))
		// This path is predictable and may sit inside the analyzed checkout, so a hostile
		// repository could commit a symlink here.
		if err := refuseSymlinks(dir); err != nil {
			return err
		}
	}
	if err := write(dir, result); err != nil {
		return err
	}
	report(stdout, dir, result)
	return compressErr
}

func buildModel(opts options) (ports.LanguageModel, func(), error) {
	closeModel := func() {}
	var model ports.LanguageModel
	switch {
	case opts.adapter == "claude-cli":
		m, err := claudecli.New("", opts.model, opts.effort)
		if err != nil {
			return nil, nil, err
		}
		model, closeModel = m, m.Close
	case opts.adapter == "anthropic-api":
		model = anthropicapi.New(opts.model, opts.effort)
	default:
		m, err := verificationModel(opts.adapter)
		if err != nil {
			return nil, nil, err
		}
		model = m
	}
	if opts.noCache {
		return model, closeModel, nil
	}
	dir := opts.cacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, nil, fmt.Errorf("locate cache directory (use --cache-dir or --no-cache): %w", err)
		}
		dir = filepath.Join(base, "diff-duck", "responses")
	}
	cached, err := modelcache.New(model, dir)
	if err != nil {
		return nil, nil, err
	}
	return cached, closeModel, nil
}

func buildPrompts(opts options) (ports.PromptSource, error) {
	if opts.promptsDir != "" {
		return promptfiles.FromDir(opts.promptsDir)
	}
	sub, err := subFS()
	if err != nil {
		return nil, err
	}
	return promptfiles.FromFS(sub, "default"), nil
}

func refuseSymlinks(dir string) error {
	for p := dir; p != "." && p != string(filepath.Separator); p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through the symlink %s; use --out to choose a directory", p)
		}
	}
	return nil
}

func write(dir string, result *domain.CompressedDiff) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode compressed diff: %w", err)
	}
	if err := replaceFile(filepath.Join(dir, "compressed-diff.json"), append(data, '\n')); err != nil {
		return err
	}
	page, err := htmlview.Render(result)
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(dir, "index.html"), page)
}

// replaceFile writes through a temporary file and a rename, which replaces a symlink at the
// path rather than following it to overwrite its target.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".diff-duck-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// printable escapes control characters, since file names from a repository can hold terminal
// escape sequences that would otherwise rewrite what the user sees.
func printable(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func report(w io.Writer, dir string, r *domain.CompressedDiff) {
	g, m, c := r.Generation, r.Metrics, r.Coverage
	fmt.Fprintf(w, "%d files, +%d -%d: %d nodes, %d outcomes, %d unassigned leaves\n", r.Files, r.Added, r.Removed, m.Nodes, m.Outcomes, m.UnassignedLeaves)
	fmt.Fprintf(w, "coverage: %d/%d lines, %d/%d files\n", c.TracedLines, c.ChangedLines, c.TracedFiles, c.ChangedFiles)
	fmt.Fprintf(w, "summaries: %d generated, %d missing, %d low confidence; compression %.1fx opening view, %.1fx full tree\n", m.ModelSummaries, m.MissingSummaries, m.LowConfidence, m.CompressionRatio, m.FullTreeRatio)
	fmt.Fprintf(w, "model: %s, prompts: %s; %d calls (%d failed), %d cached, %d in / %d out tokens, $%.4f, %s\n",
		g.Model, g.PromptVariant, g.ModelCalls, g.FailedCalls, g.CacheHits, g.InputTokens, g.OutputTokens, g.CostUSD, g.Duration.Round(1e8))
	for _, warning := range r.Warnings {
		fmt.Fprintln(w, "warning:", printable(warning))
	}
	fmt.Fprintln(w, "wrote", printable(filepath.Join(dir, "index.html")))
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
