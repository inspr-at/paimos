// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/conversationturns"
	"github.com/inspr-at/paimos/backend/db"
)

func RegisterConversationRoutes(r chi.Router) {
	r.Route("/projects/{id}/conversation/v1", func(r chi.Router) {
		r.Post("/calls", conversationSubmit)
		r.Get("/calls/{callID}", conversationGet)
		r.Get("/calls/{callID}/events", conversationEvents)
		r.Post("/calls/{callID}/cancel", conversationCancel)
	})
	r.Route("/projects/{id}/runtimes/{runtimeID}/conversation/v1", func(r chi.Router) {
		r.Post("/claim", conversationClaim)
		r.Get("/calls/{callID}/control", conversationControl)
		r.Post("/calls/{callID}/events", conversationReport)
	})
}

func conversationError(w http.ResponseWriter, err error) {
	SetControlCachePolicy(w)
	code, status := "conversation_storage_unavailable", http.StatusServiceUnavailable
	switch {
	case errors.Is(err, conversationturns.ErrInvalid):
		code, status = "conversation_invalid", http.StatusBadRequest
	case errors.Is(err, conversationturns.ErrTooLarge):
		code, status = "conversation_too_large", http.StatusRequestEntityTooLarge
	case errors.Is(err, conversationturns.ErrUnavailable):
		code, status = "conversation_unavailable", http.StatusForbidden
	case errors.Is(err, conversationturns.ErrConflict):
		code, status = "conversation_conflict", http.StatusConflict
	}
	writeHarnessJSON(w, status, map[string]string{"error": code})
}

func conversationContext(w http.ResponseWriter, r *http.Request) (auth.Principal, int64, bool) {
	SetControlCachePolicy(w)
	principal, ok := auth.GetPrincipal(r)
	project, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !ok || err != nil || project < 1 || len(r.Header.Values("Cookie")) != 0 {
		conversationError(w, conversationturns.ErrUnavailable)
		return auth.Principal{}, 0, false
	}
	return principal, project, true
}

func conversationActor(r *http.Request) (conversationturns.Actor, bool) {
	issuer := r.Header.Values(conversationturns.ActorIssuerHeader)
	subject := r.Header.Values(conversationturns.ActorSubjectHeader)
	if len(issuer) != 1 || len(subject) != 1 {
		return conversationturns.Actor{}, false
	}
	actor := conversationturns.Actor{Issuer: issuer[0], Subject: subject[0]}
	return actor, actor.Valid()
}

func conversationLease(r *http.Request) string {
	values := r.Header.Values(conversationturns.RuntimeLeaseHeader)
	if len(values) != 1 || values[0] != strings.TrimSpace(values[0]) {
		return ""
	}
	return values[0]
}

func conversationDecode(w http.ResponseWriter, r *http.Request, maximum int64, value any) bool {
	if err := DecodeControlJSON(w, r, maximum, value); err != nil {
		conversationError(w, conversationturns.ErrInvalid)
		return false
	}
	return true
}

func conversationSubmit(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	actor, ok := conversationActor(r)
	if !ok {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	var request conversationturns.Request
	if !conversationDecode(w, r, conversationturns.MaximumRequestJSONBytes, &request) {
		return
	}
	call, created, err := conversationturns.NewService(db.DB).Submit(r.Context(), principal, project, actor, request)
	if err != nil {
		conversationError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	writeHarnessJSON(w, status, call)
}

func conversationGet(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	actor, ok := conversationActor(r)
	if !ok || r.URL.RawQuery != "" {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	call, err := conversationturns.NewService(db.DB).Get(r.Context(), principal, project, actor, chi.URLParam(r, "callID"))
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, call)
}

func exactAfter(query url.Values) (int64, bool) {
	if len(query) == 0 {
		return 0, true
	}
	values, ok := query["after"]
	if !ok || len(query) != 1 || len(values) != 1 || values[0] == "" {
		return 0, false
	}
	after, err := strconv.ParseInt(values[0], 10, 64)
	return after, err == nil && after >= 0
}

func conversationEvents(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	actor, ok := conversationActor(r)
	after, queryOK := exactAfter(r.URL.Query())
	if !ok || !queryOK {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	page, err := conversationturns.NewService(db.DB).Events(r.Context(), principal, project, actor, chi.URLParam(r, "callID"), after)
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, page)
}

func conversationCancel(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	actor, ok := conversationActor(r)
	var empty struct{}
	if !ok || r.URL.RawQuery != "" || !conversationDecode(w, r, 32, &empty) {
		if !ok || r.URL.RawQuery != "" {
			conversationError(w, conversationturns.ErrInvalid)
		}
		return
	}
	call, err := conversationturns.NewService(db.DB).Cancel(r.Context(), principal, project, actor, chi.URLParam(r, "callID"))
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, call)
}

func conversationClaim(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	var request conversationturns.ClaimRequest
	if r.URL.RawQuery != "" || !conversationDecode(w, r, 1024, &request) {
		if r.URL.RawQuery != "" {
			conversationError(w, conversationturns.ErrInvalid)
		}
		return
	}
	claim, err := conversationturns.NewService(db.DB).Claim(r.Context(), principal, project,
		chi.URLParam(r, "runtimeID"), request.Generation, conversationLease(r))
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, claim)
}

func conversationControl(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	values := r.URL.Query()
	execution := values["execution_generation"]
	if len(values) != 1 || len(execution) != 1 || execution[0] == "" {
		conversationError(w, conversationturns.ErrInvalid)
		return
	}
	// Runtime generation is the immutable generation attached to the call;
	// the service resolves it before exposing any control or content.
	var generation string
	if err := db.DB.QueryRowContext(r.Context(), `SELECT runtime_generation FROM conversation_calls
		WHERE project_id=? AND call_id=? AND runtime_id=?`, project, chi.URLParam(r, "callID"), chi.URLParam(r, "runtimeID")).Scan(&generation); err != nil {
		conversationError(w, conversationturns.ErrUnavailable)
		return
	}
	control, err := conversationturns.NewService(db.DB).Control(r.Context(), principal, project, chi.URLParam(r, "runtimeID"),
		generation, conversationLease(r), chi.URLParam(r, "callID"), execution[0])
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, control)
}

func conversationReport(w http.ResponseWriter, r *http.Request) {
	principal, project, ok := conversationContext(w, r)
	if !ok {
		return
	}
	var request conversationturns.ReportRequest
	if r.URL.RawQuery != "" || !conversationDecode(w, r, conversationturns.MaximumReportJSONBytes, &request) {
		if r.URL.RawQuery != "" {
			conversationError(w, conversationturns.ErrInvalid)
		}
		return
	}
	call, err := conversationturns.NewService(db.DB).ReportCall(r.Context(), principal, project,
		chi.URLParam(r, "runtimeID"), conversationLease(r), chi.URLParam(r, "callID"), request)
	if err != nil {
		conversationError(w, err)
		return
	}
	writeHarnessJSON(w, http.StatusOK, call)
}
