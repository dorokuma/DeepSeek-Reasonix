package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveWorkspacePath resolves a user-supplied path (raw) with workspace
// confinement. When raw is empty it defaults to <workDir>/defaultBasename.
// workDir may be empty (falls back to os.Getwd). roots may be nil (unconfined).
func resolveWorkspacePath(workDir, defaultBasename, raw string, roots []string) (string, error) {
	path := strings.TrimSpace(raw)
	if path == "" {
		base := workDir
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return "", fmt.Errorf("getwd: %w", err)
			}
			base = wd
		}
		path = filepath.Join(base, defaultBasename)
	} else {
		path = resolveIn(workDir, path)
	}
	if err := confine(roots, path); err != nil {
		return "", err
	}
	return path, nil
}
