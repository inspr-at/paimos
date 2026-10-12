// SPDX-License-Identifier: AGPL-3.0-only

// Package nativesessions mounts the additive owned native session surface.
// C0 provides no storage, supervisor, transport, goroutine or enabled constructor.
package nativesessions

import (
	"net/http"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module is deliberately empty: a disabled module cannot reach native or DB
// services, even when a caller presents credentials or a valid-looking intent.
type Module struct{}

func NewDisabled() *Module { return &Module{} }

// New is the reserved integration seam. Qualification alone cannot enable C0;
// I1/I2 must implement and validate the full authorized services before wiring.
func New(nativecontract.Services) (*Module, error) {
	return nil, &nativecontract.ValidationError{Reason: nativecontract.ReasonFeatureDisabled, Path: "$"}
}

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range nativecontract.PublicRoutes() {
		mux.HandleFunc(route.Method+" "+route.Path, reportercontract.WithHeader(reportercontract.NativeSession, m.disabled))
	}
}

// AuthenticationMiddleware keeps the native envelope around authentication,
// after Server has resolved the exact route pattern. It changes only rejected
// response bodies; authentication, pairing and permission checks stay intact.
func AuthenticationMiddleware(authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		ordinary := authenticate(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			native := false
			for _, route := range nativecontract.PublicRoutes() {
				if r.Pattern == route.Method+" "+route.Path {
					native = true
					break
				}
			}
			if !native {
				ordinary.ServeHTTP(w, r)
				return
			}
			w.Header().Set(reportercontract.Header, reportercontract.NativeSession)
			w.Header().Set("Cache-Control", "no-store")
			response := &authenticationResponse{ResponseWriter: w}
			authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response.authorized = true
				next.ServeHTTP(w, r)
			})).ServeHTTP(response, r)
		})
	}
}

type authenticationResponse struct {
	http.ResponseWriter
	authorized, written, denied bool
}

func (w *authenticationResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *authenticationResponse) WriteHeader(status int) {
	if w.written {
		return
	}
	w.written = true
	if !w.authorized && (status == http.StatusUnauthorized || status == http.StatusForbidden) {
		w.denied = true
		w.Header().Del("Content-Length")
		writeReason(w.ResponseWriter, status, nativecontract.ReasonGrantMissing)
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *authenticationResponse) Write(body []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if w.denied {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

func (w *authenticationResponse) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if !w.denied {
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
}

func (*Module) disabled(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" || p.Kind != tenant.Person && p.Kind != tenant.Agent {
		writeReason(w, http.StatusUnauthorized, nativecontract.ReasonGrantMissing)
		return
	}
	// These denials remain server-side, even for an owner-workstation agent.
	// The disabled response never looks up the requested session or project.
	if len(r.URL.RawQuery) > 2048 {
		writeReason(w, http.StatusRequestEntityTooLarge, nativecontract.ReasonBufferFull)
		return
	}
	view := r.URL.Query().Get("view")
	personOnly := false
	switch r.Pattern {
	case "POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/attach/renew",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/detach",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/take",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/return",
		"POST /api/projects/{projectId}/native-sessions/{ownedSessionId}/approvals/{approvalId}/decision",
		"POST /api/sessions/{id}/control/end":
		personOnly = true
	}
	if p.Kind == tenant.Agent && (view == "terminal" || view == "expert" || personOnly) {
		writeReason(w, http.StatusForbidden, nativecontract.ReasonWrongPrincipal)
		return
	}
	writeReason(w, http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled)
}

func writeReason(w http.ResponseWriter, status int, reason nativecontract.Reason) {
	httpapi.WriteJSON(w, status, nativecontract.Error{Reason: reason, RequestID: nil, Retryable: false})
}
