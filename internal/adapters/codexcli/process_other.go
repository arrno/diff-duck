//go:build !unix

package codexcli

import "os/exec"

// killProcessGroup keeps exec's default of killing the direct child; cmd.WaitDelay still
// bounds how long a surviving grandchild can hold the output open.
func killProcessGroup(*exec.Cmd) {}
