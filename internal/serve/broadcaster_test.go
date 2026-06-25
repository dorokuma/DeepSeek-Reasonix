package serve

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"reasonix/internal/event"
)

func TestBroadcasterFanOut(t *testing.T) {
	b := NewBroadcaster()
	a, ca := b.Subscribe(-1)
	d, cd := b.Subscribe(-1)
	defer ca()
	defer cd()

	if got := b.Subscribers(); got != 2 {
		t.Fatalf("subscribers = %d, want 2", got)
	}

	b.Emit(event.Event{Kind: event.Text, Text: "hi"})

	for i, ch := range []<-chan []byte{a, d} {
		var w wireEvent
		if err := json.Unmarshal(<-ch, &w); err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		if w.Kind != "text" || w.Text != "hi" {
			t.Errorf("subscriber %d got %+v", i, w)
		}
	}
}

func TestBroadcasterUnsubscribe(t *testing.T) {
	b := NewBroadcaster()
	_, cancel := b.Subscribe(-1)
	if b.Subscribers() != 1 {
		t.Fatalf("want 1 subscriber")
	}
	cancel()
	if b.Subscribers() != 0 {
		t.Fatalf("unsubscribe should drop to 0, got %d", b.Subscribers())
	}
	// Emitting with no subscribers must not panic.
	b.Emit(event.Event{Kind: event.TurnDone})
}

func TestBroadcasterDropsSlowSubscriber(t *testing.T) {
	b := NewBroadcaster()
	ch, cancel := b.Subscribe(-1)
	defer cancel()
	// Overfill far past the 64-slot buffer without reading; Emit must not block.
	for i := 0; i < 1000; i++ {
		b.Emit(event.Event{Kind: event.Text, Text: "x"})
	}
	if len(ch) == 0 {
		t.Error("expected some buffered frames")
	}
}

func TestBroadcasterSubscribeReplay(t *testing.T) {
	b := NewBroadcaster()
	for i := 0; i < 10; i++ {
		b.Emit(event.Event{Kind: event.Text, Text: fmt.Sprintf("msg-%d", i)})
	}
	ch, cancel := b.Subscribe(3)
	defer cancel()

	var got []int64
	for len(got) < 6 {
		select {
		case msg := <-ch:
			var w wireEvent
			if err := json.Unmarshal(msg, &w); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got = append(got, w.Seq)
		case <-time.After(time.Second):
			t.Fatalf("timeout after %d events, got %v", len(got), got)
		}
	}
	want := []int64{4, 5, 6, 7, 8, 9}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("replayed seqs = %v, want %v", got, want)
	}
}

func TestBroadcasterSubscribeReplayGap(t *testing.T) {
	b := NewBroadcaster()
	// Fill the ring past capacity so early events are overwritten.
	for i := 0; i < ringSize+10; i++ {
		b.Emit(event.Event{Kind: event.Text, Text: fmt.Sprintf("msg-%d", i)})
	}
	// minSeq should now be 10 (ringSize+10 - ringSize).
	ch, cancel := b.Subscribe(5)
	defer cancel()

	// First message must be the synthetic gap event.
	var gap map[string]interface{}
	if err := json.Unmarshal(<-ch, &gap); err != nil {
		t.Fatalf("unmarshal gap: %v", err)
	}
	if kind, _ := gap["kind"].(string); kind != "gap" {
		t.Fatalf("expected kind=gap, got %v", kind)
	}
	data, ok := gap["data"].(map[string]interface{})
	if !ok {
		t.Fatal("gap missing 'data' field")
	}
	from := int64(data["from"].(float64))
	to := int64(data["to"].(float64))
	if from != 6 || to != 10 {
		t.Errorf("gap range: from=%d to=%d, want from=6 to=10", from, to)
	}

	// The next message should be the first available event (seq 10).
	var first wireEvent
	if err := json.Unmarshal(<-ch, &first); err != nil {
		t.Fatalf("unmarshal first replay event: %v", err)
	}
	if first.Seq != 10 {
		t.Errorf("first replay seq = %d, want 10", first.Seq)
	}
}

func TestBroadcasterSubscribeMinusOne(t *testing.T) {
	b := NewBroadcaster()
	ch, cancel := b.Subscribe(-1)
	defer cancel()

	b.Emit(event.Event{Kind: event.Text, Text: "hello"})

	select {
	case msg := <-ch:
		var w wireEvent
		if err := json.Unmarshal(msg, &w); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if w.Seq != 0 {
			t.Errorf("seq = %d, want 0", w.Seq)
		}
		if w.Kind != "text" || w.Text != "hello" {
			t.Errorf("unexpected event: %+v", w)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for emitted event")
	}
}
