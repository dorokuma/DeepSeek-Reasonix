//go:build windows

package mcptrust

import "golang.org/x/sys/windows"

const trustLockStillActiveExitCode = 259

func trustLockProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied still proves the process exists. Treating it as stale
		// would let a less-privileged waiter steal a live writer's lock.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(handle)
	var code uint32
	return windows.GetExitCodeProcess(handle, &code) == nil && code == trustLockStillActiveExitCode
}
