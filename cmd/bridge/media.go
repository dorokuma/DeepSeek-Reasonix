package main

import (
	"path/filepath"
	"strings"
)

// cacheFileEntry holds info about a cached file.
type cacheFileEntry struct {
	path string
	mod  int64
}

// mediaResult holds the text description and data URLs for incoming media.
type mediaResult struct {
	Text string
}

// promptSafeName returns a filesystem-safe short name derived from the first
// few words of the prompt.
func promptSafeName(name string) string {
	// Keep only alphanumeric, dash, underscore, and limit to 64 chars.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, name)
	if len(safe) > 64 {
		safe = safe[:64]
	}
	return safe
}

// detectImageMIME probes the file extension and returns a MIME type.
func detectImageMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
