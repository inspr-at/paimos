// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestBootstrapAdminRequiresASCIIMailboxMatch(t *testing.T) {
	for _, tc := range []struct {
		configured, verified string
		allowed              bool
	}{
		{"mark@example.test", "marK@example.test", false},
		{"sam@example.test", "ſam@example.test", false},
		{"admin@example.test", "admİn@example.test", false},
		{"mark@example.test", "MARK@EXAMPLE.TEST", true},
	} {
		t.Run(tc.verified, func(t *testing.T) {
			d := dbtest.Open(t)
			ctx := dbtest.Seed(t.Context())
			tid, err := tenantbootstrap.Create(ctx, d.App, "bootstrap-email", "Bootstrap email")
			if err != nil {
				t.Fatal(err)
			}
			m := &Module{pool: d.App, inTenant: db.InTenant, cfg: Config{
				BootstrapTenantSlug: "bootstrap-email", BootstrapAdminEmail: tc.configured,
			}}
			p, identityID, err := m.resolveOIDCPerson(ctx, tid, "bootstrap-email", "https://id.example.test", "verified", tc.verified, "Verified person", true, "")
			if !tc.allowed {
				if !errors.Is(err, errNotMember) {
					t.Fatalf("different mailbox became bootstrap admin: %v", err)
				}
				var people, bindings int
				if err := d.Admin.QueryRow(ctx, `SELECT
					(SELECT count(*) FROM principals WHERE tenant_id=$1 AND kind='person'),
					(SELECT count(*) FROM role_bindings WHERE tenant_id=$1)`, tid).Scan(&people, &bindings); err != nil {
					t.Fatal(err)
				}
				if people != 0 || bindings != 0 {
					t.Fatalf("denied bootstrap created access: people=%d bindings=%d", people, bindings)
				}
				return
			}
			if err != nil || !slices.Equal(p.Roles, []string{"admin"}) {
				t.Fatalf("ASCII bootstrap failed: roles=%v err=%v", p.Roles, err)
			}
			if _, err := m.startSession(ctx, identityID, tid, p.ID); err != nil {
				t.Fatal(err)
			}
			var role string
			if err := d.Admin.QueryRow(ctx, `SELECT r.key FROM role_bindings b JOIN roles r ON r.id=b.role_id
				WHERE b.tenant_id=$1 AND b.principal_id=$2 AND b.scope_type='workspace'`, tid, p.ID).Scan(&role); err != nil || role != "admin" {
				t.Fatalf("ASCII bootstrap session role=%s err=%v", role, err)
			}
		})
	}
}

func TestDevLoginRequiresASCIIMailboxMatch(t *testing.T) {
	for _, tc := range []struct{ stored, other, ascii string }{
		{"admin@example.test", "admİn@example.test", "ADMIN@EXAMPLE.TEST"},
		{"mark@example.test", "marK@example.test", "MARK@EXAMPLE.TEST"},
		{"sam@example.test", "ſam@example.test", "SAM@EXAMPLE.TEST"},
	} {
		for _, principalEmail := range []bool{false, true} {
			name := tc.other + "/identity-email"
			if principalEmail {
				name = tc.other + "/principal-email"
			}
			t.Run(name, func(t *testing.T) {
				d := dbtest.Open(t)
				ctx := dbtest.Seed(t.Context())
				tid, err := tenantbootstrap.Create(ctx, d.App, "dev-email", "Dev email")
				if err != nil {
					t.Fatal(err)
				}
				var id string
				if err := db.InTenant(ctx, d.App, tid, func(tx pgx.Tx) error {
					var identityID string
					email, identityEmail := "", tc.stored
					if principalEmail {
						email, identityEmail = tc.stored, "previous@example.test"
					}
					if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('test','admin',$1) RETURNING id::text`, identityEmail).Scan(&identityID); err != nil {
						return err
					}
					return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,identity_id,name,email,roles)
						VALUES($1,'person',$2,'Admin',$3,ARRAY['admin']) RETURNING id::text`, tid, identityID, email).Scan(&id)
				}); err != nil {
					t.Fatal(err)
				}
				m, err := New(Config{Env: envDev, SessionKey: bytes.Repeat([]byte{7}, 32), BootstrapTenantSlug: "dev-email", BootstrapAdminEmail: "root@example.test"}, d.App)
				if err != nil {
					t.Fatal(err)
				}
				app := startApp(t, m)
				client := newHTTPClient()
				body, _ := json.Marshal(map[string]string{"email": tc.other})
				status, _, _ := do(t, client, http.MethodPost, app.URL+"/api/auth/dev-login", string(body), nil)
				if status != http.StatusForbidden {
					t.Fatalf("different Unicode mailbox opened existing admin session: %d", status)
				}
				if status, _, _ := do(t, client, http.MethodGet, app.URL+"/api/me", "", nil); status != http.StatusUnauthorized {
					t.Fatalf("denied dev login retained a session: %d", status)
				}
				body, _ = json.Marshal(map[string]string{"email": tc.ascii})
				status, response, _ := do(t, client, http.MethodPost, app.URL+"/api/auth/dev-login", string(body), nil)
				var me meJSON
				if err := json.Unmarshal(response, &me); err != nil || status != http.StatusOK || me.Principal.ID != id {
					t.Fatalf("ASCII dev login failed: status=%d principal=%s err=%v", status, me.Principal.ID, err)
				}
			})
		}
	}
}

func TestDevIdentitySubjectPreservesUnicodeMailbox(t *testing.T) {
	for _, tc := range []struct{ configured, input, subject, other string }{
		{"marK@example.test", "MARK@EXAMPLE.TEST", "marK@example.test", "mark@example.test"},
		{"admİn@example.test", "ADMİN@EXAMPLE.TEST", "admİn@example.test", "admin@example.test"},
	} {
		t.Run(tc.configured, func(t *testing.T) {
			d := dbtest.Open(t)
			tid, err := tenantbootstrap.Create(t.Context(), d.App, "dev-subject", "Dev subject")
			if err != nil {
				t.Fatal(err)
			}
			m, err := New(Config{Env: envDev, SessionKey: bytes.Repeat([]byte{7}, 32), BootstrapTenantSlug: "dev-subject", BootstrapAdminEmail: tc.configured}, d.App)
			if err != nil {
				t.Fatal(err)
			}
			app := startApp(t, m)
			body, _ := json.Marshal(map[string]string{"email": tc.input})
			if status, _, _ := do(t, newHTTPClient(), http.MethodPost, app.URL+"/api/auth/dev-login", string(body), nil); status != http.StatusOK {
				t.Fatalf("exact Unicode mailbox could not bootstrap: %d", status)
			}
			var subject string
			if err := d.Admin.QueryRow(t.Context(), `SELECT i.subject FROM principals p JOIN identities i ON i.id=p.identity_id WHERE p.tenant_id=$1 AND i.issuer=$2`, tid, devIssuer).Scan(&subject); err != nil || subject != tc.subject {
				t.Fatalf("dev identity folded Unicode: subject=%s err=%v", subject, err)
			}
			body, _ = json.Marshal(map[string]string{"email": tc.other})
			if status, _, _ := do(t, newHTTPClient(), http.MethodPost, app.URL+"/api/auth/dev-login", string(body), nil); status != http.StatusForbidden {
				t.Fatalf("ASCII mailbox opened Unicode person's session: %d", status)
			}
		})
	}
}
