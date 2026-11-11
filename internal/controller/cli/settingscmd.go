package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/arrno/diffduck/internal/services"
)

const settingsUsage = `Usage:
  diffduck settings [--json]
  diffduck settings set [--provider id] [--reading-level id] [--api-key-stdin] [--json]
  diffduck settings clear-key <provider> [--json]

These read and change the model settings new diffs use, and start the app if it is not
running. Providers: claude-code, codex, anthropic-api, openai-api. Reading levels: 2nd-grade,
6th-grade, high-school, college, phd.

An API key is only ever read from a pipe, so it stays out of shell history and process
listings, and it is kept in the OS keychain. For example:
  diffduck settings set --provider openai-api --api-key-stdin < key.txt
`

// maxKeyInput bounds what is read from stdin. It is well above any key, so an oversized key
// still reaches the service and is rejected there rather than being cut short here.
const maxKeyInput = 64 << 10

func runSettings(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "set":
			return runSettingsSet(ctx, args[1:], stdout, stderr)
		case "clear-key":
			return runSettingsClearKey(ctx, args[1:], stdout, stderr)
		}
	}
	fs := flag.NewFlagSet("diffduck settings", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, settingsUsage)
		fs.PrintDefaults()
	}
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	client, code, ok := connect(ctx, stderr)
	if !ok {
		return code
	}
	view, err := client.settings(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	printSettings(stdout, view, *asJSON)
	return exitOK
}

func runSettingsSet(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck settings set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "the provider new diffs use")
	level := fs.String("reading-level", "", "how plainly summaries are written")
	keyFromStdin := fs.Bool("api-key-stdin", false, "read the API key for --provider from a pipe")
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, settingsUsage)
		fs.PrintDefaults()
	}
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	if *provider == "" && *level == "" && !*keyFromStdin {
		fs.Usage()
		return exitUsage
	}
	// A key must never land on whichever provider happens to be saved.
	if *keyFromStdin && *provider == "" {
		fmt.Fprintln(stderr, "error: --api-key-stdin needs --provider to say whose key it is")
		return exitUsage
	}
	key := ""
	if *keyFromStdin {
		var err error
		if key, err = readPipedKey(); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitUsage
		}
	}

	client, code, ok := connect(ctx, stderr)
	if !ok {
		return code
	}
	// The API saves both settings together, so a flag left out keeps its saved value.
	current, err := client.settings(ctx)
	if err != nil {
		return fail(stderr, err)
	}
	if *provider == "" {
		*provider = string(current.Provider)
	}
	if *level == "" {
		*level = string(current.ReadingLevel)
	}
	view, err := client.saveSettings(ctx, *provider, *level, key)
	if err != nil {
		return fail(stderr, err)
	}
	if view.Pinned != "" && string(current.Provider) != *provider {
		fmt.Fprintf(stderr, "warning: the app was started with --adapter %s, so new diffs use that until it restarts without --adapter\n", printable(view.Pinned))
	}
	printSettings(stdout, view, *asJSON)
	return exitOK
}

// readPipedKey returns the key on stdin. A terminal is refused because it would echo the key
// onto the screen; empty input is refused because an empty key means "keep the saved one".
func readPipedKey() (string, error) {
	if interactive() {
		return "", errors.New("--api-key-stdin reads a pipe or file, not a terminal: add < key.txt, or use the app's settings")
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxKeyInput))
	if err != nil {
		return "", errors.New("the API key could not be read from stdin")
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", errors.New("--api-key-stdin found no API key on stdin")
	}
	return key, nil
}

func runSettingsClearKey(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck settings clear-key", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "print JSON")
	positional, code, ok := parseInterleaved(fs, args)
	if !ok {
		return code
	}
	if len(positional) != 1 {
		fmt.Fprintln(stderr, "usage: diffduck settings clear-key <provider> [--json]")
		return exitUsage
	}
	client, code, ok := connect(ctx, stderr)
	if !ok {
		return code
	}
	view, err := client.clearKey(ctx, positional[0])
	if err != nil {
		return fail(stderr, err)
	}
	if string(view.Provider) == positional[0] && view.Pinned == "" {
		fmt.Fprintf(stderr, "warning: %s is still selected, so new diffs fail until you save a key or choose another provider\n", view.Provider.Label())
	}
	printSettings(stdout, view, *asJSON)
	return exitOK
}

func printSettings(w io.Writer, view *services.SettingsView, asJSON bool) {
	if asJSON {
		printJSON(w, view)
		return
	}
	fmt.Fprintf(w, "Provider: %s (%s)\n", view.Provider.Label(), view.Provider)
	fmt.Fprintf(w, "Reading level: %s (%s)\n", view.ReadingLevel.Label(), view.ReadingLevel)
	if view.Pinned != "" {
		fmt.Fprintf(w, "Pinned: %s, which new diffs use whatever provider is saved\n", printable(view.Pinned))
	}
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tAVAILABLE\tAPI KEY")
	for _, p := range view.Providers {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.ID, printable(p.Label), yesNo(p.Available), keyState(p))
	}
	_ = tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func keyState(p services.ProviderStatus) string {
	switch {
	case !p.NeedsKey:
		return "not needed"
	case p.KeySaved:
		return "saved"
	}
	return "not saved"
}
