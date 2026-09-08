// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmode"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/managedharness"
)

func RegisterBaselineBatchRoutes(r chi.Router) {
	r.Route("/projects/{id}/baseline-batches", func(r chi.Router) {
		r.With(auth.RequireProjectView).Get("/", baselineBatchWorkflow)
		r.With(auth.RequireProjectEdit).Post("/opt-in", baselineBatchOptIn)
		r.With(auth.RequireProjectEdit).Post("/import", baselineBatchImport)
		r.With(auth.RequireProjectView).Get("/{draftID}/export", baselineBatchExport)
		r.With(auth.RequireProjectEdit).Patch("/{draftID}", baselineBatchPatch)
		r.With(auth.RequireProjectEdit).Post("/{draftID}/readiness", baselineBatchReadiness)
		r.With(auth.RequireProjectEdit).Post("/{draftID}/review", baselineBatchReview)
		r.With(auth.RequireProjectEdit).Post("/{draftID}/start", baselineBatchStart)
		r.With(auth.RequireProjectView).Get("/batches/{batchID}", baselineBatchGet)
		r.With(auth.RequireProjectEdit).Post("/batches/{batchID}/control", baselineBatchControl)
		r.With(auth.RequireProjectEdit).Post("/batches/{batchID}/reconcile", baselineBatchReconcile)
		r.With(auth.RequireProjectEdit).Post("/batches/{batchID}/built-receipt", baselineBatchBuiltReceipt)
	})
}

func baselineBatchService(r *http.Request) *baselinebatch.Service {
	authorizer := deliveryAuthorizerForRequest(r)
	store := delivery.NewStore(db.DB, delivery.Options{Freshness: deliveryFreshnessPolicy(), Observer: agentmode.NotifyChange, Authorizer: authorizer})
	svc := baselinebatch.NewService(db.DB, nil, store, lifecycleintents.NewService(db.DB),
		managedharness.NewService(db.DB), nil)
	if ext, err := externalStageServiceWithAuthorizer(authorizer); err == nil {
		svc.External = ext
	}
	return svc
}

func baselineBatchActor(r *http.Request) (baselinebatch.Actor, bool) {
	p, ok := auth.GetPrincipal(r)
	if !ok {
		return baselinebatch.Actor{}, false
	}
	return baselinebatch.Actor{
		Kind:                string(p.Kind()),
		UserID:              p.UserID(),
		SessionCredentialID: p.SessionCredentialID(),
		APIKeyID:            p.APIKeyID(),
		Impersonated:        p.Impersonated(),
	}, true
}

func baselineBatchProject(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func baselineBatchError(w http.ResponseWriter, err error) {
	msg, code := "baseline batch unavailable", http.StatusInternalServerError
	switch {
	case errors.Is(err, baselinebatch.ErrInvalid):
		msg, code = err.Error(), http.StatusBadRequest
	case errors.Is(err, baselinebatch.ErrUnauthorized):
		msg, code = "unauthorized", http.StatusUnauthorized
	case errors.Is(err, baselinebatch.ErrForbidden):
		msg, code = err.Error(), http.StatusForbidden
	case errors.Is(err, baselinebatch.ErrNotFound):
		msg, code = "not found", http.StatusNotFound
	case errors.Is(err, baselinebatch.ErrConflict):
		msg, code = err.Error(), http.StatusConflict
	case errors.Is(err, baselinebatch.ErrStale):
		msg, code = err.Error(), http.StatusConflict
	case errors.Is(err, baselinebatch.ErrBlocked):
		msg, code = err.Error(), http.StatusConflict
	case errors.Is(err, baselinebatch.ErrUnavailable):
		msg, code = err.Error(), http.StatusServiceUnavailable
	case isUniqueConstraintError(err):
		// A losing racer collides with a durable uniqueness rule. That is a
		// conflict the caller can act on, never an opaque server failure.
		msg, code = "baseline batch conflict", http.StatusConflict
	}
	jsonError(w, msg, code)
}

func isUniqueConstraintError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func baselineBatchWorkflow(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := baselineBatchService(r).Workflow(r.Context(), actor, projectID)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchOptIn(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		jsonError(w, "invalid opt-in", http.StatusBadRequest)
		return
	}
	out, err := baselineBatchService(r).SetStreamEnabled(r.Context(), actor, projectID, body.Enabled)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchImport(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10+1))
	if err != nil || len(raw) == 0 {
		jsonError(w, "handover is required", http.StatusBadRequest)
		return
	}
	var envelope struct {
		Handover json.RawMessage `json:"handover"`
		Selected []string        `json:"selected_requirement_refs"`
	}
	trimmed := bytes.TrimSpace(raw)
	if json.Unmarshal(trimmed, &envelope) != nil || len(envelope.Handover) == 0 {
		envelope.Handover = trimmed
	}
	out, err := baselineBatchService(r).Import(r.Context(), actor, projectID, baselinebatch.ImportRequest{
		Handover: envelope.Handover,
		Selected: envelope.Selected,
	})
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
	jsonOK(w, out)
}

func baselineBatchPatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	draftID, err := strconv.ParseInt(chi.URLParam(r, "draftID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req baselinebatch.PatchDraftRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		jsonError(w, "invalid patch", http.StatusBadRequest)
		return
	}
	out, err := baselineBatchService(r).PatchDraft(r.Context(), actor, projectID, draftID, req)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchReadiness(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	draftID, err := strconv.ParseInt(chi.URLParam(r, "draftID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := baselineBatchService(r).RequestReadiness(r.Context(), actor, projectID, draftID)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchReview(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	draftID, err := strconv.ParseInt(chi.URLParam(r, "draftID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req baselinebatch.ReviewRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		jsonError(w, "invalid review", http.StatusBadRequest)
		return
	}
	out, err := baselineBatchService(r).Review(r.Context(), actor, projectID, draftID, req)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchStart(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	draftID, err := strconv.ParseInt(chi.URLParam(r, "draftID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req baselinebatch.StartRequest
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		jsonError(w, "invalid start", http.StatusBadRequest)
		return
	}
	if key := strings.TrimSpace(r.Header.Get("Idempotency-Key")); key != "" && req.IdempotencyKey == "" {
		req.IdempotencyKey = key
	}
	req.DraftID = draftID
	out, err := baselineBatchService(r).Start(r.Context(), actor, projectID, req)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchExport(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	draftID, err := strconv.ParseInt(chi.URLParam(r, "draftID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := baselineBatchService(r).Export(r.Context(), actor, projectID, draftID)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	jsonOK(w, out)
}

func baselineBatchGet(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := baselineBatchService(r).GetBatch(r.Context(), actor, projectID, batchID)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchControl(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var body baselinebatch.ControlRequest
	if json.NewDecoder(r.Body).Decode(&body) != nil {
		jsonError(w, "invalid control", http.StatusBadRequest)
		return
	}
	out, err := baselineBatchService(r).Control(r.Context(), actor, projectID, batchID, body)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchReconcile(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := baselineBatchService(r).Reconcile(r.Context(), actor, projectID, batchID)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}

func baselineBatchBuiltReceipt(w http.ResponseWriter, r *http.Request) {
	actor, ok := baselineBatchActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req baselinebatch.BuiltReceiptRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		jsonError(w, "invalid built receipt", http.StatusBadRequest)
		return
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		jsonError(w, "invalid built receipt", http.StatusBadRequest)
		return
	}
	out, err := baselineBatchService(r).RecordBuiltReceipt(r.Context(), actor, projectID, batchID, req)
	if err != nil {
		baselineBatchError(w, err)
		return
	}
	jsonOK(w, out)
}
