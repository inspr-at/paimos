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
// Uses existing project_agents registry for authorization.
type Service struct {
	db *sql.DB
}

// NewService creates a new agent message service.
func NewService(db *sql.DB) *Service {
	return &Service{db: db}
}

// SendMessage creates a new agent message with security checks.
// Hop count is end-to-end and system-incremented based on parent_message_id.
// If parentMessageID is provided, hop count is parent + 1.
// If parentMessageID is omitted, looks up the most recent message in the conversation
// to prevent hop-reset attacks. Only truly new conversations start at hop=1.
// Action-request messages are NEVER delivered, only held for human review.
func (s *Service) SendMessage(ctx context.Context, fromAgentID, toAgentID int64, issueID *int64, parentMessageID *int64, body string) (*Message, error) {
	// Check body size
	if len(body) > MaxBodySize {
		return nil, ErrBodyTooLarge
	}

	// Calculate hop count (end-to-end, system-tracked)
	hopCount := 1
	if parentMessageID != nil {
		// Explicit parent provided - use it
		var parentHop int
		err := s.db.QueryRowContext(ctx, `SELECT hop_count FROM agent_messages WHERE id = ?`, *parentMessageID).Scan(&parentHop)
		if err != nil {
			return nil, fmt.Errorf("fetch parent hop count: %w", err)
		}
		hopCount = parentHop + 1
	} else {
		// No parent provided - check if there's a recent message in this conversation
		// Hop is end-to-end on the conversation, not per (from,to) pair.
		// Look up last message in EITHER direction between the two agents.
		var lastHop sql.NullInt64
		err := s.db.QueryRowContext(ctx, `
			SELECT hop_count FROM agent_messages
			WHERE (from_agent_id = ? AND to_agent_id = ?) OR (from_agent_id = ? AND to_agent_id = ?)
			ORDER BY created_at DESC, id DESC LIMIT 1
		`, fromAgentID, toAgentID, toAgentID, fromAgentID).Scan(&lastHop)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("lookup last message hop: %w", err)
		}
		if lastHop.Valid {
			// Continuation of existing conversation
			hopCount = int(lastHop.Int64) + 1
		}
		// else: genuinely new conversation, hop=1
	}

	// Check hop ceiling
	if hopCount > MaxHopCount {
		return nil, ErrHopLimitExceeded
	}

	// Detect action requests
	isActionRequest := detectActionRequest(body)

	msg := &Message{
		FromAgentID:     fromAgentID,
		ToAgentID:       toAgentID,
		IssueID:         issueID,
		ParentMessageID: parentMessageID,
		HopCount:        hopCount,
		Body:            body,
		IsActionRequest: isActionRequest,
		CreatedAt:       time.Now(),
	}

	// Action-request messages are NEVER delivered, always held
	if isActionRequest {
		msg.Delivered = false
		msg.HeldReason = "action request - requires human approval"
		return msg, s.insertMessage(ctx, msg)
	}

	// Check authorization allowlist (uses project_agents registry)
	authorized, err := s.isAuthorized(ctx, toAgentID, fromAgentID)
	if err != nil {
		return nil, fmt.Errorf("check authorization: %w", err)
	}
	
	if !authorized {
		// Hold message for manual review - unlisted senders are HELD, not delivered
		msg.Delivered = false
		msg.HeldReason = "sender not in receiver allowlist"
	} else {
		// Check rate limit
		allowed, err := s.checkRateLimit(ctx, fromAgentID, toAgentID)
		if err != nil {
			return nil, fmt.Errorf("check rate limit: %w", err)
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

	return msg, s.insertMessage(ctx, msg)
}

// isAuthorized checks if sender is in receiver's allowlist using project_agents registry.
func (s *Service) isAuthorized(ctx context.Context, receiverAgentID, senderAgentID int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_message_allowlist
		WHERE receiver_agent_id = ? AND sender_agent_id = ?
	`, receiverAgentID, senderAgentID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// checkRateLimit enforces per-sender rate limits.
// Returns true if message is allowed, false if rate limit exceeded.
func (s *Service) checkRateLimit(ctx context.Context, senderAgentID, receiverAgentID int64) (bool, error) {
	now := time.Now()
	windowStart := now.Add(-1 * time.Minute)

	// Check current rate
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(message_count, 0) FROM agent_message_rate_limits
		WHERE sender_agent_id = ? AND receiver_agent_id = ?
		  AND julianday(window_start) > julianday(?)
	`, senderAgentID, receiverAgentID, windowStart.Format(time.RFC3339)).Scan(&count)
	
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}

	if count >= MaxMessagesPerMin {
		return false, nil
	}

	// Update or insert rate limit record
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_message_rate_limits (sender_agent_id, receiver_agent_id, message_count, window_start)
		VALUES (?, ?, 1, ?)
		ON CONFLICT(sender_agent_id, receiver_agent_id) DO UPDATE SET
			message_count = CASE 
				WHEN julianday(window_start) > julianday(?) THEN message_count + 1
				ELSE 1
			END,
			window_start = CASE
				WHEN julianday(window_start) > julianday(?) THEN window_start
				ELSE ?
			END
	`, senderAgentID, receiverAgentID, now.Format(time.RFC3339),
	   windowStart.Format(time.RFC3339), windowStart.Format(time.RFC3339), now.Format(time.RFC3339))

	return err == nil, err
}

// insertMessage inserts a message into the database.
func (s *Service) insertMessage(ctx context.Context, msg *Message) error {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_messages (from_agent_id, to_agent_id, issue_id, parent_message_id, hop_count, body, is_action_request, delivered, held_reason, delivered_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, msg.FromAgentID, msg.ToAgentID, msg.IssueID, msg.ParentMessageID, msg.HopCount, msg.Body, boolToInt(msg.IsActionRequest), boolToInt(msg.Delivered), msg.HeldReason, msg.DeliveredAt)
	
	if err != nil {
		// Map SQLite CHECK constraint failure for secrets to typed error
		// Only map the secret-specific CHECK, not all CHECK constraints
		errStr := err.Error()
		if strings.Contains(errStr, "paimos_contains_secret_like") ||
		   strings.Contains(errStr, "message body contains secret-like content") {
			return ErrContainsSecret
		}
		return err
	}
	
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	
	msg.ID = id
	return nil
}

// GetDeliveredMessages returns delivered messages for a receiver with per-turn bound.
// Uses cursor-based pagination (after_id) to prevent clients from bypassing the
// per-turn ceiling. Only non-action-request messages are returned.
func (s *Service) GetDeliveredMessages(ctx context.Context, receiverAgentID int64, limit int, afterID int64) ([]Message, error) {
	if limit <= 0 || limit > MaxDeliveredPerTurn {
		limit = MaxDeliveredPerTurn
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_agent_id, to_agent_id, issue_id, parent_message_id, hop_count, body, is_action_request, delivered, held_reason, created_at, delivered_at
		FROM agent_messages
		WHERE to_agent_id = ? AND delivered = 1 AND id > ?
		ORDER BY id ASC
		LIMIT ?
	`, receiverAgentID, afterID, limit)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// GetHeldMessages returns messages held for manual review.
// Includes both unauthorized senders and action-request messages.
func (s *Service) GetHeldMessages(ctx context.Context, receiverAgentID int64) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, from_agent_id, to_agent_id, issue_id, parent_message_id, hop_count, body, is_action_request, delivered, held_reason, created_at, delivered_at
		FROM agent_messages
		WHERE to_agent_id = ? AND delivered = 0
		ORDER BY created_at DESC
	`, receiverAgentID)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return s.scanMessages(rows)
}

// scanMessages scans message rows from the database.
func (s *Service) scanMessages(rows *sql.Rows) ([]Message, error) {
	var messages []Message
	for rows.Next() {
		var msg Message
		var deliveredAt sql.NullString
		var createdAt string
		var issueID, parentMessageID sql.NullInt64
		
		err := rows.Scan(&msg.ID, &msg.FromAgentID, &msg.ToAgentID, &issueID, &parentMessageID,
		                 &msg.HopCount, &msg.Body, &msg.IsActionRequest, &msg.Delivered,
		                 &msg.HeldReason, &createdAt, &deliveredAt)
		if err != nil {
			return nil, err
		}
		
		if issueID.Valid {
			msg.IssueID = &issueID.Int64
		}
		if parentMessageID.Valid {
			msg.ParentMessageID = &parentMessageID.Int64
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
// Uses existing project_agents IDs (not free-text names).
func (s *Service) AddAllowlistEntry(ctx context.Context, receiverAgentID, senderAgentID int64) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_message_allowlist (receiver_agent_id, sender_agent_id)
		VALUES (?, ?)
		ON CONFLICT DO NOTHING
	`, receiverAgentID, senderAgentID)
	return err
}

// RemoveAllowlistEntry removes a sender from receiver's allowlist.
func (s *Service) RemoveAllowlistEntry(ctx context.Context, receiverAgentID, senderAgentID int64) error {
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM agent_message_allowlist
		WHERE receiver_agent_id = ? AND sender_agent_id = ?
	`, receiverAgentID, senderAgentID)
	return err
}

// GetAllowlist returns all authorized senders for a receiver.
func (s *Service) GetAllowlist(ctx context.Context, receiverAgentID int64) ([]AllowlistEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, receiver_agent_id, sender_agent_id, created_at
		FROM agent_message_allowlist
		WHERE receiver_agent_id = ?
		ORDER BY created_at DESC
	`, receiverAgentID)
	
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []AllowlistEntry
	for rows.Next() {
		var entry AllowlistEntry
		var createdAt string
		err := rows.Scan(&entry.ID, &entry.ReceiverAgentID, &entry.SenderAgentID, &createdAt)
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
// These must be surfaced to humans and NEVER automatically executed.
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
