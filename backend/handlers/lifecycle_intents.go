// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
)

func RegisterLifecycleIntentRoutes(r chi.Router) {
	r.Route("/projects/{id}/lifecycle/v1", func(r chi.Router) {
		r.Get("/runtimes", lifecycleRuntimes)
		r.Get("/runtime-health", lifecycleRuntimeHealth)
		r.Post("/runtimes", lifecycleRegisterRuntime)
		r.Post("/runtimes/{runtimeID}/sessions", lifecycleRegisterSession)
		r.Post("/runtimes/{runtimeID}/claim", lifecycleClaim)
		r.Post("/intents", lifecycleSubmit)
		r.Get("/intents/{intentID}", lifecycleGet)
		r.Get("/intents/{intentID}/events", lifecycleEvents)
		r.Post("/intents/{intentID}/cancel", lifecycleCancel)
		r.Post("/intents/{intentID}/transition", lifecycleTransition)
	})
}
func lifecycleContext(w http.ResponseWriter, r *http.Request) (auth.Principal, int64, bool) {
	SetControlCachePolicy(w)
	p, ok := auth.GetPrincipal(r)
	project, e := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if !ok || e != nil || project <= 0 {
		lifecycleError(w, lifecycleintents.ErrUnavailable)
		return p, 0, false
	}
	if r.URL.RawQuery != "" {
		lifecycleError(w, lifecycleintents.ErrInvalid)
		return p, 0, false
	}
	return p, project, true
}
func lifecycleProof(r *http.Request, header string) string {
	values := r.Header.Values(header)
	if len(values) != 1 || values[0] != strings.TrimSpace(values[0]) {
		return ""
	}
	return values[0]
}
func lifecycleDecode(w http.ResponseWriter, r *http.Request, v any) bool {
	var raw json.RawMessage
	if DecodeControlJSON(w, r, 8192, &raw) != nil {
		lifecycleError(w, lifecycleintents.ErrInvalid)
		return false
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		lifecycleError(w, lifecycleintents.ErrInvalid)
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		lifecycleError(w, lifecycleintents.ErrInvalid)
		return false
	}
	if req, ok := v.(*lifecycleintents.Request); ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil {
			lifecycleError(w, lifecycleintents.ErrInvalid)
			return false
		}
		allowed := map[string]bool{}
		names := []string{"request_key", "operation", "runtime_id", "runtime_generation", "account_label", "account_key", "ttl_seconds"}
		switch {
		case req.Operation == "repair":
			names = append(names, "repair_layer")
		case req.Operation == "readiness":
			// A readiness probe names only what it observes.
			names = append(names, "workspace_handle", "dispatch_profile_id", "dispatch_profile_version", "baseline_digest")
		default:
			names = append(names, "workspace_handle", "agent_name", "dispatch_profile_id", "dispatch_profile_version", "ticket_id", "work_shape", "role", "parent_harness_session_id")
			if req.Operation != "start" {
				names = append(names, "session_id", "session_generation", "expected_revision")
			}
		}
		for _, name := range names {
			allowed[name] = true
		}
		for name := range fields {
			if !allowed[name] {
				lifecycleError(w, lifecycleintents.ErrInvalid)
				return false
			}
		}
	}
	return true
}
func lifecycleError(w http.ResponseWriter, err error) {
	code, status := "lifecycle_storage_unavailable", http.StatusServiceUnavailable
	switch {
	case errors.Is(err, lifecycleintents.ErrInvalid):
		code, status = "lifecycle_invalid", 400
	case errors.Is(err, lifecycleintents.ErrUnavailable):
		code, status = "lifecycle_unavailable", 403
	case errors.Is(err, lifecycleintents.ErrConflict):
		code, status = "lifecycle_conflict", 409
	}
	// Never log or echo opaque payloads, SQL errors or credential material.
	writeHarnessJSON(w, status, map[string]string{"error": code})
}
func lifecycleReply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		lifecycleError(w, err)
		return
	}
	writeHarnessJSON(w, 200, v)
}
func lifecycleRuntimes(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Runtimes(r.Context(), p, project)
	lifecycleReply(w, map[string]any{"schema_version": 1, "runtimes": out}, err)
}
func lifecycleRuntimeHealth(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).RuntimeHealth(r.Context(), p, project)
	lifecycleReply(w, out, err)
}
func lifecycleRegisterRuntime(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req lifecycleintents.Registration
	if !lifecycleDecode(w, r, &req) {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).RegisterRuntime(r.Context(), p, project, lifecycleProof(r, lifecycleintents.RuntimeLeaseHeader), req)
	lifecycleReply(w, out, err)
}
func lifecycleRegisterSession(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req lifecycleintents.SessionRegistration
	if !lifecycleDecode(w, r, &req) {
		return
	}
	err := lifecycleintents.NewService(db.DB).RegisterSession(r.Context(), p, project, chi.URLParam(r, "runtimeID"), lifecycleProof(r, lifecycleintents.RuntimeLeaseHeader), lifecycleProof(r, lifecycleintents.HarnessLeaseHeader), req)
	lifecycleReply(w, map[string]bool{"registered": true}, err)
}
func lifecycleSubmit(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req lifecycleintents.Request
	if !lifecycleDecode(w, r, &req) {
		return
	}
	out, created, err := lifecycleintents.NewService(db.DB).Submit(r.Context(), p, project, req)
	if err != nil {
		lifecycleError(w, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeHarnessJSON(w, status, out)
}
func lifecycleGet(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Get(r.Context(), p, project, chi.URLParam(r, "intentID"))
	lifecycleReply(w, out, err)
}
func lifecycleEvents(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Events(r.Context(), p, project, chi.URLParam(r, "intentID"))
	lifecycleReply(w, map[string]any{"schema_version": 1, "events": out}, err)
}
func lifecycleCancel(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !lifecycleDecode(w, r, &req) {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Cancel(r.Context(), p, project, chi.URLParam(r, "intentID"), req.ExpectedRevision)
	lifecycleReply(w, out, err)
}
func lifecycleClaim(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req struct{}
	if !lifecycleDecode(w, r, &req) {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Claim(r.Context(), p, project, chi.URLParam(r, "runtimeID"), lifecycleProof(r, lifecycleintents.RuntimeLeaseHeader))
	lifecycleReply(w, map[string]any{"schema_version": 1, "intent": out}, err)
}
func lifecycleTransition(w http.ResponseWriter, r *http.Request) {
	p, project, ok := lifecycleContext(w, r)
	if !ok {
		return
	}
	var req lifecycleintents.Transition
	if !lifecycleDecode(w, r, &req) {
		return
	}
	out, err := lifecycleintents.NewService(db.DB).Transition(r.Context(), p, project, chi.URLParam(r, "intentID"), lifecycleProof(r, lifecycleintents.RuntimeLeaseHeader), req)
	lifecycleReply(w, out, err)
}
