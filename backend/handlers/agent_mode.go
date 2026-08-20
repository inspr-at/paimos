// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
)

func AgentModeDeliveries(w http.ResponseWriter, r *http.Request) {
	serveAgentModeSnapshot(w, r, false, false)
}

func AgentModeProjectDeliveries(w http.ResponseWriter, r *http.Request) {
	serveAgentModeSnapshot(w, r, true, false)
}

func AgentModeDelivery(w http.ResponseWriter, r *http.Request) {
	serveAgentModeSnapshot(w, r, false, true)
}

func serveAgentModeSnapshot(w http.ResponseWriter, r *http.Request, projectRoute, detailRoute bool) {
	request, err := agentModeRequest(r, projectRoute, detailRoute)
	if err != nil {
		agentModeError(w, r, err)
		return
	}
	result, err := agentmode.NewReader(db.DB, agentmode.ReaderOptions{Freshness: deliveryFreshnessPolicy()}).Read(r.Context(), request)
	if err != nil {
		agentModeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	jsonOK(w, result)
}

func agentModeRequest(r *http.Request, projectRoute, detailRoute bool) (agentmode.Request, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return agentmode.Request{}, agentmode.ErrInvalid
	}
	return agentModeRequestValues(r, values, projectRoute, detailRoute)
}

func agentModeRequestValues(r *http.Request, values url.Values, projectRoute, detailRoute bool) (agentmode.Request, error) {
	user := auth.GetUser(r)
	if user == nil || user.ID <= 0 {
		return agentmode.Request{}, agentmode.ErrNotFound
	}
	filters, err := agentmode.ParseFilters(values)
	if err != nil {
		return agentmode.Request{}, err
	}
	request := agentmode.Request{UserID: user.ID, Filters: filters}
	if projectRoute {
		raw := strings.TrimSpace(chi.URLParam(r, "projectID"))
		projectID, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || projectID <= 0 {
			return agentmode.Request{}, agentmode.ErrInvalid
		}
		if filters.ProjectID != nil && *filters.ProjectID != projectID {
			return agentmode.Request{}, agentmode.ErrInvalid
		}
		request.RouteProjectID = &projectID
	}
	if detailRoute {
		request.DetailDeliveryKey = strings.TrimSpace(chi.URLParam(r, "deliveryKey"))
		if request.DetailDeliveryKey == "" {
			return agentmode.Request{}, agentmode.ErrInvalid
		}
	}
	return request, nil
}

func agentModeError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "private, no-store")
	switch {
	case errors.Is(err, agentmode.ErrInvalid), errors.Is(err, agentmode.ErrCursor):
		problemJSON(w, r, ProblemDetails{Status: http.StatusBadRequest, Detail: "invalid Agent Mode request"})
	case errors.Is(err, agentmode.ErrNotFound), errors.Is(err, agentmode.ErrUnauthorized):
		httpcontract.WriteAgentModeNotFound(w, r)
	default:
		log.Printf("agent mode snapshot: %v", err)
		problemJSON(w, r, ProblemDetails{Status: http.StatusInternalServerError, Detail: "Agent Mode snapshot unavailable"})
	}
}
