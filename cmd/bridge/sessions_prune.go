package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

const (
	defaultPruneKeep    = 50
	defaultPruneTTLDays = 30
)

// PruneSessions removes stale sessions from the state store and disk.
//
// Logic:
//  1. Load all chat records from the state store.
//  2. Records without a LastActive timestamp are treated as epoch (oldest).
//  3. Remove any record older than the TTL (default 30 days).
//  4. After TTL pruning, if more than N records remain, drop the oldest
//     excess records (N = default 50, overridable via env).
//  5. For each pruned record: delete the session file(s) on disk
//     (.jsonl.enc, .jsonl, checkpoint dir) and remove from state.json.
//  6. Log how many sessions were pruned.
func PruneSessions(st *stateStore) error {
	keepN := defaultPruneKeep
	if v := os.Getenv("SESSION_PRUNE_KEEP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			keepN = n
		}
	}

	ttlDays := defaultPruneTTLDays
	if v := os.Getenv("SESSION_PRUNE_TTL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			ttlDays = n
		}
	}
	ttl := time.Duration(ttlDays) * 24 * time.Hour
	cutoff := time.Now().Add(-ttl)

	records, err := st.load()
	if err != nil {
		return fmt.Errorf("prune: load state: %w", err)
	}

	if len(records) == 0 {
		log.Println("prune: no sessions to prune")
		return nil
	}

	// Sort by LastActive ascending (oldest first).  Zero time = epoch, so
	// records without timestamps sort to the front (pruned first).
	sort.Slice(records, func(i, j int) bool {
		return records[i].LastActive.Before(records[j].LastActive)
	})

	// Phase 1: remove records older than TTL.
	var pruned []chatRecord
	remaining := records[:0]
	for _, rec := range records {
		if !rec.LastActive.IsZero() && rec.LastActive.After(cutoff) {
			remaining = append(remaining, rec)
		} else {
			pruned = append(pruned, rec)
		}
	}

	// Phase 2: if more than keepN remain, drop the oldest excess.
	if len(remaining) > keepN {
		excess := remaining[:len(remaining)-keepN]
		pruned = append(pruned, excess...)
		remaining = remaining[len(remaining)-keepN:]
	}

	if len(pruned) == 0 {
		log.Printf("prune: 0 sessions pruned (total=%d, keep=%d, ttl=%dd)", len(records), keepN, ttlDays)
		return nil
	}

	// Delete session files and remove from state store.
	deleted := 0
	for _, rec := range pruned {
		if err := removeSessionFiles(st, rec.ChatID); err != nil {
			log.Printf("prune: remove files for chat %d: %v", rec.ChatID, err)
		}
		if err := st.remove(rec.ChatID); err != nil {
			log.Printf("prune: remove chat %d from state: %v", rec.ChatID, err)
		}
		deleted++
	}

	log.Printf("prune: removed %d sessions (total=%d, keep=%d, ttl=%dd)", deleted, len(records), keepN, ttlDays)
	return nil
}

// removeSessionFiles deletes session artifacts for a given chatID:
//   - <chatID>.jsonl.enc (encrypted session)
//   - <chatID>.jsonl      (plaintext temp copy)
//   - <chatID>.ckpt/      (checkpoint directory)
//   - <chatID>.jsonl.meta  (metadata sidecar)
func removeSessionFiles(st *stateStore, chatID int64) error {
	dir := st.sessionsDir()
	prefix := strconv.FormatInt(chatID, 10)

	for _, suffix := range []string{".jsonl.enc", ".jsonl", ".jsonl.meta"} {
		path := filepath.Join(dir, prefix+suffix)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	// Remove checkpoint directory if present.
	ckptDir := filepath.Join(dir, prefix+".ckpt")
	if info, err := os.Stat(ckptDir); err == nil && info.IsDir() {
		if err := os.RemoveAll(ckptDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("removeAll %s: %w", ckptDir, err)
		}
	}

	return nil
}
