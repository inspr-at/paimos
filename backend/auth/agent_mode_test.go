package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/inspr-at/paimos/backend/httpcontract"
	"github.com/inspr-at/paimos/backend/models"
)

func TestRequireAgentModeInternalUsesCanonicalNotFound(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/agent-mode/deliveries/missing", nil)
	request.Header.Set("X-PAIMOS-Request-Id", "request-1")
	request = request.WithContext(context.WithValue(request.Context(), UserKey, &models.User{
		ID: 7, Role: RoleExternal, Status: "active",
	}))
	external := httptest.NewRecorder()
	called := false
	RequireAgentModeInternal(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })).ServeHTTP(external, request)
	if called {
		t.Fatal("external request reached Agent Mode handler")
	}
	canonical := httptest.NewRecorder()
	httpcontract.WriteAgentModeNotFound(canonical, request)
	externalBody, _ := io.ReadAll(external.Result().Body)
	canonicalBody, _ := io.ReadAll(canonical.Result().Body)
	if external.Code != http.StatusNotFound || external.Header().Get("Content-Type") != "application/problem+json" ||
		external.Header().Get("Cache-Control") != "private, no-store" || string(externalBody) != string(canonicalBody) {
		t.Fatalf("external response differs from canonical: status=%d headers=%v body=%s want=%s",
			external.Code, external.Header(), externalBody, canonicalBody)
	}

	internal := httptest.NewRequest(http.MethodGet, "/api/agent-mode/deliveries", nil)
	internal = internal.WithContext(context.WithValue(internal.Context(), UserKey, &models.User{
		ID: 8, Role: RoleMember, Status: "active",
	}))
	passed := httptest.NewRecorder()
	RequireAgentModeInternal(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(passed, internal)
	if passed.Code != http.StatusNoContent {
		t.Fatalf("internal status=%d", passed.Code)
	}
}
