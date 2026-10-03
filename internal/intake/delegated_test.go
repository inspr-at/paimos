// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
	"github.com/inspr-at/paimos/internal/tenant"
)

const authNow int64 = 1790000000

type authorityDouble struct {
	state SessionAuthority
	err   error
}

func (s *authorityDouble) LockAuthority(_ context.Context, _ pgx.Tx, _, _ string) (SessionAuthority, error) {
	return s.state, s.err
}

func delegatedFixture(t *testing.T) (*tokens.KeySet, tokens.Claims, SessionAuthority) {
	t.Helper()
	raw, err := os.ReadFile("testdata/token.delegated.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Doc struct {
			Claims json.RawMessage `json:"claims"`
		} `json:"doc"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	c, err := tokens.ValidateClaims(tokens.Delegated, vector.Doc.Claims)
	if err != nil {
		t.Fatal(err)
	}
	// Aeon uses UUID principal/project/tenant identities; other fixture claims
	// are retained exactly, including generation, epoch and capability strings.
	c.Subject = "10000000-0000-4000-8000-000000000001"
	c.Actor = &tokens.Actor{Subject: "10000000-0000-4000-8000-000000000002"}
	c.TenantID = "10000000-0000-4000-8000-000000000003"
	c.ProjectID = "10000000-0000-4000-8000-000000000004"
	keys, err := tokens.New(t.Context(), &tokens.MemoryStore{TenantID: c.TenantID}, bytes.Repeat([]byte{7}, 32), tokens.Config{
		Issuer: c.Issuer, Audience: c.Audience, Clock: func() time.Time { return time.Unix(authNow, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	a := SessionAuthority{TenantID: c.TenantID, ProjectID: c.ProjectID, SessionID: c.SessionID,
		PluginPrincipalID: c.Subject, RequesterPrincipalID: c.Actor.Subject, Generation: c.Generation, AuthEpoch: c.AuthEpoch}
	a.Grant = &EphemeralGrant{TenantID: a.TenantID, ProjectID: a.ProjectID, SessionID: a.SessionID,
		PluginPrincipalID: a.PluginPrincipalID, RequesterPrincipalID: a.RequesterPrincipalID,
		Generation: a.Generation, AuthEpoch: a.AuthEpoch, ExpiresAt: time.Unix(authNow+600, 0)}
	return keys, c, a
}

func assertErrorCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response struct{ Error, Code string }
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || w.Code != status || response.Code != code || response.Error == "" {
		t.Fatalf("expected %d/%s, got %d %s", status, code, w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("error response must not be cached")
	}
}

func TestDelegatedRouteMatrix(t *testing.T) {
	keys, c, a := delegatedFixture(t)
	m := NewDelegated(nil, keys, &authorityDouble{state: a})
	// Literal matrix, independently compared with the copied public contract.
	routes := []struct {
		method, suffix, capability string
		write                      bool
	}{
		{"POST", "/sources", scopeWrite, true}, {"POST", "/transcript-turns", scopeWrite, true},
		{"POST", "/drafts", scopeWrite, true}, {"POST", "/drafts/10000000-0000-4000-8000-000000000005/replace", scopeWrite, true},
		{"GET", "", scopeRead, false},
	}
	for _, route := range routes {
		t.Run(route.method+route.suffix, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc(route.method+" /api/projects/{projectId}/intake"+route.suffix, m.delegated(func(w http.ResponseWriter, r *http.Request) {
				got, ok := delegatedClaims(r.Context())
				if !ok {
					t.Fatal("delegation missing")
				}
				p, ok := tenant.PrincipalFrom(r.Context())
				if !ok || p.ID != c.Subject || p.KeyCreatorID != c.Actor.Subject {
					t.Fatal("wrong principal binding")
				}
				writeResult(w, 200, map[string]bool{"ok": true}, checkAuthority(got, a, route.write, time.Unix(authNow, 0)))
			}, route.capability))
			for _, caps := range [][]string{{scopeRead}, {scopeWrite}, {scopeRead, scopeWrite}} {
				claims := c
				claims.Capabilities = caps
				token, err := keys.MintDelegated(t.Context(), claims)
				if err != nil {
					t.Fatal(err)
				}
				w := call(mux, tenant.Principal{}, token, route.method, "/api/projects/"+c.ProjectID+"/intake"+route.suffix, "")
				if contains(caps, route.capability) {
					if w.Code != 200 {
						t.Fatalf("allowed call: %d %s", w.Code, w.Body.String())
					}
				} else {
					assertErrorCode(t, w, 403, "forbidden")
				}
			}
		})
	}
	var contract struct {
		Routes []struct {
			Class, Capability string
			Checks            []string
		}
	}
	raw, err := os.ReadFile("testdata/capabilities.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	for _, r := range contract.Routes {
		if r.Capability == scopeWrite {
			if r.Class != "write" || !contains(r.Checks, "gen") || !contains(r.Checks, "ephemeral LiveGrant") {
				t.Fatal("write contract drift")
			}
		}
		if r.Capability == scopeRead && (r.Class != "read" || contains(r.Checks, "gen")) {
			t.Fatal("read contract drift")
		}
	}
}

func TestSessionAuthorityRefusals(t *testing.T) {
	_, c, base := delegatedFixture(t)
	tests := []struct {
		name   string
		change func(*SessionAuthority)
		write  bool
		code   string
	}{
		{"tenant", func(a *SessionAuthority) { a.TenantID = "foreign" }, true, "forbidden"},
		{"project", func(a *SessionAuthority) { a.ProjectID = "foreign" }, true, "forbidden"},
		{"session", func(a *SessionAuthority) { a.SessionID = "foreign" }, true, "forbidden"},
		{"plugin", func(a *SessionAuthority) { a.PluginPrincipalID = "foreign" }, true, "forbidden"},
		{"requester", func(a *SessionAuthority) { a.RequesterPrincipalID = "foreign" }, true, "forbidden"},
		{"epoch-write", func(a *SessionAuthority) { a.AuthEpoch++ }, true, "revoked"},
		{"epoch-read", func(a *SessionAuthority) { a.AuthEpoch++ }, false, "revoked"},
		{"tombstone", func(a *SessionAuthority) { a.Revoked = true }, true, "revoked"},
		{"tombstone-read", func(a *SessionAuthority) { a.Revoked = true }, false, "revoked"},
		{"generation-write", func(a *SessionAuthority) { a.Generation++ }, true, "fenced_generation"},
		{"generation-read", func(a *SessionAuthority) { a.Generation++ }, false, ""},
		{"grant-missing", func(a *SessionAuthority) { a.Grant = nil }, true, "forbidden"},
		{"grant-expired", func(a *SessionAuthority) { a.Grant.ExpiresAt = time.Unix(authNow, 0) }, true, "forbidden"},
		{"grant-generation", func(a *SessionAuthority) { a.Grant.Generation++ }, true, "forbidden"},
		{"grant-epoch", func(a *SessionAuthority) { a.Grant.AuthEpoch++ }, true, "forbidden"},
		{"grant-session", func(a *SessionAuthority) { a.Grant.SessionID = "foreign" }, true, "forbidden"},
		{"grant-tenant", func(a *SessionAuthority) { a.Grant.TenantID = "foreign" }, true, "forbidden"},
		{"grant-project", func(a *SessionAuthority) { a.Grant.ProjectID = "foreign" }, true, "forbidden"},
		{"grant-plugin", func(a *SessionAuthority) { a.Grant.PluginPrincipalID = "foreign" }, true, "forbidden"},
		{"grant-requester", func(a *SessionAuthority) { a.Grant.RequesterPrincipalID = "foreign" }, true, "forbidden"},
		{"read-without-grant", func(a *SessionAuthority) { a.Grant = nil }, false, ""},
		{"revocation-precedes-generation", func(a *SessionAuthority) { a.AuthEpoch++; a.Generation++ }, true, "revoked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := base
			g := *base.Grant
			a.Grant = &g
			tt.change(&a)
			err := checkAuthority(c, a, tt.write, time.Unix(authNow, 0))
			if tt.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			status := 409
			if tt.code == "forbidden" {
				status = 403
			}
			w := httptest.NewRecorder()
			writeResult(w, 0, nil, err)
			assertErrorCode(t, w, status, tt.code)
		})
	}
}

func TestDelegatedTokenBoundary(t *testing.T) {
	keys, c, a := delegatedFixture(t)
	m := NewDelegated(nil, keys, &authorityDouble{state: a})
	token, err := keys.MintDelegated(t.Context(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name, header, capability string
		current                  tenant.Principal
		status                   int
		code                     string
	}{
		{"invalid-signature", token[:len(token)-4] + "AAAA", scopeRead, tenant.Principal{}, 401, "unauthenticated"},
		{"malformed", "garbage", scopeRead, tenant.Principal{}, 401, "unauthenticated"},
		{"cookie-and-delegation", token, scopeRead, tenant.Principal{ID: c.Actor.Subject, TenantID: c.TenantID, Kind: tenant.Person}, 403, "forbidden"},
		{"accept-delegated", token, "", tenant.Principal{}, 403, "forbidden"},
		{"accept-key-with-cookie", "aeon_fake_key", "", tenant.Principal{ID: c.Actor.Subject, TenantID: c.TenantID, Kind: tenant.Person}, 403, "forbidden"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/projects/{projectId}/intake", m.delegated(func(http.ResponseWriter, *http.Request) { t.Fatal("denied request reached handler") }, tt.capability))
			w := call(mux, tt.current, tt.header, "GET", "/api/projects/"+c.ProjectID+"/intake", "")
			assertErrorCode(t, w, tt.status, tt.code)
		})
	}
	ctx := context.WithValue(t.Context(), delegatedKey{}, c)
	in := draftWrite{}
	if err := bindRequester(ctx, &in); err != nil || in.RequesterPrincipalID == nil || *in.RequesterPrincipalID != c.Actor.Subject {
		t.Fatal("requester not derived")
	}
	other := c.Subject
	in.RequesterPrincipalID = &other
	if err := bindRequester(ctx, &in); err == nil {
		t.Fatal("forged requester accepted")
	}
	if err := bindRequester(t.Context(), &in); err == nil {
		t.Fatal("ordinary key asserted requester")
	}
}

func TestIntakeErrorCatalogue(t *testing.T) {
	var catalog struct {
		Codes []struct {
			Code string
			HTTP int `json:"http"`
		} `json:"codes"`
	}
	raw, err := os.ReadFile("testdata/error-codes.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, entry := range catalog.Codes {
		t.Run(entry.Code, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeResult(w, 0, nil, refusal(entry.HTTP, entry.Code, entry.Code))
			assertErrorCode(t, w, entry.HTTP, entry.Code)
		})
	}
	for _, status := range []int{400, 401, 403, 404, 409, 413, 500, 503} {
		w := httptest.NewRecorder()
		writeResult(w, 0, nil, fail(status, "transport failure"))
		var he *httpError
		if !errors.As(fail(status, "transport failure"), &he) {
			t.Fatal("missing typed error")
		}
		assertErrorCode(t, w, status, he.code)
	}
	// Prevent accidently documenting a wider token permission ceiling.
	if strings.Contains(string(raw), "nodes.write") {
		t.Fatal("unexpected catalogue")
	}
}
