// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
)

func TestFencedConsumerDurableNoncePayloadOnceAndCompletionReplay(t *testing.T) {
	for _, lost := range []string{"completion", "execute"} {
		t.Run(lost, func(t *testing.T) {
			dir := t.TempDir()
			_ = os.Chmod(dir, 0700)
			lease, _ := NewProof()
			stream := agentmessage.ConsumerStream{SchemaVersion: 1, ID: uuid.NewString(), Revision: 1, Kind: "fallback", ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)}
			attempt := agentmessage.ConsumerAttempt{ID: uuid.NewString(), StreamID: stream.ID, Revision: 1, ResourceID: uuid.NewString(), Cursor: 11, State: "claimed", ExpiresAt: stream.ExpiresAt}
			var consumer *Consumers
			var nonce string
			effects, executes, completes := 0, 0, 0
			unknown := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(agentmessage.ConsumerLeaseHeader) == "" {
					t.Error("private consumer lease absent")
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/streams"):
					var in agentmessage.ConsumerRegistration
					_ = json.NewDecoder(r.Body).Decode(&in)
					stream.Generation = in.Generation
					_ = json.NewEncoder(w).Encode(stream)
				case strings.HasSuffix(r.URL.Path, "/claim"):
					var in agentmessage.ConsumerClaim
					_ = json.NewDecoder(r.Body).Decode(&in)
					attempt.RequestKey = in.RequestKey
					nonce = r.Header.Get(agentmessage.ConsumerAttemptHeader)
					entries := consumer.journal.Snapshot()
					if len(entries) != 1 || entries[0].Nonce != nonce || entries[0].Phase != "claiming" {
						t.Error("claim nonce was not synced before request")
					}
					_ = json.NewEncoder(w).Encode(agentmessage.ConsumerPage{SchemaVersion: 1, Attempt: &attempt})
				case strings.HasSuffix(r.URL.Path, "/execute"):
					executes++
					if r.Header.Get(agentmessage.ConsumerAttemptHeader) != nonce {
						t.Error("attempt nonce changed")
					}
					attempt.State = "executing"
					if lost == "execute" {
						http.Error(w, "private untrusted fixture response", 503)
						return
					}
					_ = json.NewEncoder(w).Encode(agentmessage.ConsumerPage{SchemaVersion: 1, Attempt: &attempt, Delivery: &agentmessage.ConsumerDelivery{Envelope: &agentmessage.Envelope{}, Work: agentmessage.DeliveryWork{TargetRef: "private-fixture-target"}}})
				case strings.HasSuffix(r.URL.Path, "/complete"):
					completes++
					var in agentmessage.ConsumerCompletion
					_ = json.NewDecoder(r.Body).Decode(&in)
					if r.Header.Get(agentmessage.ConsumerAttemptHeader) != nonce {
						t.Error("completion nonce changed")
					}
					if lost == "completion" && completes == 1 {
						http.Error(w, "lost response", 503)
						return
					}
					state := "completed"
					if in.Outcome == "outcome_unknown" {
						state = "outcome_unknown"
						unknown = true
					}
					_ = json.NewEncoder(w).Encode(agentmessage.ConsumerResult{SchemaVersion: 1, State: state, Cursor: 11})
				default:
					t.Error("unexpected route")
				}
			}))
			defer server.Close()
			h, e := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture-key", nil })
			if e != nil {
				t.Fatal(e)
			}
			consumer, e = NewConsumers(dir, h)
			if e != nil {
				t.Fatal(e)
			}
			registration := agentmessage.ConsumerRegistration{RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), SessionID: uuid.NewString(), SessionGeneration: uuid.NewString(), TargetID: uuid.NewString(), TargetVersion: 1, Kind: "fallback"}
			effect := func(context.Context, agentmessage.ConsumerPage) (string, error) {
				effects++
				entry := consumer.journal.Snapshot()[0]
				raw, _ := json.Marshal(entry)
				if entry.Phase != "executing" || strings.Contains(string(raw), "private-fixture-target") {
					t.Fatal("effect preceded receipt or stored target")
				}
				return "idle", nil
			}
			if e = consumer.Step(context.Background(), registration, true, effect); e == nil {
				t.Fatal("lost response not surfaced")
			}
			consumer, e = NewConsumers(dir, h)
			if e != nil {
				t.Fatal(e)
			}
			e = consumer.Step(context.Background(), registration, true, effect)
			if lost == "execute" {
				if !errors.Is(e, ErrUnknown) || effects != 0 || !unknown {
					t.Fatal("lost execute reissued payload", e, effects)
				}
			} else if e != nil || effects != 1 {
				t.Fatal("lost ack repeated vendor handoff", e, effects)
			}
			if executes != 1 {
				t.Fatal("execute endpoint was repeated")
			}
		})
	}
}

func TestFencedConsumerBusyWaitDoesNotClaimOrOpenCircuit(t *testing.T) {
	lease, _ := NewProof()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	streamID := uuid.NewString()
	claims := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/streams") {
			claims++
			http.Error(w, "unexpected", 500)
			return
		}
		var in agentmessage.ConsumerRegistration
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(agentmessage.ConsumerStream{SchemaVersion: 1, ID: streamID, Revision: 1, Generation: in.Generation, Kind: in.Kind, ExpiresAt: time.Now().Add(time.Minute).Format(time.RFC3339Nano)})
	}))
	defer server.Close()
	h, _ := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture", nil })
	c, e := NewConsumers(dir, h)
	if e != nil {
		t.Fatal(e)
	}
	in := agentmessage.ConsumerRegistration{RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), SessionID: uuid.NewString(), SessionGeneration: uuid.NewString(), TargetID: uuid.NewString(), TargetVersion: 1, Kind: "attention"}
	for range 5 {
		if e = c.Step(context.Background(), in, false, nil); e != nil {
			t.Fatal(e)
		}
	}
	if claims != 0 || c.journal.Snapshot()[0].Failures != 0 || c.journal.Snapshot()[0].Phase != "idle" {
		t.Fatal("busy wait reserved effect or consumed failure budget")
	}
}

func TestFencedConsumerServerUnknownPersistsAndCannotBeRepaired(t *testing.T) {
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "consumer_outcome_unknown"})
	}))
	defer server.Close()
	lease, _ := NewProof()
	h, _ := NewHTTP(server.URL, 42, lease, func() (string, error) { return "fixture", nil })
	c, e := NewConsumers(dir, h)
	if e != nil {
		t.Fatal(e)
	}
	in := agentmessage.ConsumerRegistration{RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), SessionID: uuid.NewString(), SessionGeneration: uuid.NewString(), TargetID: uuid.NewString(), TargetVersion: 1, Kind: "attention"}
	if e = c.Step(context.Background(), in, true, nil); !errors.Is(e, ErrUnknown) {
		t.Fatal("server custody was hidden", e)
	}
	c, e = NewConsumers(dir, h)
	if e != nil {
		t.Fatal(e)
	}
	if !errors.Is(c.Repair(), ErrUnknown) || !errors.Is(c.Step(context.Background(), in, true, nil), ErrUnknown) || calls != 1 {
		t.Fatal("server unknown custody was cleared or retried")
	}
}
