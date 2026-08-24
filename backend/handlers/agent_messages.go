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
	r.With(auth.RequireProjectView).Get("/projects/{id}/messages", listAgentMessages)
	r.With(auth.RequireProjectView).Get("/projects/{id}/messages/{messageID}", getAgentMessage)
	r.With(auth.RequireIssueAccess).Get("/issues/{id}/messages", listIssueAgentMessages)
}

type sendEnvelopeRequest struct {
	To       string         `json:"to"`
	IssueID  *int64         `json:"issue_id"`
	ReplyTo  string         `json:"reply_to"`
	ThreadID string         `json:"thread_id"`
	Body     string         `json:"body"`
	Metadata map[string]any `json:"metadata"`
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
		Body: req.Body, Metadata: req.Metadata,
	})
	if err != nil {
		writeAgentMessageError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(msg)
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
