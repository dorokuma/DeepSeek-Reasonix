package skill

// Built-in skills ship with Reasonix (init / test / install-capability).
// A user/project file with the same name overrides the built-in.
// Tool names in the bodies match internal/tool/builtin.

const builtinInstallCapabilityBody = `This skill is INLINED. Use it when the user asks to install a Reasonix MCP server or skill from a URL, local file, local folder, .mcp.json, or package name. For removing a previously installed skill or MCP server, follow the "Uninstall" rules at the bottom — same tool, different op.

Operate as an installer, not as a shell-script guesser:
1. Extract the source string exactly from the user's request. It may be an https URL, GitHub URL, local path, .mcp.json, executable path, or npm package name.
2. Decide kind only when it is explicit. Use kind="auto" when unsure.
3. First call install_source with apply=false. Include scope when the user says project/global. Include mode when they say copy/link/register; otherwise leave mode="auto".
4. Read the returned plan. If status is blocked or failed, report the concrete next step. Do not invent a command from a README when the tool could not identify a manifest.
5. Inspect the plan's actions. Each one carries a riskLevel:
   - low → safe to apply without asking.
   - medium → safe to apply, but mention what was written.
   - high → ask the user to confirm in one short question before apply=true. High actions include MCP installs that send auth headers, eager-tier servers, link targets that are absolute paths outside the project/home root, and any replace=true on an existing entry.
6. If the plan is acceptable and any needed user confirmation has happened, call install_source again with apply=true and echo back the same planId you got from the planning call. The tool refuses to apply when the planId does not match, so always re-fetch by running apply=false again if the user changed their mind about the source. Host permissions may still deny the apply call.
7. After apply=true, report what was installed, where it was persisted, and whether it is usable in the current session. For skills, prefer actions[].canonicalPath, actions[].installRoot, actions[].discoverable, and actions[].indexed over guessing from the source path. The plan's kinds field tells you how many skills vs MCP servers were touched.

Defaults:
- A folder containing many skills should be registered as a skill root, not copied.
- A single SKILL.md, <name>.md, or <name>/SKILL.md should be copied unless the user asked to link/register. The installer writes canonical <skill-name>/SKILL.md paths by default; flat <name>.md is compatibility input, not the preferred output.
- A local SKILL.md source may have references/, scripts/, assets/, or other sibling files. Treat its parent directory as the skill package so those files remain available after install.
- Local skill folders may contain grouped skills up to a bounded depth. Let install_source decide which roots to register instead of telling the user to manually split every nested folder first.
- Remote MCP URLs should use http unless the endpoint is explicitly SSE.
- Package-name MCP installs should default to npx -y <package>.
- Never put raw tokens in headers or config. Prefer ${VAR} placeholders and tell the user which env var to set.

Uninstall (op=uninstall):
- Use op=uninstall with the same name and scope as the original install. Source is ignored.
- Skill and MCP server matching happen in the chosen scope's active config; if you don't know where the entry lives, ask the user. Removal is destructive but symmetric with a previously approved install, so it is applied directly (no approval step).

Stop rather than guessing when the source is only a documentation page, README without a manifest, or a repo whose install command cannot be determined.`

const builtinTestBody = `This skill is INLINED — you run in the parent loop. The user asked you to run the tests and fix failures. Run the project's test suite, diagnose any failure, propose and apply fixes, then re-run. Repeat until green or you hit a wall worth escalating.

How to operate:
1. Detect the test command. Look at the project: go.mod → ` + "`go test ./...`" + `; package.json scripts.test → ` + "`npm test`" + ` (or pnpm/yarn); pyproject.toml/requirements.txt → ` + "`pytest`" + `; Cargo.toml → ` + "`cargo test`" + `. If you can't tell, ASK — don't guess.
2. Run it via bash. Capture stdout + stderr; for intentionally long-running commands, use bash with run_in_background=true and poll with peek-job (not bash_output — that tool does not exist).
3. Read the failures: which tests failed, the actual error, the file + line that threw. Locate the exact assertion or stack frame.
4. Fix each distinct failure:
   - Production bug (test caught a real defect) → fix the production code.
   - Test bug (test is wrong, code is right) → fix the test, and say so explicitly.
   - Environmental (missing dep, wrong toolchain, missing fixture) → say so and stop; don't install packages or change config without checking.
5. Apply the edit and re-run. Iterate.
6. Stop conditions: all green → report what changed; same test still failing after 2 attempts on the same line → STOP and explain; 3+ unrelated failures → fix one at a time, smallest first.

Don't: install/update dependencies without asking; skip/delete/disable failing tests to force green; edit the test runner config to silence failures.

Lead each turn with a one-line status (e.g. "▸ running go test ./… ", "▸ 2 failures in foo_test.go — first is …") so the user always knows where you are.`

const builtinInitBody = `This skill is INLINED — you run in the parent loop. The user invoked /init: bootstrap (or refresh) this project's AGENTS.md — the durable memory file folded into every future session. Analyze the codebase, then write a concise, high-signal AGENTS.md.

How to operate:
1. Check for an existing memory doc first: list the project root and look for AGENTS.md / REASONIX.md / CLAUDE.md. If one exists, read it and IMPROVE it in place (fix stale facts, fill gaps) — write back to that same filename, don't clobber it wholesale or create a second file.
2. Explore enough to be accurate, not exhaustive:
   - Project shape: ls / directory listing, the manifest (go.mod, package.json, pyproject.toml, Cargo.toml, …), the README.
   - Build / test / run commands: derive them from the manifest + scripts and verify the exact names — don't guess.
   - Architecture: the main packages/modules and how they fit; the entry point(s).
   - Conventions: formatting, naming, error handling, testing patterns — infer from real code (read a few representative files), not assumptions.
3. Write AGENTS.md with write_file (default filename AGENTS.md, unless an existing doc uses another name), each section terse:
   - Title + one-line description of the project.
   - ## Project — what it is, the stack, where the entry point lives.
   - ## Commands — the exact build / test / run / lint commands.
   - ## Architecture — the 3-7 load-bearing modules and their roles.
   - ## Conventions — only rules an agent must follow (style, patterns, do/don't).
   - ## Notes — leave an empty stub for later quick-adds.
4. Keep it tight — it loads into every session's prompt, so every line costs context. Prefer specifics (file paths, command names) over prose. Never include secrets.

Rules:
- Verify commands and paths against the actual files before writing them — a wrong build command is worse than none.
- Don't fabricate conventions the code doesn't demonstrate.
- After writing, summarize in one or two lines what you captured and tell the user to review and edit it.`

// builtinSkills returns the shipped skills. A fresh slice each call so callers
// can't mutate the shared set.
func builtinSkills() []Skill {
	return []Skill{
		{
			Name:        "init",
			Description: "Bootstrap or refresh this project's AGENTS.md — analyze the codebase (structure, build/test commands, architecture, conventions) and write a concise memory file loaded into every future session. Inlined — runs in the main loop so you see and approve the write.",
			Body:        builtinInitBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
		},
		{
			Name:        "install-capability",
			Description: "Install or uninstall Reasonix MCP servers and skills from a URL, GitHub/raw file, local path/folder, .mcp.json, executable, or package name. Plans with install_source (op=install or op=uninstall) before applying, surfacing per-action riskLevel.",
			Body:        builtinInstallCapabilityBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
		},
		{
			Name:        "test",
			Description: "Run the project's test suite, diagnose failures, propose+apply fixes, re-run until green (or stop after 2 attempts on the same failure). Inlined — runs in the parent loop. Detects go/npm/pnpm/yarn/pytest/cargo.",
			Body:        builtinTestBody,
			Scope:       ScopeBuiltin,
			Path:        "(builtin)",
		},
	}
}
