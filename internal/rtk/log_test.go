package rtk

import "testing"

func TestLogLevelFromEnv(t *testing.T) {
	tests := []struct {
		env  string
		want LogLevel
	}{
		{"", LogOff},
		{"off", LogOff},
		{"miss", LogMiss},
		{"decline", LogMiss},
		{"all", LogAll},
		{"1", LogAll},
		{"true", LogAll},
		{"on", LogAll},
	}
	for _, tc := range tests {
		t.Setenv("REASONIX_RTK_LOG", tc.env)
		if got := LogLevelFromEnv(); got != tc.want {
			t.Fatalf("REASONIX_RTK_LOG=%q = %v, want %v", tc.env, got, tc.want)
		}
	}
}

func TestEnvSnapshot_logLevel(t *testing.T) {
	t.Setenv("REASONIX_RTK_LOG", "miss")
	if got := EnvSnapshot()["REASONIX_RTK_LOG"]; got != "miss" {
		t.Fatalf("snapshot log = %q", got)
	}
}