package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/arrno/diff-duck/internal/adapters/gitrepo"
	"github.com/arrno/diff-duck/internal/adapters/goanalysis"
	"github.com/arrno/diff-duck/internal/adapters/sqlitestore"
	"github.com/arrno/diff-duck/internal/controller/htmlview"
	"github.com/arrno/diff-duck/internal/controller/server"
	"github.com/arrno/diff-duck/internal/services"
)

const defaultPort = 7474

type serveOptions struct {
	options
	port   int
	dbPath string
	noOpen bool
}

func runServe(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var opts serveOptions
	fs := flag.NewFlagSet("diff-duck serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&opts.port, "port", defaultPort, "port on the loopback address; 0 picks a free one")
	fs.StringVar(&opts.dbPath, "db", "", "database file (default: diff-duck/diffs.db in the user config directory)")
	fs.BoolVar(&opts.noOpen, "no-open", false, "do not open a browser")
	addModelFlags(fs, &opts.options)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || opts.concurrency < 1 || opts.port < 0 || opts.port > 65535 {
		fs.Usage()
		return exitUsage
	}
	if err := serve(ctx, opts, stdout); err != nil {
		fmt.Fprintln(stderr, "error:", printable(err.Error()))
		return exitFailure
	}
	return exitOK
}

func serve(ctx context.Context, opts serveOptions, stdout io.Writer) error {
	dbPath := opts.dbPath
	if dbPath == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return fmt.Errorf("locate config directory (use --db): %w", err)
		}
		dbPath = filepath.Join(base, "diff-duck", "diffs.db")
	}
	store, err := sqlitestore.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	model, closeModel, err := buildModel(opts.options)
	if err != nil {
		return err
	}
	defer closeModel()
	prompts, err := buildPrompts(opts.options)
	if err != nil {
		return err
	}
	page, err := htmlview.RenderApp()
	if err != nil {
		return err
	}

	library := services.NewDiffLibrary(services.LibraryDeps{
		Store:         store,
		Repositories:  gitrepo.Source{},
		Analyzer:      goanalysis.New(),
		Model:         model,
		Prompts:       prompts,
		Concurrency:   opts.concurrency,
		MaxModelCalls: opts.maxModelCalls,
	})
	if err := library.Recover(ctx); err != nil {
		return err
	}
	defer library.Shutdown()

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(opts.port)))
	if err != nil {
		return fmt.Errorf("listen on port %d (use --port to choose another): %w", opts.port, err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	httpServer := &http.Server{
		Handler:           server.Handler(library, page, port),
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	fmt.Fprintf(stdout, "DiffDuck is running at %s\nSaved diffs: %s\nPress Ctrl+C to stop.\n", url, printable(dbPath))
	if !opts.noOpen {
		openBrowser(url)
	}

	failed := make(chan error, 1)
	go func() { failed <- httpServer.Serve(listener) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return httpServer.Shutdown(shutdown)
}

// openBrowser is a convenience, and the address is already printed if it does not work.
func openBrowser(url string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if cmd := exec.Command(name, url); cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}
