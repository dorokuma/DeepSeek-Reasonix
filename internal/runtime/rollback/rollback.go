package rollback

import (
	"os"

	"reasonix/internal/runtime/snapshot"
)

type Signals struct {
	RecentExecutions     int      `json:"recent_executions"`
	RecentFailures       int      `json:"recent_failures"`
	OscillationIndex     float64  `json:"oscillation_index"`
	CorruptedMemoryNodes int      `json:"corrupted_memory_nodes"`
	ActiveStrategies     int      `json:"active_strategies"`
	RejectedStrategies   int      `json:"rejected_strategies"`
	Reasons              []string `json:"reasons,omitempty"`
}

type Decision struct {
	ShouldRollback bool     `json:"should_rollback"`
	SnapshotID     string   `json:"snapshot_id,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
}

func Evaluate(signals Signals) Decision {
	decision := Decision{}
	if signals.RecentExecutions > 0 && signals.RecentFailures*2 >= signals.RecentExecutions && signals.RecentFailures >= 3 {
		decision.ShouldRollback = true
		decision.Reasons = append(decision.Reasons, "execution failure spike")
	}
	if signals.OscillationIndex >= 0.8 {
		decision.ShouldRollback = true
		decision.Reasons = append(decision.Reasons, "control oscillation")
	}
	if signals.CorruptedMemoryNodes >= 3 {
		decision.ShouldRollback = true
		decision.Reasons = append(decision.Reasons, "memory corruption")
	}
	if signals.ActiveStrategies == 0 && signals.RejectedStrategies > 0 {
		decision.ShouldRollback = true
		decision.Reasons = append(decision.Reasons, "strategy collapse")
	}
	decision.Reasons = append(decision.Reasons, signals.Reasons...)
	return decision
}

func EvaluateWithSnapshot(root string, signals Signals) Decision {
	decision := Evaluate(signals)
	if !decision.ShouldRollback {
		return decision
	}
	snap, err := snapshot.LatestStable(root)
	if err != nil {
		if os.IsNotExist(err) {
			decision.Reasons = append(decision.Reasons, "no stable snapshot available")
			return decision
		}
		decision.Reasons = append(decision.Reasons, "snapshot lookup failed: "+err.Error())
		return decision
	}
	decision.SnapshotID = snap.ID
	return decision
}
