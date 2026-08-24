// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// RegisterAgentMessageRoutes registers HTTP routes for agent messaging (PAI-817).
func RegisterAgentMessageRoutes(r chi.Router) {
	// Send message
	r.Post("/api/agent-messages/send", sendAgentMessage)
	
	// Get delivered messages for an agent
	r.Get("/api/agent-messages/delivered/{agentID}", getDeliveredMessages)
	
	// Get held messages for an agent (requires authorization)
	r.Get("/api/agent-messages/held/{agentID}", getHeldMessages)
	
	// Manage allowlist
	r.Post("/api/agent-messages/allowlist", addToAllowlist)
	r.Delete("/api/agent-messages/allowlist", removeFromAllowlist)
	r.Get("/api/agent-messages/allowlist/{receiverAgentID}", getAgentAllowlist)
}

type sendMessageRequest struct {
	FromAgentID     int64  `json:"from_agent_id"`
	ToAgentID       int64  `json:"to_agent_id"`
	IssueID         *int64 `json:"issue_id"`
	ParentMessageID *int64 `json:"parent_message_id"`
	Body            string `json:"body"`
}

type sendMessageResponse struct {
	Message         *messageDTO `json:"message"`
	Delivered       bool        `json:"delivered"`
	HeldReason      string      `json:"held_reason,omitempty"`
	IsActionRequest bool        `json:"is_action_request"`
}

type messageDTO struct {
	ID              int64   `json:"id"`
	FromAgentID     int64   `json:"from_agent_id"`
	ToAgentID       int64   `json:"to_agent_id"`
	IssueID         *int64  `json:"issue_id"`
	ParentMessageID *int64  `json:"parent_message_id"`
	HopCount        int     `json:"hop_count"`
	Body            string  `json:"body"`
	FramedBody      string  `json:"framed_body,omitempty"` // Wrapped message with security framing
	IsActionRequest bool    `json:"is_action_request"`
	Delivered       bool    `json:"delivered"`
	HeldReason      string  `json:"held_reason,omitempty"`
	CreatedAt       string  `json:"created_at"`
	DeliveredAt     *string `json:"delivered_at"`
}

func sendAgentMessage(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	var req sendMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user can send as from_agent_id
	var projectID int64
	err := db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, req.FromAgentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	msg, err := svc.SendMessage(r.Context(), req.FromAgentID, req.ToAgentID, req.IssueID, req.ParentMessageID, req.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	resp := sendMessageResponse{
		Message:         toMessageDTO(msg, ""),
		Delivered:       msg.Delivered,
		HeldReason:      msg.HeldReason,
		IsActionRequest: msg.IsActionRequest,
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func getDeliveredMessages(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	agentID, err := strconv.ParseInt(chi.URLParam(r, "agentID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid agent ID", http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user can view messages for this agent
	var projectID int64
	err = db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, agentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	// Per-turn bound with cursor
	limit := 10 // Default to MaxDeliveredPerTurn
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= agentmessage.MaxDeliveredPerTurn {
			limit = l
		}
	}
	
	afterID := int64(0) // Cursor for pagination
	if afterStr := r.URL.Query().Get("after_id"); afterStr != "" {
		if a, err := strconv.ParseInt(afterStr, 10, 64); err == nil && a > 0 {
			afterID = a
		}
	}
	
	messages, err := svc.GetDeliveredMessages(r.Context(), agentID, limit, afterID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Apply delivery wrapper to each message
	dtos := make([]messageDTO, len(messages))
	for i, msg := range messages {
		// Fetch agent names and project info for framing
		var fromAgentName, toAgentName, projectKey string
		var issueKey sql.NullString
		
		err := db.DB.QueryRow(`
			SELECT 
				fa.name,
				ta.name,
				p.key,
				CASE WHEN i.id IS NOT NULL THEN p.key || '-' || i.issue_number ELSE NULL END
			FROM project_agents fa
			JOIN project_agents ta ON ta.id = ?
			JOIN projects p ON p.id = ta.project_id
			LEFT JOIN issues i ON i.id = ?
			WHERE fa.id = ?
		`, msg.ToAgentID, msg.IssueID, msg.FromAgentID).Scan(&fromAgentName, &toAgentName, &projectKey, &issueKey)
		
		if err != nil {
			http.Error(w, "failed to fetch agent metadata", http.StatusInternalServerError)
			return
		}
		
		// Build framed message
		framedMsg := agentmessage.FramedMessage{
			From:            fromAgentName,
			Project:         projectKey,
			Issue:           issueKey.String,
			Hop:             msg.HopCount,
			Body:            msg.Body,
			IsActionRequest: msg.IsActionRequest,
		}
		
		dtos[i] = *toMessageDTO(&msg, framedMsg.FullMessage())
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": dtos,
		"count":    len(dtos),
	})
}

func getHeldMessages(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	agentID, err := strconv.ParseInt(chi.URLParam(r, "agentID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid agent ID", http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user can view held messages for this agent
	var projectID int64
	err = db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, agentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	messages, err := svc.GetHeldMessages(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	dtos := make([]messageDTO, len(messages))
	for i, msg := range messages {
		dtos[i] = *toMessageDTO(&msg, "")
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"messages": dtos,
		"count":    len(dtos),
	})
}

type allowlistRequest struct {
	ReceiverAgentID int64 `json:"receiver_agent_id"`
	SenderAgentID   int64 `json:"sender_agent_id"`
}

func addToAllowlist(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	var req allowlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user owns receiver agent
	var projectID int64
	err := db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, req.ReceiverAgentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	if err := svc.AddAllowlistEntry(r.Context(), req.ReceiverAgentID, req.SenderAgentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "added"})
}

func removeFromAllowlist(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	var req allowlistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user owns receiver agent
	var projectID int64
	err := db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, req.ReceiverAgentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	if err := svc.RemoveAllowlistEntry(r.Context(), req.ReceiverAgentID, req.SenderAgentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	json.NewEncoder(w).Encode(map[string]string{"status": "removed"})
}

func getAgentAllowlist(w http.ResponseWriter, r *http.Request) {
	svc := agentmessage.NewService(db.DB)
	
	agentID, err := strconv.ParseInt(chi.URLParam(r, "receiverAgentID"), 10, 64)
	if err != nil {
		http.Error(w, "invalid agent ID", http.StatusBadRequest)
		return
	}
	
	// Authorization: ensure user can view allowlist for this agent
	var projectID int64
	err = db.DB.QueryRow(`SELECT project_id FROM project_agents WHERE id = ?`, agentID).Scan(&projectID)
	if err != nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	
	if !auth.CanViewProject(r, projectID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	
	entries, err := svc.GetAllowlist(r.Context(), agentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"entries": entries,
		"count":   len(entries),
	})
}

func toMessageDTO(msg *agentmessage.Message, framedBody string) *messageDTO {
	dto := &messageDTO{
		ID:              msg.ID,
		FromAgentID:     msg.FromAgentID,
		ToAgentID:       msg.ToAgentID,
		IssueID:         msg.IssueID,
		ParentMessageID: msg.ParentMessageID,
		HopCount:        msg.HopCount,
		Body:            msg.Body,
		FramedBody:      framedBody,
		IsActionRequest: msg.IsActionRequest,
		Delivered:       msg.Delivered,
		HeldReason:      msg.HeldReason,
		CreatedAt:       msg.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	if msg.DeliveredAt != nil {
		deliveredStr := msg.DeliveredAt.Format("2006-01-02T15:04:05Z")
		dto.DeliveredAt = &deliveredStr
	}
	return dto
}
