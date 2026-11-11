package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
)

const installPath = "github.com/arrno/diffduck/cmd/diffduck"

const updateUsage = `Usage:
  diffduck update [version]

Installs diffduck with go install, then restarts the app if it is running so it runs the new
binary. Saved diffs, settings, and API keys are kept. version is latest (the default, meaning
the newest release tag), main (the newest commit), a release tag such as v1.2.0, or a commit.
Needs the Go toolchain on PATH.
`

// updateTarget allows only what go install takes as a version, so nothing else reaches it.
var updateTarget = regexp.MustCompile(`^(latest|main|v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?|[0-9a-f]{7,40})$`)

// currentVersion is the module version the Go toolchain stamped into this binary: a release
// tag for go install @version, or a pseudo-version for a build from a checkout.
func currentVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "devel"
}

func runVersion(_ context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	fmt.Fprintln(stdout, "diffduck", currentVersion())
	return exitOK
}

func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, updateUsage) }
	positional, code, ok := parseInterleaved(fs, args)
	if !ok {
		return code
	}
	target := "latest"
	if len(positional) == 1 {
		target = positional[0]
	}
	if len(positional) > 1 || !updateTarget.MatchString(target) {
		fs.Usage()
		return exitUsage
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		return fail(stderr, errors.New("update runs go install, but no go command is on PATH; install Go from https://go.dev/dl/"))
	}
	binDir, err := goBinDir(ctx, goTool, dir)
	if err != nil {
		return fail(stderr, err)
	}

	before := currentVersion()
	fmt.Fprintf(stderr, "Installing %s@%s…\n", installPath, target)
	install := exec.CommandContext(ctx, goTool, "install", installPath+"@"+target)
	// Run outside any module or workspace the shell happens to be in.
	install.Dir = dir
	install.Stdout, install.Stderr = stderr, stderr
	if err := install.Run(); err != nil {
		return fail(stderr, fmt.Errorf("go install failed: %w", err))
	}
	exe := filepath.Join(binDir, "diffduck")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	after, err := installedVersion(ctx, exe)
	if err != nil {
		return fail(stderr, fmt.Errorf("go install reported success, but %s does not run: %w", printable(exe), err))
	}
	if after == before {
		fmt.Fprintf(stdout, "diffduck %s is installed at %s\n", printable(after), printable(exe))
	} else {
		fmt.Fprintf(stdout, "Updated diffduck from %s to %s at %s\n", before, printable(after), printable(exe))
	}
	warnShadowed(exe, stderr)

	// A running app keeps running the old binary until it is restarted.
	r, err := findServer(ctx, dir)
	if err != nil {
		return fail(stderr, err)
	}
	if r == nil {
		return exitOK
	}
	if err := stopServer(dir, r); err != nil {
		return fail(stderr, err)
	}
	restarted, err := launchServer(ctx, exe, dir, r.Args)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "diffduck is running again at %s\n", restarted.url())
	return exitOK
}

// goBinDir is where go install writes binaries: GOBIN, or bin under the first GOPATH entry.
// It asks for JSON because GOBIN is usually unset, and as plain lines an empty value is easy to
// lose: trimming the output once made GOPATH look like GOBIN.
func goBinDir(ctx context.Context, goTool, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, goTool, "env", "-json", "GOBIN", "GOPATH")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("go env failed: %w", err)
	}
	var env struct{ GOBIN, GOPATH string }
	if err := json.Unmarshal(out, &env); err != nil {
		return "", fmt.Errorf("go env answered something other than JSON: %w", err)
	}
	if gobin := strings.TrimSpace(env.GOBIN); gobin != "" {
		return gobin, nil
	}
	if list := filepath.SplitList(env.GOPATH); len(list) > 0 && strings.TrimSpace(list[0]) != "" {
		return filepath.Join(strings.TrimSpace(list[0]), "bin"), nil
	}
	return "", errors.New("go env names neither GOBIN nor GOPATH, so the install location is unknown")
}

func installedVersion(ctx context.Context, exe string) (string, error) {
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(strings.TrimPrefix(string(bytes.TrimSpace(out)), "diffduck")), nil
}

// warnShadowed says when the diffduck a shell finds is not the one just installed.
func warnShadowed(installed string, stderr io.Writer) {
	found, err := exec.LookPath("diffduck")
	if err != nil {
		fmt.Fprintf(stderr, "warning: %s is not on PATH, so diffduck still runs nothing new; add it to PATH\n", printable(filepath.Dir(installed)))
		return
	}
	if !samePath(found, installed) {
		fmt.Fprintf(stderr, "warning: diffduck on PATH is %s, not the updated %s; remove it or put %s first on PATH\n",
			printable(found), printable(installed), printable(filepath.Dir(installed)))
	}
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return filepath.Clean(a) == filepath.Clean(b)
	}
	return ra == rb
}
