// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const replyObligationInitialDelay = 5 * time.Minute

var replyObligationBackoff = [...]time.Duration{
	15 * time.Minute,
	time.Hour,
	4 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
}

const replyObligationMaxResurfaces = int64(len(replyObligationBackoff) + 1)

// HumanResolutionAuthority reauthorizes a human principal in the same
// transaction that records the decision. Both values are server-derived,
// value-free audit identities: the real actor user and the safe credential
// identity (never the session bearer or API key value).
type HumanResolutionAuthority func(context.Context, *sql.Tx) (actorUserID int64, actorSessionID string, err error)

type ResolveHeldMessageInput struct {
	ProjectID      int64
	MessageID      string
	Outcome        string
	IdempotencyKey string
	Authority      HumanResolutionAuthority
}

func closeReplyObligationTx(ctx context.Context, tx *sql.Tx, parentRowID, replyRowID int64) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	_, err := tx.ExecContext(ctx, `UPDATE agent_reply_obligations SET state='closed',closing_message_row_id=?,
		next_attention_at=NULL,closed_at=? WHERE message_row_id=? AND state='open' AND EXISTS(
		 SELECT 1 FROM agent_messages original JOIN agent_messages reply ON reply.id=?
		 WHERE original.id=agent_reply_obligations.message_row_id
		  AND reply.parent_message_id=original.id AND reply.reply_to=original.message_id
		  AND reply.delivered=1 AND reply.is_action_request=0
		  AND reply.from_agent_id=original.to_agent_id AND reply.to_agent_id=original.from_agent_id)`,
		replyRowID, now, parentRowID, replyRowID)
	return err
}

func replyObligationDelay(resurfaceCount int64) time.Duration {
	if resurfaceCount <= 0 {
		return replyObligationInitialDelay
	}
	index := resurfaceCount - 1
	if index >= int64(len(replyObligationBackoff)) {
		index = int64(len(replyObligationBackoff) - 1)
	}
	return replyObligationBackoff[index]
}

func validActorSession(value string) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= 64 && !strings.ContainsAny(value, "\x00\r\n")
}

// ResolveHeldMessage records a human-only terminal fact for a held action
// request. The held agent_messages row remains byte-for-byte unchanged.
func (s *Service) ResolveHeldMessage(ctx context.Context, in ResolveHeldMessageInput) (*HumanResolution, error) {
	in.MessageID = strings.TrimSpace(in.MessageID)
	in.Outcome = strings.ToLower(strings.TrimSpace(in.Outcome))
	if in.MessageID == "" {
		return nil, coded("agent_message_resolution_message_required", "message id is required")
	}
	if in.Outcome != "resolved" && in.Outcome != "dismissed" {
		return nil, coded("agent_message_resolution_outcome_invalid", "outcome must be resolved or dismissed")
	}
	if !utf8.ValidString(in.IdempotencyKey) || len([]byte(in.IdempotencyKey)) < 1 || len([]byte(in.IdempotencyKey)) > 128 {
		return nil, coded("agent_message_resolution_idempotency_required", "Idempotency-Key must be 1 to 128 UTF-8 bytes")
	}
	if in.Authority == nil {
		return nil, coded("agent_message_unauthorized", "current human authority is required")
	}
	instance := instanceName()
	if instance == "" {
		return nil, coded("agent_message_instance_invalid", "PAIMOS_AGENT_BUS_INSTANCE must be 1 to 64 UTF-8 bytes")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actorUserID, actorSessionID, err := in.Authority(ctx, tx)
	if err != nil {
		return nil, err
	}
	if actorUserID <= 0 {
		return nil, coded("agent_message_unauthorized", "current human authority is required")
	}
	actorSessionID = strings.TrimSpace(actorSessionID)
	if actorSessionID == "" || !validActorSession(actorSessionID) {
		return nil, coded("agent_message_resolution_session_invalid", "current credential identity must be 1 to 64 UTF-8 bytes without control characters")
	}

	keyDigest := sha256.Sum256([]byte(instance + "\x00held-message-resolution\x00" + in.IdempotencyKey))
	requestDigest := sha256.Sum256([]byte(in.MessageID + "\x00" + in.Outcome))
	var priorDigest []byte
	var priorID string
	err = tx.QueryRowContext(ctx, `SELECT request_digest,resolution_id FROM agent_message_human_resolutions
		WHERE instance=? AND project_id=? AND actor_user_id=? AND idempotency_key_digest=?`,
		instance, in.ProjectID, actorUserID, keyDigest[:]).Scan(&priorDigest, &priorID)
	if err == nil {
		if !bytes.Equal(priorDigest, requestDigest[:]) {
			return nil, coded("agent_message_resolution_idempotency_conflict", "Idempotency-Key was already used for a different resolution")
		}
		_ = tx.Rollback()
		return s.getHumanResolution(ctx, in.ProjectID, priorID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	var messageRowID int64
	if err := tx.QueryRowContext(ctx, `SELECT message.id FROM agent_messages message
		JOIN project_agents receiver ON receiver.id=message.to_agent_id
		WHERE message.message_id=? AND receiver.project_id=? AND message.is_action_request=1
		 AND message.delivered=0`, in.MessageID, in.ProjectID).Scan(&messageRowID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, coded("agent_message_resolution_not_held_action", "message is not a held action request in this project")
		}
		return nil, err
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT resolution_id FROM agent_message_human_resolutions WHERE message_row_id=?`, messageRowID).Scan(&existing); err == nil {
		return nil, coded("agent_message_resolution_conflict", "held action request already has a human resolution")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	resolutionID := uuid.NewString()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_message_human_resolutions(
		resolution_id,message_row_id,project_id,outcome,actor_user_id,actor_session_id,instance,idempotency_key_digest,request_digest)
		VALUES(?,?,?,?,?,?,?,?,?)`, resolutionID, messageRowID, in.ProjectID, in.Outcome, actorUserID,
		actorSessionID, instance, keyDigest[:], requestDigest[:]); err != nil {
		insertErr := err
		_ = tx.Rollback()
		return s.classifyResolutionInsertFailure(ctx, in.ProjectID, messageRowID, actorUserID,
			instance, keyDigest[:], requestDigest[:], insertErr)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getHumanResolution(ctx, in.ProjectID, resolutionID)
}

// classifyResolutionInsertFailure re-reads durable state instead of depending
// on driver-specific UNIQUE error text. A racing exact retry receives the
// original result, conflicting durable facts keep their stable coded errors,
// and an unrelated storage failure remains an error.
func (s *Service) classifyResolutionInsertFailure(ctx context.Context, projectID, messageRowID, actorUserID int64,
	instance string, keyDigest, requestDigest []byte, insertErr error) (*HumanResolution, error) {
	var priorDigest []byte
	var priorID string
	err := s.db.QueryRowContext(ctx, `SELECT request_digest,resolution_id FROM agent_message_human_resolutions
		WHERE instance=? AND project_id=? AND actor_user_id=? AND idempotency_key_digest=?`,
		instance, projectID, actorUserID, keyDigest).Scan(&priorDigest, &priorID)
	if err == nil {
		if !bytes.Equal(priorDigest, requestDigest) {
			return nil, coded("agent_message_resolution_idempotency_conflict", "Idempotency-Key was already used for a different resolution")
		}
		return s.getHumanResolution(ctx, projectID, priorID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("verify human message resolution retry: %w", err)
	}

	var existing string
	err = s.db.QueryRowContext(ctx, `SELECT resolution_id FROM agent_message_human_resolutions WHERE message_row_id=?`, messageRowID).Scan(&existing)
	if err == nil {
		return nil, coded("agent_message_resolution_conflict", "held action request already has a human resolution")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("verify held message resolution retry: %w", err)
	}
	return nil, fmt.Errorf("record human message resolution: %w", insertErr)
}

func (s *Service) getHumanResolution(ctx context.Context, projectID int64, resolutionID string) (*HumanResolution, error) {
	var out HumanResolution
	if err := s.db.QueryRowContext(ctx, `SELECT resolution.resolution_id,message.message_id,resolution.outcome,
		resolution.actor_user_id,resolution.actor_session_id,resolution.created_at
		FROM agent_message_human_resolutions resolution
		JOIN agent_messages message ON message.id=resolution.message_row_id
		WHERE resolution.project_id=? AND resolution.resolution_id=?`, projectID, resolutionID).Scan(
		&out.ResolutionID, &out.MessageID, &out.Outcome, &out.ActorUserID, &out.ActorSessionID, &out.CreatedAt); err != nil {
		return nil, err
	}
	return &out, nil
}
