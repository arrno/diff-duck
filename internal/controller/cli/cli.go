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

	"github.com/arrno/diff-duck/internal/adapters/gitrepo"
	"github.com/arrno/diff-duck/internal/adapters/promptfiles"
	"github.com/arrno/diff-duck/internal/controller/htmlview"
	"github.com/arrno/diff-duck/internal/domain"
	"github.com/arrno/diff-duck/internal/services"
	"github.com/arrno/diff-duck/internal/services/ports"
)

const usage = `DiffDuck turns the Git diff between two commits into a readable summary.

The app:
  diffduck up [flags]                       start the app in the background
  diffduck down                             stop it
  diffduck status                           say whether it is running, and where
  diffduck reset [--yes]                    delete every saved diff; settings are kept
  diffduck serve [flags]                    run the app in this terminal instead

Diffs (these start the app if it is not running):
  diffduck diff [repo] [-f from] [-t to]    build a diff and open it (default: . HEAD~1 HEAD)
  diffduck diff list [--json]               list saved diffs
  diffduck diff open [id]                   open the app, or one diff, in the browser
  diffduck diff delete <id>...              delete saved diffs

Run "diffduck <command> -h" for a command's flags.

Without the app:
  diffduck [flags] <repo-url-or-path> <base> <head>

This form writes compressed-diff.json and index.html to the output directory. To analyze a
directory named like a command, write it as ./serve.

Flags for that form:
`

// Exit codes.
const (
	exitOK          = 0
	exitFailure     = 1
	exitUsage       = 2
	exitNoSummaries = 3
)

var commands = map[string]func(context.Context, []string, io.Writer, io.Writer) int{
	"serve":  runServe,
	"up":     runUp,
	"down":   runDown,
	"status": runStatus,
	"reset":  runReset,
	"diff":   runDiff,
}

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
	readingLevel  string
}

// Run executes the command and returns its exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		if command, ok := commands[args[0]]; ok {
			return command(ctx, args[1:], stdout, stderr)
		}
	}
	opts := options{}
	fs := flag.NewFlagSet("diffduck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.out, "out", "", "output directory (default: ./diff-duck-out/<base>..<head>)")
	fs.BoolVar(&opts.structureOnly, "structure-only", false, "skip the model and output only structural facts")
	fs.StringVar(&opts.readingLevel, "reading-level", string(domain.ReadingSixthGrade), "how plainly summaries are written: 2nd-grade, 6th-grade, high-school, college, or phd")
	addModelFlags(fs, &opts)
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}

	// A revision that begins with a dash is read as a flag and never reaches git.
	positional, code, ok := parseInterleaved(fs, args)
	if !ok {
		return code
	}
	if len(positional) != 3 {
		fs.Usage()
		return exitUsage
	}
	if opts.concurrency < 1 {
		fmt.Fprintln(stderr, "error: --concurrency must be at least 1")
		return exitUsage
	}
	level, err := domain.ParseReadingLevel(opts.readingLevel)
	if err != nil {
		fmt.Fprintln(stderr, "error: --reading-level:", err)
		return exitUsage
	}

	if err := execute(ctx, opts, level, positional[0], positional[1], positional[2], stdout); err != nil {
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
	fs.StringVar(&opts.adapter, "adapter", adapterClaudeCLI, "model adapter: "+adapterNames)
	fs.StringVar(&opts.model, "model", "", "model ID for the --adapter provider only (default: that provider's default model)")
	fs.StringVar(&opts.effort, "effort", "medium", "model effort for every provider: low, medium, high, xhigh, or max")
	fs.StringVar(&opts.promptsDir, "prompts", "", "prompt variant directory holding leaf.md, parent.md, outcomes.md, and reading-levels/<level>.md (default: built in)")
	fs.IntVar(&opts.concurrency, "concurrency", 4, "maximum concurrent model calls")
	fs.BoolVar(&opts.noCache, "no-cache", false, "ignore and do not write the model response cache")
	fs.StringVar(&opts.cacheDir, "cache-dir", "", "model response cache directory (default: the user cache directory)")
	fs.IntVar(&opts.maxModelCalls, "max-model-calls", 200, "most model calls in one run, leaving the rest visibly unexplained; 0 means no limit")
}

func execute(ctx context.Context, opts options, level domain.ReadingLevel, target, base, head string, stdout io.Writer) error {
	repo, err := gitrepo.Open(ctx, target)
	if err != nil {
		return err
	}
	defer repo.Close()

	var opened ports.OpenedModel
	var source ports.PromptSource
	if !opts.structureOnly {
		opened, err = openAdapter(opts)
		if err != nil {
			return err
		}
		defer opened.Close()
		if source, err = buildPrompts(opts); err != nil {
			return err
		}
	}

	analyzers, closeAnalyzers, err := buildAnalyzers(ctx)
	if err != nil {
		return err
	}
	defer closeAnalyzers()
	differ := services.NewStructuralDiffer(repo, analyzers...)
	compressor := services.NewDiffCompressor(repo, differ, opened.Model, source, opts.concurrency)
	result, compressErr := compressor.Compress(ctx, services.CompressRequest{
		Repository:    target,
		Base:          base,
		Head:          head,
		StructureOnly: opts.structureOnly,
		MaxModelCalls: opts.maxModelCalls,
		ReadingLevel:  level,
		Provider:      opened.Provider,
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
