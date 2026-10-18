// SPDX-License-Identifier: AGPL-3.0-only

package directory

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/business/costunits"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestDirectoryListsTenantPrincipalsForStaff(t *testing.T) {
	d := dbtest.Open(t)
	plug, err := costunits.Plugin()
	if err != nil {
		t.Fatal(err)
	}
	reg := plugins.NewRegistry()
	if err := reg.Register(plug); err != nil {
		t.Fatal(err)
	}
	reg.Seal()
	mux := http.NewServeMux()
	New(d.App, reg).Mount(mux)

	ids := map[string]string{}
	tenants := map[string]string{}
	for _, slug := range []string{"dir-a", "dir-b"} {
		var id string
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,$1) RETURNING id::text`, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		tenants[slug] = id
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenants["dir-a"], func(tx pgx.Tx) error {
		for _, p := range []struct{ key, kind, name, role string }{
			{"admin", "person", "Ada Admin", "admin"}, {"member", "person", "Mia Member", "member"},
			{"customer", "person", "Cleo Customer", "customer"}, {"agent", "agent", "Nova", ""},
		} {
			roles := []string{}
			if p.role != "" {
				roles = []string{p.role}
			}
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,$3,$4) RETURNING id::text`, tenants["dir-a"], p.kind, p.name, roles).Scan(&id); err != nil {
				return err
			}
			ids[p.key] = id
		}
		// Mia has a picture; the others have none (U27: clients ask only then).
		_, err := tx.Exec(t.Context(), `INSERT INTO personal_profiles(tenant_id,principal_id,avatar_original_hash,avatar_hashes) VALUES($1,$2,$3,$4::jsonb)`,
			tenants["dir-a"], ids["member"], strings.Repeat("a", 64), `{"32":"`+strings.Repeat("b", 64)+`"}`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{ids["admin"], ids["member"], ids["customer"]} {
		dbtest.BindLegacy(t, d, tenants["dir-a"], id)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tenants["dir-b"], func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other tenant',ARRAY['admin']) RETURNING id::text`, tenants["dir-b"]).Scan(&id); err != nil {
			return err
		}
		ids["other"] = id
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, tenants["dir-b"], ids["other"])
	install := func(tenantID string, enabled bool, digest string) {
		t.Helper()
		err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO plugin_installations(tenant_id,plugin_id,version,manifest_digest_sha256,owner,enabled,permissions,updated_by_principal_id)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (tenant_id, plugin_id) DO UPDATE SET enabled = EXCLUDED.enabled, manifest_digest_sha256 = EXCLUDED.manifest_digest_sha256`,
				tenantID, plug.Manifest.ID, plug.Manifest.Version, digest, plug.Manifest.Owner, enabled, plug.Manifest.Permissions, ids["admin"])
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	call := func(p tenant.Principal) (int, []Principal) {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/business/principals", nil)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out []Principal
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	as := func(key string, kind tenant.PrincipalKind, roles ...string) tenant.Principal {
		return tenant.Principal{ID: ids[key], TenantID: tenants["dir-a"], Kind: kind, Roles: roles}
	}

	// Closed until a business plugin is enabled at its digest.
	if status, _ := call(as("admin", tenant.Person, "admin")); status != 403 {
		t.Fatalf("no installation %d", status)
	}
	install(tenants["dir-a"], true, "0000000000000000000000000000000000000000000000000000000000000000")
	if status, _ := call(as("admin", tenant.Person, "admin")); status != 403 {
		t.Fatalf("digest mismatch %d", status)
	}
	install(tenants["dir-a"], true, plug.Manifest.DigestSHA256)
	status, out := call(as("member", tenant.Person, "member"))
	if status != 200 || len(out) != 5 {
		t.Fatalf("member list %d %+v", status, out)
	}
	// People first, then agents; names only from this tenant.
	if out[0].Kind != "person" || out[3].Kind != "agent" || out[3].Name != "Nova" || len(out[3].Roles) != 0 {
		t.Fatalf("order %+v", out)
	}
	if out[4].Kind != "agent" || out[4].Name != "System" || len(out[4].Roles) != 1 || out[4].Roles[0] != "system" {
		t.Fatalf("bootstrap audit actor %+v", out[4])
	}
	for _, item := range out {
		if item.ID == ids["other"] {
			t.Fatal("another tenant's principal listed")
		}
		if item.HasAvatar != (item.ID == ids["member"]) {
			t.Fatalf("has_avatar %+v", item)
		}
	}
	if status, _ := call(as("customer", tenant.Person, "customer")); status != 403 {
		t.Fatalf("customer %d", status)
	}
	if status, _ := call(as("agent", tenant.Agent)); status != 403 {
		t.Fatalf("agent %d", status)
	}
	install(tenants["dir-a"], false, plug.Manifest.DigestSHA256)
	if status, _ := call(as("admin", tenant.Person, "admin")); status != 403 {
		t.Fatalf("disabled %d", status)
	}
}
