// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/db"
)

// RegisterAgentMessageRoutes registers HTTP routes for agent messaging (PAI-817).
func RegisterAgentMessageRoutes(r chi.Router) {
	// Send message
	r.Post("/api/agent-messages/send", sendAgentMessage)
	
	// Get delivered messages for an agent
	r.Get("/api/agent-messages/delivered/{agentID}", getDeliveredMessages)
	
	// Get held messages for an agent (requires admin or agent owner)
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
		
		// TODO: Add authorization check - ensure user can send as from_agent_id
		
		msg, err := svc.SendMessage(r.Context(), req.FromAgentID, req.ToAgentID, req.IssueID, req.ParentMessageID, req.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		
		resp := sendMessageResponse{
			Message:         toMessageDTO(msg),
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
		
		limit := 5 // Default to MaxDeliveredPerTurn
		if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
			if l, err := strconv.Atoi(limitStr); err == nil {
				limit = l
			}
		}
		
		messages, err := svc.GetDeliveredMessages(r.Context(), agentID, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		dtos := make([]messageDTO, len(messages))
		for i, msg := range messages {
			dtos[i] = *toMessageDTO(&msg)
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
		
		// TODO: Add authorization check - ensure user can view held messages for this agent
		
		messages, err := svc.GetHeldMessages(r.Context(), agentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		
		dtos := make([]messageDTO, len(messages))
		for i, msg := range messages {
			dtos[i] = *toMessageDTO(&msg)
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
		
		// TODO: Add authorization check - ensure user owns receiver agent
		
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
		
		// TODO: Add authorization check - ensure user owns receiver agent
		
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

func toMessageDTO(msg *agentmessage.Message) *messageDTO {
	dto := &messageDTO{
		ID:              msg.ID,
		FromAgentID:     msg.FromAgentID,
		ToAgentID:       msg.ToAgentID,
		IssueID:         msg.IssueID,
		ParentMessageID: msg.ParentMessageID,
		HopCount:        msg.HopCount,
		Body:            msg.Body,
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
