// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenantbootstrap"
)

func TestBootstrapResolverRequiresVerifiedEmail(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	m := newMod(t, Config{BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	baseline := map[string]int{}
	for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
		baseline[table] = scalar(t, adminPool, `SELECT count(*) FROM `+table)
	}
	for _, subject := range []string{"first-unverified", "second-unverified"} {
		if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", subject, "admin@example.com", "Admin", false, ""); !errors.Is(err, errNotMember) {
			t.Fatalf("unverified bootstrap: %v", err)
		}
		for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
			if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != baseline[table] {
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
	// Mailbox verification cannot transfer the first issuer/subject's access.
	for _, later := range []struct {
		issuer, subject string
		verified        bool
	}{
		{"https://issuer.example", "later", false},
		{"https://issuer.example", "later", true},
		{"https://other-issuer.example", "verified", true},
	} {
		if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", later.issuer, later.subject, "admin@example.com", "Later", later.verified, ""); !errors.Is(err, errNotMember) {
			t.Errorf("later issuer/subject (%s, %s, verified=%t): %v", later.issuer, later.subject, later.verified, err)
		}
	}
	for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
		want := 1
		if table == "principals" {
			want = 2
		} // Person plus the system audit actor.
		if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != want {
			t.Fatalf("%s: got %d, want only the verified member", table, n)
		}
	}
	// Membership is pinned to issuer/subject; enrollment rules do not revoke it.
	again, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "verified", "admin@example.com", "Admin", false, "")
	if err != nil || again.ID != p.ID {
		t.Fatalf("existing member: %v", err)
	}
}

func TestBootstrapConcurrentFirstEnrollment(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	baseline := map[string]int{}
	for _, table := range []string{"identities", "principals", "events"} {
		baseline[table] = scalar(t, adminPool, `SELECT count(*) FROM `+table)
	}
	m := newMod(t, Config{BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	start, done := make(chan struct{}), make(chan error, 8)
	for n := range cap(done) {
		go func() {
			<-start
			_, _, err := m.resolveOIDCPerson(ctx, tid, "inspr", "https://issuer.example", fmt.Sprintf("first-%d", n), "admin@example.com", "Admin", true, "")
			done <- err
		}()
	}
	close(start)
	successes := 0
	for range cap(done) {
		select {
		case err := <-done:
			if err == nil {
				successes++
			} else if !errors.Is(err, errNotMember) {
				t.Errorf("concurrent enrollment: %v", err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if successes != 1 {
		t.Fatalf("enrolled %d issuer/subjects, want exactly one", successes)
	}
	for _, table := range []string{"identities", "principals", "events"} {
		if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != baseline[table]+1 {
			t.Errorf("%s: got %d rows, want only the first enrollment", table, n)
		}
	}
}

func TestBootstrapCannotReopenAfterProfileChanges(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	m := newMod(t, Config{BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	p, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "original", "admin@example.com", "Original", true, "")
	if err != nil {
		t.Fatal(err)
	}
	// Even removing the live identity and legacy admin label cannot erase the
	// durable first-enrollment marker and reenable mailbox-based provisioning.
	if _, err := adminPool.Exec(t.Context(), `UPDATE principals SET identity_id=NULL,roles=ARRAY[]::text[] WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "replacement", "admin@example.com", "Replacement", true, ""); !errors.Is(err, errNotMember) {
		t.Fatalf("bootstrap reopened after profile changes: %v", err)
	}
	if n := scalar(t, adminPool, `SELECT count(*) FROM identities`); n != 1 {
		t.Fatalf("refused replacement persisted identity: %d", n)
	}
}

func TestBootstrapHonorsOperatorPinnedIdentity(t *testing.T) {
	reset(t)
	tid := insertTenant(t, "inspr", "INSPR")
	pinned, err := tenantbootstrap.BindOIDC(t.Context(), appPool, "inspr", "https://issuer.example", "pinned", "Pinned person", "admin")
	if err != nil {
		t.Fatal(err)
	}
	m := newMod(t, Config{BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	if _, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "other", "admin@example.com", "Other", true, ""); !errors.Is(err, errNotMember) {
		t.Fatalf("mailbox replaced operator-pinned identity: %v", err)
	}
	p, _, err := m.resolveOIDCPerson(t.Context(), tid, "inspr", "https://issuer.example", "pinned", "changed@example.com", "Pinned person", false, "")
	if err != nil || p.ID != pinned {
		t.Fatalf("pinned identity no longer resolves: %v", err)
	}
}

func TestBootstrapCallbackRequiresVerifiedEmail(t *testing.T) {
	reset(t)
	insertTenant(t, "inspr", "INSPR")
	bootstrapPrincipals := scalar(t, adminPool, `SELECT count(*) FROM principals`)
	issuer := startFakeOIDC(t, "aeon-public")
	m := newMod(t, Config{Env: envDev, OIDCIssuer: issuer.issuer, OIDCClientID: "aeon-public", SessionKey: bytes.Repeat([]byte{5}, 32), BootstrapTenantSlug: "inspr", BootstrapAdminEmail: "admin@example.com"})
	app := startApp(t, m)
	m.cfg.PublicURL = app.URL
	for _, claim := range []string{"absent", "false", "true", "later-absent", "later-false", "later-true"} {
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
			if claim == "true" || claim == "later-absent" || claim == "later-false" || claim == "later-true" {
				count = 1
			}
			for _, table := range []string{"identities", "principals", "role_bindings", "sessions"} {
				want := count
				if table == "principals" {
					want += bootstrapPrincipals
				}
				if n := scalar(t, adminPool, `SELECT count(*) FROM `+table); n != want {
					t.Fatalf("%s: %d rows, want %d", table, n, want)
				}
			}
		})
	}
}
