// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Deliberately create a new person with a custom role, never an owner binding.
func (f *storeFixture) permissionPerson(t *testing.T, permissions ...string) (tenant.Principal, string) {
	t.Helper()
	p := tenant.Principal{TenantID: f.tenant, Kind: tenant.Person}
	var role string
	f.run(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Custom release operator') RETURNING id::text`, f.tenant).Scan(&p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'custom_release_operator','Custom release operator') RETURNING id::text`, f.tenant).Scan(&role); err != nil {
			return err
		}
		for _, permission := range permissions {
			if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.tenant, role, permission); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.tenant, p.ID, role)
		return err
	})
	return p, role
}

func (f *storeFixture) requirePermission(t *testing.T, p tenant.Principal, permission string, want error) {
	t.Helper()
	err := authz.Require(tenant.WithPrincipal(authz.BindPool(t.Context(), f.d.App), p), permission, authz.Scope{ProjectID: f.project})
	if !errors.Is(err, want) {
		t.Fatalf("fixture authority %s: %v, want %v", permission, err, want)
	}
}

func (f *storeFixture) releaseHistory(t *testing.T, id string) {
	t.Helper()
	f.exec(t, `UPDATE project_releases SET state='frozen' WHERE release_node_id=$1`, id)
	f.exec(t, `UPDATE project_releases SET version='1.0.0',version_scheme='legacy',cut_at=$2 WHERE release_node_id=$1`, id, f.clock)
	f.exec(t, `UPDATE project_releases SET state='released',released_at=$2,reservation_basis='attested',released_by=$3 WHERE release_node_id=$1`, id, f.clock, f.actor)
}

func TestStoreFix5HistoryCorrectionSelectsLockedPermission(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			f := newStoreFixture(t)
			p, _ := f.permissionPerson(t, "nodes.read", "roles.manage")
			f.requirePermission(t, p, "roles.manage", nil)
			f.requirePermission(t, p, "releases.write", authz.ErrForbidden)
			f.releaseHistory(t, f.release)
			id := f.item(t, "ticket", "TK-1", "done", f.release, "V")
			deadline := f.clock.Add(time.Hour)
			if expired {
				deadline = f.clock
			}
			f.exec(t, `UPDATE project_releases SET entry_closes_at=$2 WHERE release_node_id=$1`, f.next, deadline)
			_, err := f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}})
			if expired {
				if !errors.Is(err, ErrEntryClosed) {
					t.Fatalf("history admission must retain its separate write deadline: %v", err)
				}
				if f.placed(t, id).Revision != 1 || f.placed(t, id).ReleaseID != f.release || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
					t.Fatal("refused correction changed state or events")
				}
				return
			}
			if err != nil {
				t.Fatalf("roles.manage alone must correct history before the deadline: %v", err)
			}
			if got := f.placed(t, id); got.ReleaseID != f.next || got.Revision != 2 {
				t.Fatalf("correction=%+v", got)
			}
			// Once neither locked container is history, roles.manage is insufficient.
			_, err = f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 2}})
			if !errors.Is(err, authz.ErrForbidden) || f.placed(t, id).Revision != 2 {
				t.Fatalf("ordinary move bypassed releases.write: %v", err)
			}
		})
	}
}

func TestStoreFix5HistoryUndoSelectsLockedPermission(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			f := newStoreFixture(t)
			p, role := f.permissionPerson(t, "nodes.read", "roles.manage", "releases.write")
			deadline := f.clock.Add(time.Hour)
			f.exec(t, `UPDATE project_releases SET entry_closes_at=$2 WHERE release_node_id=$1`, f.release, deadline)
			f.releaseHistory(t, f.release)
			id := f.item(t, "ticket", "TK-1", "done", f.release, "V")
			if _, err := f.store.Place(t.Context(), p, f.project, []PlacementRequest{{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1}}); err != nil {
				t.Fatal(err)
			}
			e := f.lastEvent(t, "ships_in.changed")
			f.exec(t, `DELETE FROM role_permissions WHERE role_id=$1 AND permission='releases.write'`, role)
			f.requirePermission(t, p, "releases.write", authz.ErrForbidden)
			if expired {
				f.clock = deadline
			}
			err := f.undo(t, p, e)
			if expired {
				if !errors.Is(err, ErrEntryClosed) || f.placed(t, id).Revision != 2 || f.placed(t, id).ReleaseID != f.next {
					t.Fatalf("undo bypassed history admission deadline: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("history undo must select roles.manage from locked containers: %v", err)
			}
			if got := f.placed(t, id); got.ReleaseID != f.release || got.Rank != "V" || got.Revision != 3 {
				t.Fatalf("history undo=%+v", got)
			}
		})
	}
}

func TestStoreFix5ExpediteCleanupSharesBatchCAS(t *testing.T) {
	for _, staleFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale_first=%t", staleFirst), func(t *testing.T) {
			f := newStoreFixture(t)
			stale := f.item(t, "ticket", "TK-1", "done", f.release, "V")
			active := f.item(t, "ticket", "TK-2", "open", f.release, "W")
			f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, stale)
			requests := []PlacementRequest{
				{ItemID: active, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1, Expedite: true},
				{ItemID: stale, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1, Slot: Slot{BeforeID: active}},
			}
			if staleFirst {
				requests[0], requests[1] = requests[1], requests[0]
			}
			out, err := f.store.Place(t.Context(), f.person, f.project, requests)
			if err != nil {
				t.Fatalf("cleanup must not invalidate the submitted item's original CAS: %v", err)
			}
			if len(out) != 2 || f.placed(t, stale).Expedite || !f.placed(t, active).Expedite || f.placed(t, stale).Revision != 2 || f.placed(t, active).Revision != 2 {
				t.Fatalf("batch must mutate each item once: %+v", out)
			}
			e := f.lastEvent(t, "ships_in.changed")
			var before, after placementSnapshot
			if err := json.Unmarshal(e.Before, &before); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(e.After, &after); err != nil {
				t.Fatal(err)
			}
			if len(before.Members) != 2 || len(after.Members) != 2 {
				t.Fatal("cleanup duplicated a requested event member")
			}
			for i, old := range before.Members {
				if old.Revision != 1 || old.Expedite != (old.ItemID == stale) || old.ItemID != after.Members[i].ItemID || after.Members[i].Revision != 2 {
					t.Fatalf("event lost original snapshot: before=%+v after=%+v", before, after)
				}
			}
			if err := f.undo(t, f.person, e); err != nil {
				t.Fatal(err)
			}
			if !f.placed(t, stale).Expedite || f.placed(t, active).Expedite || f.placed(t, stale).Rank != "V" || f.placed(t, active).Rank != "W" || f.placed(t, stale).Revision != 3 || f.placed(t, active).Revision != 3 {
				t.Fatal("batch undo failed to restore the original snapshot once per item")
			}
		})
	}
}

func TestStoreFix5FullBatchDiscoversEverySource(t *testing.T) {
	f := newStoreFixture(t)
	// Terminal containers keep this fixture below the non-terminal release cap.
	addSource := func(i int) Release {
		r := f.addRelease(t, f.project, fmt.Sprintf("REL-%d", i+3), "internal", "frozen", fmt.Sprintf("E%03dV", i))
		f.exec(t, `UPDATE project_releases SET state='released',released_at=$2 WHERE release_node_id=$1`, r.ID, f.clock)
		return f.releaseRow(t, r.ID)
	}
	staleSource := addSource(0)
	stale := f.item(t, "ticket", "TK-0", "done", staleSource.ID, "V")
	f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, stale)
	requests := make([]PlacementRequest, 100)
	sources := []string{staleSource.ID}
	for i := range requests {
		source := addSource(i + 1)
		sources = append(sources, source.ID)
		id := f.item(t, "ticket", fmt.Sprintf("TK-%d", i+1), "open", source.ID, "V")
		requests[i] = PlacementRequest{ItemID: id, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.next, ExpectedReleaseRevision: 1, Expedite: i == 0}
	}
	// Force a legal sequential plan with the stale row first in heap order.
	// A LIMIT 100 then omits a requested source rather than the stale source
	// (which the expedite lookup already discovers). No random UUID ordering.
	cfg := f.d.App.Config()
	for _, param := range []string{"enable_indexscan", "enable_indexonlyscan", "enable_bitmapscan"} {
		cfg.ConnConfig.RuntimeParams[param] = "off"
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool).WithClock(func() time.Time { return f.clock })
	out, err := store.Place(t.Context(), f.person, f.project, requests)
	if err != nil {
		t.Fatalf("all 101 distinct source containers must be discovered and locked: %v", err)
	}
	if len(out) != 101 || f.placed(t, stale).Expedite || f.placed(t, stale).Revision != 2 || f.releaseRow(t, f.next).Revision != 2 {
		t.Fatalf("incomplete full batch result: members=%d", len(out))
	}
	for _, request := range requests {
		if got := f.placed(t, request.ItemID); got.ReleaseID != f.next || got.Revision != 2 || got.Expedite != request.Expedite {
			t.Fatalf("requested placement=%+v", got)
		}
	}
	for _, source := range sources {
		if got := f.releaseRow(t, source); got.Revision != 2 {
			t.Fatalf("source container omitted: %+v", got)
		}
	}
	if f.scalar(t, `SELECT count(*) FROM events WHERE type='ships_in.changed'`) != 2 {
		t.Fatal("expanded batch events must retain the 100-member bound")
	}
}

func TestStoreFix5TombstoneExpediteUndoIsMaintenance(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			f := newStoreFixture(t)
			stale := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			active := f.item(t, "ticket", "TK-2", "open", f.release, "W")
			f.exec(t, `UPDATE ships_in SET expedite=true WHERE item_node_id=$1`, stale)
			f.exec(t, `UPDATE nodes SET deleted_at=$2 WHERE id=$1`, stale, f.clock)
			if _, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: active, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: f.release, ExpectedReleaseRevision: 1, Expedite: true}}); err != nil {
				t.Fatal(err)
			}
			e := f.lastEvent(t, "ships_in.changed")
			// Maintenance undo must not admit a deleted item through normal Place.
			_, err := f.store.Place(t.Context(), f.person, f.project, []PlacementRequest{{ItemID: stale, ExpectedProjectID: f.project, ExpectedRevision: 2, ReleaseID: f.release, ExpectedReleaseRevision: 2}})
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("live placement admitted a tombstone: %v", err)
			}
			if changed {
				f.exec(t, `UPDATE ships_in SET revision=revision+1 WHERE item_node_id=$1`, stale)
			}
			err = f.undo(t, f.person, e)
			if changed {
				if !errors.Is(err, ErrRevisionChanged) || f.placed(t, stale).Expedite || !f.placed(t, active).Expedite || f.placed(t, active).Revision != 2 {
					t.Fatalf("maintenance undo lost its CAS/atomicity: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("existing tombstone cleanup must be immediately reversible: %v", err)
			}
			if !f.placed(t, stale).Expedite || f.placed(t, active).Expedite || f.placed(t, stale).Rank != "V" || f.placed(t, active).Rank != "W" || f.placed(t, stale).Revision != 3 || f.placed(t, active).Revision != 3 || f.scalar(t, `SELECT count(*) FROM nodes WHERE id=$1 AND deleted_at IS NOT NULL`, stale) != 1 {
				t.Fatal("maintenance undo did not restore placements while preserving deletion")
			}
		})
	}
}

func TestStoreFix5AbandonSelectsStatePermission(t *testing.T) {
	for _, tc := range []struct {
		state, permission string
		expired           bool
		want              error
	}{
		{"planned", "releases.write", false, nil},
		{"planned", "releases.deploy", false, authz.ErrForbidden},
		{"building", "releases.deploy", false, nil},
		{"frozen", "releases.deploy", false, nil},
		{"building", "releases.write", false, authz.ErrForbidden},
		{"building", "releases.deploy", true, ErrEntryClosed},
	} {
		t.Run(fmt.Sprintf("%s/%s/expired=%t", tc.state, tc.permission, tc.expired), func(t *testing.T) {
			f := newStoreFixture(t)
			p, _ := f.permissionPerson(t, "nodes.read", tc.permission)
			f.requirePermission(t, p, tc.permission, nil)
			missing := "releases.write"
			if tc.permission == missing {
				missing = "releases.deploy"
			}
			f.requirePermission(t, p, missing, authz.ErrForbidden)
			f.exec(t, `UPDATE project_releases SET state=$2 WHERE release_node_id=$1`, f.release, tc.state)
			deadline := f.clock.Add(time.Hour)
			if tc.expired {
				deadline = f.clock
			}
			f.exec(t, `UPDATE project_releases SET entry_closes_at=$2 WHERE release_node_id=$1`, f.next, deadline)
			id := f.item(t, "ticket", "TK-1", "open", f.release, "V")
			original := f.placed(t, id)
			out, err := f.store.Transition(t.Context(), p, TransitionRequest{ProjectID: f.project, ReleaseID: f.release, ExpectedRevision: 1, Action: "abandon"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("abandon must select authority from fenced state: %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				if f.releaseRow(t, f.release).State != tc.state || f.releaseRow(t, f.release).Revision != 1 || !reflect.DeepEqual(f.placed(t, id), original) || f.releaseRow(t, f.next).Revision != 1 || f.scalar(t, `SELECT count(*) FROM events`) != 0 {
					t.Fatal("refused abandon left partial rollover/state/events")
				}
				return
			}
			if out.State != "abandoned" || out.Revision != 2 || f.placed(t, id).ReleaseID != f.next || f.placed(t, id).Revision != 2 || f.releaseRow(t, f.next).Revision != 2 {
				t.Fatalf("abandon=%+v placement=%+v", out, f.placed(t, id))
			}
		})
	}
}

func TestStoreFix5CanonicalGuardUnchanged(t *testing.T) {
	// Pin main's existing guard byte-for-byte; delivery coverage belongs here.
	// Filled from origin/main, not from the candidate file being checked.
	const mainGuardSHA256 = "390b7d29ea05cc19ae60a7bb7641dc7baf09f4d78184d0c5fc725c37a5e1b956"
	data, err := os.ReadFile("../agentpairing/lock_order_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != mainGuardSHA256 {
		t.Fatalf("main's canonical lock guard must remain unchanged: got %s, want %s", got, mainGuardSHA256)
	}
}
