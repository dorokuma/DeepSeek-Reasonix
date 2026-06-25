package serve

import (
	"encoding/json"
	"sync"

	"reasonix/internal/event"
)

// ringSize is the capacity of the ring buffer for reconnection replay.
const ringSize = 10000

// ringEntry holds one event in the ring buffer for reconnection replay.
type ringEntry struct {
	seq  int64
	data []byte
}

// Broadcaster is the event.Sink the controller emits to in server mode. It
// marshals each event once and fans it out to every connected SSE subscriber.
// A slow subscriber's buffer is allowed to drop rather than back-pressure the
// agent goroutine — a browser that can't keep up loses intermediate frames, not
// the whole session (it can refetch /history).
type Broadcaster struct {
	mu      sync.Mutex
	subs    map[chan []byte]struct{}
	ring    []ringEntry // ring buffer for reconnection replay (oldest entries may be overwritten)
	ringIdx int         // next write position in ring
	ringLen int         // number of entries written so far (capped at ringSize)
	nextSeq int64       // sequence number for the next event
	minSeq  int64       // smallest seq still guaranteed in the ring (0 until ring is full)
}

// NewBroadcaster returns an empty Broadcaster ready to accept subscribers.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subs: make(map[chan []byte]struct{}),
		ring: make([]ringEntry, ringSize), // keep the last ringSize events
	}
}

// Emit marshals the event to JSON, assigns a monotonically increasing sequence
// number, stores it in the ring buffer, and delivers it to every subscriber.
// Drops to a subscriber whose buffer is full rather than blocking. A marshal
// failure is dropped silently — one bad event shouldn't stall the stream.
func (b *Broadcaster) Emit(e event.Event) {
	b.mu.Lock()
	e.Seq = b.nextSeq
	b.nextSeq++
	data, err := json.Marshal(toWire(e))
	if err != nil {
		b.mu.Unlock()
		return
	}
	// Write into the ring buffer (overwrite oldest entry when full).
	b.ring[b.ringIdx] = ringEntry{seq: e.Seq, data: data}
	b.ringIdx = (b.ringIdx + 1) % len(b.ring)
	if b.ringLen < ringSize {
		b.ringLen++
	}
	if b.ringLen == ringSize {
		b.minSeq = b.nextSeq - ringSize
	}
	// Fan out to all subscribers.
	for ch := range b.subs {
		select {
		case ch <- data:
		default: // subscriber is behind; drop this frame for it
		}
	}
	b.mu.Unlock()
}

// Subscribe registers a new SSE client and returns its channel plus an
// unsubscribe func the handler must call (defer) when the client disconnects.
// If afterSeq >= 0 the channel is pre-filled with every event in the ring
// buffer whose sequence number is greater than afterSeq (best-effort replay
// for reconnection). Pass -1 to start from the next event.
//
// Best-effort semantics: if the ring buffer has overwritten entries that the
// client hasn't seen yet (afterSeq < minSeq), a synthetic "gap" event is
// injected so the client knows it missed some history. Remaining events that
// are still in the ring are replayed normally.
func (b *Broadcaster) Subscribe(afterSeq int64) (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	b.mu.Lock()
	// Emit a gap notification when the requested events have been overwritten.
	if afterSeq >= 0 && afterSeq < b.minSeq {
		gap, err := json.Marshal(map[string]interface{}{
			"seq":  0,
			"kind": "gap",
			"data": map[string]int64{
				"from": afterSeq + 1,
				"to":   b.minSeq,
			},
		})
		if err == nil {
			select {
			case ch <- gap:
			default:
				// channel full; skip gap notification
			}
		}
	}
	// Best-effort replay: deliver events newer than afterSeq from the ring.
	if afterSeq >= 0 {
		for i := 0; i < len(b.ring); i++ {
			entry := b.ring[(b.ringIdx+i)%len(b.ring)]
			if entry.seq > afterSeq && entry.data != nil {
				select {
				case ch <- entry.data:
				default:
					// channel full; stop replay to avoid blocking
					goto doneReplay
				}
			}
		}
	}
doneReplay:
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Subscribers reports the current connection count (for diagnostics/tests).
func (b *Broadcaster) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Close unsubscribes every subscriber, closing their channels. SSE handlers
// detect the closed channel and exit, allowing http.Server.Shutdown to drain
// gracefully instead of timing out on long-lived streaming connections.
func (b *Broadcaster) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		close(ch)
		delete(b.subs, ch)
	}
}
