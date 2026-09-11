// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package handlers

import (
	"errors"
	"math"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
)

func RegisterHarnessBrowserRoutes(r chi.Router) {
	r.Post("/projects/{id}/harness-sessions/{sessionID}/controls/v1/{kind}", requestHarnessControlV1)
	r.Get("/projects/{id}/harness-sessions/{sessionID}/controls/v1/retire-after-work/{controlID}", getHarnessRetirementV1)
	r.Post("/projects/{id}/harness-sessions/{sessionID}/messages/v1", sendHarnessMessageV1)
	r.Get("/projects/{id}/harness-sessions/{sessionID}/assignment-history/v1", getHarnessAssignmentHistoryV1)
}
func harnessBrowserError(w http.ResponseWriter, err error) {
	status, code := 503, "harness_browser_storage_unavailable"
	switch {
	case errors.Is(err, managedharness.ErrBrowserInvalid):
		status, code = 400, "harness_browser_invalid"
	case errors.Is(err, managedharness.ErrBrowserUnavailable):
		status, code = 403, "harness_browser_unavailable"
	case errors.Is(err, managedharness.ErrBrowserConflict):
		status, code = 409, "harness_browser_conflict"
	}
	writeHarnessJSON(w, status, map[string]string{"error": code})
}
func harnessBrowserContext(w http.ResponseWriter, r *http.Request) (auth.Principal, int64, bool) {
	SetControlCachePolicy(w)
	p, ok := auth.GetPrincipal(r)
	project, valid := strictQueryInt(chi.URLParam(r, "id"), 1, math.MaxInt64)
	if !ok || !valid {
		harnessBrowserError(w, managedharness.ErrBrowserUnavailable)
		return p, 0, false
	}
	return p, project, true
}
func requestHarnessControlV1(w http.ResponseWriter, r *http.Request) {
	p, project, ok := harnessBrowserContext(w, r)
	if !ok {
		return
	}
	var request managedharness.BrowserControlRequest
	if r.URL.RawQuery != "" || DecodeControlJSON(w, r, 1024, &request) != nil {
		harnessBrowserError(w, managedharness.ErrBrowserInvalid)
		return
	}
	service := managedharness.NewService(db.DB)
	if chi.URLParam(r, "kind") == "retire-after-work" {
		out, err := service.RequestRetirementCAS(r.Context(), p, project, chi.URLParam(r, "sessionID"), request)
		if err != nil {
			harnessBrowserError(w, err)
			return
		}
		writeHarnessJSON(w, 200, out)
		return
	}
	out, err := service.RequestControlCAS(r.Context(), p, project, chi.URLParam(r, "sessionID"), chi.URLParam(r, "kind"), request)
	if err != nil {
		harnessBrowserError(w, err)
		return
	}
	writeHarnessJSON(w, 200, out)
}
func getHarnessRetirementV1(w http.ResponseWriter, r *http.Request) {
	p, project, ok := harnessBrowserContext(w, r)
	if !ok || r.URL.RawQuery != "" {
		if ok {
			harnessBrowserError(w, managedharness.ErrBrowserInvalid)
		}
		return
	}
	out, err := managedharness.NewService(db.DB).GetRetirementBrowser(r.Context(), p, project, chi.URLParam(r, "sessionID"), chi.URLParam(r, "controlID"))
	if err != nil {
		harnessBrowserError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, out)
}
func sendHarnessMessageV1(w http.ResponseWriter, r *http.Request) {
	p, project, ok := harnessBrowserContext(w, r)
	if !ok {
		return
	}
	var request managedharness.BrowserMessageRequest
	if r.URL.RawQuery != "" || len(r.Header.Values("X-Paimos-Agent-Name")) != 0 ||
		DecodeControlJSON(w, r, 64*1024, &request) != nil {
		harnessBrowserError(w, managedharness.ErrBrowserInvalid)
		return
	}
	out, err := managedharness.NewService(db.DB).SendBrowserMessageCAS(
		r.Context(), p, project, chi.URLParam(r, "sessionID"), request,
	)
	if err != nil {
		harnessBrowserError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusCreated, out)
}
func getHarnessAssignmentHistoryV1(w http.ResponseWriter, r *http.Request) {
	p, project, ok := harnessBrowserContext(w, r)
	if !ok {
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		harnessBrowserError(w, managedharness.ErrBrowserInvalid)
		return
	}
	after, limit := int64(0), int64(50)
	for key, v := range values {
		if len(v) != 1 {
			harnessBrowserError(w, managedharness.ErrBrowserInvalid)
			return
		}
		var valid bool
		switch key {
		case "after_revision":
			after, valid = strictQueryInt(v[0], 0, math.MaxInt64)
		case "limit":
			limit, valid = strictQueryInt(v[0], 1, 100)
		default:
			valid = false
		}
		if !valid {
			harnessBrowserError(w, managedharness.ErrBrowserInvalid)
			return
		}
	}
	out, err := managedharness.NewService(db.DB).AssignmentHistory(r.Context(), p, project, chi.URLParam(r, "sessionID"), after, int(limit))
	if err != nil {
		harnessBrowserError(w, err)
		return
	}
	writeHarnessJSON(w, 200, out)
}
