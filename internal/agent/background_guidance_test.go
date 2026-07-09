package agent

import "testing"

func TestBackgroundJobPostCallGuidance_Empty(t *testing.T) {
	if g := BackgroundJobPostCallGuidance(`{"job_id":"skill-9","status":"started","label":"explore"}`); g != "" {
		t.Fatalf("want empty guidance after sync cutover, got %q", g)
	}
}
