package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/arrno/diff-duck/internal/controller/server"
	"github.com/arrno/diff-duck/internal/domain"
)

const diffUsage = `Usage:
  diffduck diff [repo] [-f from] [-t to] [--wait] [--no-open]
  diffduck diff list [--json]
  diffduck diff open [id]
  diffduck diff delete <id>...

The first form builds a diff in the running app, starting it if needed, and opens it in the
browser. repo is a path or clone URL (default: the current directory). To diff a directory
named list, open, or delete, write it as ./list.

Flags for building a diff:
`

func runDiff(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "list", "ls":
			return runDiffList(ctx, args[1:], stdout, stderr)
		case "open":
			return runDiffOpen(ctx, args[1:], stdout, stderr)
		case "delete", "rm":
			return runDiffDelete(ctx, args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("diffduck diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var from, to string
	fs.StringVar(&from, "f", "HEAD~1", "the commit to compare from")
	fs.StringVar(&from, "from", "HEAD~1", "same as -f")
	fs.StringVar(&to, "t", "HEAD", "the commit to compare to")
	fs.StringVar(&to, "to", "HEAD", "same as -t")
	wait := fs.Bool("wait", false, "wait until the diff finishes and print its overview")
	noOpen := fs.Bool("no-open", false, "do not open a browser")
	fs.Usage = func() {
		fmt.Fprint(stderr, diffUsage)
		fs.PrintDefaults()
	}
	positional, code, ok := parseInterleaved(fs, args)
	if !ok {
		return code
	}
	if len(positional) > 1 {
		fs.Usage()
		return exitUsage
	}
	repo := "."
	if len(positional) == 1 {
		repo = positional[0]
	}

	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	r, err := ensureServer(ctx, dir, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	client := newAppClient(*r)
	// Resolve here, since a relative path means this shell's directory, not the server's.
	d, err := client.create(ctx, server.ResolveRepository(repo), from, to)
	if err != nil {
		return fail(stderr, err)
	}
	url := r.url() + "#/diffs/" + d.ID
	fmt.Fprintf(stdout, "Diff %s (%s..%s in %s)\n%s\n", d.ID, printable(from), printable(to), printable(d.Repository), url)
	if !*noOpen {
		openBrowser(url)
	}
	if !*wait {
		return exitOK
	}
	return waitForDiff(ctx, client, d.ID, stdout, stderr)
}

// waitForDiff reports each stage on stderr and the finished overview on stdout.
func waitForDiff(ctx context.Context, client *appClient, id string, stdout, stderr io.Writer) int {
	stage := ""
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		d, err := client.get(ctx, id)
		if err != nil {
			return fail(stderr, err)
		}
		switch d.Status {
		case domain.SavedDiffDone:
			fmt.Fprintf(stdout, "Done: %d files, +%d -%d\n", d.Files, d.Added, d.Removed)
			if d.Overview != "" {
				fmt.Fprintln(stdout, printable(d.Overview))
			}
			if d.Error != "" {
				fmt.Fprintln(stderr, "warning:", printable(d.Error))
			}
			return exitOK
		case domain.SavedDiffFailed:
			return fail(stderr, errors.New(d.Error))
		}
		if d.Stage != stage {
			stage = d.Stage
			fmt.Fprintln(stderr, printable(stage)+"…")
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(stderr, "Stopped waiting. The diff keeps building in the app.")
			return exitFailure
		case <-tick.C:
		}
	}
}

func runDiffList(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck diff list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	client, code, ok := connect(ctx, stderr)
	if !ok {
		return code
	}
	diffs, err := client.list(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if *asJSON {
		if diffs == nil {
			diffs = []domain.SavedDiff{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(diffs)
		return exitOK
	}
	if len(diffs) == 0 {
		fmt.Fprintln(stdout, "No saved diffs.")
		return exitOK
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tCHANGE\tFILES\tCREATED\tREPOSITORY")
	for _, d := range diffs {
		fmt.Fprintf(tw, "%s\t%s\t%s..%s\t%d\t%s\t%s\n", d.ID, d.Status, printable(d.Base), printable(d.Head), d.Files,
			d.CreatedAt.Local().Format("Jan 2 15:04"), printable(d.Repository))
	}
	_ = tw.Flush()
	return exitOK
}

func runDiffOpen(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck diff open", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: diffduck diff open [id]")
		return exitUsage
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	r, err := ensureServer(ctx, dir, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	url := r.url()
	if id := fs.Arg(0); id != "" {
		if _, err := newAppClient(*r).get(ctx, id); err != nil {
			return fail(stderr, err)
		}
		url += "#/diffs/" + id
	}
	fmt.Fprintln(stdout, url)
	openBrowser(url)
	return exitOK
}

func runDiffDelete(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck diff delete", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: diffduck diff delete <id>...")
		return exitUsage
	}
	client, code, ok := connect(ctx, stderr)
	if !ok {
		return code
	}
	result := exitOK
	for _, id := range fs.Args() {
		if err := client.delete(ctx, id); err != nil {
			fmt.Fprintf(stderr, "error: %s: %s\n", printable(id), printable(err.Error()))
			result = exitFailure
			continue
		}
		fmt.Fprintln(stdout, "Deleted", id)
	}
	return result
}

// connect reaches the running app, starting it if needed.
func connect(ctx context.Context, stderr io.Writer) (*appClient, int, bool) {
	dir, err := home()
	if err != nil {
		return nil, fail(stderr, err), false
	}
	r, err := ensureServer(ctx, dir, stderr)
	if err != nil {
		return nil, fail(stderr, err), false
	}
	return newAppClient(*r), 0, true
}

// parseInterleaved allows flags before, between, or after positional arguments. An argument
// that begins with a dash is therefore always read as a flag.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, int, bool) {
	var positional []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, exitOK, false
			}
			return nil, exitUsage, false
		}
		rest = fs.Args()
		if len(rest) == 0 {
			return positional, 0, true
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
}
