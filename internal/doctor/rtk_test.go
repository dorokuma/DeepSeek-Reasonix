package doctor

import (
	"strings"
	"testing"
)

func TestRenderTextRTKSection(t *testing.T) {
	text := RenderText(Report{
		RTK: RTKReport{
			Mode:      "rewrite",
			Path:      "/home/user/.local/bin/rtk",
			Version:   "rtk 0.42.0",
			RewriteOK: true,
			GrepOK:    true,
			PipeOK:    true,
			Timeout:   "3s",
			ReadLimit: 800,
			Env: map[string]string{
				"REASONIX_RTK":          "rewrite",
				"REASONIX_RTK_TIMEOUT":  "3s",
				"REASONIX_RTK_READ_LIMIT": "800",
				"REASONIX_RTK_LOG":      "off",
			},
			Sample: "git status → rtk git status",
		},
	})
	for _, want := range []string{
		"\nrtk\n",
		"  pipe         ok",
		"  timeout      3s",
		"  REASONIX_RTK_TIMEOUT",
		"  read_limit   800",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}