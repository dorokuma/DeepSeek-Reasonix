package permission

import "strings"

// readOnlyBashCommands is the set of commands considered read-only — they
// don't modify filesystem state, network state, or process state. Each
// entry is the first word of a bash command (lowercased). Commands not in
// this set that might also be read-only (e.g. "git log") are handled
// separately by isReadOnlyBashSubject.
var readOnlyBashCommands = map[string]bool{
	"cat": true, "head": true, "tail": true, "less": true, "more": true,
	"ls": true, "find": true, "locate": true, "which": true, "whereis": true, "type": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true,
	"echo": true, "printf": true,
	"pwd": true, "cd": true, "whoami": true, "id": true, "uname": true, "hostname": true,
	"date": true, "env": true, "printenv": true,
	"wc": true, "sort": true, "uniq": true, "cut": true, "tr": true,
	"stat": true, "file": true, "du": true, "df": true,
	"ps": true, "top": true, "htop": true,
	"diff": true, "cmp": true, "comm": true,
	"man": true, "info": true, "help": true,
	"true": true, "false": true, "test": true, "[": true,
	"basename": true, "dirname": true, "realpath": true, "readlink": true,
	// Read-only utilities
	"jq": true, "yq": true,
	"dig": true, "nslookup": true, "host": true,
	"ping": true, "traceroute": true, "mtr": true,
	"ss": true, "ip": true, "ifconfig": true, "netstat": true, "lsof": true,
	"journalctl": true,
}

// readOnlyBashPrefixes are command prefixes where the second word
// determines read-only status. Each maps to the set of read-only
// subcommands.
var readOnlyBashPrefixes = map[string]map[string]bool{
	"git": {
		"log": true, "status": true, "diff": true, "show": true,
		"tag":   true,
		"blame": true, "grep": true, "ls-files": true, "ls-tree": true,
		"rev-parse": true, "rev-list": true, "describe": true, "reflog": true,
		"shortlog": true, "whatchanged": true, "cherry": true,
		"cat-file": true, "for-each-ref": true, "name-rev": true,
	},
	"go": {
		"vet": true, "doc": true, "list": true,
		"version": true, "env": true,
	},
	"npm": {
		"ls": true, "list": true, "view": true, "info": true,
		"outdated": true, "audit": true,
	},
	"cargo": {
		"check": true, "doc": true, "search": true,
	},
	"docker": {
		"ps": true, "images": true, "inspect": true, "logs": true,
		"stats": true, "info": true, "version": true,
	},
	"kubectl": {
		"get": true, "describe": true, "logs": true, "explain": true,
		"api-resources": true, "api-versions": true,
	},
}

// isReadOnlyBashSubject returns true when a bash command is a known
// read-only operation. The subject is the JSON arg value extracted by
// Subject() — for bash it is the raw command string.
func isReadOnlyBashSubject(subject string) bool {
	cmd := strings.TrimSpace(subject)
	if cmd == "" {
		return false
	}
	// Hard shell syntax (semicolons, backticks, $(), redirects, chaining) is always unsafe.
	if containsUnsafeShellSyntax(cmd) {
		return false
	}
	// Pipes: split on | and verify every segment is read-only.
	if strings.ContainsRune(cmd, '|') {
		return isReadOnlyPipeline(cmd)
	}
	return isReadOnlySingleCommand(cmd)
}

// isReadOnlyPipeline checks that every command in a pipe is read-only.
// "ls | head" → true ; "ls | rm -rf /" → false ; "cat f | tee x" → false
func isReadOnlyPipeline(cmd string) bool {
	segments := splitPipeline(cmd)
	if len(segments) <= 1 {
		return isReadOnlySingleCommand(strings.TrimSpace(cmd))
	}
	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		if !isReadOnlySingleCommand(seg) {
			return false
		}
	}
	return true
}

// splitPipeline splits cmd on unquoted pipe characters.
func splitPipeline(cmd string) []string {
	var parts []string
	var current strings.Builder
	var quote byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			current.WriteByte(c)
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			current.WriteByte(c)
			continue
		}
		if c == '|' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteByte(c)
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	return parts
}

// isReadOnlySingleCommand checks a single (non-pipeline) command.
func isReadOnlySingleCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	base := strings.ToLower(fields[0])

	// Check single-word read-only commands.
	if readOnlyBashCommands[base] {
		return !hasUnsafeReadOnlyArgs(base, fields[1:])
	}

	// Check prefix commands (git log, go vet, etc.).
	if len(fields) > 1 {
		if sub, ok := readOnlyBashPrefixes[base]; ok {
			subcmd := strings.ToLower(fields[1])
			return sub[subcmd] && !hasUnsafePrefixArgs(base, subcmd, fields[2:])
		}
	}
	return false
}

// containsShellSyntax is the original check kept for compatibility with
// BashDangerWarning callers. Use containsUnsafeShellSyntax for gating.
func containsShellSyntax(cmd string) bool {
	upper := strings.ToUpper(cmd)
	return strings.ContainsAny(cmd, ";|&<>\n`") || strings.Contains(upper, "$(") || strings.Contains(cmd, "${")
}

// containsUnsafeShellSyntax returns true when cmd contains shell operators
// that are always unsafe — pipes (|) are handled separately by isReadOnlyPipeline.
func containsUnsafeShellSyntax(cmd string) bool {
	upper := strings.ToUpper(cmd)
	// Semicolons, backticks, $(), ${}, &&, ||, &, >, < are all unsafe.
	return strings.ContainsAny(cmd, ";&<>\n`") || strings.Contains(cmd, "||") || strings.Contains(upper, "$(") || strings.Contains(cmd, "${")
}

func hasUnsafeReadOnlyArgs(base string, args []string) bool {
	switch base {
	case "find":
		return hasAnyArg(args, "-exec", "-execdir", "-delete", "-ok")
	case "sed":
		for _, arg := range args {
			if strings.HasPrefix(arg, "-i") || strings.HasPrefix(arg, "--in-place") {
				return true
			}
			// sed -e 'w /etc/crontab' writes to arbitrary paths.
			if arg == "w" || strings.HasPrefix(arg, "w ") {
				return true
			}
		}
	case "sort":
		return hasArgWithPrefix(args, "-o") || hasAnyArg(args, "--output") || hasArgWithPrefix(args, "--output=")
	}
	return false
}

func hasUnsafePrefixArgs(base, subcmd string, args []string) bool {
	switch base {
	case "git":
		switch subcmd {
		case "diff", "show", "log":
			return hasAnyArg(args, "--output") || hasArgWithPrefix(args, "--output=")
		}
	case "go":
		if subcmd == "env" {
			return hasAnyArg(args, "-w", "-u")
		}
	}
	return false
}

func hasArgWithPrefix(args []string, prefix string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, prefix) {
			return true
		}
	}
	return false
}

func hasAnyArg(args []string, unsafe ...string) bool {
	for _, arg := range args {
		for _, candidate := range unsafe {
			if arg == candidate {
				return true
			}
		}
	}
	return false
}

// dangerousBashPatterns are glob-like patterns that match destructive
// commands. Used only for a UI warning — the deny list is the actual
// enforcement mechanism.
var dangerousBashPatterns = []struct {
	pattern string
	label   string
}{
	{"rm -rf*", "recursive delete"},
	{"rm -r *", "recursive delete"},
	{"rm -fr*", "recursive delete"},
	{"git push*--force*", "force push"},
	{"git push*-f*", "force push"},
	{"git reset --hard*", "hard reset"},
	{"git clean -f*", "force clean"},
	{"chmod 777*", "world-writable"},
	{"chmod -R 777*", "world-writable recursive"},
	{"chown *", "ownership change"},
	{"sudo *", "superuser"},
	{"mkfs*", "filesystem format"},
	{"dd if=*", "raw device write"},
	{"fdisk*", "partition table"},
	{"> /dev/*", "device overwrite"},
}

// BashDangerWarning returns a short label if subject matches a known
// dangerous pattern, or "" when the command looks safe. This is a visual
// hint only — the Policy rules are the authority.
func BashDangerWarning(subject string) string {
	s := strings.TrimSpace(subject)
	for _, d := range dangerousBashPatterns {
		if matchGlob(d.pattern, s) {
			return d.label
		}
	}
	return ""
}
