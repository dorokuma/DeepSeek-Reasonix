package agent

// BackgroundJobPostCallGuidance is a no-op stub kept for callers/tests.
// Legacy task started-receipt guidance is gone with the task tool.
func BackgroundJobPostCallGuidance(result string) string {
	_ = result
	return ""
}
