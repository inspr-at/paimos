// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type storedFrame struct {
	seq  uint64
	data []byte
}

// Buffer is one RAM-only source-ordered ring, shared by all views of one exact
// owned binding. Subscribers hold positions, never their own content queues.
type Buffer struct {
	mu         sync.Mutex
	binding    nativecontract.Binding
	generation int64
	next       uint64
	frames     []storedFrame
	bytes      int
	last       time.Time
	clock      nativecontract.Clock
	wake       chan struct{}
	closed     bool
	validator  *nativecontract.Validator
}

func NewBuffer(binding nativecontract.Binding, generation int64, clock nativecontract.Clock) (*Buffer, error) {
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if _, err := Encode(v, "NativeBinding", binding); err != nil {
		return nil, err
	}
	if _, err := nativecontract.EventID(generation, 0); err != nil {
		return nil, err
	}
	return &Buffer{binding: binding, generation: generation, next: 1, clock: clockOrDefault(clock), wake: make(chan struct{}), validator: v}, nil
}

// Append is atomic, validates the whole bounded batch before changing the ring,
// and permits only exact retained retries. Explicit loss gaps account for a
// missing prefix when the outbound ring was evicted during an API outage.
func (b *Buffer) Append(events []nativecontract.StreamEvent) error {
	if len(events) == 0 || len(events) > nativecontract.PublishBatchFrames {
		return Refuse(nativecontract.ReasonBufferFull)
	}
	encoded := make([]storedFrame, 0, len(events))
	total := 0
	for _, event := range events {
		if event.Kind == "assistant.final" {
			return Refuse(nativecontract.ReasonRetentionUnapproved)
		}
		data, err := Encode(b.validator, "NativeStreamEvent", event)
		if err != nil {
			return err
		}
		seq, _ := strconv.ParseUint(event.Seq, 10, 64)
		if !event.Binding.Equal(b.binding) || event.Generation != b.generation {
			return Refuse(nativecontract.ReasonWrongGeneration)
		}
		total += len(data)
		if total > nativecontract.PublishBatchBytes {
			return Refuse(nativecontract.ReasonBufferFull)
		}
		encoded = append(encoded, storedFrame{seq, data})
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Refuse(nativecontract.ReasonWrongGeneration)
	}
	b.expire()
	next := b.next
	fresh := make([]storedFrame, 0, len(encoded))
	for i, item := range encoded {
		if item.seq < next {
			found := false
			for _, old := range b.frames {
				if old.seq == item.seq {
					found = bytes.Equal(old.data, item.data)
					break
				}
			}
			if !found {
				return Refuse(nativecontract.ReasonCursorExpired)
			}
			continue
		}
		if item.seq != next || next == math.MaxUint64 {
			return Refuse(nativecontract.ReasonInvalidInput)
		}
		next++
		if events[i].Kind == "gap" {
			var gap nativecontract.Gap
			_ = json.Unmarshal(events[i].Payload, &gap)
			dropped, _ := strconv.ParseUint(gap.DroppedFrames, 10, 64)
			if dropped > 0 {
				if dropped > math.MaxUint64-item.seq {
					return Refuse(nativecontract.ReasonBufferFull)
				}
				next = item.seq + dropped // the gap replaces the first lost position
			}
		}
		fresh = append(fresh, item)
	}
	for _, frame := range fresh {
		for len(b.frames) > 0 && (len(b.frames) >= nativecontract.LiveReplayFrames || b.bytes+len(frame.data) > nativecontract.LiveReplayBytes) {
			b.bytes -= len(b.frames[0].data)
			b.frames[0] = storedFrame{}
			b.frames = b.frames[1:]
		}
		b.frames = append(b.frames, frame)
		b.bytes += len(frame.data)
	}
	b.next = next
	if len(fresh) > 0 {
		b.last = b.clock.Now()
		close(b.wake)
		b.wake = make(chan struct{})
	}
	return nil
}

func (b *Buffer) expire() {
	if len(b.frames) > 0 && !b.clock.Now().Before(b.last.Add(nativecontract.LiveReplayIdleSeconds*time.Second)) {
		clear(b.frames)
		b.frames = nil
		b.bytes = 0
	}
}

// ReadAfter returns at most one frame and a coalescing notification, keeping
// slow subscribers bounded even while the native reader continues publishing.
func (b *Buffer) ReadAfter(after uint64) (nativecontract.StreamEvent, uint64, <-chan struct{}, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	if after < b.next-1 {
		if len(b.frames) == 0 || after+1 < b.frames[0].seq {
			to := b.next
			if len(b.frames) > 0 {
				to = b.frames[0].seq
			}
			gap := b.event(after+1, "gap", nativecontract.Gap{Reason: "buffer_evicted", DroppedFrames: strconv.FormatUint(to-after-1, 10), ReloadSnapshot: true})
			return gap, to - 1, b.wake, b.closed
		}
		for _, frame := range b.frames {
			if frame.seq > after {
				var event nativecontract.StreamEvent
				_ = json.Unmarshal(frame.data, &event)
				position := frame.seq
				if event.Kind == "gap" {
					var gap nativecontract.Gap
					_ = json.Unmarshal(event.Payload, &gap)
					lost, _ := strconv.ParseUint(gap.DroppedFrames, 10, 64)
					if lost > 0 {
						position += lost - 1
					}
				}
				return event, position, b.wake, b.closed
			}
		}
	}
	return nativecontract.StreamEvent{}, after, b.wake, b.closed
}

func (b *Buffer) event(seq uint64, kind string, payload any) nativecontract.StreamEvent {
	data, _ := json.Marshal(payload)
	position := strconv.FormatUint(seq, 10)
	return nativecontract.StreamEvent{Binding: b.binding, Generation: b.generation, Seq: position, Sequence: position, Source: "native", ObservedAt: b.clock.Now(), Kind: kind, Payload: data, DroppedFrames: "0"}
}

func (b *Buffer) Position(after string, generation int64) (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	if generation != b.generation || b.closed {
		return 0, Refuse(nativecontract.ReasonWrongGeneration)
	}
	if after == "" {
		return b.next - 1, nil
	}
	gen, seq, err := nativecontract.ParseEventID(after)
	if err != nil {
		return 0, err
	}
	if gen != b.generation {
		return 0, Refuse(nativecontract.ReasonWrongGeneration)
	}
	if seq >= b.next || seq < b.next-1 && (len(b.frames) == 0 || seq+1 < b.frames[0].seq) {
		return 0, Refuse(nativecontract.ReasonCursorExpired)
	}
	return seq, nil
}

func (b *Buffer) Stats() (frames, bytes int, next uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	return len(b.frames), b.bytes, b.next
}

func (b *Buffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		clear(b.frames)
		b.frames = nil
		b.bytes = 0
		close(b.wake)
	}
}

// Page is transient replay, never durable history. It advertises eviction even
// when the remaining page fits. Explicit finals cannot enter this ring.
func (b *Buffer) Page(query nativecontract.EventQuery, allow func(string) bool) (nativecontract.EventPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.expire()
	page := nativecontract.EventPage{Generation: b.generation, Items: []nativecontract.StreamEvent{}}
	if query.Generation != b.generation || b.closed {
		return page, Refuse(nativecontract.ReasonWrongGeneration)
	}
	limit := query.Limit
	if limit == 0 {
		limit = nativecontract.EventPageDefaultItems
	}
	if limit < 1 || limit > nativecontract.EventPageItems || len(query.Before) > 20 {
		return page, Refuse(nativecontract.ReasonBufferFull)
	}
	before := b.next
	if query.Before != "" {
		var err error
		before, err = strconv.ParseUint(query.Before, 10, 64)
		if err != nil || strconv.FormatUint(before, 10) != query.Before {
			return page, Refuse(nativecontract.ReasonInvalidInput)
		}
		if before > b.next || len(b.frames) == 0 || before <= b.frames[0].seq {
			return page, Refuse(nativecontract.ReasonCursorExpired)
		}
	}
	bytes := 0
	for i := len(b.frames) - 1; i >= 0; i-- {
		frame := b.frames[i]
		if frame.seq >= before {
			continue
		}
		var event nativecontract.StreamEvent
		_ = json.Unmarshal(frame.data, &event)
		if !allow(event.Kind) {
			continue
		}
		if len(page.Items) == limit || bytes+len(frame.data) > nativecontract.EventPageBytes-1024 {
			cursor := page.Items[len(page.Items)-1].Seq
			page.NextBefore = &cursor
			page.Truncated = true
			break
		}
		page.Items = append(page.Items, event)
		bytes += len(frame.data)
	}
	if len(b.frames) == 0 && b.next > 1 || len(b.frames) > 0 && b.frames[0].seq > 1 {
		page.Truncated = true
	}
	return page, nil
}

// Wait is useful for the outbound pump without adding an unbounded queue.
func Wait(ctx context.Context, wake <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wake:
		return nil
	}
}
