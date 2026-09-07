// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"
)

func TestFencedConsumerIndependentStreamProgressAndRepairDrain(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := agentmessage.ConsumerRegistration{RuntimeID: uuid.NewString(), RuntimeGeneration: uuid.NewString(), SessionID: uuid.NewString(), SessionGeneration: uuid.NewString(), TargetID: uuid.NewString(), TargetVersion: 1, Kind: "fallback"}
	b := a
	b.TargetID, b.SessionID = uuid.NewString(), uuid.NewString()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	aRequests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in agentmessage.ConsumerRegistration
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.TargetID == a.TargetID {
			aRequests <- struct{}{}
			once.Do(func() { close(entered) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(agentmessage.ConsumerStream{SchemaVersion: 1, ID: uuid.NewString(), Revision: 1, Generation: in.Generation, Kind: in.Kind, ExpiresAt: time.Now().Add(5 * time.Minute).Format(time.RFC3339Nano)})
	}))
	defer server.Close()
	// Unblock handlers before httptest waits during failure cleanup.
	defer cancel()
	proof, _ := NewProof()
	h, err := NewHTTP(server.URL, 42, proof, func() (string, error) { return "fixture", nil })
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c, err := NewConsumers(dir, h)
	if err != nil {
		t.Fatal(err)
	}
	aDone := make(chan error, 1)
	go func() { aDone <- c.Step(ctx, a, false, nil) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream never entered")
	}
	bDone := make(chan error, 1)
	go func() { bDone <- c.Step(ctx, b, false, nil) }()
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("healthy fenced stream blocked behind stalled stream")
	}
	<-aRequests
	waiting, stopWaiting := context.WithCancel(ctx)
	replacement := a
	replacement.TargetVersion++
	replacement.SessionGeneration = uuid.NewString()
	nextDone := make(chan error, 1)
	go func() { nextDone <- c.Step(waiting, replacement, false, nil) }()
	select {
	case <-aRequests:
		t.Fatal("target replacement overtook stalled generation")
	case <-nextDone:
		t.Fatal("replacement completed before its predecessor")
	case <-time.After(30 * time.Millisecond):
	}
	stopWaiting()
	select {
	case err := <-nextDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("same-stream wait ignored cancellation")
	}
	repaired := make(chan error, 1)
	go func() { repaired <- c.Repair() }()
	select {
	case <-repaired:
		t.Fatal("repair returned before stream drained")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-repaired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("repair did not drain")
	}
}
