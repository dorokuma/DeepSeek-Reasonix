//go:build linux

package notify

import "os/exec"

// PlatformSender delivers notifications through the host OS.
type PlatformSender struct{}

// NewPlatformSender returns the best-effort sender for the current platform.
func NewPlatformSender() PlatformSender { return PlatformSender{} }

func (PlatformSender) Send(m Message) error {
	return exec.Command("notify-send", m.Title, m.Body).Run()
}
