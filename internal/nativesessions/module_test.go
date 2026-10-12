// SPDX-License-Identifier: AGPL-3.0-only

package nativesessions_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fakeStore struct {
	nativecontract.Store
	starts int
}

func (s *fakeStore) Start(context.Context, nativecontract.Actor, nativecontract.StartIntent) (nativecontract.OperationResult, error) {
	s.starts++
	return nativecontract.OperationResult{}, errors.New("native start must be unreachable")
}

type unreadBody struct{ reads int }

func (b *unreadBody) Read([]byte) (int, error) {
	b.reads++
	return 0, errors.New("disabled route must not read the body")
}
func (*unreadBody) Close() error { return nil }

// Named risk: contract-first deployment accidentally enables native work before
// runtime qualification. Every route authenticates and remains side-effect free.
func TestDisabledNativeRoutesAuthenticateWithoutStarting(t *testing.T) {
	store := &fakeStore{}
	if module, err := nativesessions.New(nativecontract.Services{Store: store, Qualified: true}); module != nil || err == nil {
		t.Fatal("C0 enabled a qualified-looking service")
	}
	mux := http.NewServeMux()
	nativesessions.NewDisabled().Mount(mux)
	v, err := nativecontract.NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	person := tenant.Principal{ID: "00000000-0000-4000-8000-000000000001", TenantID: "00000000-0000-4000-8000-000000000002", Kind: tenant.Person}
	for _, route := range nativecontract.PublicRoutes() {
		t.Run(route.OperationID, func(t *testing.T) {
			path := route.Path
			for _, name := range []string{"projectId", "ownedSessionId", "sessionId", "approvalId", "id"} {
				path = strings.ReplaceAll(path, "{"+name+"}", person.ID)
			}
			for _, authenticated := range []bool{false, true} {
				body := &unreadBody{}
				request := httptest.NewRequest(route.Method, path, nil)
				request.Body = body
				if authenticated {
					request = request.WithContext(tenant.WithPrincipal(request.Context(), person))
				}
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, request)
				want := http.StatusUnauthorized
				if authenticated {
					want = http.StatusServiceUnavailable
				}
				if response.Code != want {
					t.Fatalf("status %d, want %d: %s", response.Code, want, response.Body.String())
				}
				if response.Header().Get(reportercontract.Header) != reportercontract.NativeSession || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatal("contract/cache header missing before authentication")
				}
				if err := v.ValidateJSON("NativeError", response.Body.Bytes()); err != nil {
					t.Fatal(err)
				}
				if authenticated && response.Body.String() != `{"reason":"feature_disabled","request_id":null,"retryable":false}`+"\n" {
					t.Fatal("disabled response is not the exact native contract")
				}
				if body.reads != 0 || store.starts != 0 {
					t.Fatal("disabled handler read input or started a native runtime")
				}
			}
		})
	}
	agent := person
	agent.Kind = tenant.Agent
	agent.OwnerWorkstation = true
	for _, path := range []string{"/api/projects/" + person.ID + "/native-sessions/" + person.ID + "/stream?view=terminal", "/api/sessions/" + person.ID + "/control/end"} {
		method := http.MethodGet
		if strings.HasSuffix(path, "/end") {
			method = http.MethodPost
		}
		request := httptest.NewRequest(method, path, nil).WithContext(tenant.WithPrincipal(context.Background(), agent))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"reason":"wrong_principal"`) {
			t.Fatal("agent can reach a person-only terminal/control surface")
		}
	}
	if store.starts != 0 {
		t.Fatalf("native starts = %d; want zero", store.starts)
	}
	// Credentials are absent or malformed, so this exercises the complete
	// server/auth boundary with no database and no injected principal.
	authMod, err := auth.New(auth.Config{SessionKey: []byte(strings.Repeat("x", 32))}, nil)
	if err != nil {
		t.Fatal(err)
	}
	production := (&httpapi.Server{
		Modules:    []httpapi.Module{authMod, nativesessions.NewDisabled()},
		Middleware: []func(http.Handler) http.Handler{authMod.Middleware},
	}).Handler()
	for _, route := range nativecontract.PublicRoutes() {
		t.Run("production authentication/"+route.OperationID, func(t *testing.T) {
			path := route.Path
			for _, name := range []string{"projectId", "ownedSessionId", "sessionId", "approvalId", "id"} {
				path = strings.ReplaceAll(path, "{"+name+"}", person.ID)
			}
			for _, credential := range []string{"none", "stale cookie", "malformed bearer"} {
				request := httptest.NewRequest(route.Method, path, nil)
				body := &unreadBody{}
				request.Body = body
				switch credential {
				case "stale cookie":
					request.AddCookie(&http.Cookie{Name: "aeon_session", Value: "not-a-session"})
				case "malformed bearer":
					request.Header.Set("Authorization", "Bearer not-a-key")
				}
				response := httptest.NewRecorder()
				production.ServeHTTP(response, request)
				if response.Code != http.StatusUnauthorized || response.Header().Get(reportercontract.Header) != reportercontract.NativeSession || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("%s: production authentication lost native status/headers: HTTP %d %s", credential, response.Code, response.Body.String())
				}
				if err := v.ValidateJSON("NativeError", response.Body.Bytes()); err != nil {
					t.Fatal("production authentication lost native error shape:", err)
				}
				if response.Body.String() != `{"reason":"grant_missing","request_id":null,"retryable":false}`+"\n" || body.reads != 0 || store.starts != 0 {
					t.Fatal("production authentication changed failure reason or caused native/input effects")
				}
			}
		})
	}
	var _ io.ReadCloser = (*unreadBody)(nil)
}
