// SPDX-License-Identifier: AGPL-3.0-only
package phoneapprovals

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"net/http"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (f *fixture) project(t *testing.T, key string) string {
	t.Helper()
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.p.TenantID, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPhoneDecisionHidesUnavailableApprovals(t *testing.T) {
	f := fixtureFor(t)
	project, otherProject := f.project(t, "PHONE-1"), f.project(t, "PHONE-2")
	f.exec(t, `DELETE FROM role_bindings WHERE principal_id=$1`, f.other.ID)
	f.exec(t, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, f.other.TenantID, f.other.ID, project)
	ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), f.other), f.db.App)
	if err := authz.RequirePattern(ctx, "POST /api/phone-approvals/{kind}/{requestId}/decision", authz.Scope{AnyProject: true}); err != nil {
		t.Fatalf("project member cannot open phone decision route: %v", err)
	}
	requestFor := func(resourceID any, expired bool) string {
		var id string
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,resource_id,rationale,expires_at) VALUES($1,$2,$2,'nodes.read',CASE WHEN $3::uuid IS NULL THEN 'tenant' ELSE 'node' END,$3,'Phone fixture',CASE WHEN $4 THEN now()-interval '1 second' ELSE now()+interval '2 hours' END) RETURNING id::text`, f.p.TenantID, f.agent.ID, resourceID, expired).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	pending, foreign, expired, decided := f.request(t), requestFor(otherProject, false), requestFor(nil, true), f.request(t)
	f.exec(t, `INSERT INTO approval_decisions(tenant_id,request_id,decided_by_principal_id,decision,reason) VALUES($1,$2,$3,'denied','Fixture decision')`, f.p.TenantID, decided, f.p.ID)
	proof := DecisionProof{Decision: Decision{Decision: "approved", Hash: strings.Repeat("0", 64)}}
	for _, p := range []tenant.Principal{f.other, f.p} {
		t.Run(p.Name, func(t *testing.T) {
			baseline := f.call(t, p, "POST", "/api/phone-approvals/approval/malformed/decision", proof, testOrigin)
			status(t, baseline, http.StatusNotFound)
			cases := []struct{ name, id string }{
				{"missing", "00000000-0000-4000-8000-000000000001"},
				{"expired", expired},
				{"decided", decided},
			}
			if p.ID == f.other.ID {
				cases = append(cases, struct{ name, id string }{"another project", foreign}, struct{ name, id string }{"no decision permission", pending})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					w := f.call(t, p, "POST", "/api/phone-approvals/approval/"+tc.id+"/decision", proof, testOrigin)
					if w.Code != baseline.Code || w.Body.String() != baseline.Body.String() || w.Header().Get("Cache-Control") != "no-store" {
						t.Fatalf("unavailable request disclosed: status=%d body=%s", w.Code, w.Body.String())
					}
				})
			}
		})
	}
}

func TestPhoneDecisionHidesUnavailableAttach(t *testing.T) {
	f := fixtureFor(t)
	f.m.pairing = agentpairing.New(f.db.App, testOrigin, "phone-test", nil)
	project := f.project(t, "PHONE-3")
	var key, pairing, computer, id string
	queries := []struct {
		sql  string
		args []any
		id   *string
	}{
		{`INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash) VALUES($1,$2,'Phone fixture','phone-fixture','fixture') RETURNING id::text`, []any{f.p.TenantID, f.agent.ID}, &key},
		{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,approved_by,state) VALUES($1,gen_random_uuid(),'123456789',$2,$2,$2,'{}',$2,$3,'redeemed') RETURNING id::text`, []any{f.p.TenantID, strings.Repeat("0", 64), f.p.ID}, &pairing},
	}
	for _, q := range queries {
		if err := f.db.Admin.QueryRow(t.Context(), q.sql, q.args...).Scan(q.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash) VALUES($1,gen_random_uuid(),$2,$3,$4,'phone-fixture',$5) RETURNING id::text`, f.p.TenantID, pairing, f.agent.ID, key, strings.Repeat("0", 64)).Scan(&computer); err != nil {
		t.Fatal(err)
	}
	snapshot := attachwatch.Snapshot{ComputerID: computer, ProjectID: project, TicketID: project}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO harness_attach_requests(tenant_id,id,computer_id,owner_id,project_id,ticket_id,snapshot,digest,user_code) VALUES($1,gen_random_uuid(),$2,$3,$4,$4,$5,$6,'123456789') RETURNING id::text`, f.p.TenantID, computer, f.p.ID, project, mustJSON(t, snapshot), strings.Repeat("0", 64)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	ctx := authz.BindPool(tenant.WithPrincipal(t.Context(), f.other), f.db.App)
	if err := authz.Require(ctx, "account.manage", authz.Scope{}); err != nil {
		t.Fatalf("non-owner lacks account management: %v", err)
	}
	proof := DecisionProof{Decision: Decision{Decision: "approved", Hash: strings.Repeat("0", 64)}}
	baseline := f.call(t, f.other, "POST", "/api/phone-approvals/attach/malformed/decision", proof, testOrigin)
	status(t, baseline, http.StatusNotFound)
	for _, target := range []string{"00000000-0000-4000-8000-000000000001", id} {
		w := f.call(t, f.other, "POST", "/api/phone-approvals/attach/"+target+"/decision", proof, testOrigin)
		if w.Code != baseline.Code || w.Body.String() != baseline.Body.String() || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("unavailable attach disclosed: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

func TestPhonePushKeySeparatesQuoteEncryption(t *testing.T) {
	master := bytes.Repeat([]byte{0x42}, 32)
	m := New(nil, nil, testOrigin, master, nil)
	if m.vault == nil {
		t.Fatal("push vault disabled")
	}
	block, err := aes.NewCipher(master)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	// Force identical nonces and associated data: only separate keys protect
	// both domains when their independently generated nonces collide.
	nonce := make([]byte, m.vault.NonceSize())
	aad := []byte("same associated data")
	payload := []byte("subscription fixture")
	pushCiphertext := m.vault.Seal(nil, nonce, payload, aad)
	if _, err := quote.Open(nil, nonce, pushCiphertext, aad); err == nil {
		t.Error("quote key decrypted push subscription")
	}
	quoteCiphertext := quote.Seal(nil, nonce, []byte("quote fixture"), aad)
	if _, err := m.vault.Open(nil, nonce, quoteCiphertext, aad); err == nil {
		t.Error("push key decrypted quote capability")
	}
	// Reconstructing the module with the same provisioned key must still open
	// subscriptions encrypted by a previous process.
	restarted := New(nil, nil, testOrigin, master, nil)
	plain, err := restarted.vault.Open(nil, nonce, pushCiphertext, aad)
	if err != nil || !bytes.Equal(plain, payload) {
		t.Fatalf("push subscription cannot survive restart: %v", err)
	}
}
