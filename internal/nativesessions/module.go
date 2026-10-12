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
	terminal := r.URL.Query().Get("view") == "terminal"
	personControl := r.Pattern == "POST /api/sessions/{id}/control/end"
	if p.Kind == tenant.Agent && (terminal || personControl) {
		writeReason(w, http.StatusForbidden, nativecontract.ReasonWrongPrincipal)
		return
	}
	writeReason(w, http.StatusServiceUnavailable, nativecontract.ReasonFeatureDisabled)
}

func writeReason(w http.ResponseWriter, status int, reason nativecontract.Reason) {
	httpapi.WriteJSON(w, status, nativecontract.Error{Reason: reason, RequestID: nil, Retryable: false})
}
