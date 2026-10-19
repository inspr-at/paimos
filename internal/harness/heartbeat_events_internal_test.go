// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: ownership and recovery capabilities change without audit history, or
// fresh observations of an unchanged process resume per-tick event storage.
func TestHeartbeatOwnershipAndCapabilitiesWriteEvents(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Agent}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('heartbeat-events','Heartbeat events') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	var project string
	ctx := dbtest.Seed(t.Context())
	if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Heartbeat writer') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PRJ-1','Heartbeat project' FROM node_kinds WHERE slug='project' RETURNING id::text`, p.TenantID).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	later := now.Add(time.Second)
	owner := ownedprocess.Identity{DaemonID: "heartbeat-daemon", Generation: "11111111111111111111111111111111", ProcessID: "22222222222222222222222222222222", RootPID: 1234, GroupID: 1234, StartedAt: now}
	sameOwner := owner
	otherOwner := owner
	otherOwner.ProcessID = "33333333333333333333333333333333"
	for _, tc := range []struct {
		name               string
		beforeOwner, owner *ownedprocess.Identity
		beforeCaps, caps   []string
		events             int
	}{
		{"ownership established", nil, &owner, []string{"inbox"}, []string{"inbox"}, 1},
		{"ownership changed", &owner, &otherOwner, []string{"inbox"}, []string{"inbox"}, 1},
		{"ownership removed", &owner, nil, []string{"inbox"}, []string{"inbox"}, 1},
		{"capability added", &owner, &sameOwner, []string{"inbox"}, []string{"inbox", "attached_reconnect_v1"}, 1},
		{"capability removed", &owner, &sameOwner, []string{"inbox", "attached_reconnect_v1"}, []string{"inbox"}, 1},
		{"ownership and capability changed", nil, &owner, []string{"inbox"}, []string{"inbox", "attached_reconnect_v1"}, 1},
		{"unchanged without ownership", nil, nil, []string{"inbox"}, []string{"inbox"}, 0},
		{"unchanged with ownership", &owner, &sameOwner, []string{"inbox", "attached_reconnect_v1"}, []string{"inbox", "attached_reconnect_v1"}, 0},
		{"capabilities reordered", &owner, &sameOwner, []string{"inbox", "attached_reconnect_v1"}, []string{"attached_reconnect_v1", "inbox"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := Session{ID: project, ProjectID: project, AgentPrincipalID: p.ID, Phase: "working", Activity: "busy", ActivitySequence: 1, HeartbeatAt: &now, ProcessObservedAt: &now, ProcessOwnership: tc.beforeOwner, Capabilities: tc.beforeCaps}
			after := before
			after.ProcessOwnership, after.Capabilities = tc.owner, tc.caps
			after.HeartbeatAt, after.ProcessObservedAt, after.ActivitySequence = &later, &later, 2
			if err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
				var oldCount, newCount int
				const count = `SELECT count(*) FROM events WHERE type='harness.heartbeat' AND after->>'id'=$1`
				if err := tx.QueryRow(ctx, count, before.ID).Scan(&oldCount); err != nil {
					return err
				}
				r, flush := deferControlEvents(httptest.NewRequest("POST", "/heartbeat", nil).WithContext(ctx), tx)
				err := recordHeartbeat(r.Context(), tx, p, before, after)
				flush(&err)
				if err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, count, before.ID).Scan(&newCount); err != nil {
					return err
				}
				if got := newCount - oldCount; got != tc.events {
					t.Fatalf("heartbeat wrote %d events, want %d", got, tc.events)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
