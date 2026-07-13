package main

import "time"

// photoEntry is a single photo in a media group batch.
type photoEntry struct {
	FileID  string `json:"file_id"`
	Caption string `json:"caption,omitempty"`
}

// mediaGroupBatch holds batched media group messages.
type mediaGroupBatch struct {
	Messages []*Message
	Timer    *time.Timer
	photos   []photoEntry
}
