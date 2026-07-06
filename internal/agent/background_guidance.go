package agent

// BackgroundJobPostCallGuidance returns post-call text for any tool that returned a Started line.
func BackgroundJobPostCallGuidance(result string) string {
	id := ExtractJobIDFromStartedResult(result)
	if id == "" {
		return ""
	}
	return taskPostCallGuidance(id)
}
