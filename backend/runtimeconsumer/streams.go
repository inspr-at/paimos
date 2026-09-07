// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package runtimeconsumer

import (
	"context"
	"sync"
)

// MaxConcurrentStreams bounds external work independently of session count.
const MaxConcurrentStreams = 8

type streamEntry struct {
	token chan struct{}
	refs  int
}

// StreamGate serializes a stream across binding revisions while allowing bounded
// independent work. Its zero value is ready for use. Owners must separately drain
// admitted calls before repair, retirement, or releasing process ownership.
type StreamGate struct {
	mu      sync.Mutex
	entries map[string]*streamEntry
	slots   chan struct{}
}

func (g *StreamGate) Acquire(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	if g.entries == nil {
		g.entries = map[string]*streamEntry{}
		g.slots = make(chan struct{}, MaxConcurrentStreams)
	}
	entry := g.entries[key]
	if entry == nil {
		entry = &streamEntry{token: make(chan struct{}, 1)}
		entry.token <- struct{}{}
		g.entries[key] = entry
	}
	entry.refs++
	g.mu.Unlock()
	drop := func() {
		g.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(g.entries, key)
		}
		g.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	case <-entry.token:
	}
	// Same-stream waiters cannot exhaust the independent-work budget.
	select {
	case <-ctx.Done():
		entry.token <- struct{}{}
		drop()
		return nil, ctx.Err()
	case g.slots <- struct{}{}:
	}
	release := func() { <-g.slots; entry.token <- struct{}{}; drop() }
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}
