package rtk

// CoverageEntry documents how one RTK filter is reached from Reasonix.
type CoverageEntry struct {
	RTKCommand string // rtk subcommand, e.g. "git"
	Via        string // "bash", "builtin:grep", "builtin:ls", "builtin:glob", "none"
	Notes      string
}

// Coverage documents Reasonix integration for each RTK output filter.
// Meta/admin commands (init, config, gain, hook, rewrite, …) are intentionally
// "bash" only — the model reaches them when it types the shell command.
func Coverage() []CoverageEntry {
	return []CoverageEntry{
		{RTKCommand: "ls", Via: "bash+builtin:ls", Notes: "rewrite gate on ls shell"},
		{RTKCommand: "tree", Via: "bash+builtin:ls", Notes: "recursive ls tries tree when rewrite accepts"},
		{RTKCommand: "read", Via: "bash", Notes: "cat/head/tail; read_file builtin stays native (line numbers)"},
		{RTKCommand: "grep", Via: "bash+builtin:grep", Notes: "bash rg/grep; builtin engine=rtk uses rewrite gate"},
		{RTKCommand: "find", Via: "bash", Notes: "glob builtin stays native — RTK find output is tree-shaped"},
		{RTKCommand: "git", Via: "bash"},
		{RTKCommand: "gh", Via: "bash"},
		{RTKCommand: "glab", Via: "bash"},
		{RTKCommand: "aws", Via: "bash"},
		{RTKCommand: "psql", Via: "bash"},
		{RTKCommand: "pnpm", Via: "bash"},
		{RTKCommand: "npm", Via: "bash"},
		{RTKCommand: "err", Via: "bash"},
		{RTKCommand: "test", Via: "bash", Notes: "explicit rtk test or make test → rtk make"},
		{RTKCommand: "json", Via: "bash", Notes: "explicit rtk json"},
		{RTKCommand: "deps", Via: "bash", Notes: "explicit rtk deps; no common native rewrite"},
		{RTKCommand: "env", Via: "bash", Notes: "explicit rtk env; plain env declines rewrite"},
		{RTKCommand: "diff", Via: "bash"},
		{RTKCommand: "log", Via: "bash", Notes: "explicit rtk log; git log routes via rtk git"},
		{RTKCommand: "dotnet", Via: "bash", Notes: "partial rewrite coverage"},
		{RTKCommand: "docker", Via: "bash", Notes: "partial rewrite coverage"},
		{RTKCommand: "kubectl", Via: "bash"},
		{RTKCommand: "summary", Via: "bash", Notes: "explicit rtk summary"},
		{RTKCommand: "wget", Via: "bash"},
		{RTKCommand: "wc", Via: "bash"},
		{RTKCommand: "jest", Via: "bash"},
		{RTKCommand: "vitest", Via: "bash"},
		{RTKCommand: "prisma", Via: "bash"},
		{RTKCommand: "tsc", Via: "bash"},
		{RTKCommand: "next", Via: "bash"},
		{RTKCommand: "lint", Via: "bash", Notes: "eslint → rtk lint"},
		{RTKCommand: "prettier", Via: "bash"},
		{RTKCommand: "format", Via: "bash", Notes: "explicit rtk format; ruff format routes via rtk ruff"},
		{RTKCommand: "playwright", Via: "bash"},
		{RTKCommand: "cargo", Via: "bash"},
		{RTKCommand: "npx", Via: "bash"},
		{RTKCommand: "curl", Via: "bash"},
		{RTKCommand: "ruff", Via: "bash"},
		{RTKCommand: "pytest", Via: "bash"},
		{RTKCommand: "mypy", Via: "bash"},
		{RTKCommand: "rake", Via: "bash"},
		{RTKCommand: "rubocop", Via: "bash"},
		{RTKCommand: "rspec", Via: "bash"},
		{RTKCommand: "pip", Via: "bash"},
		{RTKCommand: "go", Via: "bash"},
		{RTKCommand: "gt", Via: "bash"},
		{RTKCommand: "golangci-lint", Via: "bash"},
		{RTKCommand: "gradlew", Via: "bash"},
		{RTKCommand: "smart", Via: "bash", Notes: "explicit rtk smart only; no native shell rewrite"},
		{RTKCommand: "run", Via: "none", Notes: "RTK meta executor"},
		{RTKCommand: "proxy", Via: "none", Notes: "RTK meta"},
		{RTKCommand: "pipe", Via: "agent", Notes: "large tool output when filter known; never for MCP"},
		{RTKCommand: "rewrite", Via: "none", Notes: "integration gate"},
		{RTKCommand: "hook", Via: "none", Notes: "external CLI hooks"},
		{RTKCommand: "init", Via: "none", Notes: "RTK setup"},
		{RTKCommand: "config", Via: "none", Notes: "RTK setup"},
		{RTKCommand: "gain", Via: "none", Notes: "RTK analytics"},
		{RTKCommand: "discover", Via: "none", Notes: "RTK analytics"},
		{RTKCommand: "session", Via: "none", Notes: "RTK analytics"},
		{RTKCommand: "telemetry", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "learn", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "trust", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "untrust", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "verify", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "hook-audit", Via: "none", Notes: "RTK admin"},
		{RTKCommand: "cc-economics", Via: "none", Notes: "RTK analytics"},
		{RTKCommand: "help", Via: "none", Notes: "RTK CLI help"},
	}
}