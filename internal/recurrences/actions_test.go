// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/routineactions"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: queue/assignment acting as a grant, fabricated evaluation granting a
// write, and owner revocation between admission and the final mutation. Uses
// synthetic qualification and a PostgreSQL barrier before the final fence.
func TestRoutineActionGrantRejectsUnverifiedAndRevokedOwner(t *testing.T) {
	f, in, q, reader := consentFixture(t)
	enableConsentProject(t, f, q, reader)
	r := f.create(in)
	path := "/api/recurrences/" + r.ID
	f.call(f.p, "POST", path+"/resume", map[string]any{"expected_revision": r.Revision}, 200)
	r = f.get(r.ID)
	f.call(f.p, "PUT", path+"/execution-consent", map[string]any{"expected_revision": r.Revision, "execute_consent": true, "execution_principal_id": f.p.ID}, 200)
	r = f.get(r.ID)
	o := f.manual(r.ID, "action-test")
	if o.Run == nil {
		t.Fatal("missing frozen run")
	}
	var attempt string
	var at time.Time
	f.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO routine_attempts(tenant_id,run_id,attempt_key,role,principal_id,assignment_digest,state) VALUES($1,$2,'build','build',$3,$4,'running') RETURNING id::text`, f.p.TenantID, o.Run.ID, f.p.ID, o.Run.AssignmentDigest).Scan(&attempt); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT updated_at FROM nodes WHERE id=$1`, f.parent).Scan(&at)
	})
	request := routineactions.Request{AttemptID: attempt, ActionKey: "effect", Kind: "work.create", ProjectID: f.project, TargetID: f.parent, ExpectedRevision: at.UnixMicro(), WorkCreate: &routineactions.WorkCreate{Title: "Must not exist", Body: "No verified verdict"}}
	actionPath := "/api/routine-runs/" + o.Run.ID + "/actions"
	denied := f.call(f.p, "POST", actionPath, request, 403)
	if !strings.Contains(string(denied), "active_run_grant_required") {
		t.Fatalf("wrong refusal: %s", denied)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET state='running' WHERE id=$1`, o.Run.ID)
		return err
	})
	held := f.call(f.p, "POST", actionPath, request, 409)
	if !strings.Contains(string(held), "guardrail_evaluation_required") {
		t.Fatalf("missing evaluation was not held: %s", held)
	}
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.Contains(sql, "SELECT set_config('aeon.account_use_capable'") })
	f.install(New(pool).WithExecutionRuntime(reader))
	raw, _ := json.Marshal(request)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", actionPath, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.p))
		out := httptest.NewRecorder()
		f.handler.ServeHTTP(out, req)
		response <- out
	}()
	barrier.Wait(t, ctx)
	f.tx(func(tx pgx.Tx) error {
		if err := db.LockTenant(ctx, tx, f.p.TenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM role_bindings WHERE principal_id=$1`, f.p.ID)
		return err
	})
	barrier.Release()
	out := dbtest.Await(t, ctx, response)
	if out.Code != 403 || !strings.Contains(out.Body.String(), "permission") {
		t.Fatalf("revocation failed or wrong reason: %d %s", out.Code, out.Body.String())
	}
	f.tx(func(tx pgx.Tx) error {
		var effects, receipts int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE title='Must not exist'`).Scan(&effects); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM routine_actions WHERE run_id=$1 AND action_key='effect'`, o.Run.ID).Scan(&receipts); err != nil {
			return err
		}
		if effects != 0 || receipts != 0 {
			t.Fatalf("denial left a mutation/success receipt: %d %d", effects, receipts)
		}
		return nil
	})
}
