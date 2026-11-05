//go:build unix

package cli

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts cmd in its own session, so it outlives the terminal that ran `up`.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// stopProcess asks the server to shut down cleanly, which marks its running diffs failed.
func stopProcess(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

func alive(p *os.Process) bool { return p.Signal(syscall.Signal(0)) == nil }
