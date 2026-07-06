package control

import (
	"strings"
	"testing"

	"reasonix/internal/agent"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

func TestUserRequestsJobPeek(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"peek", true},
		{"Peek task-2", true},
		{"please PEEK", true},
		{"steer task-1", false},
		{"别peek", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := UserRequestsJobPeek(tc.in); got != tc.want {
			t.Errorf("UserRequestsJobPeek(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

func TestMaybeExposePeekJobWithoutRunningJobs(t *testing.T) {
	sink := event.Discard
	jm := jobs.NewManager(sink)
	defer jm.Close()

	sess := agent.NewSession("test")
	ag := agent.New(nil, nil, sess, agent.Options{Jobs: jm}, sink)
	ctrl := New(Options{Executor: ag, Sink: sink, SessionDir: t.TempDir(), Label: "test", Jobs: jm})

	out := ctrl.maybeExposePeekJob("user line", "peek")
	if !ag.DiagnosticRequested() {
		t.Fatal("literal peek must expose peek-job even when no background jobs are running")
	}
	if !strings.Contains(out, "peek-job") {
		t.Fatalf("operator note missing: %q", out)
	}
}
