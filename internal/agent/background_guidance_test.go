package agent

import (
	"strings"
	"testing"
)

func TestBackgroundJobPostCallGuidance_SkillJobID(t *testing.T) {
	g := BackgroundJobPostCallGuidance("Started task skill-9 (explore)")
	if g == "" {
		t.Fatal("expected guidance for explore Started line")
	}
	if !strings.Contains(g, `job_id="skill-9"`) {
		t.Fatalf("want skill-9 in guidance, got %q", g)
	}
}
