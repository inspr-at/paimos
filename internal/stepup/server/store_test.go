// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type fixture struct {
	d                                *dbtest.DB
	m                                *Module
	agent, otherAgent, person, other tenant.Principal
	now                              time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), now: time.Now().UTC().Truncate(time.Microsecond)}
	var tid string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name)VALUES('stepup','Step-up') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	principal := func(kind tenant.PrincipalKind, name string) tenant.Principal {
		p := tenant.Principal{TenantID: tid, Kind: kind, Name: name, BrowserSession: kind == tenant.Person, Scopes: []string{"approvals.request", "nodes.read"}}
		if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name)VALUES($1,$2,$3)RETURNING id::text`, tid, kind, name).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.d, tid, p.ID, "admin")
		return p
	}
	f.agent = principal(tenant.Agent, "Lead")
	f.otherAgent = principal(tenant.Agent, "Other agent")
	f.person = principal(tenant.Person, "Markus")
	f.other = principal(tenant.Person, "Anna")
	f.m = New(f.d.App, nil, "https://aeon.example")
	f.m.RecordResultTx = inbox.RecordResult
	f.m.now = func() time.Time { return f.now }
	return f
}
func (f *fixture) create(t *testing.T) ApprovalRequest {
	t.Helper()
	var revision int64
	var enabled *bool
	err := f.d.Admin.QueryRow(t.Context(), `SELECT enabled,revision FROM features WHERE tenant_id=$1 AND key='workspace-summary' AND project_id IS NULL`, f.agent.TenantID).Scan(&enabled, &revision)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	before, _ := json.Marshal(featureSnapshot{Key: "workspace-summary", Override: enabled, Revision: revision})
	payload, _ := json.Marshal(map[string]any{"kind": "feature", "key": "workspace-summary", "enabled": true, "expected_revision": revision})
	r, err := f.m.Create(t.Context(), f.agent, Create{Payload: payload, BeforeHash: Hash(before)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func input(r ApprovalRequest) Decide { return Decide{r.Digest, r.Revision} }
func (f *fixture) approve(t *testing.T, p tenant.Principal, r ApprovalRequest) ApprovalRequest {
	t.Helper()
	out, err := f.m.decide(t.Context(), p, r.ID, input(r), "approve", func(pgx.Tx, ApprovalRequest) (string, time.Time, error) { return "passkey_platform", f.now, nil })
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func requireStatus(t *testing.T, err error, want int) {
	t.Helper()
	var p *problem
	if !errors.As(err, &p) || p.status != want {
		t.Fatalf("want HTTP %d, got %v", want, err)
	}
}

// Risk: the protected mutation, first outcome and actor/method audit diverge.
func TestStepupTransitionsAndAtomicAudit(t *testing.T) {
	for _, outcome := range []string{"applied", "declined", "withdrawn", "expired", "stale", "failed"} {
		t.Run(outcome, func(t *testing.T) {
			f := setup(t)
			r := f.create(t)
			if r.State != "pending" || r.Revision != 1 || r.ExpiresAt.Sub(r.CreatedAt) != RequestLifetime {
				t.Fatal("invalid initial state")
			}
			var out ApprovalRequest
			var err error
			switch outcome {
			case "declined":
				out, err = f.m.Decide(t.Context(), f.person, r.ID, input(r), "decline", nil)
			case "withdrawn":
				out, err = f.m.Decide(t.Context(), f.agent, r.ID, input(r), "withdraw", nil)
			case "expired":
				f.now = r.ExpiresAt
				out = f.approve(t, f.person, r)
			case "stale":
				if _, err = f.d.Admin.Exec(t.Context(), `INSERT INTO features(tenant_id,key,enabled)VALUES($1,'workspace-summary',false)`, f.agent.TenantID); err != nil {
					t.Fatal(err)
				}
				out = f.approve(t, f.person, r)
			case "failed":
				f.m.targets["feature"] = failingTarget{}
				out = f.approve(t, f.person, r)
			default:
				out = f.approve(t, f.person, r)
			}
			if err != nil || out.State != outcome || out.Revision != 2 {
				t.Fatalf("outcome %+v: %v", out, err)
			}
			var count int
			var enabled *bool
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM features WHERE tenant_id=$1 AND enabled`, f.agent.TenantID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if outcome == "applied" && count != 1 || outcome != "applied" && count != 0 {
				t.Fatal("wrong target mutation")
			}
			if outcome == "stale" {
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT enabled FROM features WHERE tenant_id=$1`, f.agent.TenantID).Scan(&enabled); err != nil || enabled == nil || *enabled {
					t.Fatal("stale target changed")
				}
			}
			settled := f.approve(t, f.other, r)
			if settled.State != outcome || settled.Revision != 2 || nullableValue(settled.DecidedBy) != nullableValue(out.DecidedBy) {
				t.Fatal("later approver overwrote first decision")
			}
			var raw []byte
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT after FROM events WHERE type=$1`, "stepup."+outcome).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if json.Unmarshal(raw, &event) != nil || event["request_digest"] != r.Digest || event["requested_by"] != f.agent.ID || event["outcome"] != outcome {
				t.Fatalf("wrong audit %s", raw)
			}
			if outcome == "applied" || outcome == "stale" || outcome == "failed" {
				if event["method"] != "passkey_platform" || event["auth_time"] == nil || event["approved_by"] != f.person.ID {
					t.Fatalf("missing method/actor %s", raw)
				}
			}
			if strings.Contains(string(raw), "credential") || strings.Contains(string(raw), "token") {
				t.Fatal("proof material in audit")
			}
			var body, recipient string
			var unread bool
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT body,recipient_principal_id::text,acked_at IS NULL FROM inbox_messages WHERE id=$1`, r.ID).Scan(&body, &recipient, &unread); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"applied": "Approved by Markus with device passkey · applied", "declined": "Declined by Markus", "withdrawn": "Withdrawn", "expired": "Expired after 15 min · ask again", "stale": "Not applied · changed meanwhile", "failed": "Not applied · the change could not be saved"}[outcome]
			if body != want || recipient != f.agent.ID || !unread || event["result_line"] != body {
				t.Fatalf("wrong result delivery %q %s %t", body, recipient, unread)
			}
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages WHERE id=$1`, r.ID).Scan(&count); err != nil || count != 1 {
				t.Fatal("later decision duplicated result")
			}
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_permission_grants`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("created authority %d %v", count, err)
			}
		})
	}
}

// Risk: a workspace request's result leaks into a different session/project,
// or a result write failure leaves an applied target without an agent result.
func TestStepupSessionResultIsAtomicAndBound(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact session", true: "rollback"}[reject], func(t *testing.T) {
			f := setup(t)
			var project, session string
			if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'RESULT-1','Result project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.agent.TenantID).Scan(&project); err != nil {
				t.Fatal(err)
			}
			if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','fixture','unmanaged','worker',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex')) RETURNING id::text`, f.agent.TenantID, project, f.agent.ID).Scan(&session); err != nil {
				t.Fatal(err)
			}
			before := json.RawMessage(`{"key":"workspace-summary","project_id":null,"override":null,"revision":0}`)
			r, err := f.m.Create(t.Context(), f.agent, Create{Payload: json.RawMessage(`{"kind":"feature","key":"workspace-summary","enabled":true,"expected_revision":0}`), BeforeHash: Hash(before), SessionID: session})
			if err != nil {
				t.Fatal(err)
			}
			if reject {
				if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_stepup_result() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE 'stepup-result/%' THEN RAISE EXCEPTION 'fixture result rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_stepup_result BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION reject_stepup_result()`); err != nil {
					t.Fatal(err)
				}
			} else {
				// First-use System creation must not acquire the audit counter
				// before the inbox/FK writes. Inspect actual PostgreSQL locks,
				// including those acquired inside SQL functions and triggers.
				// Preserve the seeded actor and its audit, but force first use of
				// the named System sender in this decision transaction.
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE principals SET name='Existing System fixture' WHERE tenant_id=$1 AND name='System' AND roles @> ARRAY['system']::text[]`, f.agent.TenantID); err != nil {
					t.Fatal(err)
				}
				var systemCount int
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM principals WHERE tenant_id=$1 AND name='System' AND roles @> ARRAY['system']::text[]`, f.agent.TenantID).Scan(&systemCount); err != nil || systemCount != 0 {
					t.Fatalf("fixture must exercise first-use System creation: %d %v", systemCount, err)
				}
				if _, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION guard_stepup_result_lock_order() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM pg_locks WHERE pid=pg_backend_pid() AND relation='event_counters'::regclass AND mode='RowExclusiveLock') THEN RAISE EXCEPTION 'fixture event counter before result rows'; END IF; RETURN NEW; END $$; CREATE TRIGGER guard_stepup_result_lock_order BEFORE INSERT ON inbox_messages FOR EACH ROW EXECUTE FUNCTION guard_stepup_result_lock_order()`); err != nil {
					t.Fatal(err)
				}
			}
			out, err := f.m.decide(t.Context(), f.person, r.ID, input(r), "approve", func(pgx.Tx, ApprovalRequest) (string, time.Time, error) { return "oidc_reauth", f.now, nil })
			if reject {
				if err == nil || !strings.Contains(err.Error(), "fixture result rejection") {
					t.Fatalf("wrong failure: %v", err)
				}
				var state string
				var applied, messages, audits int
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT state,(SELECT count(*) FROM features WHERE enabled),(SELECT count(*) FROM inbox_messages),(SELECT count(*) FROM events WHERE type='stepup.applied') FROM stepup_requests WHERE id=$1`, r.ID).Scan(&state, &applied, &messages, &audits); err != nil || state != "pending" || applied != 0 || messages != 0 || audits != 0 {
					t.Fatal("partial apply committed after result failure", err)
				}
				return
			}
			if err != nil || out.State != "applied" {
				t.Fatal(err)
			}
			var gotProject, gotSession, recipient, body string
			var eventID int64
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT project_id::text,recipient_session_id::text,recipient_principal_id::text,body,sent_event_id FROM inbox_compat_messages WHERE id=$1`, r.ID).Scan(&gotProject, &gotSession, &recipient, &body, &eventID); err != nil || gotProject != project || gotSession != session || recipient != f.agent.ID || body != "Approved by Markus with fresh sign-in · applied" || eventID <= 0 {
				t.Fatalf("result not bound to workspace request's own chat: %s %s %s %q %v", gotProject, gotSession, recipient, body, err)
			}
		})
	}
}

type failingTarget struct{ FeatureTarget }

func (failingTarget) Apply(ctx context.Context, tx pgx.Tx, p tenant.Principal, payload, after json.RawMessage) error {
	if err := (FeatureTarget{}).Apply(ctx, tx, p, payload, after); err != nil {
		return err
	}
	return errors.New("injected apply failure after mutation")
}

// Risk: people or other agents create/withdraw requests; agents decide even
// with owner-workstation/full-access flags; lost authority is trusted.
func TestStepupOwnershipBoundsAndCurrentAuthority(t *testing.T) {
	f := setup(t)
	r := f.create(t)
	agent := f.agent
	agent.FullAccess = true
	agent.OwnerWorkstation = true
	agent.KeyCreatorID = f.person.ID
	for _, action := range []string{"approve", "decline"} {
		_, err := f.m.Decide(t.Context(), agent, r.ID, input(r), action, nil)
		if !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("agent %s: %v", action, err)
		}
	}
	_, err := f.m.Decide(t.Context(), f.otherAgent, r.ID, input(r), "withdraw", nil)
	requireStatus(t, err, 404)
	_, err = f.m.Create(t.Context(), f.person, Create{Payload: r.Payload, BeforeHash: r.BeforeHash})
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("person created agent request")
	}
	_, err = f.m.Create(t.Context(), f.agent, Create{Payload: r.Payload, BeforeHash: strings.Repeat("0", 64)})
	requireStatus(t, err, 409)
	_, err = f.m.Create(t.Context(), f.agent, Create{Payload: json.RawMessage(`{"kind":"sql","query":"UPDATE tenants"}`), BeforeHash: r.BeforeHash})
	requireStatus(t, err, 400)
	_, err = f.m.Create(t.Context(), f.agent, Create{Payload: json.RawMessage(`{"kind":"feature","padding":"` + strings.Repeat("x", payloadLimit) + `"}`), BeforeHash: r.BeforeHash})
	requireStatus(t, err, 400)
	var project, session string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PR-1','Session project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.agent.TenantID).Scan(&project); err != nil {
		t.Fatal(err)
	}
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,'codex','fixture','unmanaged','worker',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex')) RETURNING id::text`, f.agent.TenantID, project, f.agent.ID).Scan(&session); err != nil {
		t.Fatal(err)
	}
	linked, err := f.m.Create(t.Context(), f.agent, Create{Payload: r.Payload, BeforeHash: r.BeforeHash, SessionID: session})
	if err != nil || linked.SessionID == nil || *linked.SessionID != session || linked.ProjectID != nil {
		t.Fatalf("workspace request lost its own project session: %v", err)
	}
	_, err = f.m.Create(t.Context(), f.otherAgent, Create{Payload: r.Payload, BeforeHash: r.BeforeHash, SessionID: session})
	requireStatus(t, err, 403)
	wrong := input(r)
	wrong.Digest = strings.Repeat("0", 64)
	_, err = f.m.Decide(t.Context(), f.person, r.ID, wrong, "decline", nil)
	requireStatus(t, err, 409)
	dbtest.BindRole(t, f.d, f.person.TenantID, f.person.ID, "member")
	_, err = f.m.Decide(t.Context(), f.person, r.ID, input(r), "decline", nil)
	if !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("lost permission: %v", err)
	}
	out, err := f.m.Get(t.Context(), f.agent, r.ID)
	if err != nil || out.State != "pending" {
		t.Fatal("denials mutated request")
	}
}

// Risk: two reviewers overlap, then both mutate or overwrite the first audit.
func TestStepupFirstDecisionWinsWithTwoApprovers(t *testing.T) {
	f := setup(t)
	r := f.create(t)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return sql == db.TenantFenceSQL })
	first := New(pool, nil, "")
	first.RecordResultTx = inbox.RecordResult
	first.now = f.m.now
	type result struct {
		r   ApprovalRequest
		err error
	}
	done := make(chan result, 2)
	run := func(m *Module, p tenant.Principal) {
		out, err := m.decide(ctx, p, r.ID, input(r), "approve", func(pgx.Tx, ApprovalRequest) (string, time.Time, error) { return "passkey", f.now, nil })
		done <- result{out, err}
	}
	go run(first, f.person)
	holder := barrier.Wait(t, ctx)
	go run(f.m, f.other)
	dbtest.WaitForLock(t, ctx, f.d, holder, "transactionid")
	barrier.Release()
	a, b := dbtest.Await(t, ctx, done), dbtest.Await(t, ctx, done)
	if a.err != nil || b.err != nil || a.r.State != "applied" || b.r.State != "applied" || nullableValue(a.r.DecidedBy) != f.person.ID || nullableValue(b.r.DecidedBy) != f.person.ID {
		t.Fatalf("competing results %+v %+v", a, b)
	}
	var n int
	if err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE type='stepup.applied'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("first decision audit %d %v", n, err)
	}
}

// Risk: a role revoked while an approval waits is trusted in the final write.
func TestStepupApprovalRechecksAuthorityAfterFenceWait(t *testing.T) {
	f := setup(t)
	r := f.create(t)
	pool, barrier, ctx := dbtest.BarrierPool(t, f.d.App, func(sql string) bool { return strings.HasPrefix(sql, "UPDATE role_bindings") })
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(dbtest.Seed(ctx), pool, f.person.TenantID, func(tx pgx.Tx) error {
			if err := db.LockTenant(ctx, tx, f.person.TenantID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='member') WHERE tenant_id=$1 AND principal_id=$2`, f.person.TenantID, f.person.ID)
			return err
		})
	}()
	holder := barrier.Wait(t, ctx)
	approved := make(chan error, 1)
	go func() {
		_, err := f.m.decide(ctx, f.person, r.ID, input(r), "approve", func(pgx.Tx, ApprovalRequest) (string, time.Time, error) { return "passkey", f.now, nil })
		approved <- err
	}()
	dbtest.WaitForLock(t, ctx, f.d, holder, "transactionid")
	barrier.Release()
	if err := dbtest.Await(t, ctx, done); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, approved); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revoked authority applied: %v", err)
	}
	out, err := f.m.Get(t.Context(), f.agent, r.ID)
	if err != nil || out.State != "pending" {
		t.Fatal("revoked decision changed target")
	}
}
func TestStepupListKeysetsExpiryAndTenantIsolation(t *testing.T) {
	f := setup(t)
	r := f.create(t)
	f.now = f.now.Add(time.Second)
	second := f.create(t)
	page, err := f.m.List(t.Context(), f.person, "pending", 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != r.ID || !page.HasMore {
		t.Fatalf("page %+v %v", page, err)
	}
	next, err := f.m.List(t.Context(), f.person, "pending", 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != second.ID {
		t.Fatalf("next %+v %v", next, err)
	}
	_, err = f.m.List(t.Context(), f.other, "pending", 1, page.NextCursor)
	requireStatus(t, err, 400)
	page, err = f.m.List(t.Context(), f.otherAgent, "pending", 10, "")
	if err != nil || len(page.Items) != 0 {
		t.Fatal("other agent saw requests")
	}
	f.now = second.ExpiresAt
	page, err = f.m.List(t.Context(), f.person, "decided", 1, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].State != "expired" || !page.HasMore {
		t.Fatalf("expired page %+v %v", page, err)
	}
	next, err = f.m.List(t.Context(), f.person, "decided", 1, page.NextCursor)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != r.ID {
		t.Fatalf("expiry cursor lost row %+v %v", next, err)
	}
	var foreign string
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name)VALUES('foreign','Foreign')RETURNING id::text`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(t.Context(), f.d.App, foreign, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM stepup_requests`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Error("native rows crossed tenants")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
