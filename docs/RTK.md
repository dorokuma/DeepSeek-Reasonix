# RTK integration

Reasonix optionally compacts shell and tool output through [RTK](https://github.com/rtk-ai/rtk) before it reaches the model. Integration is gated on `rtk rewrite`: if rewrite declines a command, Reasonix runs the original command or native builtin path — unsupported commands are never hijacked.

## Requirements

- `rtk` on `PATH` (install via the RTK project; Reasonix does not bundle it)
- Default mode is active when the binary is present (`REASONIX_RTK=rewrite` or unset)

Verify with:

```bash
reasonix doctor
```

The `rtk` section reports rewrite, grep gate, pipe compaction, timeout, and effective env values.

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `REASONIX_RTK` | `rewrite` | `rewrite` — transparent compaction; `suggest` — log would-be rewrites only; `off` / `0` / `false` — disable |
| `REASONIX_RTK_TIMEOUT` | `3s` | Timeout for `rtk rewrite` and gated shell runs. Go duration (`5s`, `500ms`) or plain seconds (`10`) |
| `REASONIX_RTK_READ_LIMIT` | `800` in rewrite mode, `2000` when off | Default `read_file` line cap when `limit` is omitted |
| `REASONIX_RTK_LOG` | off | Set `1`, `true`, `yes`, or `on` to log accepted bash rewrites to stderr |

Example:

```bash
export REASONIX_RTK=suggest          # dry-run: log rewrites, run originals
export REASONIX_RTK_TIMEOUT=10s      # slower hosts / large repos
export REASONIX_RTK_READ_LIMIT=1200  # larger read_file pages under RTK
export REASONIX_RTK_LOG=1            # debug rewrite decisions
```

## Config (grep builtin)

In `reasonix.toml`:

```toml
[tools.search]
engine = "auto"   # default: ripgrep when on PATH (honors .gitignore)
# engine = "rtk"  # builtin grep tries rtk rewrite gate before ripgrep
# engine = "rg"   # force ripgrep
# engine = "native"
```

`auto` keeps ripgrep for the `grep` builtin so `.gitignore` semantics stay intact. Bare `rg`/`grep` in `bash` still go through `rtk rewrite` when supported.

## How tools reach RTK

| Surface | RTK path | Fallback when rewrite declines |
|---------|----------|--------------------------------|
| `bash` | `ApplySegments` → rewrite | Original shell command |
| `grep` (engine=`rtk`) | `RunShellIfRewritten` | ripgrep or native scanner |
| `ls` | rewrite gate for `ls` / `tree` | `ReadDir` / walk |
| `read_file` | **no RTK** (line numbers for `edit_file`) | native read; use `cat`/`head` in bash for compact skim |
| `glob` | **no RTK** (RTK `find` output is tree-shaped) | native glob; `find` in bash still rewrites |
| Large tool output | `rtk pipe -f <filter>` when filter is known | head/tail truncation |
| MCP tools | **no pipe** | truncation only |

Shell-covered RTK filters include `git`, `docker`, `cargo`, `pytest`, `kubectl`, and the rest listed by `rtk --help`. Meta commands (`rewrite`, `hook`, `init`, …) are not invoked by Reasonix directly.

## Large output compaction

When a tool result exceeds ~32KB, Reasonix tries `rtk pipe` only when a safe filter is known:

- `bash` / `bash_output` / `wait` — from the command (via rewrite mapping)
- `grep` builtin — `grep` filter
- MCP (`mcp__*`) — never piped

Allowed pipe filters match RTK’s allowlist: `git-log`, `git-status`, `git-diff`, `grep`, `find`, `pytest`, `cargo-test`, `go-test`, `go-build`, `tsc`, `vitest`, `mypy`, `ruff-check`, `ruff-format`, `prettier`, `log`, `fd`, `rg`.

If pipe does not shrink the payload, Reasonix falls back to head/tail truncation.