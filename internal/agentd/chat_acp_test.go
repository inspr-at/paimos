// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type acpChatInput struct{ frames chan json.RawMessage }

func (i acpChatInput) Write(raw []byte) (int, error) {
	i.frames <- append(json.RawMessage(nil), raw...)
	return len(raw), nil
}
func (acpChatInput) Close() error { return nil }

func acpChatFrame(t *testing.T, input acpChatInput) (id json.RawMessage, method, text string) {
	t.Helper()
	select {
	case raw := <-input.frames:
		var f struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				SessionID string        `json:"sessionId"`
				Prompt    []ChatContent `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &f) != nil || f.Params.SessionID != "session-1" {
			t.Fatal("input lost the owned session")
		}
		if len(f.Params.Prompt) == 1 {
			text = f.Params.Prompt[0].Text
		}
		return f.ID, f.Method, text
	case <-time.After(3 * time.Second):
		t.Fatal("ACP input missing")
		return nil, "", ""
	}
}

// Risks R9/R11/R17: wrong-session input, implicit lossy replacement, private
// vendor payloads, and a protocol acknowledgement mistaken for execution.
// All writes/turn ends are barriers; no executable or model is launched.
func TestACPChatOwnedTurnsAndRefusals(t *testing.T) {
	t.Run("cursor-persistent-adapter", func(t *testing.T) {
		r := adapterRequest(t)
		r.Profile.Harness, r.InboxEnabled = Cursor, true
		a := NewCursorAdapter(acpFixturePath(t, Cursor, "chat"), map[string]string{"account": "42"})
		a.Homes = map[string]string{"account": acpFixtureHome(t)}
		idle := make(chan struct{}, 4)
		updates := make(chan ChatUpdate, 16)
		p, err := a.Start(t.Context(), r, func(ev AdapterEvent) {
			if ev.Chat != nil {
				updates <- *ev.Chat
			}
			if ev.Activity == "idle" {
				idle <- struct{}{}
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.Stop(context.Background()); _ = p.Wait() })
		waitIdle := func() {
			select {
			case <-idle:
			case <-time.After(3 * time.Second):
				t.Fatal("Cursor did not reach idle")
			}
		}
		waitIdle()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if err := p.Control(ctx, "inbox", "second turn"); err != nil {
			t.Fatal(err)
		}
		waitIdle()
		var got []ChatUpdate
		for len(updates) > 0 {
			got = append(got, <-updates)
		}
		want := []ChatUpdate{chatState("running"), chatText("fixture reply"), chatState("idle"), chatState("running"), chatText("fixture reply"), chatState("idle")}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Cursor adapter did not project both owned turns: %#v", got)
		}
	})
	for _, harness := range []string{Cursor, OpenCode, Gemini, Grok} {
		t.Run(harness, func(t *testing.T) {
			input := acpChatInput{frames: make(chan json.RawMessage, 8)}
			updates := make(chan ChatUpdate, 32)
			p := &acpProcess{wireProcess: &wireProcess{stdin: input, sessionID: "session-1", readDone: make(chan struct{}), observe: func(ev AdapterEvent) {
				if ev.Chat != nil {
					updates <- *ev.Chat
				}
			}}, harness: harness, model: "fixture", persistent: true, done: make(chan struct{}), idleTimeout: time.Hour}
			t.Cleanup(func() { p.mu.Lock(); p.finishLocked(nil); p.mu.Unlock() })
			p.mu.Lock()
			_, err := p.promptLocked(t.Context(), "first input")
			p.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			id, method, text := acpChatFrame(t, input)
			if method != "session/prompt" || text != "first input" {
				t.Fatal("initial input changed")
			}
			for _, op := range []string{"steer", "interrupt-and-replace", "resume", "grant_permission"} {
				if err := p.Control(t.Context(), op, "replacement"); !errors.Is(err, ErrUnsupported) {
					t.Fatal("busy or unsupported action not refused", op, err)
				}
			}
			if harness == Grok {
				if err := p.Control(t.Context(), "interrupt", ""); !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "Stop") {
					t.Fatal("Grok interrupt overclaimed", err)
				}
			}
			select {
			case <-input.frames:
				t.Fatal("refusal touched the child")
			default:
			}
			p.event([]byte(`{"method":"session/update","params":{"sessionId":"foreign","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"PRIVATE_SENTINEL"}}}}`))
			p.event([]byte(`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"first reply"}}}}`))
			p.event([]byte(fmt.Sprintf(`{"id":%s,"result":{"stopReason":"end_turn"}}`, id)))
			result := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			go func() { result <- p.Control(ctx, "inbox", "send after this turn") }()
			second, method, text := acpChatFrame(t, input)
			if method != "session/prompt" || text != "send after this turn" || bytes.Equal(id, second) {
				t.Fatal("queued pickup replaced or repeated a turn")
			}
			select {
			case <-result:
				t.Fatal("input acknowledged before vendor acceptance")
			default:
			}
			p.event([]byte(`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"second reply"}}}}`))
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if harness != Grok {
				go func() { result <- p.Control(ctx, "interrupt", "") }()
				_, method, _ = acpChatFrame(t, input)
				if method != "session/cancel" {
					t.Fatal("interrupt lost cancellation transport")
				}
				select {
				case <-result:
					t.Fatal("interrupt acknowledged before turn end")
				default:
				}
				p.event([]byte(fmt.Sprintf(`{"id":%s,"result":{"stopReason":"cancelled"}}`, second)))
				if err := <-result; err != nil {
					t.Fatal(err)
				}
			} else {
				p.event([]byte(fmt.Sprintf(`{"id":%s,"result":{"stopReason":"end_turn"}}`, second)))
			}
			var got []ChatUpdate
			for len(updates) > 0 {
				got = append(got, <-updates)
			}
			want := []ChatUpdate{chatState("running"), chatText("first reply"), chatState("idle"), chatState("running"), chatText("second reply"), chatState("idle")}
			if !reflect.DeepEqual(got, want) || p.DroppedChatFrames() != 1 {
				t.Fatalf("owned lifecycle %#v, dropped %d", got, p.DroppedChatFrames())
			}
			p.mu.Lock()
			p.finishLocked(nil)
			p.mu.Unlock()
			if err := p.Control(t.Context(), "inbox", "late"); !errors.Is(err, ErrNotOwned) {
				t.Fatal("closed session resumed", err)
			}
		})
	}
	// Tool state is bounded, and malformed/oversized/content-only updates cannot
	// expose vendor titles, arguments, results, or reasoning.
	p := &wireProcess{sessionID: "session-1", observe: func(ev AdapterEvent) {
		if !ev.Chat.valid() {
			t.Fatal("invalid projection")
		}
	}}
	for i := 0; i < chatToolLimit; i++ {
		p.observeACPChat([]byte(fmt.Sprintf(`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"call-%d","kind":"execute","status":"pending"}}}`, i)))
	}
	for _, raw := range []string{
		`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call","toolCallId":"overflow","kind":"execute","status":"pending"}}}`,
		`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"tool_call_update","toolCallId":"foreign-call","status":"completed"}}}`,
		`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"image","text":"PRIVATE_SENTINEL"}}}}`,
		`{"method":"session/update","params":{"sessionId":"session-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + strings.Repeat("x", chatChunkBytes+1) + `"}}}}`,
		strings.Repeat(" ", (8<<20)+1), "{",
	} {
		p.observeACPChat([]byte(raw))
	}
	if len(p.chatTools) != chatToolLimit || p.DroppedChatFrames() != 6 {
		t.Fatal("ACP bounds not enforced", p.DroppedChatFrames())
	}
}

// Risks R3/R4/R9: deferred input must preserve its authorization and survive
// at-least-once lease completion without duplicate writes or blocking Stop.
func TestACPChatQueuedDeliveryExactlyOnceAndControlsPass(t *testing.T) {
	for _, harness := range []string{Cursor, OpenCode, Gemini, Grok} {
		t.Run(harness, func(t *testing.T) {
			s, a, e, p := managedFixture(t)
			e.mu.Lock()
			e.managedPolicy = false
			e.inboxCapable = true
			e.queueInput = true
			e.harness.Harness = harness
			e.mu.Unlock()
			api := &replayInboxAPI{fakeAPI: a, fail: true}
			s.api = api
			p.observe = func(AdapterEvent) { s.observe(e, AdapterEvent{Activity: "busy"}) }
			s.observe(e, AdapterEvent{Activity: "busy"})
			a.harnessDeliveries = []HarnessDelivery{{ID: "queued", Level: "steer", Body: "send after this turn"}}
			a.harnessControls = []HarnessControl{{ID: "queued-steer", Kind: "steer", Text: "next turn"}, {ID: "interrupt", Kind: "interrupt"}}
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 1 || p.texts[0] != "" || len(e.pending) != 1 || a.harnessDeliveryCompletions != 0 {
				t.Fatal("waiting input blocked an emergency control or touched the busy child")
			}
			// Replay the same queued control: no second pending copy is created.
			a.harnessControls = []HarnessControl{{ID: "queued-steer", Kind: "steer", Text: "next turn"}}
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(e.pending) != 1 || len(p.texts) != 1 {
				t.Fatal("leased control duplicated while waiting")
			}
			s.observe(e, AdapterEvent{Activity: "idle"})
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 2 || a.harnessDeliveryCompletions != 0 {
				t.Fatal("later input overtook the steered turn")
			}
			s.observe(e, AdapterEvent{Activity: "idle"})
			if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
				t.Fatal("lost completion reported success", err)
			}
			s.observe(e, AdapterEvent{Activity: "idle"})
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 3 || p.texts[1] != "next turn" || !strings.Contains(p.texts[2], "send after this turn") || a.harnessDeliveryCompletions != 1 || len(e.pending) != 0 {
				t.Fatal("queued input was lost or delivered twice")
			}
			if a.harnessCompletions[1] != "queued-steer:applied:queued_next_turn" {
				t.Fatal("queued steering result overclaimed", a.harnessCompletions)
			}
			// A now-expired grant must be rejected, even while busy.
			past := time.Now().Add(-time.Minute)
			s.observe(e, AdapterEvent{Activity: "busy"})
			a.harnessControls = []HarnessControl{{ID: "expired", Kind: "steer", Text: "do not send", ExpiresAt: &past, deadline: past}}
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 3 || a.harnessCompletions[2] != "expired:rejected:authorization_expired" {
				t.Fatal("expired input retained authority")
			}
			// A lost steer completion retains the applied receipt while busy.
			s.observe(e, AdapterEvent{Activity: "idle"})
			a.harnessCompletionFailures = 1
			a.harnessControls = []HarnessControl{{ID: "lost-steer", Kind: "steer", Text: "one more turn"}}
			if err := s.serviceHarness(t.Context(), e); !errors.Is(err, ErrControlUnconfirmed) {
				t.Fatal("lost steer completion hidden", err)
			}
			s.observe(e, AdapterEvent{Activity: "idle"})
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 4 || a.harnessCompletions[3] != "lost-steer:applied:queued_next_turn" {
				t.Fatal("steer completion replay injected twice")
			}
			// An ambiguous vendor write fails explicitly and is never retried.
			p.fail = true
			a.harnessControls = []HarnessControl{{ID: "uncertain-steer", Kind: "steer", Text: "uncertain input"}}
			if err := s.serviceHarness(t.Context(), e); err != nil {
				t.Fatal(err)
			}
			if len(p.texts) != 5 || a.harnessCompletions[4] != "uncertain-steer:rejected:outcome_unconfirmed" {
				t.Fatal("uncertain input overclaimed or retried")
			}
			p.fail = false
		})
	}
}
