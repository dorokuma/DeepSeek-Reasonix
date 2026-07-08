package jobs

import "testing"

func TestIdleKillSecondsByKind(t *testing.T) {
	m := NewManager(nil)
	m.Configure(ManagerPolicies{
		IdleKillSecDefault: 90,
		IdleKillByKind: map[string]int{
			"bash":  2000,
			"task":  500,
			"skill": 400,
		},
	})
	if got := m.idleKillSeconds("bash"); got != 2000 {
		t.Fatalf("bash idle = %d", got)
	}
	if got := m.idleKillSeconds("task"); got != 500 {
		t.Fatalf("task idle = %d", got)
	}
	if got := m.idleKillSeconds("unknown"); got != 90 {
		t.Fatalf("default idle = %d", got)
	}
}