//go:build unix

package codexcli

import (
	"os/exec"
	"syscall"
)

// killProcessGroup runs the command in its own process group and, when its context ends,
// kills the whole group. The `codex` on PATH is often a Node wrapper around the native
// binary, so killing only the direct child would leave the native Codex running.
func killProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
