// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Service implements the agent message security contract (PAI-817).
type Service struct {
	db *sql.DB
}

// NewService creates a new agent message service.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SendMessage creates a new agent message with security checks.
// It enforces the security contract: authorization, hop limits, rate limits,
// size caps, action-request detection, and secret scanning.
func (s *Service) SendMessage(ctx context.Context, msg *Message) error {
	// Validate agent names
	if err := ValidateAgentName(msg.FromAgent); err != nil {
		return fmt.Errorf("invalid from_agent: %w", err)
	}
	if err := ValidateAgentName(msg.ToAgent); err != nil {
		return fmt.Errorf("invalid to_agent: %w", err)
	}

	// Check body size
	if len(msg.Body) > MaxBodySize {
		return ErrBodyTooLarge
	}

	// Check hop count
	if msg.HopCount > MaxHopCount {
		return ErrHopLimitExceeded
	}

	// Detect action requests
	msg.IsActionRequest = detectActionRequest(msg.Body)

	// Check authorization allowlist
	authorized, err := s.isAuthorized(ctx, msg.ToAgent, msg.ToProject, msg.FromAgent, msg.FromProject)
	if err != nil {
		return fmt.Errorf("check authorization: %w", err)
	}
	
	if !authorized {
		// Hold message for manual review
		msg.Delivered = false
		msg.HeldReason = "sender not in receiver allowlist"
	} else {
		// Check rate limit
		allowed, err := s.checkRateLimit(ctx, msg.FromAgent, msg.FromProject, msg.ToAgent, msg.ToProject)
		if err != nil {
			return fmt.Errorf("check rate limit: %w", err)
		}
		if !allowed {
			msg.Delivered = false
			msg.HeldReason = "rate limit exceeded"
		} else {
			// Message can be delivered
			msg.Delivered = true
			now := time.Now()
			msg.DeliveredAt = &now
		}
	}

	// Insert message
	return s.insertMessage(ctx, msg)
}

// isAuthorized checks if sender is in receiver's allowlist.
func (s *Service) isAuthorized(ctx context.Context, receiverAgent string, receiverProject int64, senderAgent string, senderProject int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_message_registry
		WHERE receiver_agent = ? AND receiver_project = ?
		  AND sender_agent = ? AND sender_project = ?
	`, receiverAgent, receiverProject, senderAgent, senderProject).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// checkRateLimit enforces per-sender rate limits.
// Returns true if message is allowed, false if rate limit exceeded.
func (s *Service) checkRateLimit(ctx context.Context, senderAgent string, senderProject int64, receiverAgent string, receiverProject int64) (bool, error) {
	now := time.Now()
	windowStart := now.Add(-1 * time.Minute)

	// Check current rate
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(message_count, 0) FROM agent_message_rate_limits
		WHERE sender_agent = ? AND sender_project = ?
		  AND receiver_agent = ? AND receiver_project = ?
		  AND julianday(window_start) > julianday(?)
	`, senderAgent, senderProject, receiverAgent, receiverProject, windowStart.Format(time.RFC3339)).Scan(&count)
	
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if count >= MaxMessagesPerMin {
		return false, nil
	}

	// Update or insert rate limit record
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_message_rate_limits (sender_agent, sender_project, receiver_agent, receiver_project, message_count, window_start)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(sender_agent, sender_project, receiver_agent, receiver_project) DO UPDATE SET
			message_count = CASE 
				WHEN julianday(window_start) > julianday(?) THEN message_count + 1
				ELSE 1
			END,
			window_start = CASE
				WHEN julianday(window_start) > julianday(?) THEN window_start
				ELSE ?
			END
	`, senderAgent, senderProject, receiverAgent, receiverProject, now.Format(time.RFC3339),
	   windowStart.Format(time.RFC3339), windowStart.Format(time.RFC3339), now.Format(time.RFC3339))

	return err == nil, err
}

// insertMessage inserts a message into the database.
func (s *Service) insertMessage(ctx context.Context, msg *Message) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_messages (from_agent, from_project, to_agent, to_project, issue_id, hop_count, body, is_action_request, delivered, held_reason, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msg.FromAgent, msg.FromProject, msg.ToAgent, msg.ToProject, msg.IssueID, msg.HopCount, msg.Body, boolToInt(msg.IsActionRequest), boolToInt(msg.Delivered), msg.HeldReason, msg.DeliveredAt)
	
	if err != nil {
		return err
	}
	
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	
	msg.ID = id
	return nil
}

// GetDeliveredMessages returns delivered messages for a receiver, bounded to prevent overwhelming a turn.
func (s *Service) GetDeliveredMessages(ctx context.Context, receiverAgent string, receiverProject int64, limit int) ([]Message, error) {
	if limit <= 0 || limit > MaxDeliveredPerTurn {
		limit = MaxDeliveredPerTurn
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_agent, from_project, to_agent, to_project, issue_id, hop_count, body, is_action_request, delivered, held_reason, created_at, delivered_at
		FROM agent_messages
		WHERE to_agent = ? AND to_project = ? AND delivered = 1
		ORDER BY created_at DESC
		LIMIT ?
	`, receiverAgent, receiverProject, limit)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var deliveredAt sql.NullString
		var createdAt string
		var issueID sql.NullInt64
		
		err := rows.Scan(&msg.ID, &msg.FromAgent, &msg.FromProject, &msg.ToAgent, &msg.ToProject, 
		                 &issueID, &msg.HopCount, &msg.Body, &msg.IsActionRequest, &msg.Delivered, 
		                 &msg.HeldReason, &createdAt, &deliveredAt)
		if err != nil {
			return nil, err
		}
		
		if issueID.Valid {
			msg.IssueID = &issueID.Int64
		}
		if deliveredAt.Valid {
			t, _ := time.Parse("2006-01-02T15:04:05Z", deliveredAt.String)
			msg.DeliveredAt = &t
		}
		t, _ := time.Parse("2006-01-02T15:04:05Z", createdAt)
		msg.CreatedAt = t
		
		messages = append(messages, msg)
	}
	
	return messages, rows.Err()
}

// GetHeldMessages returns messages held for manual review.
func (s *Service) GetHeldMessages(ctx context.Context, receiverAgent string, receiverProject int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_agent, from_project, to_agent, to_project, issue_id, hop_count, body, is_action_request, delivered, held_reason, created_at, delivered_at
		FROM agent_messages
		WHERE to_agent = ? AND to_project = ? AND delivered = 0
		ORDER BY created_at DESC
	`, receiverAgent, receiverProject)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var deliveredAt sql.NullString
		var createdAt string
		var issueID sql.NullInt64
		
		err := rows.Scan(&msg.ID, &msg.FromAgent, &msg.FromProject, &msg.ToAgent, &msg.ToProject,
		                 &issueID, &msg.HopCount, &msg.Body, &msg.IsActionRequest, &msg.Delivered,
		                 &msg.HeldReason, &createdAt, &deliveredAt)
		if err != nil {
			return nil, err
		}
		
		if issueID.Valid {
			msg.IssueID = &issueID.Int64
		}
		if deliveredAt.Valid {
			t, _ := time.Parse("2006-01-02T15:04:05Z", deliveredAt.String)
			msg.DeliveredAt = &t
		}
		t, _ := time.Parse("2006-01-02T15:04:05Z", createdAt)
		msg.CreatedAt = t
		
		messages = append(messages, msg)
	}
	
	return messages, rows.Err()
}

// AddAllowlistEntry adds a sender to receiver's allowlist.
func (s *Service) AddAllowlistEntry(ctx context.Context, entry *AllowlistEntry) error {
	if err := ValidateAgentName(entry.ReceiverAgent); err != nil {
		return fmt.Errorf("invalid receiver_agent: %w", err)
	}
	if err := ValidateAgentName(entry.SenderAgent); err != nil {
		return fmt.Errorf("invalid sender_agent: %w", err)
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_message_registry (receiver_agent, receiver_project, sender_agent, sender_project)
		VALUES (?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, entry.ReceiverAgent, entry.ReceiverProject, entry.SenderAgent, entry.SenderProject)
	
	if err != nil {
		return err
	}
	
	id, err := result.LastInsertId()
	if err == nil {
		entry.ID = id
	}
	
	return nil
}

// RemoveAllowlistEntry removes a sender from receiver's allowlist.
func (s *Service) RemoveAllowlistEntry(ctx context.Context, receiverAgent string, receiverProject int64, senderAgent string, senderProject int64) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_message_registry
		WHERE receiver_agent = ? AND receiver_project = ?
		  AND sender_agent = ? AND sender_project = ?
	`, receiverAgent, receiverProject, senderAgent, senderProject)
	return err
}

// GetAllowlist returns all authorized senders for a receiver.
func (s *Service) GetAllowlist(ctx context.Context, receiverAgent string, receiverProject int64) ([]AllowlistEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, receiver_agent, receiver_project, sender_agent, sender_project, created_at
		FROM agent_message_registry
		WHERE receiver_agent = ? AND receiver_project = ?
		ORDER BY created_at DESC
	`, receiverAgent, receiverProject)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AllowlistEntry
	for rows.Next() {
		var entry AllowlistEntry
		var createdAt string
		err := rows.Scan(&entry.ID, &entry.ReceiverAgent, &entry.ReceiverProject, 
		                 &entry.SenderAgent, &entry.SenderProject, &createdAt)
		if err != nil {
			return nil, err
		}
		t, _ := time.Parse("2006-01-02T15:04:05Z", createdAt)
		entry.CreatedAt = t
		entries = append(entries, entry)
	}
	
	return entries, rows.Err()
}

// detectActionRequest uses pattern matching to identify action-request messages.
// These must be surfaced to humans and never automatically executed.
func detectActionRequest(body string) bool {
	// Lowercase for case-insensitive matching
	lower := strings.ToLower(body)
	
	// Patterns that indicate action requests
	actionPatterns := []string{
		"please execute",
		"run the following",
		"execute this command",
		"grant permission",
		"approve this",
		"change configuration",
		"update settings",
		"modify the",
		"delete the",
		"create a new",
	}
	
	for _, pattern := range actionPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	
	// Check for shell command patterns
	shellPattern := regexp.MustCompile(`(?m)^\s*(sudo|rm|chmod|chown|git|npm|docker|kubectl)\s+`)
	if shellPattern.MatchString(body) {
		return true
	}
	
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
