// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/httpcontract"
	"github.com/inspr-at/paimos/backend/workerfleet"
)

func AgentModeWorkerFleet(w http.ResponseWriter, r *http.Request) {
	serveAgentModeWorkerFleet(w, r, false, workerfleet.SchemaVersion)
}

func AgentModeProjectWorkerFleet(w http.ResponseWriter, r *http.Request) {
	serveAgentModeWorkerFleet(w, r, true, workerfleet.SchemaVersion)
}

func AgentModeWorkerFleetV2(w http.ResponseWriter, r *http.Request) {
	serveAgentModeWorkerFleet(w, r, false, workerfleet.SchemaVersionV2)
}

func AgentModeProjectWorkerFleetV2(w http.ResponseWriter, r *http.Request) {
	serveAgentModeWorkerFleet(w, r, true, workerfleet.SchemaVersionV2)
}

func serveAgentModeWorkerFleet(w http.ResponseWriter, r *http.Request, projectRoute bool, version int) {
	w.Header().Set("Cache-Control", "private, no-store")
	user := auth.GetUser(r)
	if user == nil || user.ID <= 0 {
		httpcontract.WriteAgentModeNotFound(w, r)
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		workerFleetError(w, r, workerfleet.ErrInvalid)
		return
	}
	for key, entries := range values {
		if key != "zoom" || len(entries) != 1 {
			workerFleetError(w, r, workerfleet.ErrInvalid)
			return
		}
	}
	zoom := ""
	if entries, ok := values["zoom"]; ok {
		zoom = entries[0]
		if zoom == "" {
			workerFleetError(w, r, workerfleet.ErrInvalid)
			return
		}
	}
	request := workerfleet.Request{UserID: user.ID, Zoom: zoom}
	if projectRoute {
		projectID, parseErr := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "projectID")), 10, 64)
		if parseErr != nil || projectID <= 0 {
			workerFleetError(w, r, workerfleet.ErrInvalid)
			return
		}
		request.RouteProjectID = &projectID
	}
	reader := workerfleet.NewReader(db.DB, workerfleet.ReaderOptions{LoadTrust: loadWorkerFleetTrust})
	if version == workerfleet.SchemaVersionV2 {
		snapshot, readErr := reader.ReadV2(r.Context(), request)
		if readErr != nil {
			workerFleetError(w, r, readErr)
			return
		}
		jsonOK(w, snapshot)
		return
	}
	snapshot, err := reader.Read(r.Context(), request)
	if err != nil {
		workerFleetError(w, r, err)
		return
	}
	jsonOK(w, snapshot)
}

func loadWorkerFleetTrust(ctx context.Context, tx *sql.Tx, issueIDs []int64, observedAt time.Time) (map[int64]workerfleet.TrustFact, error) {
	snapshot, err := agentmode.LoadBoundedTrust(ctx, db.DB, tx, issueIDs, observedAt, deliveryFreshnessPolicy())
	if err != nil {
		return nil, err
	}
	facts := make(map[int64]workerfleet.TrustFact, len(snapshot))
	for issueID, row := range snapshot {
		observed := row.ObservedAt
		facts[issueID] = workerfleet.TrustFact{ProgressTrusted: row.ProgressTrusted, ETATrusted: row.ETATrusted,
			Reason: row.Suppression, TrustRevision: row.TrustRevision, ObservedAt: &observed,
			Progress: row.Progress, ETA: row.ETA}
	}
	return facts, nil
}

func workerFleetError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Cache-Control", "private, no-store")
	switch {
	case errors.Is(err, workerfleet.ErrInvalid):
		problemJSON(w, r, ProblemDetails{Status: http.StatusBadRequest, Detail: "invalid worker fleet request"})
	case errors.Is(err, workerfleet.ErrNotFound), errors.Is(err, agentmode.ErrNotFound), errors.Is(err, agentmode.ErrUnauthorized):
		httpcontract.WriteAgentModeNotFound(w, r)
	default:
		log.Printf("worker fleet snapshot: %v", err)
		problemJSON(w, r, ProblemDetails{Status: http.StatusInternalServerError, Detail: "worker fleet snapshot unavailable"})
	}
}
