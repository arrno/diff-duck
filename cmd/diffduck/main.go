// Command diffduck turns the Git diff between two commits into a compressed diff.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/arrno/diff-duck/internal/controller/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
