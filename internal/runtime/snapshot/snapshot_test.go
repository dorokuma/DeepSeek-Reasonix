package snapshot

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCaptureSaveLoadLatestStableAndRestoreRaw(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	unstable, err := Capture("unstable", SystemState{MemoryGraph: map[string]string{"state": "bad"}}, false, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	stable, err := Capture("stable", SystemState{
		MemoryGraph:      map[string]string{"state": "good"},
		ControlGraph:     []string{"control"},
		StrategyRegistry: []string{"strategy"},
		EquilibriumState: map[string]float64{"score": 1},
	}, true, 2, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, unstable); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, stable); err != nil {
		t.Fatal(err)
	}
	latest, err := LatestStable(dir)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != "stable" {
		t.Fatalf("latest stable = %q, want stable", latest.ID)
	}
	raw, err := RestoreRaw(dir, "stable")
	if err != nil {
		t.Fatal(err)
	}
	var graph map[string]string
	if err := json.Unmarshal(raw.MemoryGraph.(json.RawMessage), &graph); err != nil {
		t.Fatal(err)
	}
	if graph["state"] != "good" {
		t.Fatalf("restored graph = %+v, want good", graph)
	}
}

func TestLatestStableReturnsNotExistWithoutStableSnapshot(t *testing.T) {
	_, err := LatestStable(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LatestStable error = %v, want os.ErrNotExist", err)
	}
}
