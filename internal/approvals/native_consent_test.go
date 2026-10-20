// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Risk: cached native policy, another person's approval, or an independently
// revoked retention approval can survive until the final native mutation.
func TestNativeConsentRequiresCurrentPersonApproval(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	var now time.Time
	if err := f.db.Admin.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	project := insertNode(t, f.db.Admin, f.tenantA, "project", "NATIVE-1", "Native project")
	computer, account, profile, pairing := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	facts := consent.Facts{ComputerID: computer, AccountID: account, ModelProfileID: profile,
		WorkspaceID: "native-workspace", WorkspaceRoot: "/approved/native", ToolSet: []string{"aeon_read", "aeon_comment"},
		SettingsProfile: consent.SettingsProfile, LocalHistory: "vendor_native", RetainFinals: true, Revision: 1,
		MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	if err := facts.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		var pairKey string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM agent_keys WHERE tenant_id=$1 AND principal_id=$2 ORDER BY id LIMIT 1`, f.tenantA, f.agentA.ID).Scan(&pairKey); err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'native-consent','1','claude','anthropic','fixture-model','high','strong')`, []any{f.tenantA, profile}},
			{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'246813579',$3,$3,$3,'{}','native-consent','redeemed',$4)`, []any{f.tenantA, pairing, strings.Repeat("a", 64), f.personA.ID}},
			{`INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1,$2,$3,$4,$6,'consent-daemon',$5)`, []any{f.tenantA, computer, pairing, f.agentA.ID, strings.Repeat("a", 64), pairKey}},
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at,link_revision) VALUES($1,$2,'native-consent','claude','consent-daemon',$3,'Consent',$4,$5,1)`, []any{f.tenantA, account, f.agentA.ID, f.personA.ID, now}},
			{`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,ongoing_approved_at,verification_expires_at) VALUES($1,$2,$3,$4,$5,$6::timestamptz,$6::timestamptz+interval '1 hour')`, []any{f.tenantA, account, computer, pairing, profile, now}},
		} {
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := insertKey(t, f.db.Admin, f.agentA, []string{"harness.control", "approvals.read", "harness.read"}, false)
	propose := func(scope string) Approval {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"scope": scope, "resource_kind": "node", "resource_id": project,
			"rationale": "Request native work", "expires_at": now.Add(time.Hour), "native_consent": facts})
		w := f.do(f.agentA, key, "POST", "/api/approvals", string(raw))
		if w.Code != 201 {
			t.Fatalf("propose: %d %s", w.Code, w.Body.String())
		}
		var out Approval
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.Rationale, "Workspace root: "+facts.WorkspaceRoot) || !strings.Contains(out.Rationale, "Retain explicit finals in Aeon: true") || !strings.Contains(out.Rationale, "aeon_read") {
			t.Fatal("approval card did not receive consent facts")
		}
		return out
	}
	decide := func(a Approval, decision string) {
		t.Helper()
		w := f.do(f.personA, "", "POST", "/api/approvals/"+a.ID+"/decision", `{"decision":"`+decision+`"}`)
		if w.Code != 200 {
			t.Fatalf("decide: %d %s", w.Code, w.Body.String())
		}
	}
	countFacts := func(id string) int {
		t.Helper()
		var count int
		if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM native_session_consents WHERE grant_id=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	actor := nativecontract.Actor{TenantID: f.tenantA, PrincipalID: f.personA.ID, PrincipalKind: "person", OwnerPersonID: f.personA.ID}
	intent := nativecontract.StartIntent{RequestID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: computer, AccountID: account, ModelProfileID: profile, WorkspaceID: facts.WorkspaceID}
	authority := consent.Authority{Now: func() time.Time { return now }}
	read := func(a nativecontract.Actor, in nativecontract.StartIntent) (consent.Current, error) {
		var current consent.Current
		err := db.InTenant(tenant.WithPrincipal(ctx, f.personA), f.db.App, f.tenantA, func(tx pgx.Tx) error {
			if err := db.LockCurrentTree(ctx, tx); err != nil {
				return err
			}
			var err error
			current, err = authority.CurrentTx(ctx, tx, a, in, project, consent.SessionScope)
			return err
		})
		return current, err
	}
	wantReason := func(err error, reason nativecontract.Reason) {
		t.Helper()
		var refusal *nativecontract.ValidationError
		if !errors.As(err, &refusal) || refusal.Reason != reason {
			t.Fatalf("got %v, want %s", err, reason)
		}
	}

	pending := propose(consent.SessionScope)
	intent.PolicyID = pending.ID
	if countFacts(pending.ID) != 0 {
		t.Fatal("proposal created consent")
	}
	_, err := read(actor, intent)
	wantReason(err, nativecontract.ReasonPermissionRevoked)
	// The DB itself must reject an agent decision, independent of HTTP checks.
	err = db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision) VALUES($1,$2,$3,'approved')`, f.tenantA, pending.ID, f.agentA.ID)
		return err
	})
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "P0001" || !strings.Contains(pe.Message, "only a person") {
		t.Fatalf("DB agent decision: %v", err)
	}
	if w := f.do(f.agentA, key, "POST", "/api/approvals/"+pending.ID+"/decision", `{"decision":"approved"}`); w.Code != 403 {
		t.Fatalf("agent decision HTTP %d", w.Code)
	}
	decide(pending, "denied")
	if countFacts(pending.ID) != 0 {
		t.Fatal("denial created consent")
	}
	_, err = read(actor, intent)
	wantReason(err, nativecontract.ReasonPermissionRevoked)

	approved := propose(consent.SessionScope)
	decide(approved, "approved")
	if countFacts(approved.ID) != 1 {
		t.Fatal("approved decision did not create exactly one fact row")
	}
	var events int
	if err = f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='approval.approved' AND after->>'id'=$2`, f.tenantA, approved.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("approved consent events = %d", events)
	}
	intent.PolicyID = approved.ID
	current, err := read(actor, intent)
	if err != nil {
		t.Fatal(err)
	}
	actor.GrantID, actor.GrantRevision = approved.ID, current.Revision
	check := func(a nativecontract.Actor, in nativecontract.StartIntent, policy nativecontract.Policy) error {
		return db.InTenant(tenant.WithPrincipal(ctx, f.personA), f.db.App, f.tenantA, func(tx pgx.Tx) error {
			if err := db.LockCurrentTree(ctx, tx); err != nil {
				return err
			}
			return authority.RequireNativeGrantTx(ctx, tx, a, in, policy)
		})
	}
	if err = check(actor, intent, current.Policy); err != nil {
		t.Fatal(err)
	}
	changed := actor
	changed.GrantRevision++
	wantReason(check(changed, intent, current.Policy), nativecontract.ReasonPermissionRevoked)
	changed = actor
	changed.PrincipalID = f.agentB.ID
	wantReason(check(changed, intent, current.Policy), nativecontract.ReasonPermissionRevoked)
	otherComputer := intent
	otherComputer.ComputerID = uuid.NewString()
	wantReason(check(actor, otherComputer, current.Policy), nativecontract.ReasonPermissionRevoked)
	changedPolicy := current.Policy
	changedPolicy.ToolPolicyDigest = strings.Repeat("f", 64)
	wantReason(check(actor, intent, changedPolicy), nativecontract.ReasonPermissionRevoked)

	retention := propose(consent.RetentionScope)
	decide(retention, "approved")
	checkRetention := func() error {
		return db.InTenant(tenant.WithPrincipal(ctx, f.personA), f.db.App, f.tenantA, func(tx pgx.Tx) error {
			if err := db.LockCurrentTree(ctx, tx); err != nil {
				return err
			}
			_, err := authority.RequireRetentionTx(ctx, tx, actor, intent, current, retention.ID)
			return err
		})
	}
	if err = checkRetention(); err != nil {
		t.Fatal(err)
	}
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+retention.ID+"/revoke", ""); w.Code != 200 {
		t.Fatalf("retention revoke: %d %s", w.Code, w.Body.String())
	}
	wantReason(checkRetention(), nativecontract.ReasonPermissionRevoked)
	if err = check(actor, intent, current.Policy); err != nil {
		t.Fatalf("retention withdrawal changed session consent: %v", err)
	}
	if w := f.do(f.personA, "", "POST", "/api/approvals/"+approved.ID+"/revoke", ""); w.Code != 200 {
		t.Fatalf("revoke: %d %s", w.Code, w.Body.String())
	}
	wantReason(check(actor, intent, current.Policy), nativecontract.ReasonPermissionRevoked)
	if countFacts(approved.ID) != 1 {
		t.Fatal("revocation erased consent history")
	}
}
