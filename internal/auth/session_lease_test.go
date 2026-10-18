// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestPreviewSessionLease(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('preview-session','Preview') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Reader',ARRAY['member']) RETURNING id::text`, p.TenantID).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, p.TenantID, p.ID, "member")
	var identity string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO identities(issuer,subject) VALUES('preview-test','reader') RETURNING id::text`).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{SessionKey: bytes.Repeat([]byte{1}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.WithPrincipal(t.Context(), p)
	cookie, err := m.startSession(ctx, identity, p.TenantID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://app.example.com/api/attachments/fixture/preview", nil).WithContext(ctx)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
	lease, err := m.PreviewSessionLease(r)
	if err != nil {
		t.Fatal(err)
	}
	var before, after time.Time
	if err := d.Admin.QueryRow(t.Context(), `SELECT expires_at FROM sessions WHERE tenant_id=$1`, p.TenantID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	current, err := lease(t.Context())
	if err != nil || current.ID != p.ID || current.TenantID != p.TenantID {
		t.Fatal("valid lease failed")
	}
	if err := d.Admin.QueryRow(t.Context(), `SELECT expires_at FROM sessions WHERE tenant_id=$1`, p.TenantID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !before.Equal(after) {
		t.Fatal("preview renewed session")
	}
	for _, mutation := range []string{
		`UPDATE sessions SET expires_at=now()-interval '1 second' WHERE principal_id=$1`,
		`DELETE FROM sessions WHERE principal_id=$1`,
		`UPDATE principals SET status='deactivated' WHERE id=$1`,
	} {
		cookie, err := m.startSession(ctx, identity, p.TenantID, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "https://app.example.com/api/attachments/fixture/preview", nil).WithContext(ctx)
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: cookie})
		lease, err := m.PreviewSessionLease(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := lease(t.Context()); err != nil {
			t.Fatal("fresh session lease refused")
		}
		if _, err := d.Admin.Exec(t.Context(), mutation, p.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := lease(t.Context()); err == nil {
			t.Fatal("revoked/expired session kept lease")
		}
	}
	// A capability is neither a login cookie nor an agent bearer.
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("A", 43))
	if _, err := m.PreviewSessionLease(r); err == nil {
		t.Fatal("bearer accepted as session lease")
	}
	if _, kind, err := m.authenticate(r); err != nil || kind != credNone {
		t.Fatal("preview token authenticated to API")
	}
}
