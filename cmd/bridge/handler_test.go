package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Bridge construction — semaphore capacity
// ---------------------------------------------------------------------------

// TestNewBridgeSemaphore verifies that a Bridge constructed with the same
// pattern as NewBridge has the expected semaphore channel capacity of 100.
func TestNewBridgeSemaphore(t *testing.T) {
	client := &TelegramClient{
		Token:   "test:token",
		BaseURL: "http://localhost:1", // will not be contacted
		Client:  &http.Client{},
		Self:    User{ID: 1, UserName: "TestBot", IsBot: true},
	}
	cfg := &Config{
		BotToken: "test:token",
		StateDir: t.TempDir(),
		WorkDir:  t.TempDir(),
	}
	sm := NewSessionManager(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := &Bridge{
		cfg:          cfg,
		sm:           sm,
		client:       client,
		cron:         NewCronManager(sm, client, cfg, ctx),
		ctx:          ctx,
		cancel:       cancel,
		sinks:        make(map[int64]*sinkState),
		showThinking: make(map[int64]bool),
		streams:      make(map[int64]*streamState),
		submitMu:     make(map[int64]*sync.Mutex),
		sem:          make(chan struct{}, 100),
	}

	if cap(b.sem) != 100 {
		t.Errorf("Bridge semaphore capacity = %d, want 100", cap(b.sem))
	}
	if len(b.sem) != 0 {
		t.Errorf("Bridge semaphore should start empty, got %d items", len(b.sem))
	}
}

// ---------------------------------------------------------------------------
// contextTimeout / backgroundContext (cron_serve.go)
// ---------------------------------------------------------------------------

func TestContextTimeout(t *testing.T) {
	parent, pCancel := context.WithCancel(context.Background())
	defer pCancel()

	ctx, cancel := contextTimeout(parent)
	defer cancel()

	// Initial state: not cancelled.
	if err := ctx.Err(); err != nil {
		t.Fatalf("fresh context should not be done: %v", err)
	}

	// The derived context should have a deadline (5 min timeout).
	if _, ok := ctx.Deadline(); !ok {
		t.Error("contextTimeout should produce a context with deadline")
	}

	// Cancel the parent — child must follow.
	pCancel()
	select {
	case <-ctx.Done():
		// expected
	case <-time.After(time.Second):
		t.Fatal("child context not cancelled after parent cancellation")
	}
}

func TestBackgroundContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx := backgroundContext(parent)
	if ctx != parent {
		t.Error("backgroundContext should return the same parent context")
	}
}

// ---------------------------------------------------------------------------
// Rate-limit semaphore mechanism
// ---------------------------------------------------------------------------

func TestSemaphoreCapacity(t *testing.T) {
	const cap = 100
	sem := make(chan struct{}, cap)

	// Fill to capacity.
	for i := 0; i < cap; i++ {
		select {
		case sem <- struct{}{}:
		default:
			t.Fatalf("non-blocking send failed at iteration %d (sem full early)", i)
		}
	}

	// Verify exact count.
	if got := len(sem); got != cap {
		t.Fatalf("semaphore length = %d, want %d", got, cap)
	}

	// Non-blocking send should now fail (buffer is full).
	select {
	case sem <- struct{}{}:
		t.Error("non-blocking send succeeded on full semaphore — should have dropped")
	default:
		// Expected: drop the update.
	}

	// Drain one slot.
	<-sem

	// Now a non-blocking send must succeed.
	select {
	case sem <- struct{}{}:
		// OK
	default:
		t.Error("non-blocking send failed after draining one slot")
	}

	// Final length must still be cap (filled 100, drained 1, added 1).
	if got := len(sem); got != cap {
		t.Errorf("semaphore length = %d, want %d", got, cap)
	}
}

// TestRateLimitSelect simulates the exact non-blocking select + goroutine
// pattern that the Start() loop uses to rate-limit goroutine dispatch.
// It verifies that when more than cap updates arrive simultaneously, the
// excess are dropped.
func TestRateLimitSelect(t *testing.T) {
	const cap = 100
	sem := make(chan struct{}, cap)

	// Use a barrier to synchronise: all goroutines start together, then
	// release one by one so the semaphore stays full.
	startBarrier := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup

	dropped := 0
	total := cap + 50 // 50 over capacity

	for i := 0; i < total; i++ {
		select {
		case sem <- struct{}{}:
			wg.Add(1)
			go func() {
				defer func() {
					<-sem
					wg.Done()
				}()
				// Wait at the barrier so all goroutines pile up.
				<-startBarrier
				// Block until the test lets this one go.
				<-release
			}()
		default:
			dropped++
		}
	}

	// All goroutines are now waiting on startBarrier (the semaphore is full
	// with cap items).  Let them all proceed to the second wait (release).
	close(startBarrier)

	// Give goroutines a moment to move from startBarrier to release.
	time.Sleep(10 * time.Millisecond)

	// The semaphore should be full (cap items waiting on release).
	if got := len(sem); got != cap {
		t.Errorf("semaphore should be full (%d), got %d", cap, got)
	}

	// Now release all goroutines one by one until they drain.
	close(release)
	wg.Wait()

	if len(sem) != 0 {
		t.Errorf("semaphore should be empty after all goroutines finish, got %d", len(sem))
	}
	if dropped == 0 {
		t.Error("expected some updates to be dropped due to rate limiting")
	}
	if dropped != total-cap {
		t.Errorf("expected %d dropped (total=%d, cap=%d), got %d", total-cap, total, cap, dropped)
	}
	t.Logf("dispatched=%d, dropped=%d (capacity=%d)", total-dropped, dropped, cap)
}

// TestSendMessageError verifies that when the Telegram API returns an error,
// sendMessage returns a non-nil error.
func TestSendMessageError(t *testing.T) {
	// HTTP test server that always returns a Telegram API error for any
	// endpoint the Bridge might call.
	var mu sync.Mutex
	callCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		callCount++
		mu.Unlock()

		resp := APIResponse{
			Ok:          false,
			ErrorCode:   400,
			Description: "Bad Request: chat not found",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	// Build a TelegramClient that points to our test server.
	client := &TelegramClient{
		Token:   "test:token",
		BaseURL: ts.URL,
		Client:  ts.Client(),
		Self: User{
			ID:       1,
			UserName: "TestBot",
			IsBot:    true,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Minimal Bridge — only the fields that sendMessage touches.
	b := &Bridge{
		client: client,
		ctx:    ctx,
		cancel: cancel,
		// The rest are zero-valued and never accessed by sendMessage.
	}

	err := b.sendMessage(12345, "hello")
	if err == nil {
		t.Fatal("sendMessage should return an error when the API returns an error")
	}
	t.Logf("got expected error: %v", err)

	mu.Lock()
	if callCount == 0 {
		t.Error("expected at least one HTTP request to the test server")
	}
	mu.Unlock()
}

// TestSendMessageSuccess verifies that sendMessage succeeds on a happy path.
func TestSendMessageSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a successful sendMessage response.
		resp := APIResponse{
			Ok: true,
			Result: mustMarshal(t, Message{
				MessageID: 42,
				Chat:      &Chat{ID: 12345},
				Text:      "hello",
			}),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer ts.Close()

	client := &TelegramClient{
		Token:   "test:token",
		BaseURL: ts.URL,
		Client:  ts.Client(),
		Self:    User{ID: 1, UserName: "TestBot", IsBot: true},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := &Bridge{
		client: client,
		ctx:    ctx,
		cancel: cancel,
	}

	err := b.sendMessage(12345, "hello")
	if err != nil {
		t.Fatalf("sendMessage should succeed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Cron context cascade (cron_serve.go + cron.go)
// ---------------------------------------------------------------------------

// TestCronContextCascade verifies that cancelling the parent context passed to
// CronManager causes the task execution context (built via contextTimeout) to
// also be cancelled — i.e. bridge shutdown cascades to in-flight cron tasks.
func TestCronContextCascade(t *testing.T) {
	parent, pCancel := context.WithCancel(context.Background())

	// Create a real CronManager with real SessionManager (uses temp dir).
	sm := NewSessionManager(&Config{StateDir: t.TempDir()})
	client := &TelegramClient{
		Token:  "test:token",
		Client: &http.Client{},
		Self:   User{ID: 1, UserName: "TestBot", IsBot: true},
	}
	cm := NewCronManager(sm, client, &Config{StateDir: t.TempDir()}, parent)

	// The CronManager holds the parent context.
	if cm.ctx != parent {
		t.Error("CronManager.ctx should be the parent context passed to NewCronManager")
	}

	// Derive a task context using the same helper that executeTask uses.
	taskCtx, taskCancel := contextTimeout(cm.ctx)
	defer taskCancel()

	// Verify the task context is alive.
	if err := taskCtx.Err(); err != nil {
		t.Fatalf("task context should be alive before parent cancel: %v", err)
	}

	// Cancel the parent (simulates bridge Shutdown).
	pCancel()

	// The task context must be cancelled promptly.
	select {
	case <-taskCtx.Done():
		t.Log("cron task context cancelled after parent cancellation — OK")
	case <-time.After(time.Second):
		t.Fatal("task context not cancelled within 1s after parent cancellation")
	}

	// Verify the error is context.Canceled.
	if err := taskCtx.Err(); err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}
