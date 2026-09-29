// SPDX-License-Identifier: AGPL-3.0-only

package rules

import (
	"encoding/json"
	"errors"
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
	if status, code, body := call(member, http.MethodPost, "/api/rules/layers", Scope{Layer: "agent", OwnerID: member.ID, AgentID: foreign}); status != 403 || code != "forbidden" || strings.Contains(body, authz.PreviewNotKeyCreator) {
		t.Fatalf("creating a layer stays a generic denial: %d %s %s", status, code, body)
	}
	if status, code, body := call(member, http.MethodPost, "/api/rules/layers", Scope{Layer: "agent", OwnerID: member.ID, AgentID: revoked}); status != 403 || code != "forbidden" || strings.Contains(body, "revoked") {
		t.Fatalf("creating a layer hides a revoked key: %d %s %s", status, code, body)
	}
	if status, code, body := merge(member, ""); status != 409 || code != "floor_missing" {
		t.Fatalf("role preview without a named agent: %d %s %s", status, code, body)
	}
}

func TestAgentControlDenialStaysForbiddenUntilPreview(t *testing.T) {
	err := &agentControlDenial{reason: authz.PreviewKeyRevoked}
	if !isDenied(err) || !errors.Is(err, authz.ErrForbidden) {
		t.Fatal("a named-agent reason must still be a skippable denial")
	}
	var leaked *Error
	if errors.As(err, &leaked) {
		t.Fatal("the denial itself must not be a response body")
	}
	surfaced := surfacePreviewReason(err)
	if !errors.As(surfaced, &leaked) || leaked.Status != 403 || leaked.Code != authz.PreviewKeyRevoked {
		t.Fatalf("preview: %#v", surfaced)
	}
	if other := surfacePreviewReason(authz.ErrForbidden); !errors.Is(other, authz.ErrForbidden) {
		t.Fatal(other)
	}
}

func TestProjectOnlyReaderCannotTellAgentsApart(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('preview-visibility','Preview visibility') RETURNING id::text`).Scan(&tid)
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
		if role != "" {
			dbtest.BindRole(t, d, tid, p.ID, role)
		}
		return p
	}
	owner := person("Ada Owner", "owner")
	reader := person("Rae Reader", "")
	maker := person("Mae Maker", "")
	var project, roleID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'PV-2','Preview' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, tid).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'project_rules','Project rules') RETURNING id::text`, tid).Scan(&roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'rules.read'),($1,$2,'nodes.read')`, tid, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4),($1,$5,$3,'project',$4)`, tid, reader.ID, roleID, project, maker.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	agent := func(name, status string) string {
		t.Helper()
		var id string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,status) VALUES($1,'agent',$2,$3) RETURNING id::text`, tid, name, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	existing := agent("Existing", "active")
	inactive := agent("Inactive", "deactivated")
	own := agent("Own", "active")
	missing := "99999999-9999-4999-8999-999999999999"
	if _, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id,revoked_at) VALUES($1,$2,'preview','rules-visibility-own','fixture-not-a-credential',$3,clock_timestamp())`, tid, own, maker.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	merge := func(who tenant.Principal, agentID string) (int, string, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/rules/merged?project_id="+project+"&person_id="+who.ID+"&agent_id="+agentID+"&role=builder&harness=codex", nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var parsed struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
		return rec.Code, parsed.Code, rec.Body.String()
	}
	identical := func(who tenant.Principal, label string, ids ...string) string {
		t.Helper()
		var first string
		for i, id := range ids {
			status, code, body := merge(who, id)
			if status != 403 || code != "forbidden" {
				t.Fatalf("%s %s: %d %s %s", label, id, status, code, body)
			}
			if i == 0 {
				first = body
				continue
			}
			if body != first {
				t.Fatalf("%s responses differ:\n%s\n%s", label, first, body)
			}
		}
		return first
	}
	hidden := identical(reader, "project reader", existing, inactive, missing)
	if strings.Contains(hidden, "not_key_creator") || strings.Contains(hidden, "agent_inactive") || strings.Contains(hidden, "Existing") || strings.Contains(hidden, "Inactive") {
		t.Fatalf("restricted denial names the agent: %s", hidden)
	}
	if status, code, body := merge(owner, existing); status != 403 || code != authz.PreviewNotKeyCreator || !strings.Contains(body, authz.PreviewDenialMessage(authz.PreviewNotKeyCreator)) {
		t.Fatalf("privileged existing agent: %d %s %s", status, code, body)
	}
	if status, code, body := merge(owner, inactive); status != 403 || code != authz.PreviewAgentInactive || !strings.Contains(body, authz.PreviewDenialMessage(authz.PreviewAgentInactive)) {
		t.Fatalf("privileged inactive agent: %d %s %s", status, code, body)
	}
	if _, _, body := merge(owner, missing); body != hidden {
		t.Fatalf("a missing agent changed shape:\n%s\n%s", body, hidden)
	}
	if status, code, body := merge(maker, own); status != 403 || code != authz.PreviewKeyRevoked {
		t.Fatalf("key creator without members.read: %d %s %s", status, code, body)
	}
	if got := identical(maker, "key creator, other agents", existing, inactive, missing); got != hidden {
		t.Fatalf("key creator learned about another agent:\n%s\n%s", got, hidden)
	}
}

func TestRevokedNamedAgentLayerIsSkippedAndBudgetStillRuns(t *testing.T) {
	w := newBatchWorld(t, "rules-revoke-skip")
	admin := w.principal(tenant.Person, "owner", "owner")
	worker := w.agentFor(admin, "worker")
	named := w.layer(admin, Scope{Layer: "agent", OwnerID: admin.ID, AgentID: worker.ID})
	company := w.layer(admin, Scope{Layer: "company"})
	w.publish(admin, w.set(admin, named, "Persona", testRule("persona", "Stay in role.")), "260929120000.0.0")
	companySet := w.set(admin, company, "Floor", lockedRule("safety", "Keep the locked company floor."))

	before := string(w.call(admin, "GET", "/api/rules/layers", nil, 200))
	if !strings.Contains(before, named.ID) || !strings.Contains(before, worker.ID) {
		t.Fatalf("named layer missing before revocation: %s", before)
	}
	if _, err := w.d.Admin.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE tenant_id=$1 AND principal_id=$2`, w.tid, worker.ID); err != nil {
		t.Fatal(err)
	}
	after := string(w.call(admin, "GET", "/api/rules/layers", nil, 200))
	if strings.Contains(after, named.ID) || strings.Contains(after, worker.ID) || strings.Contains(after, "key_revoked") {
		t.Fatalf("revoked layer was not skipped: %s", after)
	}
	if !strings.Contains(after, company.ID) {
		t.Fatalf("company layer dropped: %s", after)
	}
	preview := string(w.call(admin, "GET", "/api/rules/merged?project_id="+w.project+"&person_id="+admin.ID+"&agent_id="+worker.ID+"&role=builder&harness=codex", nil, 403))
	if !strings.Contains(preview, `"code":"`+authz.PreviewKeyRevoked+`"`) {
		t.Fatalf("preview hid the reason from the key creator: %s", preview)
	}
	published := string(w.call(admin, "POST", "/api/rules/publish", batch("", item(companySet, "auto")), 200))
	if strings.Contains(published, authz.PreviewKeyRevoked) {
		t.Fatalf("budget check failed closed on the revoked layer: %s", published)
	}
}
