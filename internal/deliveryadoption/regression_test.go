// SPDX-License-Identifier: AGPL-3.0-only
package deliveryadoption

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func projectExecutor(t *testing.T, f *fixture) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: f.p.TenantID, Kind: tenant.Person}
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Project executor') RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='owner'`, p.TenantID, p.ID, f.project)
		return err
	})
	f.a.Executor = p
	f.s.cfg.Authorities = []Authority{f.a}
	return p
}

func TestMappingRetainsReferencesHiddenFromProjectExecutor(t *testing.T) {
	for _, which := range []string{"member", "release"} {
		t.Run(which, func(t *testing.T) {
			f := newFixture(t)
			other := f.node(t, "project", "PR-2", "", "open")
			release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
			member := f.member(t, f.project, release, "TK-1", 1)
			p := projectExecutor(t, f)
			hidden := member
			if which == "release" {
				hidden = release
			}
			f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, hidden, other)
			var visible int
			if err := f.s.snapshot(t.Context(), p, func(tx pgx.Tx) error {
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE id=$1`, hidden).Scan(&visible)
			}); err != nil || visible != 0 {
				t.Fatalf("fixture did not hide the referenced node: %d, %v", visible, err)
			}
			r, err := f.s.DryRun(t.Context(), p, f.project)
			if err != nil {
				t.Fatal(err)
			}
			if r.Eligible || !r.Incomplete || r.Counts.Members != 1 || r.Counts.Releases != 1 || len(r.Members) != 1 || len(r.Releases) != 1 || r.Members[0].ID != member || r.Releases[0].ID != release {
				t.Fatalf("hidden reference silently disappeared: %+v", r)
			}
			found := false
			for _, reason := range r.Reasons {
				found = found || reason.Code == which+"_visibility" && reason.NodeID == hidden
			}
			if !found {
				t.Fatalf("missing precise visibility refusal: %+v", r.Reasons)
			}
			j := f.claim(t, f.project)
			var failure *failure
			if err = f.s.runAttempt(t.Context(), f.a, &j, nil); !errors.As(err, &failure) || failure.code != "eligibility" {
				t.Fatalf("hidden-reference attempt refused for the wrong reason: %v", err)
			}
			if len(f.provider.executeCalls) != 0 || f.count(t, `SELECT count(*) FROM project_delivery`) != 0 {
				t.Fatal("incomplete visibility acquired resources or adopted")
			}
		})
	}
}

func TestFinalMappingRefusesVisibilityLostAfterVerifiedBackup(t *testing.T) {
	f := newFixture(t)
	other := f.node(t, "project", "PR-2", "", "open")
	release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	member := f.member(t, f.project, release, "TK-1", 1)
	projectExecutor(t, f)
	j := f.prepare(t, f.project)
	f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, other)
	var failure *failure
	if _, err := f.s.apply(t.Context(), f.a, j); !errors.As(err, &failure) || failure.code != "eligibility" {
		t.Fatalf("final write did not refuse incomplete visibility: %v", err)
	}
	if f.count(t, `SELECT count(*) FROM project_delivery`) != 0 {
		t.Fatal("reduced mapping adopted after backup")
	}
}

func TestRecoverySuspensionBlocksCompletePassAndCleanup(t *testing.T) {
	f := newFixture(t)
	j := f.prepare(t, f.project)
	if err := f.s.fail(t.Context(), f.a, j, "stale_source", true); err != nil {
		t.Fatal(err)
	}
	before := len(f.provider.executeCalls)
	var checkpoint string
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM delivery_adoption_jobs j WHERE project_node_id=$1`, f.project).Scan(&checkpoint)
	})
	f.s.cfg.RecoveryReconciled = false
	out, err := f.s.Pass(t.Context(), "")
	if !errors.Is(err, ErrPrerequisite) || out.Project != "" || out.Reason != "rollout_dependency" {
		t.Fatalf("suspended pass: %+v, %v", out, err)
	}
	if err = f.s.Reconcile(t.Context()); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("suspended sweeper: %v", err)
	}
	if err = f.s.runAttempt(t.Context(), f.a, &j, ErrPrerequisite); !errors.Is(err, ErrPrerequisite) {
		t.Fatalf("suspended attempt: %v", err)
	}
	var after string
	f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM delivery_adoption_jobs j WHERE project_node_id=$1`, f.project).Scan(&after)
	})
	pin := f.provider.results[operationKey(j.Identity, "pin")]
	if before != len(f.provider.executeCalls) || checkpoint != after || !pin.Protected || pin.State != "complete" {
		t.Fatal("restored checkpoint authorized effects while recovery was suspended")
	}
	f.s.cfg.RecoveryReconciled = true
	if err = f.s.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT reserved_backup_bytes FROM delivery_adoption_jobs WHERE project_node_id=$1`, f.project) != 0 {
		t.Fatal("validated recovery did not resume normal cleanup")
	}
}

func TestRecoveryAmbiguitySurvivesAutomaticAndExplicitRetries(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		name := "automatic"
		if explicit {
			name = "explicit"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			j := f.prepare(t, f.project)
			f.sql(t, `UPDATE delivery_adoption_jobs SET state='pending',lease_token=NULL,lease_until=NULL,operation_journal='{"operations":[]}',resource_attempt_id=NULL,reserved_backup_bytes=0,reserved_restore_slots=0,reason_code=NULL,cleanup_state='none',next_reconcile_at=NULL,reconciliation_cursor=NULL WHERE project_node_id=$1`, f.project)
			var failure *failure
			if err := f.s.reconcileCatalog(t.Context()); !errors.As(err, &failure) || failure.code != "cleanup_blocked" {
				t.Fatalf("catalog did not detect unknown recovery history: %v", err)
			}
			before := len(f.provider.executeCalls)
			// First retry used to replace recovery_unknown with cleanup_blocked.
			out, err := f.s.Pass(t.Context(), "")
			if err != nil || out.Project != f.project || out.Reason != "cleanup_blocked" || out.State != "retry_wait" {
				t.Fatalf("first recovery retry: %+v, %v", out, err)
			}
			status, err := f.s.Status(t.Context(), f.p, f.project)
			if err != nil {
				t.Fatal(err)
			}
			if explicit {
				if _, err = f.s.Request(t.Context(), f.p, f.project, "retry", status.Revision); err != nil {
					t.Fatal(err)
				}
			} else {
				f.clock = status.NextAttempt
			}
			// Diagnostics are replaceable presentation data. Removing the old
			// reason must not remove unresolved external recovery ownership.
			f.sql(t, `UPDATE delivery_adoption_jobs SET reason_code='cleanup_blocked' WHERE project_node_id=$1`, f.project)
			// A fresh service and a catalog outage ensure the durable checkpoint,
			// not an in-memory flag or rediscovery of the pin, holds the fence.
			restarted, err := New(f.d.App, f.s.cfg, catalogUnavailable{f.provider}, f.s.reports)
			if err != nil {
				t.Fatal(err)
			}
			restarted = restarted.WithClock(func() time.Time { return f.clock })
			out, err = restarted.Pass(t.Context(), "")
			if err != nil || out.Project != f.project || out.State != "retry_wait" || out.Reason != "cleanup_blocked" {
				t.Fatalf("recovery ambiguity was cleared by retry: %+v, %v", out, err)
			}
			pin := f.provider.results[operationKey(j.Identity, "pin")]
			if len(f.provider.executeCalls) != before || !pin.Protected || pin.State != "complete" || f.count(t, `SELECT reserved_backup_bytes FROM delivery_adoption_jobs WHERE project_node_id=$1`, f.project) != 1024 || f.count(t, `SELECT count(*) FROM project_delivery`) != 0 {
				t.Fatal("unresolved recovery resumed cleanup, released quota or adopted")
			}
		})
	}
}

type catalogUnavailable struct{ Provider }

func (catalogUnavailable) List(context.Context, string, string, int) (CatalogPage, error) {
	return CatalogPage{}, errors.New("injected catalog outage")
}

func TestVerificationUsesAdoptionEvidenceAfterPlacementEdits(t *testing.T) {
	for _, action := range []string{"rerank", "move", "rollover"} {
		t.Run(action, func(t *testing.T) {
			f := newFixture(t)
			release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
			next := f.legacyRelease(t, f.project, "REL-2", "planning", 2)
			member := f.member(t, f.project, release, "TK-1", 1)
			peer := f.member(t, f.project, release, "TK-2", 2)
			j := f.prepare(t, f.project)
			if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
				t.Fatal(err)
			}
			store := delivery.NewStore(f.d.App).WithClock(func() time.Time { return f.clock })
			if action == "rollover" {
				frozen, err := store.Transition(t.Context(), f.p, delivery.TransitionRequest{ProjectID: f.project, ReleaseID: release, ExpectedRevision: 1, Action: "freeze"})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = store.Transition(t.Context(), f.p, delivery.TransitionRequest{ProjectID: f.project, ReleaseID: release, ExpectedRevision: frozen.Revision, Action: "abandon"}); err != nil {
					t.Fatal(err)
				}
			} else {
				target := release
				slot := delivery.Slot{BeforeID: peer}
				if action == "move" {
					target = next
					slot = delivery.Slot{}
				}
				if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1, ReleaseID: target, ExpectedReleaseRevision: 1, Slot: slot}}); err != nil {
					t.Fatal(err)
				}
			}
			if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1 AND source<>'adopted' AND revision>1`, member) != 1 {
				t.Fatal("fixture did not perform a real placement mutation")
			}
			v, err := f.s.Verify(t.Context(), f.p, f.project)
			if err != nil || !v.OK || v.AdoptedMembers != 2 || v.MissingArchiveMembers != 0 {
				t.Fatalf("legitimate placement edit lost adoption evidence: %+v, %v", v, err)
			}
			// Missing retained membership still fails; source changes alone do not.
			f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
			v, err = f.s.Verify(t.Context(), f.p, f.project)
			if err != nil || v.OK || v.MissingArchiveMembers != 1 {
				t.Fatalf("verification ignored genuinely missing membership: %+v, %v", v, err)
			}
		})
	}
}

func TestVerificationRetainsArchiveAfterProjectMoveAndUndo(t *testing.T) {
	for _, route := range []string{"project-move", "move", "subtree"} {
		for _, undo := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/undo=%t", route, undo), func(t *testing.T) {
				f := newFixture(t)
				target := f.node(t, "project", "DST-1", "", "open")
				release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
				member := f.member(t, f.project, release, "TK-1", 1)
				peer := f.member(t, f.project, release, "TK-2", 2)
				root := member
				if route == "subtree" {
					root = f.node(t, "epic", "EP-1", f.project, "open")
					f.sql(t, `UPDATE nodes SET parent_id=$2 WHERE id=$1`, member, root)
				}
				j := f.prepare(t, f.project)
				if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
					t.Fatal(err)
				}
				store := delivery.NewStore(f.d.App).WithClock(func() time.Time { return f.clock })
				if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1}}); err != nil {
					t.Fatal(err)
				}
				if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1 AND release_node_id IS NULL AND revision=2`, member) != 1 {
					t.Fatal("fixture did not move the adopted member to ranked backlog")
				}
				mux := http.NewServeMux()
				nodes.New(f.d.App, nil).Mount(mux)
				events.New(f.d.App, events.WithUndoHandlers(nodes.UndoHandlers())).Mount(mux)
				path, body, eventType := "move", `{"parent_id":"`+target+`"}`, "node.moved"
				if route == "project-move" {
					path, body, eventType = route, `{"project_id":"`+target+`"}`, "node.project_moved"
				}
				adoptionMutationRequest(t, mux, f.p, "/api/nodes/"+root+"/"+path, body, http.StatusOK)
				if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 || f.count(t, `SELECT count(*) FROM nodes WHERE id=$1 AND project_id=$2`, member, target) != 1 {
					t.Fatal("project move did not remove the backlog row and move the member")
				}
				var eventID int64
				f.exec(t, func(ctx context.Context, tx pgx.Tx) error {
					return tx.QueryRow(ctx, `SELECT id FROM events WHERE node_id=$1 AND type=$2 ORDER BY id DESC LIMIT 1`, root, eventType).Scan(&eventID)
				})
				if undo {
					adoptionMutationRequest(t, mux, f.p, fmt.Sprintf("/api/events/%d/undo", eventID), "", http.StatusCreated)
					if f.count(t, `SELECT count(*) FROM nodes WHERE id=$1 AND project_id=$2`, member, f.project) != 1 || f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
						t.Fatal("Undo did not return the member to its original unranked tail")
					}
				}
				if f.count(t, `SELECT count(*) FROM journey_tickets WHERE project_node_id=$1 AND ticket_node_id=$2 AND release_node_id=$3`, f.project, member, release) != 1 {
					t.Fatal("project move or Undo changed the journey archive")
				}
				v, err := f.s.Verify(t.Context(), f.p, f.project)
				if err != nil || !v.OK || v.JourneyMembers != 2 || v.AdoptedMembers != 2 || v.MissingArchiveMembers != 0 {
					t.Fatalf("authorized project move lost adoption evidence: %+v, %v", v, err)
				}
				// A move for one member must not excuse a different missing row.
				f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, peer)
				v, err = f.s.Verify(t.Context(), f.p, f.project)
				if err != nil || v.OK || v.MissingArchiveMembers != 1 || v.AdoptedMembers != 1 {
					t.Fatalf("move evidence hid another member's deletion: %+v, %v", v, err)
				}
				if undo {
					// Re-insertion supersedes the removal evidence. The old move
					// and its Undo cannot explain a later, unrecorded deletion.
					if _, err = store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 0}}); err != nil {
						t.Fatal(err)
					}
					f.sql(t, `DELETE FROM ships_in WHERE item_node_id=$1`, member)
					v, err = f.s.Verify(t.Context(), f.p, f.project)
					if err != nil || v.OK || v.MissingArchiveMembers != 2 || v.AdoptedMembers != 0 {
						t.Fatalf("old move evidence hid a later deletion: %+v, %v", v, err)
					}
				}
			})
		}
	}
}

func TestVerificationRetainsArchiveAfterAuthorizedTailRemoval(t *testing.T) {
	f := newFixture(t)
	release := f.legacyRelease(t, f.project, "REL-1", "planning", 1)
	member := f.member(t, f.project, release, "TK-1", 1)
	j := f.prepare(t, f.project)
	if _, err := f.s.apply(t.Context(), f.a, j); err != nil {
		t.Fatal(err)
	}
	store := delivery.NewStore(f.d.App).WithClock(func() time.Time { return f.clock })
	if _, err := store.Place(t.Context(), f.p, f.project, []delivery.PlacementRequest{{ItemID: member, ExpectedProjectID: f.project, ExpectedRevision: 1, Slot: delivery.Slot{Tail: true}}}); err != nil {
		t.Fatal(err)
	}
	if f.count(t, `SELECT count(*) FROM ships_in WHERE item_node_id=$1`, member) != 0 {
		t.Fatal("placement API did not remove the row for the unranked tail")
	}
	v, err := f.s.Verify(t.Context(), f.p, f.project)
	if err != nil || !v.OK || v.AdoptedMembers != 1 || v.MissingArchiveMembers != 0 {
		t.Fatalf("authorized tail removal lost adoption evidence: %+v, %v", v, err)
	}
	// Mutation evidence never excuses losing the immutable journey archive.
	f.sql(t, `DELETE FROM journey_tickets WHERE ticket_node_id=$1`, member)
	v, err = f.s.Verify(t.Context(), f.p, f.project)
	if err != nil || v.OK || v.MissingArchiveMembers != 1 {
		t.Fatalf("tail removal hid a missing archive reference: %+v, %v", v, err)
	}
}

func adoptionMutationRequest(t *testing.T, mux *http.ServeMux, p tenant.Principal, path, body string, status int) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", path, w.Code, status, w.Body.String())
	}
}
