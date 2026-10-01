// SPDX-License-Identifier: AGPL-3.0-only

package authz_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestClassicInviteLinking(t *testing.T) {
	for _, tc := range []struct {
		name, issuer, email                                          string
		candidates                                                   int
		foreign, deactivated, linked, blocked, wantLink, projectOnly bool
	}{
		{name: "classic", issuer: "paimos-classic", email: "person@example.com", candidates: 1, wantLink: true},
		{name: "no identity", email: "person@example.com", candidates: 1, wantLink: true},
		{name: "identity email only", issuer: "paimos-classic", candidates: 1, wantLink: true},
		{name: "ambiguous", issuer: "paimos-classic", email: "person@example.com", candidates: 2},
		{name: "other tenant", issuer: "paimos-classic", email: "person@example.com", candidates: 1, foreign: true},
		{name: "different mailbox", issuer: "paimos-classic", email: "person+alias@example.com", candidates: 1},
		{name: "deactivated", issuer: "paimos-classic", email: "person@example.com", candidates: 1, deactivated: true},
		{name: "already linked", issuer: "paimos-classic", email: "person@example.com", candidates: 1, linked: true, blocked: true},
		{name: "real member", issuer: "https://id.example", email: "person@example.com", candidates: 1, blocked: true},
		{name: "real member identity email", issuer: "https://id.example", email: "previous@example.com", candidates: 1, blocked: true},
		{name: "project only", issuer: "paimos-classic", email: "person@example.com", candidates: 1, wantLink: true, projectOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx := dbtest.Seed(t.Context())
			tid, err := tenantbootstrap.Create(ctx, d.App, "classic-invite", "Classic invite")
			if err != nil {
				t.Fatal(err)
			}
			ownerID, err := tenantbootstrap.BindOIDC(ctx, d.App, "classic-invite", "https://id.example", "owner", "Owner", "admin")
			if err != nil {
				t.Fatal(err)
			}
			owner := tenant.Principal{ID: ownerID, TenantID: tid, Kind: tenant.Person}
			candidateTenant := tid
			if tc.foreign {
				candidateTenant, err = tenantbootstrap.Create(ctx, d.App, "other", "Other")
				if err != nil {
					t.Fatal(err)
				}
			}
			var roleID, guestID, projectID, signinID string
			var aliases, identities []string
			if err := db.InTenant(ctx, d.App, candidateTenant, func(tx pgx.Tx) error {
				for n := range tc.candidates {
					var identity any
					identityID := ""
					if tc.issuer != "" {
						if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES($1,$2,'person@example.com') RETURNING id::text`, tc.issuer, fmt.Sprintf("classic:%d", n)).Scan(&identityID); err != nil {
							return err
						}
						identity = identityID
					}
					var id string
					if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles,status)
						VALUES($1::uuid,'person',$2::uuid,$3,$4,ARRAY['admin'],$5) RETURNING id::text`, candidateTenant, identity, fmt.Sprintf("Classic %d", n), tc.email, map[bool]string{true: "deactivated", false: "active"}[tc.deactivated]).Scan(&id); err != nil {
						return err
					}
					aliases = append(aliases, id)
					identities = append(identities, identityID)
					if !tc.foreign && !tc.deactivated {
						if err := dbtest.BindLegacyTx(ctx, tx, tid, id); err != nil {
							return err
						}
					}
				}
				if tc.linked {
					_, err := tx.Exec(ctx, `UPDATE principals SET linked_to=$3::uuid WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, aliases[0], ownerID)
					return err
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='member'`, tid).Scan(&roleID); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE tenant_id=$1::uuid AND key='guest'`, tid).Scan(&guestID); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,state) SELECT $1::uuid,id,'PRJ-1','Project','active' FROM node_kinds WHERE tenant_id=$1::uuid AND slug='project' RETURNING id::text`, tid).Scan(&projectID); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example','new-person','PERSON@example.com') RETURNING id::text`).Scan(&signinID)
			}); err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			authz.New(d.App).Mount(mux)
			body := map[string]any{"email": "person@example.com", "workspace_role_id": roleID}
			if tc.projectOnly {
				delete(body, "workspace_role_id")
				body["project_roles"] = []map[string]string{{"project_id": projectID, "role_id": guestID}}
			}
			encoded, _ := json.Marshal(body)
			req := httptest.NewRequest("POST", "/api/members/invites", strings.NewReader(string(encoded)))
			req.Host = "aeon.test"
			req = req.WithContext(tenant.WithPrincipal(req.Context(), owner))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if tc.blocked {
				if rec.Code != 409 {
					t.Fatalf("active member invite: %d %s", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != 201 {
				t.Fatalf("imported invite: %d %s", rec.Code, rec.Body.String())
			}
			var person tenant.Principal
			if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
				var err error
				// A verified first sign-in can accept the email's pending invite
				// without a token; the auth package tests the verification gate.
				person, err = authz.AcceptInvite(ctx, tx, tid, signinID, "PERSON@example.com", "New Person", "")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			for n, id := range aliases {
				var linked, identity *string
				if err := d.Admin.QueryRow(ctx, `SELECT linked_to::text,identity_id::text FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid`, candidateTenant, id).Scan(&linked, &identity); err != nil {
					t.Fatal(err)
				}
				if tc.wantLink && (linked == nil || *linked != person.ID) || !tc.wantLink && linked != nil {
					t.Fatalf("link=%v wantLink=%v", linked, tc.wantLink)
				}
				if identities[n] == "" && identity != nil || identities[n] != "" && (identity == nil || *identity != identities[n]) {
					t.Fatal("imported identity changed")
				}
			}
			if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE tenant_id=$1::uuid AND type='principal.alias_linked' AND after->>'reason'='verified_invite_email' AND after->>'linked_to'=$2`, tid, person.ID).Scan(&count); err != nil {
					return err
				}
				if count != map[bool]int{true: 1, false: 0}[tc.wantLink] {
					t.Fatalf("link events: %d", count)
				}
				// This is the legacy binder called by both startSession and import.
				if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_uninvited($1::uuid,$2::uuid)`, tid, person.ID); err != nil {
					return err
				}
				if tc.wantLink {
					if _, err := tx.Exec(ctx, `SELECT aeon_bind_legacy_uninvited($1::uuid,$2::uuid)`, tid, aliases[0]); err != nil {
						return err
					}
					if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, tid, aliases[0]).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Fatal("alias kept role bindings")
					}
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, tid, person.ID).Scan(&count); err != nil {
					return err
				}
				if count != map[bool]int{true: 0, false: 1}[tc.projectOnly] {
					t.Fatalf("workspace bindings: %d", count)
				}
				if !tc.projectOnly {
					var bound string
					if err := tx.QueryRow(ctx, `SELECT role_id::text FROM role_bindings WHERE tenant_id=$1::uuid AND principal_id=$2::uuid AND scope_type='workspace'`, tid, person.ID).Scan(&bound); err != nil {
						return err
					}
					if bound != roleID {
						t.Fatal("classic role replaced the invited role")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// Linked aliases appear only under the person in the directory.
			req = httptest.NewRequest("GET", "/api/members", nil).WithContext(tenant.WithPrincipal(ctx, owner))
			rec = httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Fatalf("directory: %d %s", rec.Code, rec.Body.String())
			}
			var directory authz.MemberDirectory
			if err := json.Unmarshal(rec.Body.Bytes(), &directory); err != nil {
				t.Fatal(err)
			}
			if tc.wantLink {
				if len(directory.Imported) != 0 {
					t.Fatal("linked alias remains in imported group")
				}
				for _, member := range directory.People {
					if member.PrincipalID == aliases[0] {
						t.Fatal("linked alias remains a separate person")
					}
					if member.PrincipalID == person.ID && len(member.Aliases) != 1 {
						t.Fatal("person missing classic alias")
					}
				}
			}
		})
	}
}
