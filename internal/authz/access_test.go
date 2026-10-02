// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAccessInvitesLifecycleAliasesAndAudit(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('p3-access','P3 access') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	owner := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"super_admin"}, Name: "Ada Owner"}
	admin := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"admin"}, Name: "Bea Admin"}
	member := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"member"}, Name: "Cam Member"}
	classic := tenant.Principal{TenantID: tid, Kind: tenant.Person, Roles: []string{"reviewer"}, Name: "cam-classic"}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for _, p := range []*tenant.Principal{&owner, &admin, &member, &classic} {
			if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email,roles) VALUES($1::uuid,'person',$2,$3,$4) RETURNING id::text`, tid, p.Name, strings.ToLower(strings.Fields(p.Name)[0])+"@example.com", p.Roles).Scan(&p.ID); err != nil {
				return err
			}
		}
		// The duplicate-member check applies to a real sign-in member, not a
		// legacy person without an identity.
		if _, err := tx.Exec(ctx, `WITH signin AS (
			INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','ada-owner','ada@example.com') RETURNING id)
			UPDATE principals SET identity_id=(SELECT id FROM signin) WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, owner.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO personal_profiles(tenant_id,principal_id,avatar_original_hash,avatar_hashes) VALUES($1::uuid,$2::uuid,$3,'{"32":"abc"}'::jsonb)`, tid, owner.ID, strings.Repeat("ab", 32)); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,state) SELECT $1::uuid,'PRJ-1',id,'Atlas','active' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, tid).Scan(new(string))
	}); err != nil {
		t.Fatal(err)
	}
	var projectID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM nodes WHERE tenant_id=$1::uuid AND key='PRJ-1'`, tid).Scan(&projectID)
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{owner, admin, member, classic} {
		dbtest.BindLegacy(t, d, tid, p.ID)
	}
	// Give the classic identity its own binding, which linking must remove.
	// (Guest is a project-only role since ADR-003 P2, so use Viewer.)
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='viewer' ON CONFLICT DO NOTHING`, tid, classic.ID); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(d.App).Mount(mux)
	call := func(method, path, body string, who tenant.Principal) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "aeon.test"
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	roleOf := func(key string) string {
		t.Helper()
		var id string
		if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key=$2`, tid, key).Scan(&id)
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	memberRole, guestRole, ownerRole := roleOf("member"), roleOf("guest"), roleOf("owner")

	reg := call("GET", "/api/authz/permissions", "", owner)
	if reg.Code != 200 || !strings.Contains(reg.Body.String(), `"agent_grantable":false`) || !strings.Contains(reg.Body.String(), `"key":"nodes.read"`) {
		t.Fatalf("registry %d %s", reg.Code, reg.Body.String())
	}
	var perms []Permission
	if err := json.Unmarshal(reg.Body.Bytes(), &perms); err != nil {
		t.Fatal(err)
	}
	for _, perm := range perms {
		if perm.Key == "ownership.transfer" && perm.AgentGrantable {
			t.Fatal("ownership.transfer is agent grantable")
		}
		if perm.Key == "nodes.read" && !perm.AgentGrantable {
			t.Fatal("nodes.read is not agent grantable")
		}
	}

	dir := call("GET", "/api/members", "", owner)
	if dir.Code != 200 {
		t.Fatalf("members %d %s", dir.Code, dir.Body.String())
	}
	var directory MemberDirectory
	if err := json.Unmarshal(dir.Body.Bytes(), &directory); err != nil {
		t.Fatal(err)
	}
	if directory.OwnerCount != 1 || !strings.Contains(dir.Body.String(), `"has_avatar":false`) || !strings.Contains(dir.Body.String(), `"has_avatar":true`) {
		t.Fatalf("directory flags %+v", directory)
	}
	var sawLast bool
	for _, person := range directory.People {
		if person.PrincipalID == owner.ID && (!person.LastOwner || !person.HasAvatar) {
			t.Fatalf("owner flags %+v", person)
		}
		if person.PrincipalID == admin.ID && person.LastOwner {
			t.Fatal("admin is last owner")
		}
		if person.LastOwner {
			sawLast = true
		}
		if person.WorkspaceRole == nil || person.WorkspaceRole.Key == "" || person.WorkspaceRole.Name == "" {
			t.Fatalf("workspace role shape %+v", person.WorkspaceRole)
		}
	}
	if !sawLast {
		t.Fatal("no last_owner")
	}

	esc := call("POST", "/api/members/invites", `{"email":"new@example.com","workspace_role_id":"`+ownerRole+`"}`, admin)
	if esc.Code != 403 || !strings.Contains(esc.Body.String(), `"field":"workspace_role_id"`) {
		t.Fatalf("escalation %d %s", esc.Code, esc.Body.String())
	}
	dup := call("POST", "/api/members/invites", `{"email":"ada@example.com","workspace_role_id":"`+memberRole+`"}`, admin)
	if dup.Code != 409 || !strings.Contains(dup.Body.String(), "already") {
		t.Fatalf("existing member %d %s", dup.Code, dup.Body.String())
	}
	var limited tenant.Principal
	limited.TenantID, limited.Kind, limited.Name = tid, tenant.Person, "Limited"
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Limited') RETURNING id::text`, tid).Scan(&limited.ID); err != nil {
			return err
		}
		var roleID string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'limited_inviter','Limited inviter') RETURNING id::text`, tid).Scan(&roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'members.manage'),($1::uuid,$2::uuid,'nodes.read')`, tid, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tid, limited.ID, roleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rec := call("POST", "/api/members/invites", `{"email":"wide@example.com","workspace_role_id":"`+memberRole+`"}`, limited); rec.Code != 403 {
		t.Fatalf("limited inviter %d %s", rec.Code, rec.Body.String())
	}

	created := call("POST", "/api/members/invites", `{"email":"New.Person@example.com","workspace_role_id":"`+memberRole+`","project_roles":[{"project_id":"`+projectID+`","role_id":"`+guestRole+`"}],"expires_in_days":14}`, admin)
	if created.Code != 201 {
		t.Fatalf("invite %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		Invite  Invite `json:"invite"`
		JoinURL string `json:"join_url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	join, err := url.Parse(payload.JoinURL)
	if err != nil || join.Query().Get("tenant") != "p3-access" || join.Query().Get("invite") == "" {
		t.Fatalf("join url %q", payload.JoinURL)
	}
	token := join.Query().Get("invite")
	if payload.Invite.Status != "pending" || payload.Invite.CreatedBy.PrincipalID != admin.ID || payload.Invite.CreatedBy.Name == "" || payload.Invite.WorkspaceRole == nil || len(payload.Invite.ProjectRoles) != 1 || payload.Invite.AcceptedBy != nil {
		t.Fatalf("invite shape %+v", payload.Invite)
	}
	listed := call("GET", "/api/members", "", owner)
	if strings.Contains(listed.Body.String(), token) {
		t.Fatal("invite token leaked into the directory")
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','mismatch','other@example.com') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		if _, err := AcceptInvite(ctx, tx, tid, identity, "other@example.com", "Other", token); err != ErrNoInvite {
			t.Fatalf("mismatched email accepted: %v", err)
		}
		sum := sha256.Sum256([]byte(token))
		var match bool
		if err := tx.QueryRow(ctx, `SELECT token_hash=$2 FROM invites WHERE id=$1::uuid`, payload.Invite.ID, sum[:]).Scan(&match); err != nil {
			return err
		}
		if !match {
			t.Fatal("stored hash does not match the token")
		}
		var raw string
		if err := tx.QueryRow(ctx, `SELECT coalesce(email,'')||coalesce(token_hash::text,'') FROM invites WHERE id=$1::uuid`, payload.Invite.ID).Scan(&raw); err != nil {
			return err
		}
		if strings.Contains(raw, token) {
			t.Fatal("plaintext token stored")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Admin.Exec(ctx, `UPDATE invites SET created_at=now() - interval '2 days', expires_at=now() - interval '1 minute' WHERE id=$1::uuid`, payload.Invite.ID); err != nil {
		t.Fatal(err)
	}
	expired := call("GET", "/api/members", "", owner)
	if !strings.Contains(expired.Body.String(), `"status":"expired"`) {
		t.Fatalf("expired invite missing %s", expired.Body.String())
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','expired','new.person@example.com') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		if _, err := AcceptInvite(ctx, tx, tid, identity, "new.person@example.com", "New", token); err != ErrNoInvite {
			t.Fatalf("expired accept %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fresh := call("POST", "/api/members/invites", `{"email":"guest.only@example.com","project_roles":[{"project_id":"`+projectID+`","role_id":"`+guestRole+`"}]}`, owner)
	if fresh.Code != 201 {
		t.Fatalf("project invite %d %s", fresh.Code, fresh.Body.String())
	}
	var projectInvite struct {
		JoinURL string `json:"join_url"`
	}
	if err := json.Unmarshal(fresh.Body.Bytes(), &projectInvite); err != nil {
		t.Fatal(err)
	}
	projectToken := mustQuery(t, projectInvite.JoinURL, "invite")
	var enrolled tenant.Principal
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var identity string
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','guest','guest.only@example.com') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		var err error
		enrolled, err = AcceptInvite(ctx, tx, tid, identity, "Guest.Only@example.com", "Guest Only", projectToken)
		if err != nil {
			return err
		}
		var scope, status string
		if err := tx.QueryRow(ctx, `SELECT b.scope_type, i.status FROM role_bindings b, (
			SELECT CASE WHEN accepted_at IS NOT NULL THEN 'accepted' ELSE 'pending' END AS status FROM invites WHERE lower(email)=lower('guest.only@example.com')
		) i WHERE b.principal_id=$1::uuid AND b.scope_type='project' AND b.scope_id=$2::uuid`, enrolled.ID, projectID).Scan(&scope, &status); err != nil {
			return err
		}
		if scope != "project" || status != "accepted" {
			t.Fatalf("binding %s status %s", scope, status)
		}
		var events int
		return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type IN ('binding.set','invite.accepted') AND after->>'principal_id'=$1`, enrolled.ID).Scan(&events)
	}); err != nil {
		t.Fatal(err)
	}

	// ADR-003 P2: the enrolled guest sees exactly the invited project, and
	// Guest cannot be invited for the workspace, nor Owner for a project.
	var seen []string
	if err := db.InTenant(tenant.WithPrincipal(ctx, enrolled), d.App, tid, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM nodes`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seen = append(seen, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != projectID {
		t.Fatalf("invited guest sees %v, want only %s", seen, projectID)
	}
	if rec := call("POST", "/api/members/invites", `{"email":"ws.guest@example.com","workspace_role_id":"`+guestRole+`"}`, owner); rec.Code != 400 || !strings.Contains(rec.Body.String(), "project_only_role") {
		t.Fatalf("workspace guest invite %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/api/members/invites", `{"email":"proj.owner@example.com","project_roles":[{"project_id":"`+projectID+`","role_id":"`+ownerRole+`"}]}`, owner); rec.Code != 400 {
		t.Fatalf("project owner invite %d %s", rec.Code, rec.Body.String())
	}

	var identityID string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','cam','cam@example.com') RETURNING id::text`).Scan(&identityID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO sessions(id,identity_id,tenant_id,principal_id,expires_at) VALUES('session-cam',$1::uuid,$2::uuid,$3::uuid,now() + interval '1 day')`, identityID, tid, member.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rec := call("POST", "/api/members/"+owner.ID+"/deactivate", "", admin); rec.Code != 409 || !strings.Contains(rec.Body.String(), "last active owner") {
		t.Fatalf("last owner %d %s", rec.Code, rec.Body.String())
	}
	off := call("POST", "/api/members/"+member.ID+"/deactivate", "", admin)
	if off.Code != 200 || !strings.Contains(off.Body.String(), `"status":"deactivated"`) {
		t.Fatalf("deactivate %d %s", off.Code, off.Body.String())
	}
	var sessions int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE principal_id=$1::uuid`, member.ID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions %d %v", sessions, err)
	}
	if rec := call("POST", "/api/members/"+member.ID+"/deactivate", "", admin); rec.Code != 409 {
		t.Fatalf("second deactivate %d", rec.Code)
	}
	if rec := call("POST", "/api/members/"+member.ID+"/reactivate", "", admin); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"active"`) {
		t.Fatalf("reactivate %d %s", rec.Code, rec.Body.String())
	}

	if rec := call("POST", "/api/members/"+member.ID+"/aliases", `{"from_principal_id":"`+member.ID+`"}`, admin); rec.Code != 400 {
		t.Fatalf("self alias %d %s", rec.Code, rec.Body.String())
	}
	linked := call("POST", "/api/members/"+member.ID+"/aliases", `{"from_principal_id":"`+classic.ID+`"}`, admin)
	if linked.Code != 200 {
		t.Fatalf("alias %d %s", linked.Code, linked.Body.String())
	}
	var linkedMember Member
	if err := json.Unmarshal(linked.Body.Bytes(), &linkedMember); err != nil {
		t.Fatal(err)
	}
	if len(linkedMember.Aliases) != 1 || linkedMember.Aliases[0].PrincipalID != classic.ID {
		t.Fatalf("aliases %+v", linkedMember.Aliases)
	}
	var aliasBindings int
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE principal_id=$1::uuid`, classic.ID).Scan(&aliasBindings)
	}); err != nil || aliasBindings != 0 {
		t.Fatalf("alias kept %d bindings (%v)", aliasBindings, err)
	}
	if rec := call("POST", "/api/members/"+classic.ID+"/aliases", `{"from_principal_id":"`+admin.ID+`"}`, owner); rec.Code != 400 && rec.Code != 409 {
		t.Fatalf("chain %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/api/members/"+admin.ID+"/aliases", `{"from_principal_id":"`+owner.ID+`"}`, owner); rec.Code != 409 {
		t.Fatalf("alias last owner %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("DELETE", "/api/members/"+member.ID+"/aliases/"+classic.ID, "", admin); rec.Code != 204 {
		t.Fatalf("unlink %d %s", rec.Code, rec.Body.String())
	}

	again := call("POST", "/api/members/"+member.ID+"/deactivate", "", admin)
	if again.Code != 200 {
		t.Fatalf("deactivate for audit %d %s", again.Code, again.Body.String())
	}
	audit := call("GET", "/api/audit?category=access", "", owner)
	if audit.Code != 200 {
		t.Fatalf("audit %d %s", audit.Code, audit.Body.String())
	}
	var page struct {
		Items []struct {
			Type    string        `json:"type"`
			Actor   PrincipalRef  `json:"actor"`
			Subject *PrincipalRef `json:"subject"`
		} `json:"items"`
		Next any `json:"next_after"`
	}
	if err := json.Unmarshal(audit.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range page.Items {
		if item.Type == "principal.deactivated" && item.Subject != nil && item.Subject.PrincipalID == member.ID {
			if item.Subject.Name != member.Name || item.Actor.Name != admin.Name {
				t.Fatalf("names actor %q subject %q", item.Actor.Name, item.Subject.Name)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("deactivated name missing in %+v", page.Items)
	}
	if rec := call("GET", "/api/audit?category=access", "", member); rec.Code != 403 {
		t.Fatalf("member audit %d", rec.Code)
	}
	if rec := call("GET", "/api/audit", "", owner); rec.Code != 400 {
		t.Fatalf("missing category %d", rec.Code)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		for i := 0; i < 60; i++ {
			if _, err := tx.Exec(ctx, `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1::uuid,$2::uuid,'invite.revoked',$3::jsonb)`, tid, owner.ID, `{"email":"page@example.com"}`); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first := call("GET", "/api/audit?category=access", "", owner)
	var paged struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextAfter *int64 `json:"next_after"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &paged); err != nil {
		t.Fatal(err)
	}
	if len(paged.Items) != auditPage || paged.NextAfter == nil {
		t.Fatalf("page len %d next %v", len(paged.Items), paged.NextAfter)
	}
	second := call("GET", "/api/audit?category=access&after="+strconv.FormatInt(*paged.NextAfter, 10), "", owner)
	var rest struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if len(rest.Items) == 0 || rest.Items[0].ID <= *paged.NextAfter {
		t.Fatalf("cursor %+v", rest.Items)
	}
}

func mustQuery(t *testing.T, raw, key string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil || u.Query().Get(key) == "" {
		t.Fatalf("url %s", raw)
	}
	return u.Query().Get(key)
}
