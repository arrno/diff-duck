package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/arrno/diff-duck/internal/adapters/sqlitestore"
)

const (
	startTimeout = 20 * time.Second
	stopTimeout  = 10 * time.Second
)

// runUp starts the app in the background. It takes the same flags as serve.
func runUp(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts serveOptions
	fs := serveFlags(&opts, stderr, "diffduck up")
	if code, ok := parseServe(fs, args, &opts); !ok {
		return code
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	if r, _ := findServer(ctx, dir); r != nil {
		fmt.Fprintf(stdout, "DiffDuck is already running at %s\n", r.url())
		return exitOK
	}
	r, err := startServer(ctx, dir, args)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "DiffDuck is running at %s\nStop it with: diffduck down\n", r.url())
	return exitOK
}

func runDown(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck down", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	r, err := findServer(ctx, dir)
	if err != nil {
		return fail(stderr, err)
	}
	if r == nil {
		fmt.Fprintln(stdout, "DiffDuck is not running.")
		return exitOK
	}
	if err := stopServer(dir, r); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintln(stdout, "DiffDuck stopped.")
	return exitOK
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("diffduck status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if code, ok := parseNone(fs, args); !ok {
		return code
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	r, err := findServer(ctx, dir)
	if err != nil {
		return fail(stderr, err)
	}
	if r == nil {
		fmt.Fprintln(stdout, "DiffDuck is not running. Start it with: diffduck up")
		return exitFailure
	}
	fmt.Fprintf(stdout, "DiffDuck is running at %s\nSaved diffs: %s\n", r.url(), printable(r.DB))
	return exitOK
}

// runReset deletes every saved diff. The model settings stay. A running server is stopped
// first and started again afterwards with the same flags.
func runReset(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diffduck reset", flag.ContinueOnError)
	flags.SetOutput(stderr)
	yes := flags.Bool("yes", false, "do not ask for confirmation")
	if code, ok := parseNone(flags, args); !ok {
		return code
	}
	dir, err := home()
	if err != nil {
		return fail(stderr, err)
	}
	r, err := findServer(ctx, dir)
	if err != nil {
		return fail(stderr, err)
	}
	db := defaultDB(dir)
	if r != nil {
		db = r.DB
	}
	if !*yes {
		if !interactive() {
			fmt.Fprintln(stderr, "error: reset deletes every saved diff; pass --yes to confirm")
			return exitUsage
		}
		fmt.Fprintf(stdout, "Delete every saved diff in %s? [y/N] ", printable(db))
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && strings.TrimSpace(answer) == "" {
			fmt.Fprintln(stderr, "\nerror: no answer; pass --yes to confirm")
			return exitUsage
		}
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(stdout, "Nothing was deleted.")
			return exitOK
		}
	}
	if r != nil {
		if err := stopServer(dir, r); err != nil {
			return fail(stderr, err)
		}
	}
	cleared := sqlitestore.ClearDiffs(ctx, db)
	if cleared == nil {
		fmt.Fprintln(stdout, "Deleted every saved diff.")
	}
	// A failed reset still brings back the app it stopped.
	if r != nil {
		restarted, err := startServer(ctx, dir, r.Args)
		if err != nil {
			return fail(stderr, errors.Join(cleared, err))
		}
		fmt.Fprintf(stdout, "DiffDuck is running again at %s\n", restarted.url())
	}
	if cleared != nil {
		return fail(stderr, cleared)
	}
	return exitOK
}

// findServer returns the recorded server if it is still answering, and clears a stale record.
func findServer(ctx context.Context, dir string) (*serverRecord, error) {
	r, err := readRecord(dir)
	if err != nil || r == nil {
		return nil, err
	}
	if newAppClient(*r).health(ctx, r.PID) {
		return r, nil
	}
	removeRecord(dir, r.PID)
	return nil, nil
}

// ensureServer returns the running server, starting one with default settings if needed.
func ensureServer(ctx context.Context, dir string, stderr io.Writer) (*serverRecord, error) {
	if r, err := findServer(ctx, dir); r != nil || err != nil {
		return r, err
	}
	r, err := startServer(ctx, dir, nil)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stderr, "Started DiffDuck at %s (stop it with: diffduck down)\n", r.url())
	return r, nil
}

// startServer runs `serve` detached, logging to the home directory, and waits for it to answer.
func startServer(ctx context.Context, dir string, args []string) (*serverRecord, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(logPath(dir), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	cmd := exec.Command(exe, append([]string{"serve", "--no-open"}, args...)...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(), homeEnv+"="+dir)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start DiffDuck: %w", err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	deadline := time.After(startTimeout)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-exited:
			return nil, fmt.Errorf("DiffDuck did not start:\n%s", logTail(dir))
		case <-deadline:
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("DiffDuck did not start within %s:\n%s", startTimeout, logTail(dir))
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			return nil, ctx.Err()
		case <-tick.C:
			r, _ := readRecord(dir)
			if r != nil && r.PID == cmd.Process.Pid && newAppClient(*r).health(ctx, r.PID) {
				return r, nil
			}
		}
	}
}

func stopServer(dir string, r *serverRecord) error {
	p, err := os.FindProcess(r.PID)
	if err != nil {
		return err
	}
	if err := stopProcess(p); err != nil {
		return fmt.Errorf("stop DiffDuck (pid %d): %w", r.PID, err)
	}
	for deadline := time.Now().Add(stopTimeout); alive(p); {
		if time.Now().After(deadline) {
			return fmt.Errorf("DiffDuck (pid %d) did not stop within %s", r.PID, stopTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
	removeRecord(dir, r.PID)
	return nil
}

func logTail(dir string) string {
	data, err := os.ReadFile(logPath(dir))
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return printable(strings.Join(lines, "\n"))
}

func interactive() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func fail(stderr io.Writer, err error) int {
	fmt.Fprintln(stderr, "error:", printable(err.Error()))
	return exitFailure
}

// parseNone parses a command that takes flags but no arguments.
func parseNone(fs *flag.FlagSet, args []string) (int, bool) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK, false
		}
		return exitUsage, false
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(fs.Output(), "%s takes no arguments\n", fs.Name())
		return exitUsage, false
	}
	return 0, true
}
