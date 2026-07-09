package agent

// BackgroundJobPostCallGuidance used to append long wait/poll rules after a
// started-task receipt. Task is synchronous now; keep a no-op so old call sites
// and tests still compile without re-teaching the model phantom tools.
func BackgroundJobPostCallGuidance(result string) string {
	_ = result
	return ""
}
