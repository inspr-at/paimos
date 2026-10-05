// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/usagedashboard"
	"github.com/jackc/pgx/v5"
)

// Dependency contract fixtures, not replacement production storage. These are
// the consumed columns of AEON-734 (project_lead_generations, 1258) and AEON-689
// (episodes/contributions, 1247). When integrated, their real schema is used.
func (w *world) leadContracts(t *testing.T) {
	t.Helper()
	_, err := w.db.Admin.Exec(t.Context(), `CREATE TABLE IF NOT EXISTS project_lead_generations (
 tenant_id uuid NOT NULL,project_id uuid NOT NULL,generation bigint NOT NULL,session_id uuid NOT NULL,
 PRIMARY KEY(tenant_id,project_id,generation),UNIQUE(tenant_id,session_id));
 CREATE TABLE IF NOT EXISTS ticket_work_episodes (
 tenant_id uuid NOT NULL,id uuid NOT NULL,ticket_node_id uuid NOT NULL,source_project_id uuid NOT NULL,
 coverage_complete boolean NOT NULL DEFAULT true,PRIMARY KEY(tenant_id,id));
 CREATE TABLE IF NOT EXISTS ticket_work_contributions (
 tenant_id uuid NOT NULL,id uuid NOT NULL,episode_id uuid NOT NULL,principal_id uuid NOT NULL,source_project_id uuid NOT NULL,
 role text NOT NULL,session_id uuid,run_id uuid,first_work_at timestamptz NOT NULL DEFAULT now(),
 active_ms numeric NOT NULL DEFAULT 0,waiting_ms numeric NOT NULL DEFAULT 0,tokens numeric NOT NULL DEFAULT 0,
 timing_known boolean NOT NULL DEFAULT false,tokens_known boolean NOT NULL DEFAULT false,
 timing_complete boolean NOT NULL DEFAULT false,tokens_complete boolean NOT NULL DEFAULT false,
 stopped boolean NOT NULL DEFAULT false,gap boolean NOT NULL DEFAULT false,PRIMARY KEY(tenant_id,id));`)
	if err != nil {
		t.Fatal(err)
	}
	// Test fixtures retain the real tenant boundary even before dependencies land.
	for _, table := range []string{"project_lead_generations", "ticket_work_episodes", "ticket_work_contributions"} {
		_, err = w.db.Admin.Exec(t.Context(), fmt.Sprintf(`ALTER TABLE %s ENABLE ROW LEVEL SECURITY; ALTER TABLE %s FORCE ROW LEVEL SECURITY; GRANT ALL ON %s TO %s`, table, table, table, w.db.Role))
		if err != nil {
			t.Fatal(err)
		}
		var exists bool
		if err = w.db.Admin.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_policies WHERE tablename=$1)`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			_, err = w.db.Admin.Exec(t.Context(), fmt.Sprintf(`CREATE POLICY tenant_fixture ON %s USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid) WITH CHECK(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)`, table))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}
func (w *world) leadSession(t *testing.T, project string, parent *string, run *string, role string, digest byte) string {
	t.Helper()
	id := uid()
	at := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	management := "unmanaged"
	if run != nil {
		management = "managed"
	}
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,parent_id,run_id,harness,host,management,role,ref_digest,lease_digest,created_at)
  VALUES($1,$2,$3,$4,$5,$6,'codex','builder',$7,$8,$9,$9,$10)`, w.home.TenantID, id, project, w.agent, parent, run, management, role, []byte{digest}, at)
		return err
	})
	return id
}
func (w *world) leadGeneration(t *testing.T, project, session string, generation int) {
	t.Helper()
	w.tx(t, w.home, func(tx pgx.Tx) error {
		// Real AEON-734 history references its singleton row.
		var singleton bool
		if err := tx.QueryRow(t.Context(), `SELECT to_regclass('project_leads') IS NOT NULL`).Scan(&singleton); err != nil {
			return err
		}
		if singleton {
			if _, err := tx.Exec(t.Context(), `INSERT INTO project_leads(tenant_id,project_id,owner_principal_id,session_id,generation,state) VALUES($1,$2,$3,$4,$5,'working') ON CONFLICT(tenant_id,project_id) DO UPDATE SET session_id=EXCLUDED.session_id,generation=EXCLUDED.generation`, w.home.TenantID, project, w.home.ID, session, generation); err != nil {
				return err
			}
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_generations(tenant_id,project_id,generation,session_id) VALUES($1,$2,$3,$4)`, w.home.TenantID, project, generation, session)
		return err
	})
}
func (w *world) leadContribution(t *testing.T, project, episode, session, role string, run *string, known bool, tokens int) string {
	t.Helper()
	id := uid()
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO ticket_work_contributions(tenant_id,id,episode_id,principal_id,source_project_id,role,session_id,run_id,first_work_at,tokens,active_ms,waiting_ms,tokens_known,timing_known,tokens_complete,timing_complete,stopped)
  VALUES($1,$2,$3,$4,$5,$6,$7,$8,'2026-09-10T12:00:00Z',$9,100,20,$10,$10,$10,$10,true)`, w.home.TenantID, id, episode, w.agent, project, role, session, run, tokens, known)
		return err
	})
	return id
}
func (w *world) leadEpisode(t *testing.T, project, ticket string) string {
	t.Helper()
	id := uid()
	w.tx(t, w.home, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(t.Context(), `SELECT id::text FROM ticket_work_episodes WHERE ticket_node_id=$1 ORDER BY id DESC LIMIT 1`, ticket).Scan(&existing)
		if err == nil {
			id = existing
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		_, err = tx.Exec(t.Context(), `INSERT INTO ticket_work_episodes(tenant_id,id,ticket_node_id,source_project_id) VALUES($1,$2,$3,$4)`, w.home.TenantID, id, ticket, project)
		return err
	})
	return id
}
func (w *world) getLeadUsage(t *testing.T, p tenant.Principal, project, query string) (int, usagedashboard.LeadUsage, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/projects/"+project+"/lead/usage?from=2026-09-01&to=2026-10-01"+query, nil).WithContext(tenant.WithPrincipal(context.Background(), p))
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r)
	var out usagedashboard.LeadUsage
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, out, rec.Body.String()
}
func TestLeadUsageGenerationSuccessionAndUnknownEvidence(t *testing.T) {
	w := newWorld(t)
	w.leadContracts(t)
	project := w.project(t, w.home, "LEAD-1", "Lead")
	lead := w.leadSession(t, project, nil, nil, "coordinator", 1)
	w.leadGeneration(t, project, lead, 1)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode,subscription_label) VALUES($1,$2,'lead-model',1,10,4,0,false,'subscription','private-plan-beacon')`, w.home.TenantID, lead)
		return err
	})

	worker := w.leadSession(t, project, &lead, nil, "worker", 2)
	nested := w.leadSession(t, project, &worker, nil, "worker", 3)
	review := w.leadSession(t, project, &lead, nil, "worker", 4)
	fix := w.leadSession(t, project, &lead, nil, "worker", 5)
	unknown := w.leadSession(t, project, &lead, nil, "worker", 6)
	ticket := w.ticket(t, w.home, project, "LEAD-2", "Work")
	episode := w.leadEpisode(t, project, ticket)
	first := w.leadContribution(t, project, episode, worker, "built", nil, true, 30)
	w.leadContribution(t, project, episode, nested, "built", nil, true, 15)
	w.leadContribution(t, project, episode, review, "reviewed", nil, true, 7)
	w.leadContribution(t, project, episode, fix, "fixed", nil, true, 11)
	w.leadContribution(t, project, episode, unknown, "built", nil, false, 9000)
	successor := w.leadSession(t, project, nil, nil, "coordinator", 7)
	w.leadGeneration(t, project, successor, 2)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET parent_id=$2,usage_lead_session_id=$2 WHERE id=$1`, worker, successor)
		return err
	})
	for range 2 {
		code, out, body := w.getLeadUsage(t, w.home, project, "&generation=1")
		if code != 200 || out.Total.Tokens.Measured == nil || *out.Total.Tokens.Measured != "77" || out.Worker.Tokens.Unknown != 1 || out.Lead.Tokens.Known != 1 || out.Review.Contributions != 1 || out.Fix.Contributions != 1 || !out.Partial || out.Forecast != "estimate_unavailable" {
			t.Fatalf("usage %d %s", code, body)
		}
		if out.Total.Attempts != 6 || out.Worker.Contributions != 3 || out.Total.Contributions != 6 {
			t.Fatalf("duplicate contributions: %s", body)
		}
	}
	code, out, body := w.getLeadUsage(t, w.home, project, "&generation=2")
	if code != 200 || out.Worker.Contributions != 0 || out.Total.Tokens.Measured != nil || out.Total.Contributions != 1 {
		t.Fatalf("succession %d %s", code, body)
	}
	code, out, body = w.getLeadUsage(t, w.home, project, "")
	if code != 200 || out.Total.Contributions != 7 || *out.Total.Tokens.Measured != "77" || out.Lead.Contributions != 2 || strings.Contains(body, "private-plan-beacon") {
		t.Fatalf("project roll-up %d %s", code, body)
	}
	// Retained fixture evidence must still exist after replay and succession.
	var count int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM ticket_work_contributions WHERE id=$1 AND episode_id=$2`, first, episode).Scan(&count); err != nil || count != 1 {
		t.Fatalf("fixture lost: %d %v", count, err)
	}
}
func TestLeadUsageUnavailableBoundsAndPrivacy(t *testing.T) {
	w := newWorld(t)
	project := w.project(t, w.home, "PRIVATE-1", "Private")
	code, _, body := w.getLeadUsage(t, w.home, project, "")
	if code != 503 || !strings.Contains(body, "unavailable") {
		t.Fatalf("missing dependency %d %s", code, body)
	}
	w.leadContracts(t)
	lead := w.leadSession(t, project, nil, nil, "coordinator", 1)
	w.leadGeneration(t, project, lead, 1)
	hidden := w.project(t, w.home, "HIDDEN-1", "Secret")
	ticket := w.ticket(t, w.home, hidden, "HIDDEN-2", "Secret work")
	episode := w.leadEpisode(t, hidden, ticket)
	worker := w.leadSession(t, project, &lead, nil, "worker", 2)
	w.leadContribution(t, hidden, episode, worker, "built", nil, true, 999)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, w.home.TenantID, w.member.ID, project)
		return err
	})
	code, out, body := w.getLeadUsage(t, w.member, project, "")
	if code != 200 || out.Total.Tokens.Measured != nil || !out.Partial || strings.Contains(body, hidden) || strings.Contains(body, "999") || out.Held.State != "withheld" {
		t.Fatalf("privacy %d %s", code, body)
	}
	code, _, body = w.getLeadUsage(t, w.member, hidden, "")
	if code != 403 {
		t.Fatalf("hidden project %d %s", code, body)
	}
	code, _, body = w.getLeadUsage(t, w.foreign, project, "")
	if code != 404 {
		t.Fatalf("tenant isolation %d %s", code, body)
	}
	for _, q := range []string{"&generation=-1", "&generation=0", "&generation=999999999999999999999", "&extra=x", "&generation=1&generation=2", "&to=2026-11-01", "&extra=" + strings.Repeat("x", 600)} {
		code, _, body = w.getLeadUsage(t, w.home, project, q)
		if code != 400 {
			t.Fatalf("bounds %s: %d %s", q, code, body)
		}
	}
	code, _, body = w.getLeadUsage(t, w.home, project, "&generation=2")
	if code != 404 {
		t.Fatalf("unknown generation %d %s", code, body)
	}
}

func TestLeadUsageHoldsReleaseAndRetryDoNotDoubleCount(t *testing.T) {
	w := newWorld(t)
	w.leadContracts(t)
	project := w.project(t, w.home, "HOLDS-1", "Holds")
	lead := w.leadSession(t, project, nil, nil, "coordinator", 1)
	w.leadGeneration(t, project, lead, 1)
	account, window := w.allowanceWindow(t, "lead-hold-fixture", "private-account-beacon", 1000, 0, 0)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=false WHERE id=$1`, account)
		return err
	})
	run := w.managedRun(t, w.home, project)
	worker := w.leadSession(t, project, &lead, &run, "worker", 2)
	// Same run exposed by another harness generation and two episode segments:
	// reserve once, but retain the disjoint contributions' measurements.
	duplicate := w.leadSession(t, project, &lead, &run, "worker", 3)
	ticket := w.ticket(t, w.home, project, "HOLDS-2", "Work")
	episode := w.leadEpisode(t, project, ticket)
	w.leadContribution(t, project, episode, worker, "built", &run, true, 20)
	w.leadContribution(t, project, episode, duplicate, "fixed", &run, true, 5)
	retry := w.managedRun(t, w.home, project)
	retrySession := w.leadSession(t, project, &lead, &retry, "worker", 4)
	w.leadContribution(t, project, episode, retrySession, "built", &retry, false, 99999)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET account_id=$1 WHERE id=ANY($2::uuid[])`, account, []string{run, retry}); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) VALUES($1,$2,$4,70),($1,$3,$4,40)`, w.home.TenantID, run, retry, window)
		return err
	})
	for range 2 {
		code, out, body := w.getLeadUsage(t, w.home, project, "")
		if code != 200 || len(out.Held.Windows) != 1 || out.Held.Windows[0].Reserved != "110" || out.Held.State != "available" || *out.Total.Tokens.Measured != "25" || out.Total.Attempts != 3 || out.Worker.Tokens.Unknown != 1 {
			t.Fatalf("holds/attempts %d %s", code, body)
		}
		if strings.Contains(body, "private-account-beacon") || strings.Contains(body, "allowance") || strings.Contains(body, "percent") {
			t.Fatalf("invented quota/account label: %s", body)
		}
	}
	// An administrator is not this account's owner.
	dbtest.BindRole(t, w.db, w.home.TenantID, w.admin.ID, "owner")
	code, out, body := w.getLeadUsage(t, w.admin, project, "")
	if code != 200 || len(out.Held.Windows) != 0 || out.Held.State != "withheld" || strings.Contains(body, window) || *out.Total.Tokens.Measured != "25" {
		t.Fatalf("owner privacy %d %s", code, body)
	}
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_reservations SET state=CASE WHEN run_id=$1 THEN 'released' ELSE 'settled' END,actual_units=CASE WHEN run_id=$1 THEN NULL ELSE 9 END,settled_at=now() WHERE run_id=ANY($2::uuid[])`, run, []string{run, retry})
		return err
	})
	code, out, body = w.getLeadUsage(t, w.home, project, "")
	if code != 200 || len(out.Held.Windows) != 1 || out.Held.Windows[0].Reserved != "0" || out.Held.Windows[0].Settled == nil || *out.Held.Windows[0].Settled != "9" || *out.Total.Tokens.Measured != "25" {
		t.Fatalf("released holds %d %s", code, body)
	}
}

func TestLeadUsageContributionLimitIsHonest(t *testing.T) {
	w := newWorld(t)
	w.leadContracts(t)
	project := w.project(t, w.home, "CAP-1", "Cap")
	lead := w.leadSession(t, project, nil, nil, "coordinator", 1)
	w.leadGeneration(t, project, lead, 1)
	worker := w.leadSession(t, project, &lead, nil, "worker", 2)
	ticket := w.ticket(t, w.home, project, "CAP-2", "Work")
	episode := w.leadEpisode(t, project, ticket)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO ticket_work_contributions(tenant_id,id,episode_id,principal_id,source_project_id,role,session_id,first_work_at,tokens,tokens_known)
  SELECT $1,gen_random_uuid(),$2,$3,$4,'built',$5,'2026-09-10T12:00:00Z',1,true FROM generate_series(1,5001)`, w.home.TenantID, episode, w.agent, project, worker)
		return err
	})
	code, out, body := w.getLeadUsage(t, w.home, project, "")
	if code != 200 || !out.Partial || !out.Truncated || out.Worker.Contributions != 5000 || *out.Worker.Tokens.Measured != "5000" || !strings.Contains(body, "contribution_limit") {
		t.Fatalf("cap %d %s", code, body)
	}
	var retained int
	if err := w.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM ticket_work_contributions WHERE episode_id=$1`, episode).Scan(&retained); err != nil || retained != 5001 {
		t.Fatalf("fixture was pruned: %d %v", retained, err)
	}
}

func TestLeadUsageSharedRunNeverAddsLeadAndWorkerCounters(t *testing.T) {
	w := newWorld(t)
	w.leadContracts(t)
	project := w.project(t, w.home, "SHARED-1", "Shared run")
	run := w.managedRun(t, w.home, project)
	lead := w.leadSession(t, project, nil, &run, "coordinator", 1)
	w.leadGeneration(t, project, lead, 1)
	worker := w.leadSession(t, project, &lead, &run, "worker", 2)
	ticket := w.ticket(t, w.home, project, "SHARED-2", "Work")
	episode := w.leadEpisode(t, project, ticket)
	w.leadContribution(t, project, episode, worker, "built", &run, true, 30)
	w.tx(t, w.home, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO harness_session_usage(tenant_id,session_id,model,sequence,input_tokens,output_tokens,cached_input_tokens,provisional,billing_mode) VALUES($1,$2,'shared-model',1,1000,10,0,false,'subscription')`, w.home.TenantID, lead)
		return err
	})
	code, out, body := w.getLeadUsage(t, w.home, project, "")
	if code != 200 || !out.Partial || out.Total.Attempts != 1 || out.Lead.Tokens.Measured != nil || out.Lead.Tokens.Unknown != 1 || out.Total.Tokens.Measured == nil || *out.Total.Tokens.Measured != "30" {
		t.Fatalf("overlapping attempt %d %s", code, body)
	}
}
