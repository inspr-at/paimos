// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// RegisterAgentMessageRoutes exposes only project-scoped, name-addressed
// messaging. The numeric PAI-817 storage API is intentionally not public.
func RegisterAgentMessageRoutes(r chi.Router) {
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/messages", sendAgentMessage)
	r.With(auth.RequireProjectView).Get("/projects/{id}/messages/listen", listenAgentMessages)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/messages/ack", ackAgentMessages)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/messages/delivery-complete", completeAgentMessageDelivery)
	r.With(auth.RequireProjectEdit).Post("/projects/{id}/message-allowlist", allowAgentMessageSender)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-targets", registerAgentMessageTarget)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/projects/{id}/message-targets", listAgentMessageTargets)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-targets/requeue", requeueAgentMessageTargets)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Get("/projects/{id}/message-deliveries", listAgentMessageDeliveries)
	r.With(auth.RequireAdmin, auth.RequireProjectView).Post("/projects/{id}/message-deliveries/{deliveryID}/requeue", requeueAgentMessageDelivery)
	r.With(auth.RequireProjectView).Get("/projects/{id}/messages", listAgentMessages)
	r.With(auth.RequireProjectView).Get("/projects/{id}/messages/{messageID}", getAgentMessage)
	r.With(auth.RequireIssueAccess).Get("/issues/{id}/messages", listIssueAgentMessages)
}

type ackEnvelopeRequest struct {
	To     string `json:"to"`
	Cursor int64  `json:"cursor"`
}

type allowSenderRequest struct {
	Receiver string `json:"receiver"`
	Sender   string `json:"sender"`
}

type sendEnvelopeRequest struct {
	To              string         `json:"to"`
	IssueID         *int64         `json:"issue_id"`
	ReplyTo         string         `json:"reply_to"`
	ThreadID        string         `json:"thread_id"`
	Body            string         `json:"body"`
	Metadata        map[string]any `json:"metadata"`
	IsActionRequest bool           `json:"is_action_request"`
	DeliveryLevel   string         `json:"delivery_level"`
}

type completeDeliveryRequest struct {
	To             string `json:"to"`
	Cursor         int64  `json:"cursor"`
	DeliveryID     string `json:"delivery_id"`
	EffectiveLevel string `json:"effective_level"`
	FallbackReason string `json:"fallback_reason"`
}

// registerTargetRequest is the closed admin registration contract. target_ref
// and target_secret are write-only: the service encrypts both and no read API
// ever returns them.
type registerTargetRequest struct {
	Address      string `json:"address"`
	Adapter      string `json:"adapter"`
	TargetKind   string `json:"target_kind"`
	TargetRef    string `json:"target_ref"`
	TargetSecret string `json:"target_secret"`
	MaximumLevel string `json:"maximum_level"`
	Role         string `json:"role"`
}

type requeueTargetRequest struct {
	Address string `json:"address"`
}

func sendAgentMessage(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req sendEnvelopeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentmessage.MaxBodySize+8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	idempotencyKey := ""
	if values := r.Header.Values("Idempotency-Key"); len(values) > 1 {
		messageProblem(w, r, "agent_message_idempotency_key_invalid", "exactly one Idempotency-Key header is allowed", http.StatusBadRequest)
		return
	} else if len(values) == 1 {
		idempotencyKey = values[0]
		if idempotencyKey == "" {
			messageProblem(w, r, "agent_message_idempotency_key_invalid", "Idempotency-Key must not be empty", http.StatusBadRequest)
			return
		}
	}
	agent, session := readAgentAttribution(r)
	sender, sessionID := "", ""
	if agent != nil {
		sender = *agent
	}
	if session != nil {
		sessionID = *session
	}
	msg, err := agentmessage.NewService(db.DB).SendEnvelope(r.Context(), agentmessage.SendEnvelopeInput{
		ProjectID: projectID, Sender: sender, SessionID: sessionID, To: req.To,
		IssueID: req.IssueID, ReplyTo: strings.TrimSpace(req.ReplyTo), ThreadID: strings.TrimSpace(req.ThreadID),
		Body: req.Body, Metadata: req.Metadata, ActionRequest: req.IsActionRequest,
		DeliveryLevel: req.DeliveryLevel, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(msg)
}

func completeAgentMessageDelivery(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req completeDeliveryRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	agent, _ := readAgentAttribution(r)
	attributed := ""
	if agent != nil {
		attributed = *agent
	}
	state, err := agentmessage.NewService(db.DB).CompleteLocalDelivery(r.Context(), agentmessage.CompleteDeliveryInput{
		ProjectID: projectID, Address: strings.TrimSpace(req.To), Agent: attributed, Cursor: req.Cursor,
		DeliveryID: strings.TrimSpace(req.DeliveryID), EffectiveLevel: strings.TrimSpace(req.EffectiveLevel),
		FallbackReason: strings.TrimSpace(req.FallbackReason),
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, state)
}

func registerAgentMessageTarget(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req registerTargetRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	target, err := agentmessage.NewService(db.DB).RegisterTarget(r.Context(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: req.Address, Adapter: req.Adapter, TargetKind: req.TargetKind,
		TargetRef: req.TargetRef, TargetSecret: req.TargetSecret, MaximumLevel: req.MaximumLevel, Role: req.Role,
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(target)
}

func listAgentMessageTargets(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	targets, err := agentmessage.NewService(db.DB).ListTargets(r.Context(), projectID, r.URL.Query().Get("address"))
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, map[string]any{"targets": targets, "count": len(targets)})
}

func requeueAgentMessageTargets(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req requeueTargetRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	count, err := agentmessage.NewService(db.DB).RequeueMissingTargets(r.Context(), projectID, req.Address)
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, map[string]any{"address": strings.TrimSpace(req.Address), "requeued": count})
}

func listAgentMessageDeliveries(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	deliveries, err := agentmessage.NewService(db.DB).ListDeliveryStatus(r.Context(), projectID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]any{"deliveries": deliveries, "count": len(deliveries)})
}

func requeueAgentMessageDelivery(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	deliveryID := strings.TrimSpace(chi.URLParam(r, "deliveryID"))
	if err := agentmessage.NewService(db.DB).RequeueDelivery(r.Context(), projectID, deliveryID); err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, map[string]any{"delivery_id": deliveryID, "state": "pending"})
}

func listAgentMessages(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	messages, err := agentmessage.NewService(db.DB).ListEnvelopes(r.Context(), agentmessage.ListFilter{
		ProjectID: projectID, To: to, ThreadID: strings.TrimSpace(r.URL.Query().Get("thread")), DeliveredOnly: to != "", AfterID: after, Limit: limit,
	})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Add the PAI-817 untrusted-data framing only on addressee/listen reads.
	// Human project/issue inspection keeps the structured raw envelope.
	if to != "" {
		for i := range messages {
			frameAgentEnvelope(&messages[i])
		}
	}
	jsonOK(w, map[string]any{"messages": messages, "count": len(messages)})
}

func listenAgentMessages(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	agent, _ := readAgentAttribution(r)
	attributed := ""
	if agent != nil {
		attributed = *agent
	}
	page, err := agentmessage.NewService(db.DB).ListInbox(r.Context(), agentmessage.InboxInput{
		ProjectID: projectID, Address: strings.TrimSpace(r.URL.Query().Get("to")), Agent: attributed,
		WorkerAdapter: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("delivery"))), AfterID: after, Limit: limit,
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	for i := range page.Messages {
		frameAgentEnvelope(&page.Messages[i])
	}
	jsonOK(w, page)
}

func ackAgentMessages(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req ackEnvelopeRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	agent, _ := readAgentAttribution(r)
	attributed := ""
	if agent != nil {
		attributed = *agent
	}
	state, err := agentmessage.NewService(db.DB).AckInbox(r.Context(), agentmessage.AckInput{
		ProjectID: projectID, Address: strings.TrimSpace(req.To), Agent: attributed, Cursor: req.Cursor,
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, state)
}

func allowAgentMessageSender(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	var req allowSenderRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		messageProblem(w, r, "agent_message_request_invalid", err.Error(), http.StatusBadRequest)
		return
	}
	if err := agentmessage.NewService(db.DB).AllowSender(r.Context(), projectID, req.Receiver, req.Sender); err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	jsonOK(w, map[string]any{"receiver": strings.TrimSpace(req.Receiver), "sender": strings.TrimSpace(req.Sender), "allowed": true})
}

func getAgentMessage(w http.ResponseWriter, r *http.Request) {
	projectID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid project id", http.StatusBadRequest)
		return
	}
	msg, err := agentmessage.NewService(db.DB).GetEnvelope(r.Context(), projectID, chi.URLParam(r, "messageID"))
	if errors.Is(err, sql.ErrNoRows) {
		jsonError(w, "message not found", http.StatusNotFound)
		return
	}
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	frameAgentEnvelope(msg)
	jsonOK(w, msg)
}

func frameAgentEnvelope(message *agentmessage.Envelope) {
	if message == nil || len(message.Parts) == 0 {
		return
	}
	message.Parts[0].Text = (agentmessage.FramedMessage{
		From: message.From, Project: message.ContextID, Issue: message.TaskID,
		Hop: message.Hop, Body: message.Parts[0].Text, IsActionRequest: message.IsActionRequest,
	}).FullMessage()
}

func listIssueAgentMessages(w http.ResponseWriter, r *http.Request) {
	issueID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		jsonError(w, "invalid issue id", http.StatusBadRequest)
		return
	}
	var projectID int64
	if err := db.DB.QueryRowContext(r.Context(), `SELECT project_id FROM issues WHERE id=?`, issueID).Scan(&projectID); err != nil {
		jsonError(w, "issue not found", http.StatusNotFound)
		return
	}
	messages, err := agentmessage.NewService(db.DB).ListEnvelopes(r.Context(), agentmessage.ListFilter{ProjectID: projectID, IssueID: &issueID, Limit: 100})
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, messages)
}

func writeAgentMessageError(w http.ResponseWriter, r *http.Request, err error) {
	code, status := "agent_message_write_failed", http.StatusBadRequest
	var coded *agentmessage.CodedError
	if errors.As(err, &coded) {
		code = coded.Code
		if code == "agent_message_idempotency_conflict" {
			status = http.StatusConflict
		}
	}
	if errors.Is(err, agentmessage.ErrBodyTooLarge) {
		code, status = "agent_message_body_too_large", http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, agentmessage.ErrRateLimitExceeded) {
		code, status = "agent_message_rate_limited", http.StatusTooManyRequests
	}
	if errors.Is(err, agentmessage.ErrHopLimitExceeded) {
		code = "agent_message_hop_limit"
	}
	if errors.Is(err, agentmessage.ErrContainsSecret) {
		code = "agent_message_secret_rejected"
	}
	messageProblem(w, r, code, err.Error(), status)
}

func messageProblem(w http.ResponseWriter, r *http.Request, code, detail string, status int) {
	problemJSON(w, r, ProblemDetails{Type: "https://paimos.com/errors/" + code, Title: http.StatusText(status), Status: status, Detail: detail, Code: code})
}
