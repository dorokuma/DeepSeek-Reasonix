package rtk

import (
	"testing"
)

func TestEnvDocs_complete(t *testing.T) {
	docs := EnvDocs()
	if len(docs) < 3 {
		t.Fatalf("want at least 4 env docs, got %d", len(docs))
	}
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Name == "" || d.Default == "" || d.Description == "" {
			t.Fatalf("incomplete doc: %+v", d)
		}
		if seen[d.Name] {
			t.Fatalf("duplicate env doc: %s", d.Name)
		}
		seen[d.Name] = true
	}
	for _, want := range []string{"REASONIX_RTK", "REASONIX_RTK_TIMEOUT", "REASONIX_RTK_READ_LIMIT"} {
		if !seen[want] {
			t.Fatalf("missing env doc for %s", want)
		}
	}
}

func TestEnvSnapshot_defaults(t *testing.T) {
	t.Setenv("REASONIX_RTK", "")
	t.Setenv("REASONIX_RTK_TIMEOUT", "")
	t.Setenv("REASONIX_RTK_READ_LIMIT", "")
	snap := EnvSnapshot()
	if snap["REASONIX_RTK"] != "rewrite" {
		t.Fatalf("mode = %q", snap["REASONIX_RTK"])
	}
	if snap["REASONIX_RTK_TIMEOUT"] != "3s" {
		t.Fatalf("timeout = %q", snap["REASONIX_RTK_TIMEOUT"])
	}
}

func TestEnvSnapshot_override(t *testing.T) {
	t.Setenv("REASONIX_RTK", "off")
	t.Setenv("REASONIX_RTK_TIMEOUT", "10")
	snap := EnvSnapshot()
	if snap["REASONIX_RTK"] != "off" {
		t.Fatalf("mode = %q", snap["REASONIX_RTK"])
	}
	if snap["REASONIX_RTK_TIMEOUT"] != "10s" {
		t.Fatalf("timeout = %q", snap["REASONIX_RTK_TIMEOUT"])
	}
}
