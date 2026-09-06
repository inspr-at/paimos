// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func RegisterConsumerRoutes(r chi.Router) {
	r.Route("/projects/{id}/consumers/v1", func(r chi.Router) {
		r.Post("/streams", consumerRegister)
		r.Post("/streams/{streamID}/claim", consumerClaim)
		r.Post("/streams/{streamID}/attempts/{attemptID}/execute", consumerExecute)
		r.Post("/streams/{streamID}/attempts/{attemptID}/complete", consumerComplete)
		r.Post("/runtime-health", consumerHealth)
	})
}
func consumerContext(w http.ResponseWriter, r *http.Request) (agentmessage.ConsumerCredentials, int64, bool) {
	SetControlCachePolicy(w)
	p, ok := auth.GetPrincipal(r)
	c := agentmessage.ConsumerCredentials{Principal: p, RuntimeLease: lifecycleProof(r, lifecycleintents.RuntimeLeaseHeader), ConsumerLease: lifecycleProof(r, agentmessage.ConsumerLeaseHeader), AttemptNonce: lifecycleProof(r, agentmessage.ConsumerAttemptHeader)}
	project, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !ok || p.Kind() != auth.PrincipalAPIKey || err != nil || project <= 0 {
		consumerReply(w, nil, agentmessage.ErrConsumerUnavailable)
		return c, 0, false
	}
	if r.URL.RawQuery != "" {
		consumerReply(w, nil, agentmessage.ErrConsumerInvalid)
		return c, 0, false
	}
	return c, project, true
}
func consumerDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	var raw json.RawMessage
	if DecodeControlJSON(w, r, 4096, &raw) != nil {
		consumerReply(w, nil, agentmessage.ErrConsumerInvalid)
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		consumerReply(w, nil, agentmessage.ErrConsumerInvalid)
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		consumerReply(w, nil, agentmessage.ErrConsumerInvalid)
		return false
	}
	return true
}
func consumerReply(w http.ResponseWriter, out any, err error) {
	if err == nil {
		writeHarnessJSON(w, http.StatusOK, out)
		return
	}
	code, status := "consumer_storage_unavailable", http.StatusServiceUnavailable
	for _, known := range []struct {
		err    error
		status int
	}{
		{agentmessage.ErrConsumerInvalid, 400}, {agentmessage.ErrConsumerUnavailable, 403},
		{agentmessage.ErrConsumerConflict, 409}, {agentmessage.ErrConsumerHandoff, 409}, {agentmessage.ErrConsumerUnknown, 409},
	} {
		if errors.Is(err, known.err) {
			code, status = known.err.Error(), known.status
			break
		}
	}
	writeHarnessJSON(w, status, map[string]string{"error": code})
}
func consumerRegister(w http.ResponseWriter, r *http.Request) {
	c, project, ok := consumerContext(w, r)
	if !ok {
		return
	}
	var in agentmessage.ConsumerRegistration
	if !consumerDecode(w, r, &in) {
		return
	}
	out, err := agentmessage.NewService(db.DB).RegisterConsumer(r.Context(), c, project, in)
	consumerReply(w, out, err)
}
func consumerClaim(w http.ResponseWriter, r *http.Request) {
	c, project, ok := consumerContext(w, r)
	if !ok {
		return
	}
	var in agentmessage.ConsumerClaim
	if !consumerDecode(w, r, &in) {
		return
	}
	out, err := agentmessage.NewService(db.DB).ClaimConsumer(r.Context(), c, project, chi.URLParam(r, "streamID"), in)
	consumerReply(w, out, err)
}
func consumerExecute(w http.ResponseWriter, r *http.Request) {
	c, project, ok := consumerContext(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !consumerDecode(w, r, &in) {
		return
	}
	out, err := agentmessage.NewService(db.DB).ExecuteConsumer(r.Context(), c, project, chi.URLParam(r, "streamID"), chi.URLParam(r, "attemptID"), in.ExpectedRevision)
	consumerReply(w, out, err)
}
func consumerComplete(w http.ResponseWriter, r *http.Request) {
	c, project, ok := consumerContext(w, r)
	if !ok {
		return
	}
	var in agentmessage.ConsumerCompletion
	if !consumerDecode(w, r, &in) {
		return
	}
	out, err := agentmessage.NewService(db.DB).CompleteConsumer(r.Context(), c, project, chi.URLParam(r, "streamID"), chi.URLParam(r, "attemptID"), in)
	consumerReply(w, out, err)
}
func consumerHealth(w http.ResponseWriter, r *http.Request) {
	c, project, ok := consumerContext(w, r)
	if !ok {
		return
	}
	var in agentmessage.RuntimeHealthInput
	if !consumerDecode(w, r, &in) {
		return
	}
	out, err := agentmessage.NewService(db.DB).PublishRuntimeHealth(r.Context(), c, project, in)
	consumerReply(w, out, err)
}
