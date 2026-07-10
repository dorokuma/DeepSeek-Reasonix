package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"reasonix/internal/checkpoint"
	"reasonix/internal/event"
	"reasonix/internal/memory"
	"reasonix/internal/permission"
)

// --- memory ---
//
// c.mem is treated as an immutable snapshot guarded by c.mu: reads take the lock
// and return the pointer; writes mutate disk then swap in a freshly discovered
// snapshot. A turn-tail note is queued for each write so the change applies this
// session without disturbing the cache-stable system prefix (it folds into the
// prefix on the next session). All of these are no-ops returning "" when memory
// is disabled.

// QuickAdd appends a one-line note to the doc-memory file for scope (project
// REASONIX.md by default) — the write side of "#<note>". Returns the file written.
func (c *Controller) QuickAdd(scope memory.Scope, note string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	path := c.mem.DocPath(scope)
	if path == "" {
		return "", fmt.Errorf("no target file for memory scope %q", scope)
	}
	if err := memory.AppendDoc(path, note); err != nil {
		return "", err
	}
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
	return path, nil
}

// SaveDoc overwrites a recognized memory doc with body — the save side of the
// desktop panel's in-place editor. Returns the file written.
func (c *Controller) SaveDoc(path, body string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return "", nil
	}
	written, err := c.mem.WriteDoc(path, body)
	if err != nil {
		return "", err
	}
	// Inject the new content once on the next turn: the cached prefix still holds
	// the pre-edit version this session, so handing the model the current text
	// avoids a stale-guidance gap until the next session re-folds it into the
	// prefix. Trimmed to a single tail note (drained by Compose), not per-turn.
	c.pendingMemory = append(c.pendingMemory,
		"Memory file "+written+" was just edited. Its current contents:\n"+strings.TrimSpace(body))
	c.refreshMemoryLocked()
	return written, nil
}

// ForgetMemory deletes a saved auto-memory by name — the panel/TUI delete action,
// the manual counterpart to the model's `forget` tool. It queues a turn-tail note
// so the deletion applies this session (the cached prefix still lists the fact
// until the next session re-folds the index).
func (c *Controller) ForgetMemory(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mem == nil {
		return nil
	}
	if err := c.mem.Store.Delete(name); err != nil {
		return err
	}
	c.pendingMemory = append(c.pendingMemory,
		"Deleted memory \""+name+"\" — disregard its line still shown in the saved-memories index until next session.")
	c.refreshMemoryLocked()
	return nil
}

// QueueMemory implements memory.Queue: when the model runs the remember/forget
// tool, the tool calls this with a note that rides the next turn so the change
// applies this session without touching the cache-stable prefix. It also
// refreshes the snapshot a memory panel reads.
func (c *Controller) QueueMemory(note string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pendingMemory = append(c.pendingMemory, note)
	c.refreshMemoryLocked()
}

// Memory returns the loaded memory snapshot (nil when memory is disabled), for
// frontends that surface a memory panel or the /memory command. The returned
// *Set is immutable — mutations go through QuickAdd / SaveDoc.
func (c *Controller) Memory() *memory.Set {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mem
}

// refreshMemoryLocked re-discovers memory from disk so a later Memory() reflects
// a just-applied write. Caller holds c.mu.
func (c *Controller) refreshMemoryLocked() {
	if c.mem == nil {
		return
	}
	c.mem = memory.Load(memory.Options{CWD: c.mem.CWD, UserDir: c.mem.UserDir})
}

// --- approval bridge (agent gate → events) ---

// gateApprover adapts the Controller to permission.Approver. It is distinct
// from the public Approve command (different signature, different direction).
type gateApprover struct{ c *Controller }

func (g gateApprover) Approve(ctx context.Context, tool, subject string, args json.RawMessage) (bool, bool, error) {
	// Auto-allow without prompting while YOLO/bypass mode is on. Deny rules
	// already bit before this point, so they still block.
	g.c.mu.Lock()
	auto := g.c.bypass
	g.c.mu.Unlock()
	if auto {
		return true, false, nil
	}
	scope := "gate"
	if tool == "spawn_agent" {
		scope = "task"
	}
	preview := permission.Preview(tool, args)
	return g.c.requestApproval(ctx, tool, subject, preview, scope)
}

// requestApproval emits an ApprovalRequest and blocks until Approve(ID, …)
// answers or ctx is cancelled. A prior tool-wide session grant short-circuits.
// promptMu serialises outstanding prompts.
// parseRewind parses the arguments after "/rewind". The user may provide:
//
//	/rewind              → latest checkpoint, both
//	/rewind <turn>       → that turn, both
//	/rewind <turn> <scope> → that turn, code|conversation|both
//
// If no turn is given, the latest checkpoint is used. If no scope is given, Both is assumed.
func parseRewind(args string, cps []checkpoint.Meta) (int, RewindScope, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		if len(cps) == 0 {
			return 0, RewindBoth, fmt.Errorf("no checkpoints available")
		}
		return cps[len(cps)-1].Turn, RewindBoth, nil
	}
	turn, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, RewindBoth, fmt.Errorf("invalid turn: %w", err)
	}
	scope := RewindBoth
	if len(fields) >= 2 {
		switch strings.ToLower(fields[1]) {
		case "code":
			scope = RewindCode
		case "conversation":
			scope = RewindConversation
		case "both":
			scope = RewindBoth
		default:
			return 0, RewindBoth, fmt.Errorf("unknown scope %q", fields[1])
		}
	}
	return turn, scope, nil
}

func (c *Controller) requestApproval(ctx context.Context, tool, subject, preview, scope string) (bool, bool, error) {
	// Session grants are tool-wide: "allow for this session" / "allow persistently"
	// mean the user trusts this tool (write_file, bash, …), not just this one
	// file/command, so a different subject for the same tool isn't re-prompted.
	// Deny rules still bite upstream of here.
	key := tool

	c.mu.Lock()
	// YOLO/bypass auto-allows every approval without prompting.
	// Deny rules bit upstream.
	if c.bypass || c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.mu.Unlock()

	c.promptMu.Lock()
	defer c.promptMu.Unlock()

	// Re-check the grant: a session grant may have landed while we queued behind
	// another prompt for the same subject.
	c.mu.Lock()
	if c.bypass || c.granted[key] {
		c.mu.Unlock()
		return true, false, nil
	}
	c.nextID++
	id := strconv.Itoa(c.nextID)
	reply := make(chan approvalReply, 1)
	c.approvals[id] = reply
	c.mu.Unlock()

	c.sink.Emit(event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: id, Tool: tool, Subject: subject, Preview: preview, Scope: scope}})
	// The agent now needs the user's attention; a Notification hook can ping an
	// external channel (desktop notice, phone) while the run blocks on the reply.
	if subject != "" {
		go c.hooks.Notification(ctx, "approval needed: "+tool+" "+subject)
	} else {
		go c.hooks.Notification(ctx, "approval needed: "+tool)
	}

	select {
	case r := <-reply:
		if r.allow && r.session {
			c.mu.Lock()
			c.granted[key] = true
			c.mu.Unlock()
		}
		// When persist is true, remember=true signals Gate.OnRemember to write
		// the rule to the on-disk config.
		remember := r.persist
		return r.allow, remember, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.approvals, id)
		c.mu.Unlock()
		return false, false, ctx.Err()
	}
}
