// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPreviewDenialMessagesAndFlags(t *testing.T) {
	if got := PreviewDenialMessage(PreviewNotKeyCreator); got != "You didn't create a key for this agent." {
		t.Fatal(got)
	}
	if got := PreviewDenialMessage(PreviewKeyRevoked); !strings.Contains(got, "revoked") {
		t.Fatal(got)
	}
	if got := PreviewDenialMessage(PreviewKeyExpired); !strings.Contains(got, "expired") {
		t.Fatal(got)
	}
	if got := PreviewDenialMessage(PreviewAgentInactive); !strings.Contains(got, "deactivated") {
		t.Fatal(got)
	}
	allowed, reason := previewFromFlags("active", true, true, true)
	if !allowed || reason != "" {
		t.Fatalf("valid key must win: %v %s", allowed, reason)
	}
	if allowed, reason = previewFromFlags("deactivated", true, false, true); allowed || reason != PreviewAgentInactive {
		t.Fatalf("inactive agent: %v %s", allowed, reason)
	}
	if allowed, reason = previewFromFlags("active", false, true, true); allowed || reason != PreviewKeyExpired {
		t.Fatalf("expired key: %v %s", allowed, reason)
	}
	if allowed, reason = previewFromFlags("active", false, false, true); allowed || reason != PreviewKeyRevoked {
		t.Fatalf("revoked key: %v %s", allowed, reason)
	}
	if allowed, reason = previewFromFlags("active", false, false, false); allowed || reason != PreviewNotKeyCreator {
		t.Fatalf("no key: %v %s", allowed, reason)
	}
}

func TestMemberListPreviewHidesCreatorExceptForWorkspaceAdmins(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('preview-members','Preview members') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	person := func(name, role string) tenant.Principal {
		t.Helper()
		p := tenant.Principal{TenantID: tid, Kind: tenant.Person, Name: name}
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email) VALUES($1::uuid,'person',$2,$3) RETURNING id::text`, tid, name, strings.ToLower(strings.Fields(name)[0])+"@example.com").Scan(&p.ID)
		}); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, d, tid, p.ID, role)
		return p
	}
	owner := person("Ada Owner", "owner")
	admin := person("Bea Admin", "admin")
	member := person("Cam Member", "member")
	var readerRole string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'preview_reader','Preview reader') RETURNING id::text`, tid).Scan(&readerRole); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'members.read')`, tid, readerRole)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reader := person("Dee Reader", "preview_reader")
	agent := func(name string) string {
		t.Helper()
		var id string
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'agent',$2) RETURNING id::text`, tid, name).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	held := agent("Held")
	foreign := agent("Foreign")
	revoked := agent("Revoked")
	expired := agent("Expired")
	quiet := agent("Quiet")
	insertKey := func(principal, creator, prefix string, revoke, expire bool) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,created_by_principal_id,revoked_at,expires_at) VALUES($1,$2,'preview',$3,'fixture-not-a-credential',$4,CASE WHEN $5 THEN clock_timestamp() - interval '1 hour' END,CASE WHEN $6 THEN clock_timestamp() - interval '1 hour' END)`, tid, principal, prefix, creator, revoke, expire); err != nil {
			t.Fatal(err)
		}
	}
	insertKey(held, member.ID, "preview-held", false, false)
	insertKey(foreign, owner.ID, "preview-foreign", false, false)
	insertKey(revoked, member.ID, "preview-revoked", true, false)
	insertKey(expired, member.ID, "preview-expired", false, true)
	insertKey(quiet, member.ID, "preview-quiet", false, false)
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, quiet); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	directory := func(who tenant.Principal) MemberDirectory {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/members", nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("members %s: %d %s", who.Name, rec.Code, rec.Body.String())
		}
		var out MemberDirectory
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	byName := func(dir MemberDirectory, name string) AgentMember {
		t.Helper()
		for _, item := range dir.Agents {
			if item.Name == name {
				return item
			}
		}
		t.Fatalf("missing agent %s", name)
		return AgentMember{}
	}
	memberDir := directory(member)
	if strings.Contains(mustJSON(t, memberDir), `"creator_name"`) {
		t.Fatalf("member saw a creator name: %s", mustJSON(t, memberDir))
	}
	readerDir := directory(reader)
	if strings.Contains(mustJSON(t, readerDir), `"creator_name"`) {
		t.Fatalf("a non-admin with members.read saw a creator name: %s", mustJSON(t, readerDir))
	}
	heldRow := byName(memberDir, "Held")
	if !heldRow.Preview.Allowed || heldRow.Preview.Reason != "" {
		t.Fatalf("own key: %+v", heldRow.Preview)
	}
	if row := byName(memberDir, "Foreign"); row.Preview.Allowed || row.Preview.Reason != PreviewNotKeyCreator || row.Preview.CreatorName != "" {
		t.Fatalf("someone else's key: %+v", row.Preview)
	}
	if row := byName(memberDir, "Revoked"); row.Preview.Allowed || row.Preview.Reason != PreviewKeyRevoked {
		t.Fatalf("revoked: %+v", row.Preview)
	}
	if row := byName(memberDir, "Expired"); row.Preview.Allowed || row.Preview.Reason != PreviewKeyExpired {
		t.Fatalf("expired: %+v", row.Preview)
	}
	if row := byName(memberDir, "Quiet"); row.Preview.Allowed || row.Preview.Reason != PreviewAgentInactive || row.Preview.CreatorName != "" {
		t.Fatalf("inactive: %+v", row.Preview)
	}
	for _, who := range []tenant.Principal{owner, admin} {
		dir := directory(who)
		row := byName(dir, "Held")
		if row.Preview.Allowed || row.Preview.Reason != PreviewNotKeyCreator || row.Preview.CreatorName != member.Name {
			t.Fatalf("%s should see the key creator: %+v", who.Name, row.Preview)
		}
		own := byName(dir, "Foreign")
		if who.ID == owner.ID && (!own.Preview.Allowed || own.Preview.CreatorName != "") {
			t.Fatalf("owner's own agent leaked a name or was denied: %+v", own.Preview)
		}
		if who.ID == admin.ID && (own.Preview.Allowed || own.Preview.Reason != PreviewNotKeyCreator || own.Preview.CreatorName != owner.Name) {
			t.Fatalf("admin should see who holds Foreign: %+v", own.Preview)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
