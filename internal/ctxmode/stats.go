package ctxmode

import (
	"fmt"
	"sync"
)

// Stats tracks token savings from ctxmode compaction across a session.
type Stats struct {
	mu             sync.Mutex
	TotalOriginal  int64 // bytes before compaction
	TotalCompacted int64 // bytes after compaction
	Compactions    int64 // number of compaction events
}

// Record records a compaction event.
func (st *Stats) Record(original, compacted int) {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.TotalOriginal += int64(original)
	st.TotalCompacted += int64(compacted)
	st.Compactions++
	st.mu.Unlock()
}

// Snapshot returns a copy of current stats.
func (st *Stats) Snapshot() (original, compacted int64, compactions int64) {
	if st == nil {
		return 0, 0, 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.TotalOriginal, st.TotalCompacted, st.Compactions
}

// SavingsPercent returns the percentage of bytes kept out of context.
func (st *Stats) SavingsPercent() float64 {
	if st == nil {
		return 0
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.TotalOriginal == 0 {
		return 0
	}
	saved := st.TotalOriginal - st.TotalCompacted
	return float64(saved) / float64(st.TotalOriginal) * 100
}

// String returns a human-readable summary.
func (st *Stats) String() string {
	orig, comp, count := st.Snapshot()
	if count == 0 {
		return "ctx stats: no compactions yet"
	}
	saved := orig - comp
	pct := float64(saved) / float64(orig) * 100
	return fmt.Sprintf("ctx stats: %d compactions, %d→%d bytes (%.1f%% kept out of context)",
		count, orig, comp, pct)
}
