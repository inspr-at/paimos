// SPDX-License-Identifier: AGPL-3.0-only

package receipts_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions/consent"
	"github.com/inspr-at/paimos/internal/nativesessions/receipts"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: a receipt becomes its own authority, a paired key lacks exact private
// runtime proof, or a withdrawn retention decision survives until final drain.
// Uses production authorities and actual person approval decisions throughout.
func TestProductionReceiptAuthorityRechecksConsentAndExactEvidence(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	tid := f.owner.TenantID
	dbtest.BindRole(t, f.d, tid, f.owner.ID, "admin")
	profile, request, proposer := uuid.NewString(), uuid.NewString(), uuid.NewString()
	ledger := f.actor.LedgerGeneration
	if err := db.InTenant(dbtest.Seed(ctx), f.d.App, tid, func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','native proposer')`, []any{tid, proposer}},
			{`INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'receipt-native','1','claude','anthropic','fixture-model','high','strong')`, []any{tid, profile}},
			{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by) VALUES($1,$2,'246813579',$3,$3,$3,'{}','receipt-native','redeemed',$4)`, []any{tid, request, strings.Repeat("a", 64), f.owner.ID}},
			{`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'native worker','native_worker',$3,$4,$5) RETURNING id::text`, []any{tid, f.agent.ID, strings.Repeat("b", 64), f.agent.Scopes, f.owner.ID}},
		} {
			if strings.Contains(q.sql, "RETURNING") {
				if err := tx.QueryRow(ctx, q.sql, q.args...).Scan(&f.agent.KeyID); err != nil {
					return err
				}
			} else if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		f.agent.AuthKeyID, f.agent.KeyCreatorID = f.agent.KeyID, f.owner.ID
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,ledger_generation) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, []any{tid, f.actor.ComputerID, request, f.agent.ID, f.agent.KeyID, f.actor.DaemonID, strings.Repeat("a", 64), ledger}},
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at,link_revision,last_probe_at,last_probe_ok,last_daemon_generation) VALUES($1,$2,'receipt-native','claude',$3,$4,'Native',$5,$6,1,$6,true,$7)`, []any{tid, "placeholder", f.actor.DaemonID, f.agent.ID, f.owner.ID, f.now, f.actor.DaemonGeneration}},
		} {
			if q.args[1] == "placeholder" {
				q.args[1] = uuid.NewString()
			}
			if _, err := tx.Exec(ctx, q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var account string
	if err := f.d.Admin.QueryRow(ctx, `SELECT id::text FROM agent_accounts WHERE tenant_id=$1 AND account_key='receipt-native'`, tid).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,ongoing_approved_at,verification_expires_at) VALUES($1,$2,$3,$4,$5,$6::timestamptz,$6::timestamptz+interval '1 hour')`, tid, account, f.actor.ComputerID, request, profile, f.now); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.d, tid, proposer, "member")
	// The proposer has control; the paired runtime keeps its issued worker-only
	// ceiling and never borrows the original person's control/chat permissions.
	proposerPrincipal := tenant.Principal{TenantID: tid, ID: proposer, Kind: tenant.Agent, Scopes: []string{"harness.control", "harness.read", "approvals.read"}}
	// lookupKeyScopes hashes just the opaque secret following the final '_'.
	// Reuse the existing helper's token format with a known test-only value.
	keyToken := "aeon_nativeproposer_" + strings.Repeat("c", 64)
	keyHash := sha256.Sum256([]byte(strings.Repeat("c", 64)))
	if _, err := f.d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'native proposer','nativeproposer',$3,$4,$5)`, tid, proposer, hex.EncodeToString(keyHash[:]), proposerPrincipal.Scopes, f.owner.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	approvals.New(f.d.App).Mount(mux)
	call := func(p tenant.Principal, path string, body any) approvals.Approval {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw)).WithContext(tenant.WithPrincipal(ctx, p))
		r.Header.Set("Content-Type", "application/json")
		if p.Kind == tenant.Agent {
			r.Header.Set("Authorization", "Bearer "+keyToken)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("approval %s: %d %s", path, w.Code, w.Body.String())
		}
		var out approvals.Approval
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	facts := consent.Facts{ComputerID: f.actor.ComputerID, AccountID: account, WorkspaceID: "native", WorkspaceRoot: "/native/approved", ModelProfileID: profile, ToolSet: []string{}, SettingsProfile: consent.SettingsProfile, LocalHistory: "vendor_native", RetainFinals: true, Revision: 1, MaximumRuntimeSeconds: 3600, MaximumTurns: 100, MaximumTokens: 10000}
	approve := func(scope string) string {
		t.Helper()
		a := call(proposerPrincipal, "/api/approvals", map[string]any{"scope": scope, "resource_kind": "node", "resource_id": f.binding.ProjectID, "rationale": "Native consent", "expires_at": f.now.Add(time.Hour), "native_consent": facts})
		call(f.owner, "/api/approvals/"+a.ID+"/decision", map[string]string{"decision": "approved"})
		return a.ID
	}
	grant, retention := approve(consent.SessionScope), approve(consent.RetentionScope)
	current := consent.Authority{Now: func() time.Time { return f.now }, Capabilities: nativecontract.Capabilities{Steer: "unsupported", Quota: "unsupported", QualificationDigest: f.actor.CapabilityDigest}}
	a := receipts.CurrentAuthority{Consent: current}
	intent := nativecontract.StartIntent{RequestID: uuid.NewString(), Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: f.actor.ComputerID, AccountID: account, WorkspaceID: facts.WorkspaceID, ModelProfileID: profile, PolicyID: grant}
	original := nativecontract.Actor{TenantID: tid, PrincipalID: f.owner.ID, PrincipalKind: "person", OwnerPersonID: f.owner.ID, GrantID: grant, GrantRevision: 1}
	personCtx := tenant.WithPrincipal(ctx, f.owner)
	if err := db.InTenant(personCtx, f.d.App, tid, func(tx pgx.Tx) error {
		if err := db.LockCurrentTree(ctx, tx); err != nil {
			return err
		}
		authorized, err := current.AuthorizeTx(ctx, tx, original, f.binding.ProjectID, intent)
		if err != nil {
			return err
		}
		view := nativecontract.SessionView{OwnedSessionID: f.binding.OwnedSessionID, CurrentBinding: &f.binding, OwnerPersonID: f.owner.ID, Role: "coordinator", Profile: "native", Harness: "claude", State: "running", Mode: "structured", Capabilities: current.Capabilities, Revision: 1, Generation: 1, ConversationRef: f.authority.final.ConversationRef}
		actorRaw, _ := json.Marshal(map[string]any{"principal": f.owner, "key_id": "", "auth_key_id": "", "actor_owner_id": f.owner.ID, "grant_id": grant, "grant_revision": 1})
		intentRaw, _ := json.Marshal(intent)
		policyRaw, _ := json.Marshal(authorized.Policy)
		launchRaw, _ := json.Marshal(authorized.Launch)
		viewRaw, _ := json.Marshal(view)
		if _, err = tx.Exec(ctx, `INSERT INTO native_session_bindings(tenant_id,id,project_id,owner_person_id,actor_principal_id,role,harness,account_id,computer_id,paired_principal_id,workspace_id,model_profile_id,policy_id,daemon_id,current_harness_session_id,runtime_epoch,daemon_generation,ledger_generation,binding_revision,view,created_at) VALUES($1,$2,$3,$4,$4,'coordinator','claude',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,1,$16,$17)`, tid, f.binding.OwnedSessionID, f.binding.ProjectID, f.owner.ID, account, f.actor.ComputerID, f.agent.ID, facts.WorkspaceID, profile, grant, f.binding.DaemonID, f.binding.HarnessSessionID, f.binding.RuntimeEpoch, f.binding.DaemonGeneration, ledger, viewRaw, f.now); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO native_session_intents(tenant_id,id,request_id,owned_session_id,project_id,owner_person_id,actor_principal_id,actor_kind,actor_authority,actor_grant_id,actor_grant_revision,intent,policy,launch_config,created_at) VALUES($1,$2,$3,$4,$5,$6,$6,'person',$7,$8,1,$9,$10,$11,$12)`, tid, uuid.NewString(), intent.RequestID, f.binding.OwnedSessionID, f.binding.ProjectID, f.owner.ID, actorRaw, grant, intentRaw, policyRaw, launchRaw, f.now); err != nil {
			return err
		}
		f.authority.intent.ProcessIdentityDigest = strings.Repeat("d", 64)
		if err = a.RegisterOperationTx(personCtx, tx, f.authority.intent); err != nil {
			return err
		}
		drain, err := a.IssueDrainTx(personCtx, tx, f.binding, retention, f.authority.final.Target.ConversationID, f.authority.final.Target.RoleID)
		if err == nil {
			f.final.DrainGrantID = drain.GrantID
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	f.repo, err = receipts.New(f.d.App, a, f.chat, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	proof := receipts.RuntimeProof{WorkerLease: "native-receipts-fixture-worker-lease", Identity: nativecontract.NativeIdentity{RuntimeEpoch: f.binding.RuntimeEpoch, ProcessIdentityDigest: f.authority.intent.ProcessIdentityDigest, PID: 1234, StartedAt: f.now}}
	report := func(r nativecontract.Receipt) error {
		sequence, _ := strconv.ParseUint(r.SourceSequence, 10, 64)
		proof.Evidence = nativecontract.NativeEvidence{Stage: r.State, RequestID: r.RequestID, InputID: f.final.InputID, SourceSequence: sequence, ObservedAt: f.now}
		if r.TurnRef != nil {
			proof.Evidence.TurnRef = *r.TurnRef
		}
		_, err := f.repo.Report(receipts.WithRuntimeProof(tenant.WithPrincipal(ctx, f.agent), proof), f.actor, r)
		return err
	}
	r := f.authority.intent.Receipt
	_, err = f.repo.Report(tenant.WithPrincipal(ctx, f.agent), f.actor, r)
	reason(t, err, nativecontract.ReasonPermissionRevoked)
	for i, state := range []string{"accepted", "write_started", "write_acknowledged", "turn_started", "turn_completed"} {
		r.State = state
		if i >= 2 {
			r.SourceSequence = fmt.Sprint(i - 1)
		}
		if i >= 3 {
			r.TurnRef = &f.final.TurnRef
		}
		if err = report(r); err != nil {
			t.Fatalf("production report %s: %v", state, err)
		}
	}
	finalCtx := receipts.WithRuntimeProof(tenant.WithPrincipal(ctx, f.agent), proof)
	stored, err := f.repo.Persist(finalCtx, f.actor, f.final)
	if err != nil || stored.State != "persisted" {
		t.Fatalf("production final: %+v %v", stored, err)
	}
	call(f.owner, "/api/approvals/"+retention+"/revoke", nil)
	withheld := f.final
	withheld.FinalID = uuid.NewString()
	rejected, err := f.repo.Persist(finalCtx, f.actor, withheld)
	if err != nil || rejected.State != "rejected" || rejected.Reason == nil || *rejected.Reason != nativecontract.ReasonPermissionRevoked {
		t.Fatalf("withdrawn retention did not refuse final drain: %+v %v", rejected, err)
	}
}
