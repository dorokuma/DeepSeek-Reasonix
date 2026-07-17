package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"reasonix/internal/provider"
)

func TestWriteSessionCostRoundsDust(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := writeSessionCost(path, 0.41009400000000007, "¥"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path + ".cost")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Cost     float64 `json:"cost"`
		Currency string  `json:"currency"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Cost != provider.RoundCost(0.41009400000000007) {
		t.Fatalf("cost=%v want rounded", v.Cost)
	}
	if v.Currency != "¥" {
		t.Fatalf("currency=%q", v.Currency)
	}
}

func TestSessionCacheSidecarPromptOnlyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	// prompt/total without hit/miss — must still persist for resume alignment.
	if err := writeSessionCache(path, 0, 0, 1200, 1300, false); err != nil {
		t.Fatal(err)
	}
	hit, miss, prompt, total, reported := readSessionCache(path)
	if hit != 0 || miss != 0 || prompt != 1200 || total != 1300 {
		t.Fatalf("got %d %d %d %d", hit, miss, prompt, total)
	}
	if reported {
		t.Fatal("reported should be false")
	}
}

func TestSessionCacheSidecarLegacyReportedDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	// Legacy shape without cacheReported field.
	if err := os.WriteFile(path+".cache", []byte(`{"cacheHit":9,"cacheMiss":1,"promptTokens":10,"totalTokens":12}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hit, miss, prompt, total, reported := readSessionCache(path)
	if hit != 9 || miss != 1 || prompt != 10 || total != 12 {
		t.Fatalf("got %d %d %d %d", hit, miss, prompt, total)
	}
	if !reported {
		t.Fatal("legacy non-zero split should default reported=true")
	}
}
