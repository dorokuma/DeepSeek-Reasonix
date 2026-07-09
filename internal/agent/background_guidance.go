package agent

// BackgroundJobPostCallGuidance returns short post-call text for a Started receipt.
func BackgroundJobPostCallGuidance(result string) string {
	id := ExtractJobIDFromStartedResult(result)
	if id == "" {
		return ""
	}
	return taskPostCallGuidance(id)
}
