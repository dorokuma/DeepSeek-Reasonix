//go:build windows

package proc

import (
	"os/exec"
	"strconv"
)

// KillTree terminates cmd and every descendant it spawned. Process.Kill only
// signals the direct child, so a launcher (cmd.exe → node.exe) leaves the
// grandchild alive holding the inherited stdout/stderr pipes — which makes
// cmd.Wait block forever. taskkill /T walks the live tree and kills it all.
func KillTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	kill := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
	HideWindow(kill)
	_ = kill.Run()
	_ = cmd.Process.Kill()
}
