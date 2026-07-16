// Formats a tool call as a Claude-style card line: a "● Verb(primary arg)"
// header instead of the raw "-> name {json}", plus the "⎿" continuation gutter.
package cli

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"reasonix/internal/tool"
)

// connector is the Claude-style "⎿" gutter that ties a continuation block (tool
// output, streamed thinking) to the header line above it.
const connector = "  ⎿  "

// connectorBlock renders lines under the connector: the first carries the "⎿"
// gutter, the rest align beneath it. Returns "" for no lines.
func connectorBlock(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	indent := strings.Repeat(" ", ansi.StringWidth(connector))
	out := dim(connector) + lines[0]
	for _, ln := range lines[1:] {
		out += "\n" + dim(indent) + ln
	}
	return out
}

// assistantBlock wraps rendered markdown with an assistant prefix ("▸") on the
// first line and indents continuation lines to align under it.  The marker uses
// the light cyan/blue toolRead color (not faint grey) so it reads as a live
// reply indicator; body text stays uncolored (terminal default white).
// Distinguishes assistant replies from tool cards (which use "●").
func assistantBlock(rendered string) string {
	lines := strings.Split(rendered, "\n")
	if len(lines) < 2 {
		// Single line (or none): just prepend the prefix.
		return themeFg(activeCLITheme.toolRead, "  ▸ ") + rendered
	}
	prefix := themeFg(activeCLITheme.toolRead, "  ▸ ") // light cyan/blue; visible width: 4 cols
	indent := "    "      // 4 spaces — aligns content under the "▸"
	out := prefix + lines[0]
	for _, ln := range lines[1:] {
		out += "\n" + indent + ln
	}
	return out
}

// userBlock wraps rendered user input with a user prefix ("›") on the
// first line and indents continuation lines to align under it. This
// distinguishes user messages from assistant replies (which use "▸") and
// tool cards (which use "●") in the transcript.
func userBlock(rendered string) string {
	lines := strings.Split(rendered, "\n")
	if len(lines) < 2 {
		return accent("  › ") + rendered
	}
	prefix := accent("  › ") // visible width: 4 cols
	indent := "    "         // 4 spaces — aligns content under the "›"
	out := prefix + lines[0]
	for _, ln := range lines[1:] {
		out += "\n" + indent + ln
	}
	return out
}

// toolVerb maps a tool's snake_case id to the verb shown in its card.
var toolVerb = map[string]string{
	"bash":          "Bash",
	"read_file":     "Read",
	"write_file":    "Write",
	"edit_file":     "Update",
	"multi_edit":    "Update",
	"move_file":     "Move",
	"delete_range":  "Update",
	"delete_symbol": "Update",
	"notebook_edit": "Update",
	"glob":          "Glob",
	"grep":          "Search",
	"ls":            "List",
	"web_search":    "Search",
	"spawn_agent":     "Spawn agent",
	"wait_agent":      "Wait agent",
	"send_input":    "Send input",
	"close_agent":   "Close agent",
	"resume_agent":  "Resume agent",
	"note":          "Note",
	"audit_finish":  "Report",
}

// toolArgKey is the JSON field shown in parentheses for each tool.
var toolArgKey = map[string]string{
	"bash":          "command",
	"read_file":     "path",
	"write_file":    "path",
	"edit_file":     "path",
	"multi_edit":    "path",
	"move_file":     "source_path",
	"delete_range":  "path",
	"delete_symbol": "name",
	"notebook_edit": "path",
	"glob":          "pattern",
	"grep":          "pattern",
	"ls":            "path",
	"web_search":    "query",
	"spawn_agent":     "task_name",
	"note":          "kind",
	"audit_finish":  "summary",
}

// toolDot returns the "●" status glyph coloured by the tool's category so the eye
// can tell reads (cyan) from writes (green), shell (yellow), process control
// (magenta), and everything else (copper) at a glance.
func toolDot(name string) string {
	var c cliColor
	switch toolCategory[name] {
	case "read":
		c = activeCLITheme.toolRead
	case "write":
		c = activeCLITheme.success
	case "exec":
		c = activeCLITheme.warn
	case "proc":
		c = activeCLITheme.toolProc
	default:
		c = activeCLITheme.accent
	}
	return themeFg(c, "●")
}

var toolCategory = map[string]string{
	"read_file": "read", "ls": "read", "glob": "read", "grep": "read",
	"web_search": "read",
	"write_file": "write", "edit_file": "write", "multi_edit": "write",
	"move_file": "write", "delete_range": "write", "delete_symbol": "write", "notebook_edit": "write",
	"note":         "write",
	"audit_finish": "write",
	"bash":         "exec",
}

// toolDisplayName returns the card verb for a tool: a mapped builtin verb, the
// short name for an MCP tool (mcp_server_tool), or the raw id as a fallback.
func toolDisplayName(name string) string {
	if _, short, ok := tool.SplitMCPName(name); ok {
		return short
	}
	if v, ok := toolVerb[name]; ok {
		return v
	}
	return name
}

// toolArg pulls the primary argument shown in the card's parentheses.
func toolArg(name, args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return ""
	}
	v, ok := m[toolArgKey[name]]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		return argList(x)
	case float64:
		return strconv.Itoa(int(x))
	default:
		return ""
	}
}

func argList(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// toolCard renders the dispatch line: "  ● Verb(arg)", with the argument
// wrapped to fit within width. Continuation lines are indented to align with
// the opening parenthesis so the card stays readable on narrow terminals.
func toolCard(name, args string, width int) string {
	arg := toolArg(name, args)
	if arg == "" {
		return "  " + toolDot(name) + " " + bold(toolDisplayName(name))
	}

	// Prefix for the first line: "  ● Name("
	prefix := "  " + toolDot(name) + " " + bold(toolDisplayName(name)) + dim("(")
	prefixW := ansi.StringWidth(prefix)

	// Available width for the arg.  contentW = width-1 (scrollbar column).
	// Reserve 1 column for ")" on the last line.
	avail := (width - 1) - prefixW - 1
	if avail < 1 {
		avail = 1
	}

	wrapped := strings.Split(ansi.Wrap(arg, avail, ""), "\n")
	last := len(wrapped) - 1

	var sb strings.Builder
	sb.WriteString(prefix)
	sb.WriteString(wrapped[0])

	if last == 0 {
		sb.WriteString(dim(")"))
	} else {
		pad := strings.Repeat(" ", prefixW)
		for i := 1; i <= last; i++ {
			sb.WriteString("\n")
			sb.WriteString(pad)
			sb.WriteString(wrapped[i])
			if i == last {
				sb.WriteString(dim(")"))
			}
		}
	}

	return sb.String()
}

// toolHead builds "Verb(arg)" with the verb bold and the arg clamped to fit the
// remaining width; shared by toolCard and the diff block header.
func toolHead(name, arg string, width int) string {
	label := toolDisplayName(name)
	head := bold(label)
	if arg != "" {
		avail := width - 4 - len([]rune(label)) - 2
		head += dim("(") + clampPlain(arg, avail) + dim(")")
	}
	return head
}
