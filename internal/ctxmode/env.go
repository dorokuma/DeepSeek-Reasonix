package ctxmode

import "strconv"

// EnvSnapshot returns effective ctxmode env for doctor output.
func EnvSnapshot() map[string]string {
	return map[string]string{
		"REASONIX_CTX":           envActive(),
		"REASONIX_CTX_THRESHOLD": strconv.Itoa(ThresholdBytes()),
		"REASONIX_CTX_LOG":       logEnv(),
	}
}

func envActive() string {
	if Active() {
		return "on"
	}
	return "off"
}

func logEnv() string {
	switch LogLevelFromEnv() {
	case LogMiss:
		return "miss"
	case LogAll:
		return "all"
	default:
		return "off"
	}
}

// Probe is a doctor smoke result for ctxmode.
type Probe struct {
	Active    bool              `json:"active"`
	Threshold int               `json:"threshold"`
	JournalOK bool              `json:"journal_ok"`
	Env       map[string]string `json:"env,omitempty"`
}

// JournalProbeOK opens an in-memory journal and verifies FTS5 record/search.
func JournalProbeOK() bool {
	j, err := openJournal("")
	if err != nil {
		return false
	}
	defer j.Close()
	j.Record("probe", "reasonix", "ctxmode journal fts5")
	return len(j.search("reasonix", nil, 3)) > 0
}

// CollectProbe reports ctxmode configuration.
func CollectProbe() Probe {
	return Probe{
		Active:    Active(),
		Threshold: ThresholdBytes(),
		JournalOK: JournalProbeOK(),
		Env:       EnvSnapshot(),
	}
}