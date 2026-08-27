// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

var addressPart = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

type SendEnvelopeInput struct {
	ProjectID int64
	Sender    string
	SessionID string
	To        string
	IssueID   *int64
	ReplyTo   string
	ThreadID  string
	Body      string
	Metadata  map[string]any
	// ActionRequest is the sender's explicit, typed declaration that the
	// free-text body asks the receiver to act. Heuristic detection remains a
	// defence-in-depth fallback, but callers do not have to depend on prose
	// classification to enter the human-only held path.
	ActionRequest  bool
	DeliveryLevel  string
	IdempotencyKey string
}

type ListFilter struct {
	ProjectID     int64
	To            string
	ThreadID      string
	IssueID       *int64
	DeliveredOnly bool
	AfterID       int64
	Limit         int
}

type InboxInput struct {
	ProjectID     int64
	Address       string
	Agent         string
	WorkerAdapter string
	AfterID       int64
	Limit         int
}

type AckInput struct {
	ProjectID int64
	Address   string
	Agent     string
	Cursor    int64
}

func parseAddress(raw string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 || !addressPart.MatchString(parts[0]) || !addressPart.MatchString(parts[1]) {
		return "", "", &CodedError{Code: AddressErrorCodeInvalid, Err: fmt.Errorf("address must be <harness>:<registered-agent>")}
	}
	return strings.ToLower(parts[0]), parts[1], nil
}

func coded(code, detail string) error { return &CodedError{Code: code, Err: errors.New(detail)} }

// SendEnvelope resolves both parties inside one project and derives the sender
// exclusively from trusted request attribution. Client-supplied numeric agent
// IDs are never accepted.
func (s *Service) SendEnvelope(ctx context.Context, in SendEnvelopeInput) (*Envelope, error) {
	if strings.TrimSpace(in.Sender) == "" {
		return nil, coded("agent_message_attribution_required", "X-Paimos-Agent-Name attribution is required")
	}
	targetHarness, targetName, err := parseAddress(in.To)
	if err != nil {
		return nil, err
	}
	if !utf8.ValidString(in.Body) || len([]byte(in.Body)) == 0 {
		return nil, coded("agent_message_body_required", "message body is required")
	}
	if len([]byte(in.Body)) > MaxBodySize {
		return nil, ErrBodyTooLarge
	}
	in.DeliveryLevel = strings.ToLower(strings.TrimSpace(in.DeliveryLevel))
	if in.DeliveryLevel == "" {
		in.DeliveryLevel = "simple"
	}
	if in.DeliveryLevel != "simple" && in.DeliveryLevel != "steer" {
		return nil, coded("agent_message_delivery_level_invalid", "delivery_level must be simple or steer")
	}
	if in.IdempotencyKey != "" && (!utf8.ValidString(in.IdempotencyKey) || len([]byte(in.IdempotencyKey)) < 1 || len([]byte(in.IdempotencyKey)) > 128) {
		return nil, coded("agent_message_idempotency_key_invalid", "Idempotency-Key must be 1 to 128 UTF-8 bytes")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var projectKey string
	if err := tx.QueryRowContext(ctx, `SELECT key FROM projects WHERE id=?`, in.ProjectID).Scan(&projectKey); err != nil {
		return nil, coded("agent_message_project_unknown", "project not found")
	}
	var fromID, toID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, strings.TrimSpace(in.Sender)).Scan(&fromID); err != nil {
		return nil, coded("agent_message_sender_unknown", "attributed sender is not registered in this project")
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, in.ProjectID, targetName).Scan(&toID); err != nil {
		return nil, coded("agent_message_addressee_unknown", "addressee is not registered in this project")
	}
	instance := instanceName()
	if instance == "" {
		return nil, coded("agent_message_instance_invalid", "PAIMOS_AGENT_BUS_INSTANCE must be 1 to 64 UTF-8 bytes")
	}
	requestDigest, err := sendRequestDigest(in, targetHarness+":"+targetName)
	if err != nil {
		return nil, coded("agent_message_metadata_invalid", "metadata must be JSON encodable")
	}
	if in.IdempotencyKey != "" {
		keyDigest := sha256.Sum256([]byte(instance + "\x00" + in.IdempotencyKey))
		var priorDigest []byte
		var priorMessageID string
		err := tx.QueryRowContext(ctx, `SELECT ami.request_digest,am.message_id
			FROM agent_message_idempotency ami JOIN agent_messages am ON am.id=ami.message_row_id
			WHERE ami.instance=? AND ami.project_id=? AND ami.sender_agent_id=? AND ami.key_digest=?`,
			instance, in.ProjectID, fromID, keyDigest[:]).Scan(&priorDigest, &priorMessageID)
		if err == nil {
			if !bytes.Equal(priorDigest, requestDigest) {
				return nil, coded("agent_message_idempotency_conflict", "Idempotency-Key was already used for a different message request")
			}
			_ = tx.Rollback()
			return s.GetEnvelope(ctx, in.ProjectID, priorMessageID)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}

	taskID := ""
	if in.IssueID != nil {
		if err := tx.QueryRowContext(ctx, `SELECT p.key||'-'||i.issue_number FROM issues i JOIN projects p ON p.id=i.project_id WHERE i.id=? AND i.project_id=?`, *in.IssueID, in.ProjectID).Scan(&taskID); err != nil {
			return nil, coded("agent_message_issue_unknown", "issue is not in this project")
		}
	}

	hop, parentID, threadID := 1, (*int64)(nil), strings.TrimSpace(in.ThreadID)
	if in.ReplyTo != "" {
		var parentHop int
		var parentThread string
		var pid int64
		if err := tx.QueryRowContext(ctx, `SELECT am.id,am.hop_count,am.thread_id FROM agent_messages am JOIN project_agents pa ON pa.id=am.from_agent_id WHERE am.message_id=? AND pa.project_id=?`, in.ReplyTo, in.ProjectID).Scan(&pid, &parentHop, &parentThread); err != nil {
			return nil, coded("agent_message_reply_unknown", "reply_to message not found in project")
		}
		parentID, hop = &pid, parentHop+1
		if threadID == "" {
			threadID = parentThread
		}
	} else if threadID != "" {
		// A caller cannot reset the hop budget merely by omitting reply_to.
		// An existing thread always advances from its last durable row.
		var lastHop int
		err := tx.QueryRowContext(ctx, `SELECT am.hop_count FROM agent_messages am JOIN project_agents pa ON pa.id=am.from_agent_id WHERE pa.project_id=? AND am.thread_id=? ORDER BY am.id DESC LIMIT 1`, in.ProjectID, threadID).Scan(&lastHop)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			hop = lastHop + 1
		}
	}
	if hop > MaxHopCount {
		return nil, ErrHopLimitExceeded
	}

	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages WHERE from_agent_id=? AND created_at>=datetime('now','-1 minute')`, fromID).Scan(&recent); err != nil {
		return nil, err
	}
	if recent >= MaxMessagesPerMin {
		return nil, ErrRateLimitExceeded
	}

	messageUUID, err := uuid.NewV7()
	if err != nil {
		messageUUID = uuid.Must(uuid.NewRandom())
	}
	messageID := messageUUID.String()
	if threadID == "" {
		threadID = messageID
	}
	fromAddress := "paimos:" + strings.TrimSpace(in.Sender)
	toAddress := targetHarness + ":" + targetName
	parts := []TextPart{{Kind: "text", Text: in.Body}}
	partsJSON, _ := json.Marshal(parts)
	metadata := in.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, coded("agent_message_metadata_invalid", "metadata must be JSON encodable")
	}

	isAction := in.ActionRequest || detectActionRequest(in.Body)
	authorized := 0
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_message_allowlist WHERE receiver_agent_id=? AND sender_agent_id=?`, toID, fromID).Scan(&authorized); err != nil {
		return nil, err
	}
	delivered := authorized > 0 && !isAction
	heldReason := ""
	var deliveredAt any
	if isAction {
		heldReason = "action request - requires human approval"
	} else if !delivered {
		heldReason = "sender not in receiver allowlist"
	}
	if delivered {
		deliveredAt = time.Now().UTC().Format(time.RFC3339)
	}
	primaryTargetID, fallbackTargetID := "", ""
	if delivered {
		primaryTargetID, fallbackTargetID, err = resolveTargetVersionsTx(ctx, tx, instance, in.ProjectID, toAddress)
		if err != nil {
			return nil, err
		}
	}

	result, err := tx.ExecContext(ctx, `INSERT INTO agent_messages
		(from_agent_id,to_agent_id,issue_id,parent_message_id,hop_count,body,is_action_request,delivered,held_reason,delivered_at,
		 message_id,context_id,task_id,role,parts_json,metadata_json,from_address,to_address,reply_to,thread_id,session_id,
		 delivery_level,delivery_fallback,delivery_primary_target_id,delivery_fallback_target_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		fromID, toID, in.IssueID, parentID, hop, in.Body, boolToInt(isAction), boolToInt(delivered), heldReason, deliveredAt,
		messageID, projectKey, taskID, "agent", string(partsJSON), string(metadataJSON), fromAddress, toAddress, in.ReplyTo, threadID, strings.TrimSpace(in.SessionID),
		in.DeliveryLevel, "simple", nullableString(primaryTargetID), nullableString(fallbackTargetID))
	if err != nil {
		if strings.Contains(err.Error(), "paimos_contains_secret_like") {
			return nil, ErrContainsSecret
		}
		return nil, err
	}
	messageRowID, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	if delivered {
		state, reason := "pending", ""
		if primaryTargetID == "" && fallbackTargetID == "" {
			state, reason = "blocked", "target_missing"
		}
		deliveryUUID, uuidErr := uuid.NewV7()
		if uuidErr != nil {
			deliveryUUID = uuid.Must(uuid.NewRandom())
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_deliveries
			(delivery_id,message_row_id,instance,primary_target_id,fallback_target_id,requested_level,state,fallback_reason)
			VALUES(?,?,?,?,?,?,?,?)`, deliveryUUID.String(), messageRowID, instance,
			nullableString(primaryTargetID), nullableString(fallbackTargetID), in.DeliveryLevel, state, reason); err != nil {
			return nil, err
		}
	}
	if in.IdempotencyKey != "" {
		keyDigest := sha256.Sum256([]byte(instance + "\x00" + in.IdempotencyKey))
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_idempotency
			(instance,project_id,sender_agent_id,key_digest,request_digest,message_row_id) VALUES(?,?,?,?,?,?)`,
			instance, in.ProjectID, fromID, keyDigest[:], requestDigest, messageRowID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetEnvelope(ctx, in.ProjectID, messageID)
}

func sendRequestDigest(in SendEnvelopeInput, normalizedTo string) ([]byte, error) {
	metadata := in.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	canonical := struct {
		To            string         `json:"to"`
		IssueID       *int64         `json:"issue_id,omitempty"`
		ReplyTo       string         `json:"reply_to,omitempty"`
		ThreadID      string         `json:"thread_id,omitempty"`
		Body          string         `json:"body"`
		Metadata      map[string]any `json:"metadata"`
		ActionRequest bool           `json:"is_action_request"`
		DeliveryLevel string         `json:"delivery_level"`
	}{normalizedTo, in.IssueID, strings.TrimSpace(in.ReplyTo), strings.TrimSpace(in.ThreadID), in.Body, metadata, in.ActionRequest, in.DeliveryLevel}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return digest[:], nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Service) GetEnvelope(ctx context.Context, projectID int64, messageID string) (*Envelope, error) {
	return scanEnvelope(s.db.QueryRowContext(ctx, envelopeSelect+` WHERE pa.project_id=? AND am.message_id=?`, projectID, messageID))
}

func (s *Service) ListEnvelopes(ctx context.Context, f ListFilter) ([]Envelope, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	if f.To != "" && f.Limit > MaxDeliveredPerTurn {
		f.Limit = MaxDeliveredPerTurn
	}
	q := envelopeSelect + ` WHERE pa.project_id=? AND am.id>?`
	args := []any{f.ProjectID, f.AfterID}
	if f.To != "" {
		q += ` AND am.to_address=?`
		args = append(args, f.To)
	}
	if f.ThreadID != "" {
		q += ` AND am.thread_id=?`
		args = append(args, f.ThreadID)
	}
	if f.IssueID != nil {
		q += ` AND am.issue_id=?`
		args = append(args, *f.IssueID)
	}
	if f.DeliveredOnly {
		q += ` AND am.delivered=1 AND am.is_action_request=0`
	}
	q += ` ORDER BY am.id ASC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Envelope{}
	for rows.Next() {
		e, err := scanEnvelope(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListInbox binds an addressee read to trusted request attribution and starts
// after the receiver's durable acknowledged cursor. A caller may advance the
// in-memory position with AfterID, but only AckInbox changes durable state.
func (s *Service) ListInbox(ctx context.Context, in InboxInput) (*InboxPage, error) {
	if in.WorkerAdapter != "" && !IsLocalWorkerAdapter(in.WorkerAdapter) {
		return nil, coded("agent_message_worker_adapter_invalid", "delivery must name a local worker adapter: codex, claude_resume, or claude_channel")
	}
	address, _, err := s.resolveAttributedInbox(ctx, in.ProjectID, in.Address, in.Agent)
	if err != nil {
		return nil, err
	}
	var cursor int64
	err = s.db.QueryRowContext(ctx, `SELECT cursor FROM agent_message_cursors WHERE project_id=? AND address=?`, in.ProjectID, address).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if in.AfterID < cursor {
		in.AfterID = cursor
	}
	messages, err := s.ListEnvelopes(ctx, ListFilter{
		ProjectID: in.ProjectID, To: address, DeliveredOnly: true, AfterID: in.AfterID, Limit: in.Limit,
	})
	if err != nil {
		return nil, err
	}
	if in.WorkerAdapter != "" {
		leased := messages[:0]
		for i := range messages {
			include, err := s.attachDeliveryWork(ctx, in.ProjectID, address, in.Agent, in.WorkerAdapter, &messages[i])
			if err != nil {
				return nil, err
			}
			if include {
				leased = append(leased, messages[i])
			}
		}
		messages = leased
	}
	next := in.AfterID
	for i := range messages {
		if messages[i].Cursor > next {
			next = messages[i].Cursor
		}
	}
	return &InboxPage{Address: address, Cursor: cursor, NextCursor: next, Messages: messages}, nil
}

// AckInbox advances one receiver/address cursor monotonically and records the
// read timestamp for delivered rows covered by that acknowledgement. The
// cursor must name a real delivered message in this inbox, preventing a client
// from skipping arbitrary future rows.
func (s *Service) AckInbox(ctx context.Context, in AckInput) (*CursorState, error) {
	if in.Cursor <= 0 {
		return nil, coded("agent_message_cursor_invalid", "cursor must be greater than zero")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	address, agentID, err := resolveAttributedInboxQuery(ctx, tx, in.ProjectID, in.Address, in.Agent)
	if err != nil {
		return nil, err
	}
	state, err := ackInboxTx(ctx, tx, in.ProjectID, address, agentID, in.Cursor)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return state, nil
}

func ackInboxTx(ctx context.Context, tx *sql.Tx, projectID int64, address string, agentID, cursor int64) (*CursorState, error) {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT cursor FROM agent_message_cursors WHERE project_id=? AND address=?`, projectID, address).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if cursor <= current {
		return &CursorState{Address: address, Cursor: current}, nil
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages am
		JOIN project_agents pa ON pa.id=am.to_agent_id
		WHERE pa.project_id=? AND am.id=? AND am.to_address=? AND am.delivered=1 AND am.is_action_request=0`,
		projectID, cursor, address).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, coded("agent_message_cursor_unknown", "cursor is not a delivered message in this inbox")
	}
	readAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_messages SET read_at=COALESCE(read_at,?)
		WHERE to_agent_id=? AND to_address=? AND delivered=1 AND is_action_request=0 AND id>? AND id<=?`,
		readAt, agentID, address, current, cursor); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_cursors(project_id,project_agent_id,address,cursor,updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(project_id,address) DO UPDATE SET cursor=excluded.cursor,updated_at=excluded.updated_at
		WHERE excluded.cursor>agent_message_cursors.cursor`, projectID, agentID, address, cursor, readAt); err != nil {
		return nil, err
	}
	return &CursorState{Address: address, Cursor: cursor}, nil
}

// AllowSender is the human/operator control plane for the PAI-817 sender
// allowlist. Addresses stay name-based; only the existing project-agent IDs are
// persisted.
func (s *Service) AllowSender(ctx context.Context, projectID int64, receiverAddress, senderAddress string) error {
	_, receiverName, err := parseAddress(receiverAddress)
	if err != nil {
		return err
	}
	_, senderName, err := parseAddress(senderAddress)
	if err != nil {
		return err
	}
	var receiverID, senderID int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, receiverName).Scan(&receiverID); err != nil {
		return coded("agent_message_addressee_unknown", "receiver is not registered in this project")
	}
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, senderName).Scan(&senderID); err != nil {
		return coded("agent_message_sender_unknown", "sender is not registered in this project")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO agent_message_allowlist(receiver_agent_id,sender_agent_id) VALUES(?,?)
		ON CONFLICT(receiver_agent_id,sender_agent_id) DO NOTHING`, receiverID, senderID)
	return err
}

func (s *Service) resolveAttributedInbox(ctx context.Context, projectID int64, rawAddress, attributedAgent string) (string, int64, error) {
	return resolveAttributedInboxQuery(ctx, s.db, projectID, rawAddress, attributedAgent)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func resolveAttributedInboxQuery(ctx context.Context, q queryRower, projectID int64, rawAddress, attributedAgent string) (string, int64, error) {
	harness, name, err := parseAddress(rawAddress)
	if err != nil {
		return "", 0, err
	}
	if strings.TrimSpace(attributedAgent) == "" {
		return "", 0, coded("agent_message_attribution_required", "X-Paimos-Agent-Name attribution is required")
	}
	if name != strings.TrimSpace(attributedAgent) {
		return "", 0, coded("agent_message_addressee_mismatch", "attributed agent does not match inbox addressee")
	}
	var agentID int64
	if err := q.QueryRowContext(ctx, `SELECT id FROM project_agents WHERE project_id=? AND name=?`, projectID, name).Scan(&agentID); err != nil {
		return "", 0, coded("agent_message_addressee_unknown", "addressee is not registered in this project")
	}
	return harness + ":" + name, agentID, nil
}

const envelopeSelect = `SELECT am.id,am.message_id,am.context_id,am.task_id,am.role,am.parts_json,am.metadata_json,
	am.from_address,am.to_address,am.reply_to,am.thread_id,am.hop_count,am.delivered,am.held_reason,am.is_action_request,am.created_at,COALESCE(am.read_at,''),
	am.delivery_level,am.delivery_fallback,COALESCE(am.delivery_primary_target_id,''),
	COALESCE((SELECT target_kind FROM agent_message_targets WHERE id=am.delivery_primary_target_id),''),
	COALESCE(am.delivery_fallback_target_id,''),
	COALESCE((SELECT target_kind FROM agent_message_targets WHERE id=am.delivery_fallback_target_id),'')
	FROM agent_messages am JOIN project_agents pa ON pa.id=am.from_agent_id`

type scanner interface{ Scan(...any) error }

func scanEnvelope(row scanner) (*Envelope, error) {
	var e Envelope
	var parts, metadata, primaryID, primaryKind, fallbackID, fallbackKind string
	if err := row.Scan(&e.Cursor, &e.MessageID, &e.ContextID, &e.TaskID, &e.Role, &parts, &metadata, &e.From, &e.To, &e.ReplyTo, &e.ThreadID, &e.Hop, &e.Delivered, &e.HeldReason, &e.IsActionRequest, &e.CreatedAt, &e.ReadAt,
		&e.DeliveryLevel, &e.DeliveryFallback, &primaryID, &primaryKind, &fallbackID, &fallbackKind); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(parts), &e.Parts); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadata), &e.Metadata); err != nil {
		return nil, err
	}
	if primaryID != "" || fallbackID != "" {
		e.DeliveryTarget = &DeliveryTargetSnapshot{}
		if primaryID != "" {
			e.DeliveryTarget.Primary = &DeliveryTargetBinding{BindingID: primaryID, Kind: primaryKind}
		}
		if fallbackID != "" {
			e.DeliveryTarget.SimpleFallback = &DeliveryTargetBinding{BindingID: fallbackID, Kind: fallbackKind}
		}
	}
	return &e, nil
}
