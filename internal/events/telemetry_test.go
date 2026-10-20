// SPDX-License-Identifier: AGPL-3.0-only
package events

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
)

// Risk: a rolled-back tick escapes as live freshness or commits another audit
// row. A committed event is an ordering barrier on the actual notify channel.
func TestHeartbeatFreshnessNotifyCommitWithoutEvent(t *testing.T) {
	d, a, _ := fixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	listener, err := db.SubscribeNotifications(ctx, d.App, "aeon_events", a.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close(context.Background())
	sentinel := errors.New("rollback")
	err = db.InTenant(dbtest.Seed(ctx), d.App, a.TenantID, func(tx pgx.Tx) error {
		if err := NotifyHarnessHeartbeat(ctx, tx, a.TenantID, a.ID); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	marker := appendEvents(t, d, a, 1)[0]
	n, err := listener.WaitForNotification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hint map[string]json.RawMessage
	if json.Unmarshal([]byte(n.Payload), &hint) != nil || string(hint["id"]) != strconv.FormatInt(marker.ID, 10) || len(hint) != 2 {
		t.Fatalf("rollback notified before commit barrier: %s", n.Payload)
	}
	before := logPosition(t, d, a)
	err = db.InTenant(dbtest.Seed(ctx), d.App, a.TenantID, func(tx pgx.Tx) error { return NotifyHarnessHeartbeat(ctx, tx, a.TenantID, a.ID) })
	if err != nil {
		t.Fatal(err)
	}
	n, err = listener.WaitForNotification(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hint = nil
	if json.Unmarshal([]byte(n.Payload), &hint) != nil || len(hint) != 2 || string(hint["heartbeat_session_id"]) != strconv.Quote(a.ID) || string(hint["tenant_id"]) != strconv.Quote(a.TenantID) {
		t.Fatalf("non-value-free notify: %s", n.Payload)
	}
	if after := logPosition(t, d, a); before != after {
		t.Fatalf("heartbeat advanced durable log: %d -> %d", before, after)
	}
}
