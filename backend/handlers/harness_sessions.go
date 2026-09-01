// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const harnessWorkerLeaseHeader = "X-Paimos-Harness-Worker-Lease"

// RegisterHarnessSessionRoutes owns the PAI-848 control plane. These routes
// never spawn a process and are intentionally distinct from agent_runs and
// the attribution-only `paimos session start` lifecycle.
func RegisterHarnessSessionRoutes(r chi.Router) {
	r.With(auth.RequireProjectView).Get("/projects/{id}/harness-sessions", listHarnessSessions)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions", registerHarnessSession)
	r.With(auth.RequireProjectView).Get("/projects/{id}/harness-sessions/{sessionID}", getHarnessSession)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/heartbeat", heartbeatHarnessSession)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/yield", yieldHarnessSession)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/drain", drainHarnessDeliveries)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/complete-delivery", completeHarnessDelivery)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/drain-steer", drainHarnessSteer)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/complete-steer", completeHarnessSteer)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/controls/{kind}", requestHarnessControl)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/controls/{controlID}/complete", completeHarnessControl)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/harness-sessions/{sessionID}/stop", stopHarnessSession)
}

type harnessRegisterRequest struct {
	AgentName       string                     `json:"agent_name"`
	Harness         string                     `json:"harness"`
	Host            string                     `json:"host"`
	SessionRef      string                     `json:"harness_session_ref"`
	WorkerLease     string                     `json:"worker_lease"`
	MessageTargetID string                     `json:"message_target_id"`
	ManagementMode  string                     `json:"management_mode"`
	Role            string                     `json:"role"`
	SteerMode       string                     `json:"steer_mode"`
	Capabilities    models.HarnessCapabilities `json:"advertised_capabilities"`
}
type heartbeatRequest struct {
	Phase string `json:"phase"`
}
type completeControlRequest struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}
type completeSteerRequest struct {
	Cursor         int64  `json:"cursor"`
	DeliveryID     string `json:"delivery_id"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason"`
}

func harnessProjectID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}
func decodeHarnessJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	if err := DecodeControlJSON(w, r, 16*1024, out); err != nil {
		harnessProblem(w, err, "harness_session_request_invalid", http.StatusBadRequest)
		return false
	}
	return true
}
func writeHarnessJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func harnessProblem(w http.ResponseWriter, err error, fallback string, status int) {
	code := managedharness.ErrorCode(err)
	if code == "" {
		var messageErr *agentmessage.CodedError
		if errors.As(err, &messageErr) {
			code = messageErr.Code
		}
	}
	if code == "" {
		code = fallback
	}
	messageProblem(w, nil, code, err.Error(), status)
}
func harnessStatus(err error) int {
	if managedharness.IsCode(err, managedharness.CodeNotFound) {
		return http.StatusNotFound
	}
	if managedharness.IsCode(err, managedharness.CodeConflict) {
		return http.StatusConflict
	}
	return http.StatusBadRequest
}
func requireHarnessWorker(w http.ResponseWriter, r *http.Request, projectID int64) (models.HarnessSession, bool) {
	session, err := managedharness.NewService(db.DB).Get(r.Context(), projectID, chi.URLParam(r, "sessionID"))
	if err != nil {
		harnessProblem(w, err, "harness_session_not_found", harnessStatus(err))
		return session, false
	}
	agent, _ := readAgentAttribution(r)
	leaseHeaders := r.Header.Values(harnessWorkerLeaseHeader)
	lease := ""
	if len(leaseHeaders) == 1 && leaseHeaders[0] == strings.TrimSpace(leaseHeaders[0]) {
		lease = leaseHeaders[0]
	}
	leaseOK, verifyErr := managedharness.NewService(db.DB).VerifyWorkerLease(r.Context(), projectID, session.ID, lease)
	if verifyErr != nil || agent == nil || *agent != session.AgentName || !leaseOK {
		harnessProblem(w, errors.New("harness worker authorization failed"), "harness_session_worker_authorization_failed", http.StatusForbidden)
		return session, false
	}
	return session, true
}

func registerHarnessSession(w http.ResponseWriter, r *http.Request) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	var req harnessRegisterRequest
	if !decodeHarnessJSON(w, r, &req) {
		return
	}
	session, created, err := managedharness.NewService(db.DB).Register(r.Context(), managedharness.RegisterInput{ProjectID: projectID, AgentName: req.AgentName, Harness: req.Harness, Host: req.Host, SessionRef: req.SessionRef, WorkerLease: req.WorkerLease, MessageTargetID: req.MessageTargetID, ManagementMode: req.ManagementMode, Role: req.Role, SteerMode: req.SteerMode, Capabilities: req.Capabilities})
	if err != nil {
		harnessProblem(w, err, "harness_session_register_failed", harnessStatus(err))
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeHarnessJSON(w, status, session)
}
func listHarnessSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	out, err := managedharness.NewService(db.DB).List(r.Context(), id)
	if err != nil {
		harnessProblem(w, err, "harness_session_list_failed", 500)
		return
	}
	writeHarnessJSON(w, 200, out)
}
func getHarnessSession(w http.ResponseWriter, r *http.Request) {
	id, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	out, err := managedharness.NewService(db.DB).Get(r.Context(), id, chi.URLParam(r, "sessionID"))
	if err != nil {
		harnessProblem(w, err, "harness_session_not_found", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 200, out)
}
func heartbeatHarnessSession(w http.ResponseWriter, r *http.Request) {
	id, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, id)
	if !ok {
		return
	}
	var req heartbeatRequest
	if !decodeHarnessJSON(w, r, &req) {
		return
	}
	out, err := managedharness.NewService(db.DB).Heartbeat(r.Context(), session.ID, req.Phase)
	if err != nil {
		harnessProblem(w, err, "harness_session_heartbeat_failed", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 200, out)
}
func yieldHarnessSession(w http.ResponseWriter, r *http.Request) {
	id, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, id)
	if !ok {
		return
	}
	out, err := managedharness.NewService(db.DB).Yield(r.Context(), session.ID)
	if err != nil {
		harnessProblem(w, err, "harness_session_yield_failed", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 200, out)
}

func drainHarnessDeliveries(w http.ResponseWriter, r *http.Request) {
	drainHarnessInbox(w, r, false)
}

func drainHarnessSteer(w http.ResponseWriter, r *http.Request) {
	drainHarnessInbox(w, r, true)
}

func drainHarnessInbox(w http.ResponseWriter, r *http.Request, requireSteer bool) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, projectID)
	if !ok {
		return
	}
	if session.ManagementMode != managedharness.ManagementManaged || session.Phase == managedharness.PhaseStopped || !session.Capabilities.Inbox {
		harnessProblem(w, errors.New("managed inbox delivery is unavailable"), managedharness.CodeCapabilityUnavailable, 400)
		return
	}
	if requireSteer && (session.SteerMode != managedharness.SteerOwned || !session.Capabilities.Steer) {
		harnessProblem(w, errors.New("owned steer is unavailable"), managedharness.CodeCapabilityUnavailable, 400)
		return
	}
	// Never filter by requested delivery level here: the canonical ledger is
	// FIFO across simple and steer work, so a steer-capable worker must first
	// complete any older simple message for the same address.
	page, err := agentmessage.NewService(db.DB).ListInbox(r.Context(), agentmessage.InboxInput{ProjectID: projectID, Address: managedharness.Address(session), Agent: session.AgentName, WorkerAdapter: agentmessage.AdapterManagedHarness, TargetID: session.MessageTargetID, Limit: 100})
	if err != nil {
		harnessProblem(w, err, "harness_session_delivery_drain_failed", 400)
		return
	}
	for i := range page.Messages {
		if page.Messages[i].DeliveryWork != nil {
			page.Messages[i].DeliveryWork.TargetRef = ""
		}
	}
	writeHarnessJSON(w, 200, page)
}

func completeHarnessDelivery(w http.ResponseWriter, r *http.Request) {
	completeHarnessInboxDelivery(w, r, false)
}

func completeHarnessSteer(w http.ResponseWriter, r *http.Request) {
	completeHarnessInboxDelivery(w, r, true)
}

func completeHarnessInboxDelivery(w http.ResponseWriter, r *http.Request, requireSteer bool) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, projectID)
	if !ok {
		return
	}
	if session.ManagementMode != managedharness.ManagementManaged || session.Phase == managedharness.PhaseStopped || !session.Capabilities.Inbox {
		harnessProblem(w, errors.New("managed inbox delivery is unavailable"), managedharness.CodeCapabilityUnavailable, 400)
		return
	}
	if requireSteer && (session.SteerMode != managedharness.SteerOwned || !session.Capabilities.Steer) {
		harnessProblem(w, errors.New("owned steer is unavailable"), managedharness.CodeCapabilityUnavailable, 400)
		return
	}
	var req completeSteerRequest
	if !decodeHarnessJSON(w, r, &req) {
		return
	}
	if req.EffectiveLevel == "steer" && (session.SteerMode != managedharness.SteerOwned || !session.Capabilities.Steer) {
		harnessProblem(w, errors.New("effective steer exceeds this session's advertised capability"), managedharness.CodeCapabilityUnavailable, 400)
		return
	}
	state, err := agentmessage.NewService(db.DB).CompleteLocalDelivery(r.Context(), agentmessage.CompleteDeliveryInput{ProjectID: projectID, Address: managedharness.Address(session), Agent: session.AgentName, Cursor: req.Cursor, DeliveryID: req.DeliveryID, EffectiveLevel: req.EffectiveLevel, FallbackReason: req.FallbackReason, TargetID: session.MessageTargetID})
	if err != nil {
		harnessProblem(w, err, "harness_session_delivery_complete_failed", 400)
		return
	}
	writeHarnessJSON(w, 200, state)
}
func requestHarnessControl(w http.ResponseWriter, r *http.Request) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, err := managedharness.NewService(db.DB).Get(r.Context(), projectID, chi.URLParam(r, "sessionID"))
	if err != nil {
		harnessProblem(w, err, "harness_session_not_found", harnessStatus(err))
		return
	}
	principal, ok := auth.GetPrincipal(r)
	if !ok {
		harnessProblem(w, errors.New("authenticated principal required"), "harness_session_principal_required", 401)
		return
	}
	out, err := managedharness.NewService(db.DB).RequestControl(r.Context(), session.ID, chi.URLParam(r, "kind"), principal.ActorUserID())
	if err != nil {
		harnessProblem(w, err, "harness_session_control_failed", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 201, out)
}
func completeHarnessControl(w http.ResponseWriter, r *http.Request) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, projectID)
	if !ok {
		return
	}
	var req completeControlRequest
	if !decodeHarnessJSON(w, r, &req) {
		return
	}
	out, err := managedharness.NewService(db.DB).CompleteControl(r.Context(), session.ID, chi.URLParam(r, "controlID"), req.Outcome, req.Reason)
	if err != nil {
		harnessProblem(w, err, "harness_session_control_complete_failed", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 200, out)
}
func stopHarnessSession(w http.ResponseWriter, r *http.Request) {
	projectID, ok := harnessProjectID(w, r)
	if !ok {
		return
	}
	session, ok := requireHarnessWorker(w, r, projectID)
	if !ok {
		return
	}
	out, err := managedharness.NewService(db.DB).Stop(r.Context(), session.ID)
	if err != nil {
		harnessProblem(w, err, "harness_session_stop_failed", harnessStatus(err))
		return
	}
	writeHarnessJSON(w, 200, out)
}
