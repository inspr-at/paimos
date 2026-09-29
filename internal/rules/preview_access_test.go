// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestNamedAgentPreviewReasonsStayBehindTheActiveKeyRule(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('preview-rules','Preview rules') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	person := func(name, role string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: name}
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, tid, name).Scan(&p.ID)
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
		return p
	}
	owner := person("Ada Owner", "owner")
	member := person("Cam Member", "member")
	var project string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PV-1','Preview' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project)
	}); err != nil {
		t.Fatal(err)
	}
	agent := func(name string) string {
		t.Helper()
		var id string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent',$2) RETURNING id::text`, tid, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	insertKey := func(principal, creator, prefix string, revoke, expire bool) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id,revoked_at,expires_at) VALUES($1,$2,'preview',$3,'fixture-not-a-credential',$4,CASE WHEN $5 THEN clock_timestamp() - interval '1 hour' END,CASE WHEN $6 THEN clock_timestamp() - interval '1 hour' END)`, tid, principal, prefix, creator, revoke, expire); err != nil {
			t.Fatal(err)
		}
	}
	held := agent("Held")
	foreign := agent("Foreign")
	revoked := agent("Revoked")
	expired := agent("Expired")
	both := agent("Both")
	quiet := agent("Quiet")
	insertKey(held, member.ID, "rules-preview-held", false, false)
	insertKey(held, member.ID, "rules-preview-held-old", true, false)
	insertKey(foreign, owner.ID, "rules-preview-foreign", false, false)
	insertKey(revoked, member.ID, "rules-preview-revoked", true, false)
	insertKey(expired, member.ID, "rules-preview-expired", false, true)
	insertKey(both, member.ID, "rules-preview-both", true, true)
	insertKey(quiet, member.ID, "rules-preview-quiet", false, false)
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, quiet); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(who tenant.Principal, method, path string, in any) (int, string, string) {
		t.Helper()
		body := ""
		if in != nil {
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			body = string(raw)
		}
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var parsed struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
		return rec.Code, parsed.Code, rec.Body.String()
	}
	merge := func(who tenant.Principal, agentID string) (int, string, string) {
		t.Helper()
		path := "/api/rules/merged?project_id=" + project + "&person_id=" + who.ID + "&role=builder&harness=codex"
		if agentID != "" {
			path += "&agent_id=" + agentID
		}
		return call(who, http.MethodGet, path, nil)
	}
	// A current key still previews. There is no company floor, so the merge is refused after authorization.
	if status, code, body := merge(member, held); status != 409 || code != "floor_missing" {
		t.Fatalf("own active key: %d %s %s", status, code, body)
	}
	if status, code, body := merge(owner, foreign); status != 409 || code != "floor_missing" {
		t.Fatalf("creator still previews: %d %s %s", status, code, body)
	}
	denied := func(who tenant.Principal, agentID, want string) {
		t.Helper()
		status, code, body := merge(who, agentID)
		if status != 403 || code != want || !strings.Contains(body, authz.PreviewDenialMessage(want)) {
			t.Fatalf("%s: %d %s %s", want, status, code, body)
		}
		if strings.Contains(body, owner.Name) || strings.Contains(body, member.Name) {
			t.Fatalf("denial named a person: %s", body)
		}
	}
	denied(member, foreign, authz.PreviewNotKeyCreator)
	denied(owner, held, authz.PreviewNotKeyCreator)
	denied(member, revoked, authz.PreviewKeyRevoked)
	denied(member, expired, authz.PreviewKeyExpired)
	denied(member, both, authz.PreviewKeyRevoked)
	denied(member, quiet, authz.PreviewAgentInactive)
	if status, code, body := merge(member, "99999999-9999-4999-8999-999999999999"); status != 403 || code != "forbidden" {
		t.Fatalf("missing agent stays a generic denial: %d %s %s", status, code, body)
	}
	if status, code, body := call(member, http.MethodPost, "/api/rules/layers", Scope{Layer: "agent", OwnerID: member.ID, AgentID: held}); status != 200 {
		t.Fatalf("own named layer: %d %s %s", status, code, body)
	}
	if status, code, body := call(member, http.MethodPost, "/api/rules/layers", Scope{Layer: "agent", OwnerID: member.ID, AgentID: foreign}); status != 403 || code != authz.PreviewNotKeyCreator {
		t.Fatalf("foreign named layer: %d %s %s", status, code, body)
	}
	if status, code, body := merge(member, ""); status != 409 || code != "floor_missing" {
		t.Fatalf("role preview without a named agent: %d %s %s", status, code, body)
	}
}
