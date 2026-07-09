package agent

import "testing"

func TestBackgroundJobPostCallGuidance_Empty(t *testing.T) {
	if g := BackgroundJobPostCallGuidance(`{"job_id":"skill-9","status":"started"}`); g != "" {
		t.Fatalf("want empty, got %q", g)
	}
}
