# Changelog

All notable changes to the Go line (Reasonix 1.0+) are recorded here. The legacy
`0.x` TypeScript history lives on the [`v1`](https://github.com/esengine/DeepSeek-Reasonix/tree/v1)
branch.


## [Unreleased]

### Fixed
- Deduplicate resume-load rollback and sub-agent Options construction after 2.0.1.
- `close_agent` treats session persist failure as a warning (`closed: true` + `persist_warning`); agent stays closed and the slot stays free.
- Both sub-agent entry paths inherit parent `KeepMultimodalTurns` when stamped on context.

## [2.0.1] — 2026-07-16

### Fixed
- `resume_agent` concurrent disk-resume no longer double-counts open slots; load failures roll back the open slot and surface the error.
- `spawn_agent` / `resume_agent` open-slot cap is checked under the same lock (no race past max concurrent).
- ACP path uses the same sub-agent session directory as interactive boot.
- Closed-agent `send_input` error points at `resume_agent` first.
- Root tool context now stamps Options; sub-agents never inherit `main_agent_allowed` / main readonly caps.
- Provider request path runs full `SanitizeHistory` (tool pairing + multimodal prune); short histories no longer prune all images.

### Chore
- Remove accidental binaries and audit scratch from the tree; gitignore local build outputs and one-off notes.
- Strip large accidental blobs from git history where practical.

## [2.0.0] — 2026-07-16

### Multi-agent (Codex V1 aligned)

- Replace legacy interrupt/followup surface with Codex-style tools: `spawn_agent`, `send_input`, `wait_agent`, `close_agent`, `resume_agent`.
- One-layer only: hard ban on nested sub-agents (root may spawn; children cannot).
- Soft interrupt via `send_input(interrupt=true)`; completed agents stay open until `close_agent`.
- Wait timeout support; sub-agent sessions persisted for resume; soft-queue no longer silent-drop.
- Tool guidance aligned with Codex, Reasonix product wording (REASONIX.md, no nesting).

## [1.0.0] — 2026-06-03

First stable release — a **ground-up rewrite in Go**. Not an upgrade of the `0.x`
TypeScript line; a new codebase that becomes the default (`main-v2`).

### Highlights

- **Go kernel**: a single static binary (CGO-free), cross-compiled for
  darwin/linux/windows on amd64 + arm64. Distributed via npm (the package wraps
  the native binary), Homebrew (`esengine/reasonix` tap), and release archives;
  no Node runtime needed to run it.
- **Agent core**: the loop, built-in tools (read/write/edit/multi_edit/glob/grep/
  ls/bash/web_fetch/todo_write), permission gate, sandboxed bash, and the
  DeepSeek prefix-cache–oriented design.
- **Subagents**: `task` plus explore/research/review/security_review skill agents.
- **Skills & hooks**: Claude-Code-style skills (`internal/skill`) and hooks
  (`internal/hook`), symlink-aware and slash-integrated.
- **MCP client**: connect external servers over stdio / Streamable HTTP; reads
  `[[plugins]]` and a Claude-Code `.mcp.json`.
- **Memory**: `REASONIX.md` hierarchy + auto-memory, folded into the cache-stable
  prefix.
- **ACP** (`reasonix acp`) and an HTTP/SSE server frontend.

### Fixed

- **File encoding support restored** — GBK/GB18030 (and other non-UTF-8) files
  can now be read, edited, and grepped correctly. The v2 rewrite had dropped
  v1's encoding detection; files in CJK Windows charsets were silently misread
  or rejected as binary. The read/edit/write round-trip now preserves the
  original file encoding. (#2637)

### Notes

- Versions: the legacy TypeScript line stays in `0.x`; the Go line starts at
  `1.0.0`. See [docs/MIGRATING.md](docs/MIGRATING.md).
- Release archives ship a bare binary.
[1.0.0]: https://github.com/esengine/DeepSeek-Reasonix/releases/tag/v1.0.0
