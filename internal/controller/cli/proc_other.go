//go:build !unix

package cli

import (
	"os"
	"os/exec"
)

func detach(*exec.Cmd) {}

// stopProcess cannot ask nicely here, so the next start marks interrupted diffs failed.
func stopProcess(p *os.Process) error { return p.Kill() }

func alive(p *os.Process) bool {
	_, err := os.FindProcess(p.Pid)
	return err == nil
}
