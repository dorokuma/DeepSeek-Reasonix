package rtk

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var helpCmdLine = regexp.MustCompile(`^  ([a-z][a-z0-9-]*)  +`)

// ListHelpCommands runs "rtk --help" and returns documented subcommand names.
// Used by tests to keep Coverage() aligned with the installed RTK binary.
func ListHelpCommands() ([]string, error) {
	if !ensureBin() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "--help").CombinedOutput()
	if err != nil {
		return nil, err
	}
	var cmds []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		m := helpCmdLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		cmds = append(cmds, name)
	}
	return cmds, nil
}