package rtk

import (
	"testing"
)

// integrationOnly are RTK subcommands Reasonix never invokes directly.
var integrationOnly = map[string]bool{
	"help": true,
	"mvn":  true,
}

func TestCoverageListsAllRTKFilters(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Coverage() {
		if e.RTKCommand == "" {
			t.Fatal("empty RTK command in coverage table")
		}
		if seen[e.RTKCommand] {
			t.Fatalf("duplicate coverage entry: %s", e.RTKCommand)
		}
		seen[e.RTKCommand] = true
		if e.Via == "" {
			t.Fatalf("%s: missing Via", e.RTKCommand)
		}
	}
	if len(seen) < 40 {
		t.Fatalf("coverage table too small: %d entries", len(seen))
	}
}

func TestCoverageMatchesRTKHelp(t *testing.T) {
	if !Available() {
		t.Skip("rtk not on PATH")
	}
	help, err := ListHelpCommands()
	if err != nil {
		t.Fatal(err)
	}
	if len(help) == 0 {
		t.Fatal("rtk --help returned no commands")
	}
	byName := map[string]CoverageEntry{}
	for _, e := range Coverage() {
		byName[e.RTKCommand] = e
	}
	for _, cmd := range help {
		if integrationOnly[cmd] {
			continue
		}
		e, ok := byName[cmd]
		if !ok {
			t.Errorf("rtk --help lists %q but Coverage() has no entry", cmd)
			continue
		}
		if e.Via == "" {
			t.Errorf("%s: missing Via in coverage", cmd)
		}
	}
	for name := range byName {
		if integrationOnly[name] {
			continue
		}
		found := false
		for _, cmd := range help {
			if cmd == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Coverage() lists %q but rtk --help does not", name)
		}
	}
}