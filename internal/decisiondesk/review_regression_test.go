// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (f *fixture) notices(t *testing.T, limit int) []Item {
	t.Helper()
	var items []Item
	err := db.InTenant(db.AllProjects(t.Context(), "desk notice regression"), f.d.App, f.reader.TenantID, func(tx pgx.Tx) error {
		var err error
		items, err = NoticesTx(t.Context(), tx, f.reader, limit)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestUndecidableApprovalDoesNotStarveHeldQuestion(t *testing.T) {
	f := setup(t)
	approval := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	q := f.ask(t, f.project, "paused", []string{f.ticket})
	visible := f.page(t, f.reader, 100, nil)
	if len(visible.Items) != 2 || visible.Items[0].ID != approval {
		t.Fatal("fixture must put a readable, undecidable approval first")
	}
	notices := f.notices(t, 1)
	if len(notices) != 1 || notices[0].ID != q.ID {
		t.Fatalf("undecidable approval blocked limit=1: %+v", notices)
	}
	if won, err := f.claim(t, f.reader, notices[0]); err != nil || !won {
		t.Fatalf("authorized question was not claimable: %t %v", won, err)
	}
}

func TestUndecidableQuestionDoesNotStarveAuthorizedQuestion(t *testing.T) {
	f := setup(t)
	var otherTicket string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Other held work',$2 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work' RETURNING id::text`, f.person.TenantID, f.otherProject).Scan(&otherTicket)
	})
	if err != nil {
		t.Fatal(err)
	}
	first := f.ask(t, f.otherProject, "paused", []string{otherTicket})
	second := f.ask(t, f.project, "paused", []string{f.ticket})
	var role string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'read_question','Read question') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'questions.read')`, f.person.TenantID, role)
	f.exec(t, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.person.TenantID, f.reader.ID, role, f.otherProject)
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.desk_write','on',true)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE desk_questions SET created_at=now()-interval '1 hour' WHERE node_id=$1`, first.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	page := f.page(t, f.reader, 100, nil)
	if len(page.Items) != 2 || page.Items[0].ID != first.ID || !page.Items[0].Held || !page.Items[1].Held {
		t.Fatalf("fixture did not retain both held sources: %+v", page.Items)
	}
	notices := f.notices(t, 1)
	if len(notices) != 1 || notices[0].ID != second.ID {
		t.Fatalf("read-only question starved deciding recipient: %+v", notices)
	}
}

func TestScopeDeniedApprovalReceivesTerminalSkippedClaim(t *testing.T) {
	f := setup(t)
	var role string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'notify_decider','Notify decider') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'approvals.decide')`, f.person.TenantID, role)
	f.exec(t, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.person.TenantID, f.reader.ID, role)
	approval := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	// The project member can change tickets in this project, but the native
	// approval scope check requires workspace nodes.write, which this role lacks.
	q := f.ask(t, f.project, "paused", []string{f.ticket})
	first := f.notices(t, 1)
	if len(first) != 1 || first[0].ID != approval {
		t.Fatal("scope-denied approval must lead the scan")
	}
	if won, err := f.claim(t, f.reader, first[0]); err != nil || won {
		t.Fatalf("scope-denied approval admitted: %t %v", won, err)
	}
	var state string
	var completed bool
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT state,completed_at IS NOT NULL FROM desk_notification_claims WHERE tenant_id=$1 AND recipient_id=$2 AND item_id=$3`, f.person.TenantID, f.reader.ID, approval).Scan(&state, &completed); err != nil {
		t.Fatal(err)
	}
	if state != "skipped" || !completed {
		t.Fatal("undecidable scope did not receive terminal evidence")
	}
	next := f.notices(t, 1)
	if len(next) != 1 || next[0].ID != q.ID {
		t.Fatalf("terminal skip did not advance the scan: %+v", next)
	}
	if won, err := f.claim(t, f.reader, next[0]); err != nil || !won {
		t.Fatalf("next authorized source was lost: %t %v", won, err)
	}
}

func TestClaimWaitsForHeldOpenDeactivation(t *testing.T) {
	for _, fence := range []string{"UPDATE", "NO KEY UPDATE"} {
		t.Run(fence, func(t *testing.T) {
			f := setup(t)
			f.ask(t, f.project, "paused", []string{f.ticket})
			item := f.page(t, f.reader, 100, nil).Items[0]
			ctx, cancel := context.WithTimeout(db.AllProjects(t.Context(), "held deactivation regression"), 15*time.Second)
			defer cancel()
			revocation, err := f.d.Admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer revocation.Rollback(context.Background())
			if _, err := revocation.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR `+fence, f.person.TenantID); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var won bool
			var claimErr error
			go func() {
				defer close(done)
				claimErr = db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
					var err error
					won, err = ClaimTx(ctx, tx, f.reader, item)
					return err
				})
			}()
			if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, revocation.Conn().PgConn().PID(), done); lock != "transactionid" {
				t.Fatalf("claim failed to wait on tenant revocation: lock=%q", lock)
			}
			if _, err := revocation.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, f.reader.ID); err != nil {
				t.Fatal(err)
			}
			if err := revocation.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			dbtest.Await(t, ctx, done)
			if claimErr != nil || won {
				t.Fatalf("claim survived committed deactivation: %t %v", won, claimErr)
			}
			var count int
			if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM desk_notification_claims WHERE recipient_id=$1`, f.reader.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("deactivated recipient retained a claim: %d %v", count, err)
			}
		})
	}
}

func TestClaimWaitsForProjectAccessMutationWithoutTenantInversion(t *testing.T) {
	f := setup(t)
	q := f.ask(t, f.project, "paused", []string{f.ticket})
	page := f.page(t, f.reader, 100, nil)
	if len(page.Items) != 1 || page.Items[0].ID != q.ID || !page.Items[0].Held {
		t.Fatal("fixture must retain an authorized, unclaimed held question")
	}
	item := page.Items[0]
	// Pause the real project-member DELETE after tenant/tree entry. The claim
	// must wait on the tenant fence before reaching tree or source records.
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(query string) bool {
		return strings.Contains(query, "pg_advisory_xact_lock(hashtextextended($1::text, 0))")
	})
	ctx, cancel := context.WithCancel(db.AllProjects(ctx, "project access notification regression"))
	defer cancel()
	mux := http.NewServeMux()
	authz.New(pool).Mount(mux)
	r := httptest.NewRequest(http.MethodDelete, "/api/projects/"+f.project+"/members/"+f.reader.ID, nil).
		WithContext(tenant.WithPrincipal(ctx, f.person))
	w := httptest.NewRecorder()
	mutationDone := make(chan struct{})
	go func() {
		defer close(mutationDone)
		mux.ServeHTTP(w, r)
	}()
	mutationPID := barrier.Wait(t, ctx)

	claimDone := make(chan struct{})
	var won bool
	var claimErr error
	go func() {
		defer close(claimDone)
		claimErr = db.InTenant(ctx, f.d.App, f.reader.TenantID, func(tx pgx.Tx) error {
			var err error
			won, err = ClaimTx(ctx, tx, f.reader, item)
			return err
		})
	}()
	t.Cleanup(func() {
		cancel()
		barrier.Release()
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		dbtest.Await(t, cleanupCtx, mutationDone)
		dbtest.Await(t, cleanupCtx, claimDone)
	})
	if lock := dbtest.BlockedOrDone(t, ctx, f.d.Admin, mutationPID, claimDone); lock != "transactionid" {
		t.Fatalf("claim did not overlap the project's tenant fence: lock=%q", lock)
	}
	// NOWAIT proves the ordering at the observed overlap, without depending on
	// the deadlock detector's timing or choice of victim. The paused mutation
	// already retains the conflicting tenant fence before the claim can enter.
	probe, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Rollback(context.Background())
	if _, err := probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR KEY SHARE NOWAIT`, f.reader.TenantID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			t.Fatal("project tenant fence blocks foreign-key readers (SQLSTATE 55P03)")
		}
		t.Fatalf("unexpected tenant lock probe failure: %v", err)
	}
	_, err = probe.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR UPDATE NOWAIT`, f.reader.TenantID)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("mutation did not retain conflicting tenant fence: %v", err)
	}
	if err := probe.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	barrier.Release()
	dbtest.Await(t, ctx, mutationDone)
	if w.Code != http.StatusNoContent {
		t.Fatalf("project access removal failed: %d %s", w.Code, w.Body.String())
	}
	dbtest.Await(t, ctx, claimDone)
	if claimErr != nil || won {
		t.Fatalf("claim survived committed project access removal: %t %v", won, claimErr)
	}
	var bindings, claims int
	if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='project' AND scope_id=$3`, f.reader.TenantID, f.reader.ID, f.project).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM desk_notification_claims WHERE tenant_id=$1 AND recipient_id=$2 AND item_id=$3`, f.reader.TenantID, f.reader.ID, item.ID).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 || claims != 0 {
		t.Fatalf("revoked recipient retained a binding or claim: bindings=%d claims=%d", bindings, claims)
	}
	// An authorized recipient can still claim this exact source/revision: the
	// denial must be caused by committed access removal, not missing held work.
	if won, err := f.claim(t, f.person, item); err != nil || !won {
		t.Fatalf("access removal lost the held source for other recipients: %t %v", won, err)
	}
}

func TestClaimUsesCurrentSourceProjectNotCallerHint(t *testing.T) {
	f := setup(t)
	f.ask(t, f.project, "paused", []string{f.ticket})
	item := f.page(t, f.reader, 100, nil).Items[0]
	item.ProjectID = f.otherProject // No reader binding exists here.
	if won, err := f.claim(t, f.reader, item); err != nil || !won {
		t.Fatalf("caller hint overrode authorized source project: %t %v", won, err)
	}
}

func TestSourceClaimDoesNotScanProjectionAndCoverageIsExplicit(t *testing.T) {
	f := setup(t)
	f.ask(t, f.project, "paused", []string{f.ticket})
	item := f.page(t, f.reader, 100, nil).Items[0]
	f.addUnrelatedProjects(t)
	if won, err := f.claim(t, f.reader, item); err != nil || !won {
		t.Fatalf("source claim unnecessarily depends on tenant projection: %t %v", won, err)
	}
	err := db.InTenant(db.AllProjects(t.Context(), "current source regression"), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		current, err := CurrentTx(t.Context(), tx, f.reader, item)
		if err == nil && !current {
			t.Fatal("authorized current source was lost")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) addUnrelatedProjects(t *testing.T) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Unrelated project' FROM node_kinds k CROSS JOIN generate_series(1,1000) WHERE k.tenant_id=$1 AND k.slug='project'`, f.person.TenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCoverageFailureExplainsLimit(t *testing.T) {
	f := setup(t)
	f.addUnrelatedProjects(t)
	r := httptest.NewRequest("GET", "/api/decision-desk/projection", nil).WithContext(tenant.WithPrincipal(t.Context(), f.reader))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "coverage exceeds 1000 projects") {
		t.Fatalf("coverage failure is opaque: %d %s", w.Code, w.Body.String())
	}
}

func TestProjectionApprovalLabelRetainsScopeWithoutRationale(t *testing.T) {
	f := setup(t)
	approval := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	page := f.page(t, f.person, 100, nil)
	if len(page.Items) != 1 || page.Items[0].ID != approval {
		t.Fatal("approval fixture was lost")
	}
	if !strings.Contains(page.Items[0].Title, "nodes.write") || strings.Contains(page.Items[0].Title, "Private rationale") {
		t.Fatalf("approval label lost safe scope context: %q", page.Items[0].Title)
	}
}

// Risk: irrelevant manual checks exhaust person-step coverage and suppress the
// reader's real requests, questions and approvals across the whole Desk.
func TestPersonStepProjectionIgnoresIrrelevantCoverage(t *testing.T) {
	f := setup(t)
	question := f.ask(t, f.project, "carries_on", nil)
	approval := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	var definition string
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon,allowed_child_kinds) VALUES($1,'definition','Definition','DEF','list',ARRAY[]::text[])`, f.person.TenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,fields)
		 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Workspace definition','{"checks":{}}'
		 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='definition' RETURNING id::text`, f.person.TenantID).Scan(&definition); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET state='open',fields=jsonb_build_object('assignee',$2::text,'tags',jsonb_build_array('anchor')) WHERE id=$1`, f.ticket, f.reader.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id,state)
		 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Bulk irrelevant work',$2,'open'
		 FROM node_kinds k CROSS JOIN generate_series(1,1001) WHERE k.tenant_id=$1 AND k.slug='work'`, f.person.TenantID, f.project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name           string
		rule           map[string]any
		fields         map[string]any
		state          string
		event          string
		activeForOther bool
	}{
		{name: "non-applicable UI", rule: map[string]any{"condition": "ui"}},
		{name: "non-applicable code", rule: map[string]any{"condition": "code"}, fields: map[string]any{"labels": []string{"docs"}}},
		{name: "non-applicable blocked", rule: map[string]any{"condition": "blocked"}},
		{name: "non-applicable release notes", rule: map[string]any{"condition": "release_notes"}, fields: map[string]any{"hide_from_release_notes": true}},
		{name: "non-applicable releases", rule: map[string]any{"condition": "releases"}},
		{name: "non-applicable kinds", rule: map[string]any{"kinds": []string{"task"}}},
		{name: "non-applicable labels", rule: map[string]any{"labels": []string{"bulk", "required"}}},
		{name: "already ticked", event: "entry_check.ticked"},
		{name: "already ticked legacy event", event: "node.entry_check_ticked"},
		{name: "another assignee", fields: map[string]any{"assignee": f.person.ID}, activeForOther: true},
		{name: "another named owner", rule: map[string]any{"owner_id": f.person.ID}, activeForOther: true},
		{name: "another fallback owner", rule: map[string]any{"fallback_owner_id": f.person.ID}, fields: map[string]any{"assignee": f.agent.ID}, activeForOther: true},
		{name: "reassigned manual owner", event: "node.person_step_reassigned", activeForOther: true},
		{name: "another human check owner", state: "done", fields: map[string]any{"assignee": f.person.ID}, activeForOther: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkID := fmt.Sprintf("manual:bulk-%d", i)
			rule := map[string]any{"id": checkID, "label": "Bulk check", "labels": []string{"bulk"}}
			for key, value := range tc.rule {
				rule[key] = value
			}
			fields := map[string]any{"assignee": f.reader.ID, "tags": []string{"bulk"}}
			for key, value := range tc.fields {
				fields[key] = value
			}
			definitionJSON, err := json.Marshal(map[string]any{"checks": map[string]any{"in_progress": []any{
				map[string]any{"id": "manual:anchor", "label": "Reader's check", "labels": []string{"anchor"}}, rule,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			fieldsJSON, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			state := tc.state
			if state == "" {
				state = "open"
			}
			err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb,updated_at=$3 WHERE id=$1`, definition, definitionJSON, time.Date(2026, 10, 15, 0, 0, i, 0, time.UTC)); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2::jsonb,state=$3,human_check=CASE WHEN $3='done' THEN 'Verify deployment' END WHERE tenant_id=$1 AND title='Bulk irrelevant work'`, f.person.TenantID, fieldsJSON, state); err != nil {
					return err
				}
				if tc.event != "" {
					_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after)
					 SELECT n.tenant_id,$2,n.id,$3,jsonb_build_object('check_id',$4::text,'definition_id',d.id,'definition_revision',d.updated_at,'checked',true,'owner_id',$5::text)
					 FROM nodes n CROSS JOIN nodes d WHERE n.tenant_id=$1 AND n.title='Bulk irrelevant work' AND d.id=$6`, f.person.TenantID, f.person.ID, tc.event, checkID, f.person.ID, definition)
					return err
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Retain and verify the >1000 fixtures, plus a native sample showing
			// that they are inactive or currently owned by someone else.
			var count, active int
			var owned bool
			var anchorID string
			err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND title='Bulk irrelevant work'`, f.person.TenantID).Scan(&count); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*)::int,coalesce(bool_and(step->'owner'->>'id'=$2),false)
				 FROM jsonb_array_elements(aeon_person_steps((SELECT id FROM nodes WHERE tenant_id=$1 AND title='Bulk irrelevant work' ORDER BY id LIMIT 1))) step
				 WHERE step->>'active'='true'`, f.person.TenantID, f.person.ID).Scan(&active, &owned); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT step->>'id' FROM jsonb_array_elements(aeon_person_steps($1)) step WHERE step->>'active'='true' AND step->'owner'->>'id'=$2`, f.ticket, f.reader.ID).Scan(&anchorID)
			})
			if err != nil {
				t.Fatal(err)
			}
			if count != 1001 || tc.activeForOther && (active != 1 || !owned) || !tc.activeForOther && active != 0 {
				t.Fatalf("wrong irrelevant fixtures: count=%d active=%d ownedByOther=%t", count, active, owned)
			}
			page := f.page(t, f.reader, 100, nil)
			if page.Counts.Open != 3 || len(page.Items) != 3 || page.HasMore {
				t.Fatalf("irrelevant person steps changed Desk counts: %+v", page)
			}
			want := map[string]string{question.ID: "question", approval: "approval", anchorID: "action_request"}
			for _, item := range page.Items {
				if want[item.ID] != item.Kind {
					t.Fatalf("Desk lost an authorized source or disclosed another owner's step: %+v", item)
				}
				delete(want, item.ID)
			}
			if len(want) != 0 {
				t.Fatalf("Desk lost authorized sources: %+v", want)
			}
		})
	}
}
