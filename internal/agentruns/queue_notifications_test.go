// SPDX-License-Identifier: AGPL-3.0-only
package agentruns_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// LISTEN's initial frame is the barrier. A later committed creation wakes the
// real stream, and revoking the exact key closes it before another hint.
func TestQueuedRunNotificationsWakeAndRecheckExactKey(t *testing.T) {
	f := setup(t)
	o := f.order(t, nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/runs/queued/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+f.token)
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("invalid stream response %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	frame := func() string {
		t.Helper()
		var lines []string
		for scanner.Scan() {
			if scanner.Text() == "" {
				return strings.Join(lines, "\n")
			}
			lines = append(lines, scanner.Text())
		}
		t.Fatalf("stream ended early: %v", scanner.Err())
		return ""
	}
	if got := frame(); got != "event: queue.wake\ndata: {}" {
		t.Fatalf("initial hint disclosed content: %s", got)
	}
	run := f.run(t, o)
	if got := frame(); got != "event: queue.wake\ndata: {}" {
		t.Fatalf("creation did not wake: %s", got)
	}
	for _, typ := range []string{"queue.routed", "agent_pairing.verification_created"} {
		f.tx(t, f.person, func(tx pgx.Tx) error {
			_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &o.NodeID, Type: typ, Before: run, After: map[string]string{"run_id": run.ID}})
			return err
		})
		if got := frame(); got != "event: queue.wake\ndata: {}" {
			t.Fatalf("%s did not wake: %s", typ, got)
		}
	}
	// Count the queue-channel commit hints directly: ordinary probe events must
	// never cause the daemon stream to spend a transaction checking relevance.
	listener, err := pgx.ConnectConfig(ctx, f.d.App.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background())
	if _, err := listener.Exec(ctx, "LISTEN "+events.QueueHintChannel); err != nil {
		t.Fatal(err)
	}
	var queuedEvent int64
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := events.Append(t.Context(), tx, f.person, events.Change{Type: "account.probed", After: map[string]string{"id": uuid()}}); err != nil {
			return err
		}
		e, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &o.NodeID, Type: "run.created", After: run})
		queuedEvent = e.ID
		return err
	})
	notice, err := listener.WaitForNotification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hint struct {
		ID int64 `json:"id"`
	}
	if json.Unmarshal([]byte(notice.Payload), &hint) != nil || hint.ID != queuedEvent {
		t.Fatal("probe event leaked onto the queue hint channel")
	}
	if got := frame(); got != "event: queue.wake\ndata: {}" {
		t.Fatalf("queued event did not wake: %s", got)
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE principal_id=$1`, f.agent.ID)
		return err
	})
	// The commit below guarantees a notification without waiting for keepalive.
	f.run(t, o)
	if scanner.Scan() {
		t.Fatalf("revoked key received a frame: %s", scanner.Text())
	}
	if ctx.Err() != nil {
		t.Fatal("revocation did not close stream; test deadline expired")
	}
}
