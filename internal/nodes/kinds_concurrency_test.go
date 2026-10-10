// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestKindConcurrentDisjointPatches(t *testing.T) {
	p := newPrincipal(t, "kind-concurrency")
	var id string
	if err := adminPool.QueryRow(t.Context(), `SELECT id::text FROM node_kinds WHERE tenant_id=$1 AND slug='work'`, p.TenantID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, appPool, func(sql string) bool { return strings.Contains(sql, "UPDATE node_kinds") })
	m := New(pool, nil).(*Module)
	ctx = tenant.WithPrincipal(ctx, p)
	first := make(chan error, 1)
	go func() {
		_, err := m.updateKind(ctx, p, id, map[string]json.RawMessage{"label": json.RawMessage(`"New label"`)})
		first <- err
	}()
	pid := barrier.Wait(t, ctx)
	second := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		_, err := m.updateKind(ctx, p, id, map[string]json.RawMessage{"icon": json.RawMessage(`"star"`)})
		second <- err
		close(done)
	}()
	if dbtest.BlockedOrDone(t, ctx, adminPool, pid, done) == "" {
		t.Fatal("second patch did not overlap first")
	}
	barrier.Release()
	if err := dbtest.Await(t, ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, second); err != nil {
		t.Fatal(err)
	}
	var label, icon string
	if err := adminPool.QueryRow(ctx, `SELECT label,icon FROM node_kinds WHERE id=$1`, id).Scan(&label, &icon); err != nil {
		t.Fatal(err)
	}
	if label != "New label" || icon != "star" {
		t.Fatalf("lost patch: label=%q icon=%q", label, icon)
	}
	var beforeLabel string
	if err := adminPool.QueryRow(ctx, `SELECT before->>'label' FROM events WHERE tenant_id=$1 AND type='kind.updated' ORDER BY id DESC LIMIT 1`, p.TenantID).Scan(&beforeLabel); err != nil {
		t.Fatal(err)
	}
	if beforeLabel != label {
		t.Fatalf("stale event preimage: %q", beforeLabel)
	}
}
