package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

)

func TestWriteMetricsIncludesReadinessFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.json")
	if err := writeMetrics(path, RunMetrics{
		PromptTokens:                  10,
		CompletionTokens:              3,
		CacheHitTokens:                7,
		CacheMissTokens:               3,
		Steps:                         2,
		ReadinessChecks:               1,
		ReadinessAllowed:              1,
		ReadinessBlocks:               0,
		ReadinessRecoveries:           1,
		ReadinessErrors:               0,
		ReadinessMissingProjectChecks: 0,
		ReadinessMissingCompleteSteps: 0,
		ReadinessIncompleteTodos:      0,
		ReadinessCommandMismatches:    0,
	}); err != nil {
		t.Fatalf("writeMetrics: %v", err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{
		"readiness_checks",
		"readiness_allowed",
		"readiness_blocks",
		"readiness_recoveries",
		"readiness_errors",
		"readiness_missing_project_checks",
		"readiness_missing_complete_steps",
		"readiness_incomplete_todos",
		"readiness_command_mismatches",
	} {
		if _, ok := got[key]; !ok {
			t.Fatalf("metrics JSON missing %q: %s", key, string(b))
		}
	}
}
