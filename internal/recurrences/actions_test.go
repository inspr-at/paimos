// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/routineactions"
	"github.com/inspr-at/paimos/internal/routineguard"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risks: queue/assignment acting as a grant, a tampered frozen assignment or
// fabricated evaluation granting a write, and owner revocation before mutation. Uses
// synthetic qualification and a PostgreSQL barrier at the final access fence.
func TestRoutineActionGrantRejectsUnverifiedAndRevokedOwner(t *testing.T) {
	f, in, q, reader := consentFixture(t)
	enableConsentProject(t, f, q, reader)
	install := func(m *Module) {
		f.install(m)
		mux := http.NewServeMux()
		m.Mount(mux)
		routineactions.New(m.pool, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, b routineactions.Binding, c routineguard.Context) (tenant.Principal, routineguard.Decision, string, error) {
			return m.AuthorizeActionTx(ctx, tx, p, ActionBinding(b), c)
		}).Mount(mux)
		f.handler = mux
	}
	install(f.m)
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
	var frozen json.RawMessage
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT assignment FROM routine_runs WHERE id=$1`, o.Run.ID).Scan(&frozen)
	})
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET assignment=jsonb_set(assignment,'{goal}','"Tampered frozen goal"') WHERE id=$1`, o.Run.ID)
		return err
	})
	tampered := f.call(f.p, "POST", actionPath, request, 409)
	if !strings.Contains(string(tampered), "routine_assignment_changed") {
		t.Fatalf("tampered snapshot was not denied: %s", tampered)
	}
	f.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE routine_runs SET assignment=$2 WHERE id=$1`, o.Run.ID, frozen)
		return err
	})
	var retainedOwner string
	f.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Retained owner') RETURNING id::text`, f.p.TenantID).Scan(&retainedOwner)
	})
	dbtest.BindRole(t, f.d, f.p.TenantID, retainedOwner, "owner")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	holder, err := f.d.App.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(t.Context())
	if _, err := holder.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true),set_config('aeon.visible_projects','*',true)`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.LockTenant(ctx, holder, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("POST", actionPath, strings.NewReader(string(raw))).WithContext(tenant.WithPrincipal(ctx, f.p))
		out := httptest.NewRecorder()
		f.handler.ServeHTTP(out, req)
		response <- out
	}()
	dbtest.WaitForLock(t, ctx, f.d, holder.Conn().PgConn().PID(), "transactionid")
	// Visibility was read before this change; final RequireTx must read the live
	// binding after its real access-fence wait. Keep read access and another owner.
	if _, err := holder.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE key='viewer') WHERE principal_id=$1`, f.p.ID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
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
