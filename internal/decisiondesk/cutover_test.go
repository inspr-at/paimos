// SPDX-License-Identifier: AGPL-3.0-only
package decisiondesk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestCutoverLinksKeepSourceIdentity(t *testing.T) {
	t.Run("outward action reuses Desk phone and receipt", checkOutwardActionDesk)
	f := setup(t)
	q := f.ask(t, f.project, "carries_on", nil)
	a := f.approval(t, f.ticket, time.Now().Add(time.Hour))
	page, err := f.m.Read(t.Context(), f.person, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Counts.Open != 2 {
		t.Fatalf("source count: %+v", page)
	}
	want := map[string]string{q.ID: "q:", a: "a:"}
	for _, item := range page.Items {
		if item.Href != "/decision-desk?item="+want[item.ID]+item.ID || item.Source == "" {
			t.Fatalf("source identity/link lost: %+v", item)
		}
	}
}

// Risk: action requests could become a second screen or lose project-only
// decision/notification authority between Desk, phone and inbox integration.
func checkOutwardActionDesk(t *testing.T) {
	f := setup(t)
	approvals.New(f.d.App, inbox.RecordActionOutcomeTx).Mount(f.mux)
	inbox.New(f.d.App).Mount(f.mux)
	phoneapprovals.New(f.d.App, nil, "https://actions.example.test", nil, nil).Mount(f.mux)
	f.agent.Scopes = []string{"approvals.request", "approvals.read", "inbox.read"}
	const fixtureSecret = "action-fixture-only"
	sum := sha256.Sum256([]byte(fixtureSecret))
	f.exec(t, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','actionfixture',$3,$4,$5)`, f.agent.TenantID, f.agent.ID, hex.EncodeToString(sum[:]), f.agent.Scopes, f.person.ID)
	// Give the existing project reader the project lead's decision authority,
	// while deliberately retaining no workspace binding.
	f.exec(t, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key='admin') WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='project'`, f.person.TenantID, f.reader.ID)
	var now time.Time
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	call := func(p tenant.Principal, method, path string, body any, want int) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		if p.Kind == tenant.Agent {
			r.Header.Set("Authorization", "Bearer aeon_actionfixture_"+fixtureSecret)
		}
		w := httptest.NewRecorder()
		f.mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, w.Code, w.Body.String(), want)
		}
		return w.Body.Bytes()
	}
	in := approvals.ActionInput{IdempotencyKey: "desk-action", Kind: "external_message", What: "Send the agreed customer update", Why: "The project needs to communicate its recorded decision", ExactEffect: "Send the exact approved update to the existing project contact", Reversibility: "irreversible", HumanInput: "none", ExpiresAt: now.Add(time.Hour)}
	var action approvals.ActionRequest
	if err := json.Unmarshal(call(f.agent, "POST", "/api/projects/"+f.project+"/action-requests", in, 201), &action); err != nil {
		t.Fatal(err)
	}
	page, err := f.m.Read(t.Context(), f.reader, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if page.Counts.Open != 1 || page.Counts.Held != 1 || len(page.Items) != 1 {
		t.Fatalf("action did not reach existing Desk: %+v", page)
	}
	item := page.Items[0]
	if action.ApprovalID == nil || item.ID != *action.ApprovalID || item.Kind != "approval" || item.ProjectID != f.project || item.Title != "Outward action approval" || !item.CanDecide || item.Href != "/decision-desk?item=a:"+item.ID || !item.PushEligible(page.AsOf) {
		t.Fatalf("native Desk source or project authority lost: %+v", item)
	}
	if err := db.InTenant(tenant.WithPrincipal(t.Context(), f.reader), f.d.App, f.reader.TenantID, func(tx pgx.Tx) error {
		notices, err := NoticesTx(t.Context(), tx, f.reader, 10)
		if err == nil && (len(notices) != 1 || notices[0].ID != item.ID) {
			t.Fatalf("project-only lead missed notification: %+v", notices)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	phone := call(f.reader, "GET", "/api/phone-approvals/approval/"+item.ID, nil, http.StatusOK)
	if !strings.Contains(string(phone), action.RequestDigestSHA256) || !strings.Contains(string(phone), in.ExactEffect) || !strings.Contains(string(phone), `"pending":true`) {
		t.Fatalf("phone lost immutable action context: %s", phone)
	}
	call(f.reader, "POST", "/api/projects/"+f.project+"/action-approvals/"+item.ID+"/decision", map[string]string{"decision": "approved", "reason": "Exact wording and recipient checked"}, http.StatusOK)
	page, err = f.m.Read(t.Context(), f.reader, 100, nil)
	if err != nil || page.Counts.Open != 0 {
		t.Fatalf("resolved action stayed on Desk: %+v err=%v", page, err)
	}
	var resolved approvals.ActionRequest
	if err := json.Unmarshal(call(f.agent, "GET", "/api/projects/"+f.project+"/action-requests/"+action.ID, nil, http.StatusOK), &resolved); err != nil {
		t.Fatal(err)
	}
	if resolved.State != "approved" || resolved.OutcomeReceipt == nil || resolved.OutcomeReceipt.State != "queued" {
		t.Fatalf("agent receipt missing: %+v", resolved)
	}
	if data := call(f.agent, "GET", "/api/inbox/messages?wait_ms=0", nil, http.StatusOK); !strings.Contains(string(data), resolved.OutcomeReceipt.MessageID) || !strings.Contains(string(data), resolved.Reason) {
		t.Fatalf("agent inbox lacks the decision: %s", data)
	}
}

func TestTierCutoverUsesNativeRowsAndCurrentProjectPermissions(t *testing.T) {
	f := setup(t)
	makeRequest := func(project string) (string, string) {
		var session, request string
		if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,harness,host,management,role,capabilities,ref_digest,lease_digest)
		 VALUES($1,$2,$3,'codex','cutover-test','managed','worker',ARRAY['service_tier_v1'],gen_random_uuid()::text::bytea,gen_random_uuid()::text::bytea) RETURNING id::text`, f.person.TenantID, project, f.agent.ID).Scan(&session); err != nil {
			t.Fatal(err)
		}
		if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO harness_tier_requests(tenant_id,id,session_id,tier,reason,requested_by_principal_id)
		 VALUES($1,gen_random_uuid(),$2,'fast','Native protected request',$3) RETURNING id::text`, f.person.TenantID, session, f.agent.ID).Scan(&request); err != nil {
			t.Fatal(err)
		}
		return session, request
	}
	session, visible := makeRequest(f.project)
	_, hidden := makeRequest(f.otherProject)
	read := func(p tenant.Principal) Page {
		page, err := f.m.Read(t.Context(), p, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := read(f.person); page.Counts.Open != 2 {
		t.Fatalf("owner missing native requests: %+v", page)
	}
	page := read(f.reader)
	if page.Counts.Open != 1 || len(page.Items) != 1 || page.Items[0].ID != visible || page.Items[0].ID == hidden {
		t.Fatalf("mixed project access: %+v", page)
	}
	item := page.Items[0]
	if item.Kind != "tier_request" || item.Revision != 0 || item.Href != "/decision-desk?item=t:"+visible || !strings.Contains(item.Source, session) || item.PushEligible(time.Now()) {
		t.Fatalf("native protected pointer: %+v", item)
	}
	f.exec(t, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.reader.TenantID, f.reader.ID)
	if page := read(f.reader); page.Counts.Open != 0 {
		t.Fatalf("revoked access still counted: %+v", page)
	}
	f.exec(t, `UPDATE harness_tier_requests SET state='declined',decided_at=clock_timestamp(),decided_by_principal_id=$2 WHERE id=$1`, visible, f.person.ID)
	if page := read(f.person); page.Counts.Open != 1 || page.Items[0].ID != hidden {
		t.Fatalf("settled native row still counted: %+v", page)
	}
}
