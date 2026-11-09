//go:build !unix

package codexcli

import "os/exec"

// canStopProcessTree is false here: this platform gives the adapter no way to kill the
// native Codex that the `codex` wrapper starts, so the first-tool-call stop (K2) could not
// reach it. Available reports false and New refuses, failing closed.
const canStopProcessTree = false

// killProcessGroup is never reached, because New refuses on this platform; it exists so the
// package builds here.
func killProcessGroup(*exec.Cmd) {}
