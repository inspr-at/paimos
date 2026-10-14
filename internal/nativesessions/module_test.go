// SPDX-License-Identifier: AGPL-3.0-only

package nativesessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativesessions"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

type fakeStore struct {
	nativecontract.Store
	starts    int
	mutations int
	result    *nativecontract.OperationResult
	failure   error
}

func (s *fakeStore) Start(context.Context, nativecontract.Actor, nativecontract.StartIntent) (nativecontract.OperationResult, error) {
	s.starts++
	if s.failure != nil {
		return nativecontract.OperationResult{}, s.failure
	}
	if s.result != nil {
		return *s.result, nil
	}
	return nativecontract.OperationResult{}, errors.New("native start must be unreachable")
}

type storeFailure struct {
	status int
	reason nativecontract.Reason
}

func (e storeFailure) Error() string   { return string(e.reason) }
func (e storeFailure) HTTPStatus() int { return e.status }
func (e storeFailure) Unwrap() error {
	return &nativecontract.ValidationError{Reason: e.reason, Path: "$"}
}

func (s *fakeStore) Mutate(context.Context, nativecontract.Actor, nativecontract.Command) (nativecontract.OperationResult, error) {
	s.mutations++
	return nativecontract.OperationResult{}, &nativecontract.ValidationError{Reason: nativecontract.ReasonGrantMissing, Path: "$"}
}

type fakeFrames struct{}

func (fakeFrames) Subscribe(context.Context, nativecontract.Binding) (nativecontract.Subscription, error) {
	return nil, errors.New("unused frame source")
}

type fakeFinals struct{}

func (fakeFinals) Persist(context.Context, nativecontract.FinalDelivery) (nativecontract.FinalReceipt, error) {
	return nativecontract.FinalReceipt{}, errors.New("unused final sink")
}

// Risk: enabling the C0 constructor dispatches unauthenticated or cross-target
// writes, or reports an accepted start as evidence of a launched native writer.
func TestQualifiedNativeRoutesBindCallerAndTargetBeforeDispatch(t *testing.T) {
	const project = "01900000-0000-4000-8000-000000000001"
	const tenantID = "01900000-0000-4000-8000-000000000002"
	const personID = "01900000-0000-4000-8000-000000000003"
	const sessionID = "01900000-0000-4000-8000-000000000004"
	const requestID = "01900000-0000-4000-8000-000000000005"
	p := tenant.Principal{ID: personID, TenantID: tenantID, Kind: tenant.Person}
	store := &fakeStore{result: &nativecontract.OperationResult{Receipt: nativecontract.Receipt{RequestID: requestID, OperationID: requestID, Operation: "start", State: "accepted", SourceSequence: "0", UpdatedAt: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}}}
	module, err := nativesessions.New(nativecontract.Services{Store: store, Frames: fakeFrames{}, Finals: fakeFinals{}, Qualified: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	module.Mount(mux)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Person") == "yes" {
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		mux.ServeHTTP(w, r)
	}))
	defer server.Close()
	post := func(path string, value any, authenticated bool, wantStatus int, wantReason string) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+path, strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			r.Header.Set("X-Test-Person", "yes")
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 8193))
		if err != nil || len(body) > 8192 {
			t.Fatal("response bound", err)
		}
		if response.StatusCode != wantStatus || wantReason != "" && !strings.Contains(string(body), `"reason":"`+wantReason+`"`) {
			t.Fatalf("HTTP %d %s; want %d/%s", response.StatusCode, body, wantStatus, wantReason)
		}
		if response.Header.Get(reportercontract.Header) != reportercontract.NativeSession || response.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("native headers missing")
		}
	}
	start := nativecontract.StartIntent{RequestID: requestID, Role: "coordinator", Profile: "native", Harness: "claude", ComputerID: requestID, AccountID: requestID, WorkspaceID: "approved", ModelProfileID: requestID, PolicyID: requestID}
	post("/api/projects/"+project+"/native-sessions", start, false, 401, "grant_missing")
	if store.starts != 0 {
		t.Fatal("unauthenticated request reached the store")
	}
	post("/api/projects/"+project+"/native-sessions", start, true, 202, "")
	if store.starts != 1 {
		t.Fatal("qualified C0 constructor did not dispatch to start storage")
	}
	for _, denied := range []storeFailure{
		{http.StatusForbidden, nativecontract.ReasonPermissionRevoked},
		{http.StatusNotFound, nativecontract.ReasonInvalidInput},
		{http.StatusConflict, nativecontract.ReasonWrongGeneration},
	} {
		store.failure = denied
		post("/api/projects/"+project+"/native-sessions", start, true, denied.status, string(denied.reason))
	}
	store.failure = nil
	binding := nativecontract.Binding{TenantID: tenantID, ProjectID: project, OwnedSessionID: sessionID, HarnessSessionID: requestID, RuntimeEpoch: requestID, DaemonID: "fixture", DaemonGeneration: "generation-1", BindingRevision: 2}
	input := nativecontract.Input{RequestID: requestID, ExpectedBinding: binding, InputID: requestID, Mode: "now", Text: "person input", ExpiresAt: time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)}
	path := "/api/projects/" + project + "/native-sessions/" + sessionID + "/input"
	input.ExpectedBinding.OwnedSessionID = personID
	post(path, input, true, 409, "identity_mismatch")
	input.ExpectedBinding = binding
	input.ExpectedBinding.TenantID = personID
	post(path, input, true, 409, "identity_mismatch")
	if store.mutations != 0 {
		t.Fatal("cross-target body reached mutation storage")
	}
	input.ExpectedBinding = binding
	post(path, input, true, 403, "grant_missing")
	if store.mutations != 1 {
		t.Fatal("exact target did not reach final store authorization")
	}
	post(path, map[string]any{"invalid": strings.Repeat("x", nativecontract.RequestBodyBytes+1)}, true, 413, "buffer_full")
	if store.mutations != 1 {
		t.Fatal("oversized body reached mutation storage")
	}
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
