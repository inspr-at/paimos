// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public
// License along with this program. If not, see <https://www.gnu.org/licenses/>.

package handlers

// SSE stream for voice-intake sessions (PAI-704). Follows the ChangesStream
// recipe: the durable intake_events log is the replay source, `id:` carries
// the per-session seq so EventSource reconnects resume via Last-Event-ID, and
// the server's 2m WriteTimeout is allowed to cut long streams — clients
// reconnect and replay. The broker is per-session and in-process; a dropped
// (buffer-full) subscriber heals on its next reconnect replay.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/db"
)

const intakeHeartbeatInterval = 25 * time.Second

// intakeStreamEvent is the wire shape for one SSE message. Persisted events
// carry Seq (>0) and are emitted with an SSE id; ephemeral ones (stage
// progress, session-state pokes) have Seq 0 and no id, so they never disturb
// Last-Event-ID resume.
type intakeStreamEvent struct {
	Seq       int64           `json:"seq,omitempty"`
	Kind      string          `json:"kind"`
	Source    string          `json:"source,omitempty"`
	Label     string          `json:"label,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt string          `json:"created_at,omitempty"`
}

type intakeBroker struct {
	mu   sync.RWMutex
	subs map[int64]map[chan intakeStreamEvent]struct{}
}

var globalIntakeBroker = &intakeBroker{subs: map[int64]map[chan intakeStreamEvent]struct{}{}}

func (b *intakeBroker) Subscribe(sessionID int64) chan intakeStreamEvent {
	ch := make(chan intakeStreamEvent, 32)
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = map[chan intakeStreamEvent]struct{}{}
	}
	b.subs[sessionID][ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *intakeBroker) Unsubscribe(sessionID int64, ch chan intakeStreamEvent) {
	b.mu.Lock()
	if subs := b.subs[sessionID]; subs != nil {
		if _, ok := subs[ch]; ok {
			delete(subs, ch)
			close(ch)
		}
		if len(subs) == 0 {
			delete(b.subs, sessionID)
		}
	}
	b.mu.Unlock()
}

func (b *intakeBroker) Publish(sessionID int64, ev intakeStreamEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs[sessionID] {
		select {
		case ch <- ev:
		default: // slow subscriber: drop; reconnect replay heals it
		}
	}
}

// publishIntakeEvent loads one persisted event by seq and fans it out.
func publishIntakeEvent(ctx context.Context, sessionID, seq int64) {
	ev, err := loadIntakeStreamEvent(ctx, sessionID, seq)
	if err != nil {
		log.Printf("intake stream: load event failed (session=%d seq=%d): %v", sessionID, seq, err)
		return
	}
	globalIntakeBroker.Publish(sessionID, ev)
}

// publishIntakeEventsFrom fans out every persisted event with seq >= from,
// in order (used after multi-event writes like restore).
func publishIntakeEventsFrom(ctx context.Context, sessionID, from int64) {
	events, err := loadIntakeStreamEvents(ctx, sessionID, from-1)
	if err != nil {
		log.Printf("intake stream: load events failed (session=%d from=%d): %v", sessionID, from, err)
		return
	}
	for _, ev := range events {
		globalIntakeBroker.Publish(sessionID, ev)
	}
}

// publishIntakeSessionState pokes subscribers to re-read the session row
// (status/pin/language changes that don't append events).
func publishIntakeSessionState(ctx context.Context, sessionID int64) {
	s, err := loadIntakeSession(ctx, sessionID)
	if err != nil {
		return
	}
	payload, err := json.Marshal(s)
	if err != nil {
		return
	}
	globalIntakeBroker.Publish(sessionID, intakeStreamEvent{Kind: "session", Payload: payload})
}

func loadIntakeStreamEvent(ctx context.Context, sessionID, seq int64) (intakeStreamEvent, error) {
	var ev intakeStreamEvent
	var payload string
	err := db.DB.QueryRowContext(ctx,
		`SELECT seq, kind, source, label, payload_json, created_at
		 FROM intake_events WHERE session_id = ? AND seq = ?`, sessionID, seq).
		Scan(&ev.Seq, &ev.Kind, &ev.Source, &ev.Label, &payload, &ev.CreatedAt)
	if err != nil {
		return ev, err
	}
	if payload != "" {
		ev.Payload = json.RawMessage(payload)
	}
	return ev, nil
}

func loadIntakeStreamEvents(ctx context.Context, sessionID, sinceSeq int64) ([]intakeStreamEvent, error) {
	rows, err := db.DB.QueryContext(ctx,
		`SELECT seq, kind, source, label, payload_json, created_at
		 FROM intake_events WHERE session_id = ? AND seq > ? ORDER BY seq ASC`, sessionID, sinceSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []intakeStreamEvent{}
	for rows.Next() {
		var ev intakeStreamEvent
		var payload string
		if err := rows.Scan(&ev.Seq, &ev.Kind, &ev.Source, &ev.Label, &payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		if payload != "" {
			ev.Payload = json.RawMessage(payload)
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// IntakeSessionStream handles GET /api/intake/sessions/{id}/stream.
func IntakeSessionStream(w http.ResponseWriter, r *http.Request) {
	s, _, ok := requireIntakeSession(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	since, err := changeStreamSince(r) // same since/Last-Event-ID semantics as /api/changes
	if err != nil {
		jsonError(w, "invalid since", http.StatusBadRequest)
		return
	}

	ch := globalIntakeBroker.Subscribe(s.ID)
	defer globalIntakeBroker.Unsubscribe(s.ID, ch)

	replay, err := loadIntakeStreamEvents(r.Context(), s.ID, since)
	if err != nil {
		jsonError(w, "replay failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprintf(w, ":connected\n\n")
	flusher.Flush()

	lastSeq := since
	for _, ev := range replay {
		writeIntakeStreamEvent(w, flusher, ev)
		lastSeq = ev.Seq
	}

	heartbeat := time.NewTicker(intakeHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			// Suppress duplicates already sent during replay (publish raced
			// the subscribe); ephemeral events (Seq 0) always pass.
			if ev.Seq > 0 && ev.Seq <= lastSeq {
				continue
			}
			writeIntakeStreamEvent(w, flusher, ev)
			if ev.Seq > 0 {
				lastSeq = ev.Seq
			}
		case <-heartbeat.C:
			fmt.Fprintf(w, ":heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func writeIntakeStreamEvent(w http.ResponseWriter, flusher http.Flusher, ev intakeStreamEvent) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if ev.Seq > 0 {
		fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", ev.Kind, ev.Seq, payload)
	} else {
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload)
	}
	flusher.Flush()
}
