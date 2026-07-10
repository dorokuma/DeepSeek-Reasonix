// Package provider defines the model-backend abstraction. This file provides
// ChunkRing, a fixed-size ring buffer shared by streaming provider
// implementations to absorb backpressure without blocking HTTP readers.
package provider

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// ChunkRing is a fixed-size ring buffer that absorbs chunks when the output
// channel is full, preventing backpressure from reaching the HTTP read loop.
// When the buffer is full the oldest chunk is overwritten (drop-oldest).
//
// A single drain goroutine pulls chunks from the buffer and forwards them
// to the output channel, potentially blocking on send without affecting the
// HTTP reader. On ctx cancellation or stop signal the goroutine exits; any
// remaining buffer items are drained by Close().
type ChunkRing struct {
	mu     sync.Mutex
	buf    []Chunk // FIFO queue; oldest at front (index 0)
	cap    int     // max queue size before dropping oldest
	out    chan<- Chunk
	wakeCh chan struct{} // signals the drain goroutine
	stopCh chan struct{} // closed to signal drain to stop
	done   chan struct{} // closed when drain has exited
	ctx    context.Context
}

// NewChunkRing creates and starts a ChunkRing. The background drain goroutine
// runs until Close is called or ctx is cancelled.
func NewChunkRing(ctx context.Context, out chan<- Chunk, cap int) *ChunkRing {
	cr := &ChunkRing{
		buf:    make([]Chunk, 0, cap),
		cap:    cap,
		out:    out,
		wakeCh: make(chan struct{}, 1),
		stopCh: make(chan struct{}),
		done:   make(chan struct{}),
		ctx:    ctx,
	}
	go cr.drain()
	return cr
}

// Send enqueues a chunk into the ring buffer. The chunk is later forwarded
// to the output channel by the drain goroutine. Returns false when the ring
// has been closed (stopCh). The ctx parameter is accepted for interface
// compatibility but cancellation is handled via stopCh.
func (cr *ChunkRing) Send(ctx context.Context, chunk Chunk) bool {
	cr.mu.Lock()
	select {
	case <-cr.stopCh:
		cr.mu.Unlock()
		return false
	default:
	}

	if len(cr.buf) >= cr.cap {
		// Zero out before dropping so the old Chunk's fields can be GC'd.
		cr.buf[0] = Chunk{}
		cr.buf = cr.buf[1:]
		fmt.Fprintf(os.Stderr, "chunkRing: dropping chunk type=%d (buffer full)\n", chunk.Type)
	}
	cr.buf = append(cr.buf, chunk)
	cr.mu.Unlock()

	// Wake the drain goroutine (non-blocking).
	select {
	case cr.wakeCh <- struct{}{}:
	default:
	}
	return true
}

// drain runs in a background goroutine, pulling chunks from the queue and
// forwarding them to the output channel. It may block on channel send —
// that is deliberate: the blocking happens in this goroutine, never in the
// HTTP reader.
func (cr *ChunkRing) drain() {
	defer close(cr.done)
	for {
		select {
		case <-cr.ctx.Done():
			cr.drainAll()
			return
		case <-cr.stopCh:
			cr.drainAll()
			return
		case <-cr.wakeCh:
		}
		cr.drainAll()
	}
}

// drainAll sends every chunk in the queue to the output channel, blocking on
// send as needed. Each chunk is peeked from the queue but only removed AFTER
// the send succeeds, so a stopCh/ctx preemption between iterations never
// loses data. When stopCh fires during a blocking send we try one last
// non-blocking send before giving up — this way a temporarily-busy consumer
// (processing a prior chunk) still receives the data, but a truly-gone
// consumer doesn't cause Close to hang forever.
func (cr *ChunkRing) drainAll() {
	for {
		cr.mu.Lock()
		if len(cr.buf) == 0 {
			cr.mu.Unlock()
			return
		}
		chunk := cr.buf[0]
		// Peek, don't pop — if we bail on stopCh/ctx the chunk stays
		// in the buffer for Close to drain.
		cr.mu.Unlock()

		select {
		case cr.out <- chunk:
			// Sent successfully; pop under lock.
			cr.mu.Lock()
			if len(cr.buf) > 0 && cr.buf[0] == chunk {
				var zero Chunk
				cr.buf[0] = zero
				cr.buf = cr.buf[1:]
			}
			cr.mu.Unlock()
		case <-cr.stopCh:
			// stopCh closed — try one last non-blocking send so a
			// busy consumer doesn't lose data. If that fails the
			// consumer is truly gone and we bail; Close will drain
			// remaining items.
			select {
			case cr.out <- chunk:
				cr.mu.Lock()
				if len(cr.buf) > 0 && cr.buf[0] == chunk {
					var zero Chunk
					cr.buf[0] = zero
					cr.buf = cr.buf[1:]
				}
				cr.mu.Unlock()
			default:
			}
			return
		case <-cr.ctx.Done():
			return
		}
	}
}

// Close signals the drain goroutine to stop and waits for it to exit, then
// drains any remaining items from the buffer and closes the output channel.
// No new chunks can be added after Close returns because Send checks stopCh.
func (cr *ChunkRing) Close() {
	close(cr.stopCh)
	<-cr.done
	// Drain any remaining items (drainAll may have been preempted by
	// stopCh before the buffer was empty). We use blocking sends with
	// ctx as backstop — in normal operation the consumer is still reading.
	// A timeout backstop ensures we don't hang if the consumer has exited.
	drainCtx, cancel := context.WithTimeout(cr.ctx, 100*time.Millisecond)
	defer cancel()

	for {
		cr.mu.Lock()
		if len(cr.buf) == 0 {
			cr.mu.Unlock()
			break
		}
		chunk := cr.buf[0]
		var zero Chunk
		cr.buf[0] = zero
		cr.buf = cr.buf[1:]
		cr.mu.Unlock()

		select {
		case cr.out <- chunk:
		case <-drainCtx.Done():
			return
		}
	}
	close(cr.out)
}
