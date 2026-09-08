// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/mailer"
	"github.com/inspr-at/paimos/backend/releaseacceptance"
)

var releaseAcceptanceMail mailer.Mailer

func releaseAcceptanceService() *releaseacceptance.Service {
	mail := releaseAcceptanceMail
	if mail == nil {
		mail = mailer.FromEnv()
	}
	return releaseacceptance.NewService(db.DB, nil, mail, nil)
}

func RegisterReleaseAcceptanceRoutes(r chi.Router) {
	r.With(auth.RequireProjectView).Get("/projects/{id}/release-records", releaseAcceptanceList)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/baseline-batches/batches/{batchID}/release-record", releaseAcceptanceMint)
	r.With(auth.RequireProjectView).Get("/projects/{id}/baseline-batches/batches/{batchID}/release-record", releaseAcceptanceGetByBatch)
	r.With(auth.RequireProjectView).Get("/projects/{id}/release-records/{releaseID}/acceptance", releaseAcceptanceGet)
	r.With(auth.RequireProjectEdit).Put("/projects/{id}/release-records/{releaseID}/acceptance", releaseAcceptanceConfigure)
	r.With(auth.RequireProjectView).Post("/projects/{id}/release-records/{releaseID}/acceptance/confirm", releaseAcceptanceConfirm)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/release-records/{releaseID}/acceptance/email/preview", releaseAcceptancePreview)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/release-records/{releaseID}/acceptance/email/authorize-send", releaseAcceptanceAuthorizeSend)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/release-records/{releaseID}/acceptance/email/record-external", releaseAcceptanceRecordExternal)
	r.With(auth.RequireProjectView).Get("/projects/{id}/release-records/{releaseID}/acceptance/evidence", releaseAcceptanceEvidence)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/release-records/{releaseID}/acceptance/deployment-target", releaseAcceptanceBindTarget)
	r.With(auth.RequireProjectView).Get("/projects/{id}/acceptance-standing-policies", releaseAcceptanceListPolicies)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/acceptance-standing-policies", releaseAcceptanceApprovePolicy)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/acceptance-standing-policies/{policyID}/revoke", releaseAcceptanceRevokePolicy)
	r.With(auth.RequireProjectView).Post("/projects/{id}/release-records/{releaseID}/acceptance/apply-policy", releaseAcceptanceApplyPolicy)
}

func RegisterPortalReleaseAcceptanceRoutes(r chi.Router) {
	r.Get("/portal/projects/{id}/release-records", releaseAcceptanceList)
	r.Get("/portal/projects/{id}/release-records/{releaseID}/acceptance", releaseAcceptanceGet)
	r.Post("/portal/projects/{id}/release-records/{releaseID}/acceptance/confirm", releaseAcceptanceConfirm)
}

func releaseAcceptanceActor(r *http.Request) (releaseacceptance.Actor, bool) {
	p, ok := auth.GetPrincipal(r)
	if !ok {
		return releaseacceptance.Actor{}, false
	}
	actor := releaseacceptance.Actor{
		Kind:                string(p.Kind()),
		UserID:              p.UserID(),
		SessionCredentialID: p.SessionCredentialID(),
		APIKeyID:            p.APIKeyID(),
		Impersonated:        p.Impersonated(),
	}
	if agent, _ := readAgentAttribution(r); agent != nil {
		actor.Kind = "agent"
	}
	return actor, true
}

func releaseAcceptanceIDs(r *http.Request) (projectID, releaseID int64, ok bool) {
	projectID, ok = baselineBatchProject(r)
	if !ok {
		return 0, 0, false
	}
	if raw := chi.URLParam(r, "releaseID"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, 0, false
		}
		releaseID = id
	}
	return projectID, releaseID, true
}

func releaseAcceptanceError(w http.ResponseWriter, err error) {
	msg, code := "release acceptance unavailable", http.StatusInternalServerError
	switch {
	case errors.Is(err, releaseacceptance.ErrInvalid):
		msg, code = err.Error(), http.StatusBadRequest
	case errors.Is(err, releaseacceptance.ErrUnauthorized):
		msg, code = "unauthorized", http.StatusUnauthorized
	case errors.Is(err, releaseacceptance.ErrForbidden):
		msg, code = err.Error(), http.StatusForbidden
	case errors.Is(err, releaseacceptance.ErrNotFound):
		msg, code = "not found", http.StatusNotFound
	case errors.Is(err, releaseacceptance.ErrConflict), errors.Is(err, releaseacceptance.ErrStale):
		msg, code = err.Error(), http.StatusConflict
	case errors.Is(err, releaseacceptance.ErrUnavailable):
		msg, code = err.Error(), http.StatusServiceUnavailable
	}
	jsonError(w, msg, code)
}

func decodeAcceptanceBody(r *http.Request, dest any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 96<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}

func releaseAcceptanceList(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, _, okID := releaseAcceptanceIDs(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().List(r.Context(), actor, projectID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceMint(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil || batchID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().Mint(r.Context(), actor, projectID, batchID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceGetByBatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, okID := baselineBatchProject(r)
	batchID, err := strconv.ParseInt(chi.URLParam(r, "batchID"), 10, 64)
	if !ok || !okID || err != nil || batchID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().GetByBatch(r.Context(), actor, projectID, batchID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceGet(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().Get(r.Context(), actor, projectID, releaseID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceConfigure(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.ConfigureRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().Configure(r.Context(), actor, projectID, releaseID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

type confirmBody struct {
	PartyRef    string `json:"party_ref"`
	Attestation string `json:"attestation"`
}

func releaseAcceptanceConfirm(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req confirmBody
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().Confirm(r.Context(), actor, projectID, releaseID, req.PartyRef, req.Attestation)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptancePreview(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.PreviewRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().SavePreview(r.Context(), actor, projectID, releaseID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceAuthorizeSend(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.AuthorizeSendRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().AuthorizeSend(r.Context(), actor, projectID, releaseID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceRecordExternal(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.RecordExternalRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().RecordExternal(r.Context(), actor, projectID, releaseID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceEvidence(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	ct, body, err := releaseAcceptanceService().Export(r.Context(), actor, projectID, releaseID, r.URL.Query().Get("format"))
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if strings.HasPrefix(ct, "text/html") {
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
	}
	w.WriteHeader(http.StatusOK)
	// #nosec G705 -- JSON and message/rfc822 are non-HTML content types; HTML interpolates only html.EscapeString values and is served with nosniff plus default-src 'none'.
	_, _ = w.Write(body)
}

func releaseAcceptanceBindTarget(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.BindTargetRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().BindTarget(r.Context(), actor, projectID, releaseID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceListPolicies(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, _, okID := releaseAcceptanceIDs(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().ListPolicies(r.Context(), actor, projectID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceApprovePolicy(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, _, okID := releaseAcceptanceIDs(r)
	if !ok || !okID {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req releaseacceptance.PolicyRequest
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().ApprovePolicy(r.Context(), actor, projectID, req)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

func releaseAcceptanceRevokePolicy(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, _, okID := releaseAcceptanceIDs(r)
	policyID, err := strconv.ParseInt(chi.URLParam(r, "policyID"), 10, 64)
	if !ok || !okID || err != nil || policyID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	out, err := releaseAcceptanceService().RevokePolicy(r.Context(), actor, projectID, policyID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}

type applyPolicyBody struct {
	PolicyID int64 `json:"policy_id"`
}

func releaseAcceptanceApplyPolicy(w http.ResponseWriter, r *http.Request) {
	actor, ok := releaseAcceptanceActor(r)
	projectID, releaseID, okID := releaseAcceptanceIDs(r)
	if !ok || !okID || releaseID <= 0 {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	var req applyPolicyBody
	if err := decodeAcceptanceBody(r, &req); err != nil {
		jsonError(w, "invalid request", http.StatusBadRequest)
		return
	}
	out, err := releaseAcceptanceService().ApplyPolicy(r.Context(), actor, projectID, releaseID, req.PolicyID)
	if err != nil {
		releaseAcceptanceError(w, err)
		return
	}
	jsonOK(w, out)
}
