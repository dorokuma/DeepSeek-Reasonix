package serve

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"reasonix/internal/config"
	"reasonix/internal/control"
	"reasonix/internal/event"
	"reasonix/internal/jobs"
)

func TestServeJobCancelAndPeek(t *testing.T) {
	bc := NewBroadcaster()
	jm := jobs.NewManager(event.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	j, err := jm.Start(ctx, "task", "test", func(ctx context.Context, _ io.Writer) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	ctrl := control.New(control.Options{Sink: bc, Jobs: jm, Runner: fakeRunner{got: got}})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	peekBody := `{"job_id":"` + j.ID + `"}`
	resp, err := http.Post(srv.URL+"/jobs/peek", "application/json", strings.NewReader(peekBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("peek status=%d", resp.StatusCode)
	}

	cancelBody := `{"job_id":"` + j.ID + `"}`
	resp2, err := http.Post(srv.URL+"/jobs/cancel", "application/json", strings.NewReader(cancelBody))
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("cancel status=%d", resp2.StatusCode)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("job not cancelled in time")
		default:
			st, err := jm.Peek(j.ID)
			if err == jobs.ErrJobNotFound {
				return
			}
			if st.Status == "killed" || st.Status == "done" {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func TestServeJobCancelNotFound(t *testing.T) {
	bc := NewBroadcaster()
	jm := jobs.NewManager(event.Discard)
	ctrl := control.New(control.Options{Sink: bc, Jobs: jm, Runner: fakeRunner{got: make(chan string, 1)}})
	srv := httptest.NewServer(New(ctrl, bc, config.ServeConfig{}).Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/jobs/cancel", "application/json", strings.NewReader(`{"job_id":"task-9999"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cancel unknown status=%d", resp.StatusCode)
	}
}