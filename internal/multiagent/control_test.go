package multiagent

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	fn func(ctx context.Context, path, message string, depth int) (string, error)
}

func (f fakeRunner) Run(ctx context.Context, path, message string, depth int) (string, error) {
	if f.fn != nil {
		return f.fn(ctx, path, message, depth)
	}
	return "ok:" + message, nil
}

func TestSpawnWaitMailbox(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		time.Sleep(50 * time.Millisecond)
		return "done-answer", nil
	}})
	path, nick, err := c.Spawn(context.Background(), RootPath, "explore", "find X", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "explore") || nick == "" {
		t.Fatalf("path/nick %q %q", path, nick)
	}
	msg, timedOut := c.Wait(context.Background(), 5000)
	if timedOut || msg != "Wait completed." {
		t.Fatalf("wait got %q timedOut=%v", msg, timedOut)
	}
	mails := c.Mailbox().Drain()
	if len(mails) != 1 || !strings.Contains(mails[0].Message, "done-answer") {
		t.Fatalf("mails %#v", mails)
	}
}

func TestWaitSteer(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	_, _, err := c.Spawn(context.Background(), RootPath, "slow", "long", 0)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		c.NotifySteer()
	}()
	msg, timedOut := c.Wait(context.Background(), 5000)
	if timedOut || !strings.Contains(msg, "interrupted") {
		t.Fatalf("want steered, got %q timed=%v", msg, timedOut)
	}
}

func TestListAndInterrupt(t *testing.T) {
	c := NewControl()
	c.SetRunner(fakeRunner{fn: func(ctx context.Context, path, message string, depth int) (string, error) {
		time.Sleep(200 * time.Millisecond)
		return "x", nil
	}})
	path, _, err := c.Spawn(context.Background(), RootPath, "job", "m", 0)
	if err != nil {
		t.Fatal(err)
	}
	list := c.List("")
	if len(list) != 1 {
		t.Fatalf("list %d", len(list))
	}
	prev, err := c.Interrupt(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = prev
}
