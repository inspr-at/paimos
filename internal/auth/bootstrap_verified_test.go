// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"bytes"
	"errors"
	"net/http"
	"testing"
)

func TestBootstrapResolverRequiresVerifiedEmail(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	m := newMod(t, Config{BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	for _, subject := range []string{"first-unverified", "second-unverified"} {
		if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", subject, "admin@example.com", "Admin", false, ""); !errors.Is(err, errNotMember) {
			t.Fatalf("unverified bootstrap: %v", err)
		}
		for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
			if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != 0 {
				t.Fatalf("%s: %d rows after denied enrollment", table, n)
			}
		}
	}
	p, iid, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "verified", "Admin@Example.com", "Admin", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.startSession(t.Context(), iid, tid, p.ID); err != nil {
		t.Fatal(err)
	}
	// A later subject with the same mailbox still needs verification.
	if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "later", "admin@example.com", "Later", false, ""); !errors.Is(err, errNotMember) {
		t.Fatalf("later subject: %v", err)
	}
	for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
		if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != 1 {
			t.Fatalf("%s: got %d, want only the verified member", table, n)
		}
	}
	// Membership is pinned to issuer/subject; enrollment rules do not revoke it.
	again, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "verified", "admin@example.com", "Admin", false, "")
	if err != nil || again.ID != p.ID {
		t.Fatalf("existing member: %v", err)
	}
}

func TestBootstrapCallbackRequiresVerifiedEmail(t *testing.T) {
	reset(t)
	insertTenant(t, "inspr", "INSPR")
	issuer := startFakeOIDC(t, "aeon-public")
	m := newMod(t, Config{Env: envDev, OIDCIssuer: issuer.issuer, OIDCClientID: "aeon-public", SessionKey: bytes.Repeat([]byte{5}, 32), BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	app := startApp(t, m)
	m.cfg.PublicURL = app.URL
	for _, claim := range []string{"absent", "false", "true", "later-absent", "later-false"} {
		t.Run(claim, func(t *testing.T) {
			c := newHTTPClient()
			login, err := c.Get(app.URL + "/api/auth/login")
			if err != nil {
				t.Fatal(err)
			}
			login.Body.Close()
			if login.StatusCode != http.StatusFound {
				t.Fatalf("login: %d", login.StatusCode)
			}
			q := assertAuthURL(t, app.URL, login.Header.Get("Location"))
			issuer.allow(claim, q.Get("code_challenge"), q.Get("nonce"), claim, "admin@example.com", "Admin", false)
			issuer.mu.Lock()
			pending := issuer.codes[claim]
			switch claim {
			case "absent", "later-absent":
				pending.emailVerified = nil
			case "false", "later-false":
				v := false
				pending.emailVerified = &v
			}
			issuer.codes[claim] = pending
			issuer.mu.Unlock()
			status, _, res := do(t, c, http.MethodGet, callbackURL(app.URL, claim, q.Get("state")), "", nil)
			want := "/signin?error=not_member"
			if claim == "true" {
				want = app.URL + "/"
			}
			if status != http.StatusFound || res.Header.Get("Location") != want {
				t.Fatalf("callback: %d, redirect %q", status, res.Header.Get("Location"))
			}
			if claim != "true" {
				for _, cookie := range res.Cookies() {
					if cookie.Name == sessionCookieName && cookie.Value != "" {
						t.Fatal("denied enrollment issued a session cookie")
					}
				}
			}
			count := 0
			if claim == "true" || claim == "later-absent" || claim == "later-false" {
				count = 1
			}
			for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
				if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != count {
					t.Fatalf("%s: %d rows, want %d", table, n, count)
				}
			}
		})
	}
}
