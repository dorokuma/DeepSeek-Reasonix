//go:build !windows

package proc

import "os/exec"

// KillTree terminates cmd's process; off Windows it kills the direct child only.
func KillTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
}
