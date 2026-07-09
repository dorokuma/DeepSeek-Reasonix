package boot

import (
	"testing"

	"reasonix/internal/config"
)

func TestSubagentModelRefUsesConfiguredDefault(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.SubagentModel = "deepseek-pro"

	got := subagentModelRef(cfg, "task")
	if got != "deepseek-pro" {
		t.Fatalf("subagent model = %q, want deepseek-pro", got)
	}
}

func TestSubagentModelRefHonorsPerRoleMap(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.SubagentModel = "mimo-pro"
	cfg.Agent.SubagentModels = map[string]string{"task": "deepseek-pro"}

	got := subagentModelRef(cfg, "task")
	if got != "deepseek-pro" {
		t.Fatalf("per-role config should override default, got %q", got)
	}

	got = subagentModelRef(cfg, "other")
	if got != "mimo-pro" {
		t.Fatalf("unknown role should fall back to default, got %q", got)
	}
}

func TestSubagentModelRefAcceptsRoleAliases(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.SubagentModels = map[string]string{"security_review": "deepseek-pro"}

	got := subagentModelRef(cfg, "security-review")
	if got != "deepseek-pro" {
		t.Fatalf("underscore alias should configure hyphen role, got %q", got)
	}
}

func TestSubagentEffortRefHonorsPrecedence(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.SubagentEffort = "high"
	cfg.Agent.SubagentEfforts = map[string]string{"task": "max"}

	got := subagentEffortRef(cfg, "task")
	if got != "max" {
		t.Fatalf("per-role effort config should override default, got %q", got)
	}

	got = subagentEffortRef(cfg, "other")
	if got != "high" {
		t.Fatalf("default subagent effort = %q, want high", got)
	}
}

func TestSubagentEffortRefAcceptsRoleAliases(t *testing.T) {
	cfg := config.Default()
	cfg.Agent.SubagentEfforts = map[string]string{"security_review": "max"}

	got := subagentEffortRef(cfg, "security-review")
	if got != "max" {
		t.Fatalf("underscore alias should configure hyphen role effort, got %q", got)
	}
}
