// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestInviteAcceptanceRequiresVerifiedMatchingEmail(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, ownerID, roleID, importedID, importedIdentity string
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('invite-oidc','Invite') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	token := "verified-invite-token-0123456789abcd"
	sum := sha256.Sum256([]byte(token))
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('paimos-classic','invite-person','person@example.com') RETURNING id::text`).Scan(&importedIdentity); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles) VALUES($1::uuid,'person',$2::uuid,'Classic Person','person@example.com',ARRAY['admin']) RETURNING id::text`, tid, importedIdentity).Scan(&importedID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Owner',ARRAY['super_admin']) RETURNING id::text`, tid).Scan(&ownerID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='member'`, tid).Scan(&roleID); err != nil {
			return err
		}
		// Authority comes from bindings (ADR-003), not the legacy role text.
		if _, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type)
			SELECT $1::uuid,$2::uuid,id,'workspace' FROM roles WHERE tenant_id=$1::uuid AND key='owner'`, tid, ownerID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by)
			VALUES($1::uuid,'person@example.com',$2::uuid,$3::bytea,now() + interval '7 days',$4::uuid)`, tid, roleID, sum[:], ownerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := &Module{pool: d.App, inTenant: db.InTenant, cfg: Config{BootstrapTenantSlug: "other", BootstrapAdminEmail: "root@example.com"}}
	if _, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "unverified", "person@example.com", "Person", false, token); err != errNotMember {
		t.Fatalf("unverified email: %v", err)
	}
	if _, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "mismatch", "someone.else@example.com", "Else", true, token); err != errNotMember {
		t.Fatalf("mismatched email: %v", err)
	}
	if _, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "wrong-token", "person@example.com", "Person", true, "different-token"); !errors.Is(err, errNotMember) {
		t.Fatalf("wrong token: %v", err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE invites SET created_at=now() - interval '2 days', expires_at=now() - interval '1 minute' WHERE tenant_id=$1::uuid`, tid); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "expired", "person@example.com", "Person", true, token); !errors.Is(err, errNotMember) {
		t.Fatalf("expired invite: %v", err)
	}
	if _, err := d.Admin.Exec(ctx, `UPDATE invites SET created_at=now(), expires_at=now() + interval '7 days' WHERE tenant_id=$1::uuid`, tid); err != nil {
		t.Fatal(err)
	}
	// An inviter who has since lost the authority to grant the role enrols no one.
	setOwnerRole := func(key string) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1::uuid AND key=$3)
			WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, tid, ownerID, key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE role_bindings DISABLE TRIGGER role_bindings_last_owner`); err != nil {
		t.Fatal(err)
	}
	setOwnerRole("viewer")
	if _, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "demoted", "person@example.com", "Person", true, token); !errors.Is(err, errNotMember) {
		t.Fatalf("invite from a demoted inviter: %v", err)
	}
	setOwnerRole("owner")
	if _, err := d.Admin.Exec(ctx, `ALTER TABLE role_bindings ENABLE TRIGGER role_bindings_last_owner`); err != nil {
		t.Fatal(err)
	}
	var deniedLinks int
	if err := d.Admin.QueryRow(ctx, `SELECT count(*) FROM principals WHERE tenant_id=$1::uuid AND linked_to IS NOT NULL`, tid).Scan(&deniedLinks); err != nil {
		t.Fatal(err)
	}
	if deniedLinks != 0 {
		t.Fatal("a refused acceptance linked the imported account")
	}
	person, _, err := m.resolveOIDCPerson(ctx, tid, "invite-oidc", "https://id.example", "accepted", "person@example.com", "Accepted Person", true, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		var status, role string
		if err := tx.QueryRow(ctx, `SELECT CASE WHEN i.accepted_at IS NOT NULL THEN 'accepted' ELSE 'open' END, r.key
			FROM invites i JOIN role_bindings b ON b.tenant_id=i.tenant_id AND b.principal_id=i.accepted_by AND b.scope_type='workspace'
			JOIN roles r ON r.id=b.role_id
			WHERE i.tenant_id=$1::uuid AND i.accepted_by=$2::uuid`, tid, person.ID).Scan(&status, &role); err != nil {
			return err
		}
		if status != "accepted" || role != "member" || person.Name != "Accepted Person" {
			t.Fatalf("accepted %s role %s name %s", status, role, person.Name)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var linked, identity string
	if err := d.Admin.QueryRow(ctx, `SELECT linked_to::text,identity_id::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, importedID).Scan(&linked, &identity); err != nil {
		t.Fatal(err)
	}
	if linked != person.ID || identity != importedIdentity {
		t.Fatalf("classic identity not preserved: linked=%s identity=%s", linked, identity)
	}
}

func TestVerifiedInviteRejectsUnicodeMailboxCaseMappings(t *testing.T) {
	for _, tc := range []struct{ invited, verified string }{
		{"admin@example.com", "admİn@example.com"},
		{"mark@example.com", "marK@example.com"},
	} {
		t.Run(tc.verified, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx := dbtest.Seed(t.Context())
			var tid, ownerID, importedID string
			token := "unicode-invite-test-token"
			hash := sha256.Sum256([]byte(token))
			if err := db.InTenant(ctx, d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('unicode-invite','Unicode invite') RETURNING id::text`).Scan(&tid)
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Owner',ARRAY['super_admin']) RETURNING id::text`, tid).Scan(&ownerID); err != nil {
					return err
				}
				if err := dbtest.BindLegacyTx(ctx, tx, tid, ownerID); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email,roles) VALUES($1::uuid,'person','Imported',$2,ARRAY['admin']) RETURNING id::text`, tid, tc.invited).Scan(&importedID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO invites(tenant_id,email,workspace_role_id,token_hash,expires_at,created_by)
					SELECT $1::uuid,$2,id,$3,now()+interval '7 days',$4::uuid FROM roles WHERE tenant_id=$1::uuid AND key='member'`, tid, tc.invited, hash[:], ownerID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			m := &Module{pool: d.App, inTenant: db.InTenant, cfg: Config{BootstrapTenantSlug: "other"}}
			for n, supplied := range []string{"", token} {
				if _, _, err := m.resolveOIDCPerson(ctx, tid, "unicode-invite", "https://id.example", fmt.Sprintf("unicode-%d", n), tc.verified, "Verified other mailbox", true, supplied); !errors.Is(err, errNotMember) {
					t.Fatalf("Unicode mailbox accepted or revealed imported match (token=%v): %v", supplied != "", err)
				}
			}
			var consumed, linked, enrolled int
			if err := d.Admin.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM invites WHERE tenant_id=$1::uuid AND accepted_at IS NOT NULL),
				(SELECT count(*) FROM principals WHERE tenant_id=$1::uuid AND linked_to IS NOT NULL),
				(SELECT count(*) FROM principals WHERE tenant_id=$1::uuid AND id NOT IN ($2::uuid,$3::uuid) AND kind='person')`, tid, ownerID, importedID).Scan(&consumed, &linked, &enrolled); err != nil {
				t.Fatal(err)
			}
			if consumed != 0 || linked != 0 || enrolled != 0 {
				t.Fatalf("Unicode mismatch changed membership: accepted=%d linked=%d enrolled=%d", consumed, linked, enrolled)
			}
			if _, _, err := m.resolveOIDCPerson(ctx, tid, "unicode-invite", "https://id.example", "real-mailbox", strings.ToUpper(tc.invited), "Real mailbox", true, token); err != nil {
				t.Fatalf("ASCII case matching failed: %v", err)
			}
		})
	}
}

func TestImportedSignInHintRequiresVerifiedTenantEmail(t *testing.T) {
	d := dbtest.Open(t)
	ctx := dbtest.Seed(t.Context())
	var tid, foreign string
	if err := db.InTenant(ctx, d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('hint','Hint') RETURNING id::text`).Scan(&tid); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('foreign-hint','Foreign') RETURNING id::text`).Scan(&foreign)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO principals(tenant_id,kind,name,email) VALUES($1::uuid,'person','Imported','person@example.com')`, tid)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m := &Module{pool: d.App, inTenant: db.InTenant, cfg: Config{BootstrapTenantSlug: "other"}}
	for _, tc := range []struct {
		name, tenant, email string
		verified, hint      bool
	}{
		{"verified", tid, "person@example.com", true, true},
		{"unverified", tid, "person@example.com", false, false},
		{"wrong mailbox", tid, "person+else@example.com", true, false},
		{"foreign tenant", foreign, "person@example.com", true, false},
	} {
		_, _, err := m.resolveOIDCPerson(t.Context(), tc.tenant, "hint", "https://id.example", tc.name, tc.email, "Person", tc.verified, "")
		if !errors.Is(err, errNotMember) || errors.Is(err, errImportedNotMember) != tc.hint {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestAgentKeyPrincipalScopeAndCeiling(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid string
	owner := tenant.Principal{Kind: tenant.Person, Name: "Owner"}
	limited := tenant.Principal{Kind: tenant.Person, Name: "Limited"}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('keys','Keys') RETURNING id::text`).Scan(&tid)
	}); err != nil {
		t.Fatal(err)
	}
	owner.TenantID, limited.TenantID = tid, tid
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','Owner',ARRAY['super_admin']) RETURNING id::text`, tid).Scan(&owner.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1::uuid,'person','Limited') RETURNING id::text`, tid).Scan(&limited.ID); err != nil {
			return err
		}
		var roleID string
		if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,'key_minter','Key minter') RETURNING id::text`, tid).Scan(&roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,'keys.manage'),($1::uuid,$2::uuid,'nodes.read')`, tid, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, tid, limited.ID, roleID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, tid, owner.ID)
	mod := &Module{pool: d.App, inTenant: db.InTenant}
	mux := http.NewServeMux()
	mod.Mount(mux)
	call := func(body string, who tenant.Principal) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/agent-keys", strings.NewReader(body))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), who))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	created := call(`{"name":"worker","scopes":["events:read","account.manage"]}`, owner)
	if created.Code != 201 || !strings.Contains(created.Body.String(), `"events.read"`) || strings.Contains(created.Body.String(), "events:read") {
		t.Fatalf("normalized scopes %d %s", created.Code, created.Body.String())
	}
	var key struct {
		PrincipalID string   `json:"principal_id"`
		Scopes      []string `json:"scopes"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if len(key.Scopes) != 2 || key.Scopes[0] != "events.read" {
		t.Fatalf("scopes %+v", key.Scopes)
	}
	again := call(`{"principal_id":"`+key.PrincipalID+`","name":"second","scopes":["nodes.read"]}`, owner)
	if again.Code != 201 || !strings.Contains(again.Body.String(), key.PrincipalID) {
		t.Fatalf("principal id %d %s", again.Code, again.Body.String())
	}
	if rec := call(`{"name":"nope","scopes":["ownership.transfer"]}`, owner); rec.Code != 400 {
		t.Fatalf("non grantable %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(`{"principal_id":"`+key.PrincipalID+`","scopes":["nodes.write"]}`, limited); rec.Code != 403 {
		t.Fatalf("ceiling %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(`{"principal_id":"`+owner.ID+`","name":"person"}`, owner); rec.Code != 400 {
		t.Fatalf("person principal %d %s", rec.Code, rec.Body.String())
	}
}
