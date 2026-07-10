package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

func (c *Controller) Submit(input string) {
	c.mu.Lock()
	c.autoReentryDepth = 0
	c.mu.Unlock()
	trimmed := strings.TrimSpace(input)
	if strings.HasPrefix(trimmed, "!") {
		c.RunShell(trimmed[1:])
		return
	}
	switch {
	case trimmed == "/compact" || strings.HasPrefix(trimmed, "/compact "):
		c.mu.Lock()
		running := c.running
		c.mu.Unlock()
		if running {
			c.notice("cannot compact while a turn is running")
			return
		}
		focus := strings.TrimSpace(strings.TrimPrefix(trimmed, "/compact"))
		// Cancel any running turn before compacting to avoid session data
		// corruption from concurrent read/write.
		if c.Running() {
			c.Cancel()
		}
		c.runGuarded(trimmed, func(ctx context.Context) error {
			if err := c.Compact(ctx, focus); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("compaction failed: %w", err)
			}
			c.notice("compacted")
			if err := c.Snapshot(); err != nil {
				slog.Warn("controller: snapshot after compact", "err", err)
			}
			return nil
		})
	case trimmed == "/new":
		// Cancel any running turn before creating a new session to avoid
		// the turn operating on a session that's about to be replaced.
		if c.Running() {
			c.Cancel()
		}
		c.runGuarded(trimmed, func(ctx context.Context) error {
			if err := c.NewSession(); err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return fmt.Errorf("new session failed: %w", err)
			}
			c.notice("new session")
			return nil
		})
	case strings.HasPrefix(trimmed, "/mcp_"):
		c.runGuarded(trimmed, func(ctx context.Context) error {
			sent, found, err := c.MCPPrompt(ctx, trimmed)
			if err != nil {
				return err
			}
			if !found {
				c.notice("unknown command: " + trimmed)
				return nil
			}
			return c.runTurnWithRaw(ctx, sent, sent)
		})
	case strings.HasPrefix(trimmed, "/"):
		if ref, ok := FileRefLine(trimmed); ok {
			c.runRefTurn(ref)
			return
		}
		// Read-only management verbs (/model /memory /skills /hooks /mcp) emit a
		// listing Notice, so Submit-based frontends (desktop, HTTP) get them with
		// no extra wiring. (The chat TUI handles these itself with richer output.)
		fields := strings.Fields(trimmed)
		switch fields[0] {
		case "/tree":
			c.notice(c.BranchTreeText())
			return
		case "/branch":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if turn, name, fromTurn, err := ParseBranchTarget(args); err != nil {
				c.notice(err.Error())
			} else if fromTurn {
				if _, err := c.ForkNamed(turn-1, name); err != nil {
					c.notice(err.Error())
				}
			} else {
				if _, err := c.Branch(name); err != nil {
					c.notice(err.Error())
				}
			}
			return
		case "/switch":
			ref := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			if _, err := c.SwitchBranch(ref); err != nil {
				c.notice(err.Error())
			}
			return
		case "/rewind":
			args := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
			turn, scope, err := parseRewind(args, c.Checkpoints())
			if err != nil {
				c.notice("usage: /rewind [turn] [code|conversation|both]")
				return
			}
			if err := c.Rewind(turn, scope); err != nil {
				c.notice(err.Error())
			}
			return
		}
		if c.managementNotice(trimmed) {
			return
		}
		// A custom command wins over a skill of the same name; both resolve to a
		// turn. (Built-in slash verbs like /compact are handled above.)
		if sent, ok := c.CustomCommand(trimmed); ok {
			c.runGuarded(trimmed, func(ctx context.Context) error {
				return c.runTurnWithRaw(ctx, sent, sent)
			})
			return
		}
		if sent, ok := c.RunSkill(trimmed); ok {
			c.runGuarded(trimmed, func(ctx context.Context) error {
				return c.runTurnWithRaw(ctx, sent, sent)
			})
			return
		}
		c.notice("unknown command: " + trimmed)
	default:
		c.runRefTurn(input)
	}
}

// shellTimeout is the maximum time a user-invoked "!command" may run. Matches
// the bash tool's timeout so behaviour is consistent across invocation paths.
const shellTimeout = 120 * time.Second

// shellWaitDelay bounds how long cmd.Run() waits after context cancellation for
// the child's pipes to drain, matching the bash tool's WaitDelay.
const shellWaitDelay = 5 * time.Second

// shellWriter forwards each chunk of shell output to a callback, so RunShell
// can stream live progress to the frontend as the command produces output.
