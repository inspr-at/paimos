// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"context"
	"errors"
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
	// Pause the real project-member DELETE after it owns tenant and tree.
	// The claim must wait on tenant before taking the tree fence.
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
	// The observed overlap must still permit foreign-key readers.
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
