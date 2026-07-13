package main

import "time"

// StickerCacheEntry caches sticker metadata to avoid re-downloading.
type StickerCacheEntry struct {
	Description string    `json:"description"`
	Emoji       string    `json:"emoji"`
	SetName     string    `json:"set_name"`
	FileID      string    `json:"file_id,omitempty"`
	CachedAt    time.Time `json:"cached_at"`
}
