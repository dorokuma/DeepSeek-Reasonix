package canary

import (
	"hash/fnv"
	"strings"
)

type Mode string

const (
	SafeMode           Mode = "SAFE_MODE"
	CanaryMode         Mode = "CANARY_MODE"
	FullProductionMode Mode = "FULL_PRODUCTION_MODE"
)

type Policy struct {
	Mode           Mode    `json:"mode"`
	TrafficPercent int     `json:"traffic_percent"`
	StabilityScore float64 `json:"stability_score,omitempty"`
	MinStableRuns  int     `json:"min_stable_runs"`
}

type Evaluation struct {
	Mode    Mode     `json:"mode"`
	Enabled bool     `json:"enabled"`
	Reasons []string `json:"reasons,omitempty"`
}

func DefaultPolicy() Policy {
	return Policy{
		Mode:           CanaryMode,
		TrafficPercent: 10,
		MinStableRuns:  5,
	}
}

func Evaluate(policy Policy, key string) Evaluation {
	policy = Normalize(policy)
	switch policy.Mode {
	case SafeMode:
		return Evaluation{Mode: SafeMode, Enabled: false, Reasons: []string{"safe mode blocks production hardening execution"}}
	case FullProductionMode:
		return Evaluation{Mode: FullProductionMode, Enabled: true, Reasons: []string{"full production mode enabled"}}
	default:
		enabled := bucket(key) < policy.TrafficPercent
		reason := "canary traffic excluded"
		if enabled {
			reason = "canary traffic included"
		}
		return Evaluation{Mode: CanaryMode, Enabled: enabled, Reasons: []string{reason}}
	}
}

func Promote(policy Policy, stableRuns int, stabilityScore float64) Policy {
	policy = Normalize(policy)
	policy.StabilityScore = stabilityScore
	if policy.Mode == SafeMode {
		policy.Mode = CanaryMode
		policy.TrafficPercent = 5
		return policy
	}
	if policy.Mode != CanaryMode {
		return policy
	}
	if stableRuns < policy.MinStableRuns || stabilityScore < 0.85 {
		return policy
	}
	switch {
	case policy.TrafficPercent < 10:
		policy.TrafficPercent = 10
	case policy.TrafficPercent < 25:
		policy.TrafficPercent = 25
	case policy.TrafficPercent < 50:
		policy.TrafficPercent = 50
	case policy.TrafficPercent < 100:
		policy.TrafficPercent = 100
	default:
		policy.Mode = FullProductionMode
	}
	return policy
}

func Normalize(policy Policy) Policy {
	if policy.Mode == "" {
		policy.Mode = CanaryMode
	}
	if policy.TrafficPercent <= 0 {
		policy.TrafficPercent = 10
	}
	if policy.TrafficPercent > 100 {
		policy.TrafficPercent = 100
	}
	if policy.Mode == SafeMode {
		policy.TrafficPercent = 0
	}
	if policy.Mode == FullProductionMode {
		policy.TrafficPercent = 100
	}
	if policy.MinStableRuns <= 0 {
		policy.MinStableRuns = 5
	}
	return policy
}

func bucket(key string) int {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "default"
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % 100)
}
