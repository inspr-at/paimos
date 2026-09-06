// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package agentmessage

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/secretvault"
)

type ConsumerClaim struct {
	ExpectedRevision int64  `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}
type ConsumerAttempt struct {
	ID         string `json:"id"`
	StreamID   string `json:"stream_id"`
	Revision   int64  `json:"stream_revision"`
	RequestKey string `json:"request_key"`
	ResourceID string `json:"resource_id"`
	Cursor     int64  `json:"cursor"`
	State      string `json:"state"`
	ExpiresAt  string `json:"expires_at"`
}
type ConsumerPage struct {
	SchemaVersion int               `json:"schema_version"`
	Attempt       *ConsumerAttempt  `json:"attempt"`
	Delivery      *ConsumerDelivery `json:"delivery,omitempty"`
	Attention     *AttentionPage    `json:"attention,omitempty"`
}
type ConsumerDelivery struct {
	Envelope *Envelope    `json:"envelope"`
	Work     DeliveryWork `json:"work"`
}
type ConsumerCompletion struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Outcome          string `json:"outcome"`
	EffectiveLevel   string `json:"effective_level"`
	FallbackReason   string `json:"fallback_reason"`
}
type ConsumerResult struct {
	SchemaVersion int    `json:"schema_version"`
	State         string `json:"state"`
	Cursor        int64  `json:"cursor"`
}
type consumerAttempt struct {
	ConsumerAttempt
	owner         consumerOwner
	nonce, digest []byte
	result        string
}

func loadAttempt(ctx context.Context, tx *sql.Tx, id string) (consumerAttempt, error) {
	var out consumerAttempt
	var raw string
	e := tx.QueryRowContext(ctx, `SELECT id,stream_id,stream_revision,request_key,resource_id,cursor,state,expires_at,owner_json,nonce_digest,consumer_digest,result_json FROM agent_consumer_attempts WHERE id=?`, id).Scan(&out.ID, &out.StreamID, &out.Revision, &out.RequestKey, &out.ResourceID, &out.Cursor, &out.State, &out.ExpiresAt, &raw, &out.nonce, &out.digest, &out.result)
	if e != nil {
		return out, ErrConsumerUnavailable
	}
	if json.Unmarshal([]byte(raw), &out.owner) != nil {
		return out, ErrConsumerStorage
	}
	return out, nil
}
func consumerAttemptProof(c ConsumerCredentials, a consumerAttempt) bool {
	return a.owner.UserID == c.Principal.UserID() && a.owner.KeyID == c.Principal.APIKeyID() && consumerProof(c.AttemptNonce) && consumerProof(c.ConsumerLease) && subtle.ConstantTimeCompare(a.nonce, consumerDigest(c.AttemptNonce)) == 1 && subtle.ConstantTimeCompare(a.digest, consumerDigest(c.ConsumerLease)) == 1
}
func (s *Service) ClaimConsumer(ctx context.Context, c ConsumerCredentials, project int64, id string, in ConsumerClaim) (ConsumerPage, error) {
	if !consumerID(in.RequestKey) || in.ExpectedRevision < 1 || !consumerProof(c.AttemptNonce) {
		return ConsumerPage{}, ErrConsumerInvalid
	}
	// Reuse canonical attention projection. It performs its own transactional
	// current authority check; the claim below rechecks again before leasing.
	var kind string
	if s.db.QueryRowContext(ctx, `SELECT kind FROM agent_consumer_streams WHERE id=? AND project_id=?`, id, project).Scan(&kind) == nil && kind == "attention" {
		_, e := s.projectAttention(ctx, func(ctx context.Context, tx *sql.Tx) (bool, error) {
			_, e := authorizeConsumer(ctx, tx, c, project, id, in.ExpectedRevision)
			return e == nil, e
		})
		if e != nil {
			return ConsumerPage{}, ErrConsumerUnavailable
		}
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	defer tx.Rollback()
	stream, e := authorizeConsumer(ctx, tx, c, project, id, in.ExpectedRevision)
	if e != nil {
		return ConsumerPage{}, e
	}
	var attemptID string
	e = tx.QueryRowContext(ctx, `SELECT id FROM agent_consumer_attempts WHERE stream_id=? AND stream_revision=? AND request_key=?`, id, stream.Revision, in.RequestKey).Scan(&attemptID)
	if e == nil {
		a, err := loadAttempt(ctx, tx, attemptID)
		if err != nil {
			return ConsumerPage{}, err
		}
		if !consumerAttemptProof(c, a) {
			return ConsumerPage{}, ErrConsumerUnavailable
		}
		if err = expireConsumerAttempt(ctx, tx, &a); err != nil {
			return ConsumerPage{}, err
		}
		if tx.Commit() != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		return ConsumerPage{SchemaVersion: 1, Attempt: &a.ConsumerAttempt}, nil
	}
	if e != sql.ErrNoRows {
		return ConsumerPage{}, ErrConsumerStorage
	}
	e = tx.QueryRowContext(ctx, `SELECT id FROM agent_consumer_attempts WHERE stream_id=? AND state IN ('claimed','executing','outcome_unknown')`, id).Scan(&attemptID)
	if e == nil {
		a, err := loadAttempt(ctx, tx, attemptID)
		if err != nil {
			return ConsumerPage{}, err
		}
		if err = expireConsumerAttempt(ctx, tx, &a); err != nil {
			return ConsumerPage{}, err
		}
		if a.State != "released" {
			if tx.Commit() != nil {
				return ConsumerPage{}, ErrConsumerStorage
			}
			if a.State == "outcome_unknown" {
				return ConsumerPage{}, ErrConsumerUnknown
			}
			return ConsumerPage{}, ErrConsumerConflict
		}
	} else if e != sql.ErrNoRows {
		return ConsumerPage{}, ErrConsumerStorage
	}
	var used int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_consumer_attempts WHERE stream_id=? AND nonce_digest=?`, id, consumerDigest(c.AttemptNonce)).Scan(&used) != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	if used > 0 {
		return ConsumerPage{}, ErrConsumerConflict
	}
	var count int
	if tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_consumer_attempts WHERE stream_id=?`, id).Scan(&count) != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	if count >= 10000 {
		return ConsumerPage{}, ErrConsumerConflict
	}
	resource, cursor, e := reserveConsumerWork(ctx, tx, stream)
	if e != nil {
		return ConsumerPage{}, e
	}
	if resource == "" {
		if tx.Commit() != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		return ConsumerPage{SchemaVersion: 1}, nil
	}
	a := ConsumerAttempt{ID: uuid.NewString(), StreamID: id, Revision: stream.Revision, RequestKey: in.RequestKey, ResourceID: resource, Cursor: cursor, State: "claimed", ExpiresAt: consumerStamp(time.Now().Add(60 * time.Second))}
	_, e = tx.ExecContext(ctx, `INSERT INTO agent_consumer_attempts(id,stream_id,stream_revision,request_key,resource_id,cursor,state,expires_at,owner_json,nonce_digest,consumer_digest) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a.ID, id, a.Revision, a.RequestKey, a.ResourceID, a.Cursor, a.State, a.ExpiresAt, consumerJSON(stream.consumerOwner), consumerDigest(c.AttemptNonce), stream.digest)
	if e != nil || tx.Commit() != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	return ConsumerPage{SchemaVersion: 1, Attempt: &a}, nil
}
func expireConsumerAttempt(ctx context.Context, tx *sql.Tx, a *consumerAttempt) error {
	return expireConsumerAttemptAt(ctx, tx, a, time.Now())
}
func expireConsumerAttemptAt(ctx context.Context, tx *sql.Tx, a *consumerAttempt, now time.Time) error {
	deadline, err := time.Parse(time.RFC3339Nano, a.ExpiresAt)
	if (err == nil && deadline.After(now)) || (a.State != "claimed" && a.State != "executing") {
		return nil
	}
	state := "outcome_unknown"
	if a.State == "claimed" {
		state = "released"
	}
	if _, e := tx.ExecContext(ctx, `UPDATE agent_consumer_attempts SET state=? WHERE id=?`, state, a.ID); e != nil {
		return ErrConsumerStorage
	}
	if state == "released" {
		var e error
		if a.owner.Registration.Kind == "fallback" {
			_, e = tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='pending',lease_until=NULL,consumer_fence=consumer_fence+1 WHERE delivery_id=? AND state='leased'`, a.ResourceID)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='pending',lease_until=NULL,consumer_fence=consumer_fence+1 WHERE batch_id=? AND state='leased'`, a.ResourceID)
		}
		if e != nil {
			return ErrConsumerStorage
		}
	}
	a.State = state
	return nil
}
func reserveConsumerWork(ctx context.Context, tx *sql.Tx, s ownedStream) (string, int64, error) {
	if s.Kind == "attention" {
		return reserveConsumerAttention(ctx, tx, s)
	}
	var id, state, target, reason string
	var cursor int64
	e := tx.QueryRowContext(ctx, `SELECT d.delivery_id,d.state,`+selectedDeliveryTargetSQL+`,m.id,d.fallback_reason FROM agent_message_deliveries d JOIN agent_messages m ON m.id=d.message_row_id WHERE d.instance=? AND m.to_agent_id=? AND m.to_address=? AND m.delivered=1 AND m.is_action_request=0 AND d.state NOT IN ('handed_off','dead') ORDER BY m.id LIMIT 1`, instanceName(), s.AgentID, s.Address).Scan(&id, &state, &target, &cursor, &reason)
	if e == sql.ErrNoRows {
		return "", 0, nil
	}
	if e != nil {
		return "", 0, ErrConsumerStorage
	}
	if target != s.Registration.TargetID {
		return "", 0, nil
	}
	if state == "leased" {
		return "", 0, ErrConsumerHandoff
	}
	if state != "pending" && state != "retry" {
		return "", 0, nil
	}
	r, e := tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='leased',attempt_count=attempt_count+1,lease_until=?,consumer_fence=consumer_fence+1 WHERE delivery_id=? AND state IN ('pending','retry') AND next_attempt_at<=?`, consumerStamp(time.Now().Add(60*time.Second)), id, consumerStamp(time.Now()))
	if e != nil {
		return "", 0, ErrConsumerStorage
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return "", 0, nil
	}
	return id, cursor, nil
}
func reserveConsumerAttention(ctx context.Context, tx *sql.Tx, s ownedStream) (string, int64, error) {
	var id, state, target string
	var from, to int64
	e := tx.QueryRowContext(ctx, `SELECT batch_id,state,COALESCE(target_id,''),from_cursor,to_cursor FROM agent_attention_batches WHERE receiver_project_agent_id=? AND state IN ('pending','leased','blocked')`, s.AgentID).Scan(&id, &state, &target, &from, &to)
	if e == sql.ErrNoRows {
		if e = tx.QueryRowContext(ctx, `SELECT cursor FROM agent_attention_cursors WHERE receiver_project_agent_id=?`, s.AgentID).Scan(&from); e != nil && e != sql.ErrNoRows {
			return "", 0, ErrConsumerStorage
		}
		var first string
		e = tx.QueryRowContext(ctx, `SELECT ai.created_at FROM agent_attention_items ai WHERE ai.receiver_project_agent_id=? AND ai.id>? AND `+activeAttentionItemPredicate+` ORDER BY ai.id LIMIT 1`, s.AgentID, from).Scan(&first)
		if e == sql.ErrNoRows {
			return "", 0, nil
		}
		if e != nil {
			return "", 0, ErrConsumerStorage
		}
		at, e := time.Parse(time.RFC3339Nano, first)
		if e != nil {
			return "", 0, ErrConsumerStorage
		}
		rows, e := tx.QueryContext(ctx, `SELECT ai.id FROM agent_attention_items ai WHERE ai.receiver_project_agent_id=? AND ai.id>? AND ai.created_at<=? AND `+activeAttentionItemPredicate+` ORDER BY ai.id LIMIT 32`, s.AgentID, from, consumerStamp(at.Add(attentionCoalesceWindow)))
		if e != nil {
			return "", 0, ErrConsumerStorage
		}
		count := 0
		for rows.Next() {
			if rows.Scan(&to) != nil {
				rows.Close()
				return "", 0, ErrConsumerStorage
			}
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return "", 0, ErrConsumerStorage
		}
		if count == 0 {
			return "", 0, nil
		}
		id = uuid.NewString()
		state = "pending"
		target = s.Registration.TargetID
		_, e = tx.ExecContext(ctx, `INSERT INTO agent_attention_batches(batch_id,receiver_project_id,receiver_project_agent_id,address,from_cursor,to_cursor,item_count,state,target_id) VALUES(?,?,?,?,?,?,?,'pending',?)`, id, s.ProjectID, s.AgentID, s.Address, from, to, count, target)
		if e != nil {
			return "", 0, ErrConsumerStorage
		}
	} else if e != nil {
		return "", 0, ErrConsumerStorage
	}
	if state == "leased" {
		return "", 0, ErrConsumerHandoff
	}
	if target != s.Registration.TargetID || state == "blocked" {
		return "", 0, ErrConsumerHandoff
	}
	targetRow, e := GetTargetTx(ctx, tx, s.ProjectID, target)
	if e != nil {
		return "", 0, ErrConsumerUnavailable
	}
	_, e = tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='leased',worker_adapter=?,lease_until=?,consumer_fence=consumer_fence+1 WHERE batch_id=? AND state='pending'`, targetRow.Adapter, consumerStamp(time.Now().Add(60*time.Second)), id)
	if e != nil {
		return "", 0, ErrConsumerStorage
	}
	return id, to, nil
}
func consumerSelected(ctx context.Context, tx *sql.Tx, a consumerAttempt) error {
	var selected, state string
	var cursor int64
	var e error
	if a.owner.Registration.Kind == "fallback" {
		e = tx.QueryRowContext(ctx, `SELECT `+selectedDeliveryTargetSQL+`,d.state,m.id FROM agent_message_deliveries d JOIN agent_messages m ON m.id=d.message_row_id WHERE d.delivery_id=? AND m.to_agent_id=? AND m.to_address=? AND d.instance=?`, a.ResourceID, a.owner.AgentID, a.owner.Address, instanceName()).Scan(&selected, &state, &cursor)
	} else {
		e = tx.QueryRowContext(ctx, `SELECT target_id,state,to_cursor FROM agent_attention_batches WHERE batch_id=? AND receiver_project_agent_id=? AND address=?`, a.ResourceID, a.owner.AgentID, a.owner.Address).Scan(&selected, &state, &cursor)
	}
	if e != nil || selected != a.owner.Registration.TargetID || state != "leased" || cursor != a.Cursor {
		return ErrConsumerUnavailable
	}
	return nil
}
func (s *Service) ExecuteConsumer(ctx context.Context, c ConsumerCredentials, project int64, id, attemptID string, revision int64) (ConsumerPage, error) {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	defer tx.Rollback()
	_, e = authorizeConsumer(ctx, tx, c, project, id, revision)
	if e != nil {
		return ConsumerPage{}, e
	}
	a, e := loadAttempt(ctx, tx, attemptID)
	if e != nil || a.StreamID != id || a.Revision != revision || !consumerAttemptProof(c, a) {
		return ConsumerPage{}, ErrConsumerUnavailable
	}
	if e = expireConsumerAttempt(ctx, tx, &a); e != nil {
		return ConsumerPage{}, e
	}
	page := ConsumerPage{SchemaVersion: 1, Attempt: &a.ConsumerAttempt}
	if a.State != "claimed" {
		if tx.Commit() != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		return page, nil
	}
	if e = consumerSelected(ctx, tx, a); e != nil {
		return ConsumerPage{}, e
	}
	target, e := GetTargetTx(ctx, tx, project, a.owner.Registration.TargetID)
	if e != nil {
		return ConsumerPage{}, ErrConsumerUnavailable
	}
	var cipher []byte
	if tx.QueryRowContext(ctx, `SELECT target_ref_cipher FROM agent_message_targets WHERE id=?`, target.ID).Scan(&cipher) != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	plain, e := secretvault.Decrypt(targetSecretDomain, cipher)
	if e != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	if a.owner.Registration.Kind == "fallback" {
		envelope, e := scanEnvelope(tx.QueryRowContext(ctx, envelopeSelect+` WHERE am.id=? AND am.to_agent_id=? AND am.to_address=? AND am.delivered=1 AND am.is_action_request=0`, a.Cursor, a.owner.AgentID, a.owner.Address))
		if e != nil {
			return ConsumerPage{}, ErrConsumerUnavailable
		}
		var requested, reason string
		if tx.QueryRowContext(ctx, `SELECT requested_level,fallback_reason FROM agent_message_deliveries WHERE delivery_id=?`, a.ResourceID).Scan(&requested, &reason) != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		page.Delivery = &ConsumerDelivery{Envelope: envelope, Work: DeliveryWork{DeliveryID: a.ResourceID, Instance: instanceName(), ProjectID: project, State: "leased", Adapter: target.Adapter, TargetKind: target.TargetKind, TargetRef: string(plain), MaximumLevel: target.MaximumLevel, RequestedLevel: requested, FallbackReason: reason}}
	} else {
		var from int64
		if tx.QueryRowContext(ctx, `SELECT from_cursor FROM agent_attention_batches WHERE batch_id=?`, a.ResourceID).Scan(&from) != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		rows, e := tx.QueryContext(ctx, `SELECT ai.id,printf('attention-%d',ai.id),ai.source_project_id,ai.source_kind,ai.source_id,ai.source_sequence,ai.attention_kind,ai.reason_code,ai.occurred_at FROM agent_attention_items ai WHERE ai.receiver_project_agent_id=? AND ai.id>? AND ai.id<=? AND `+activeAttentionItemPredicate+` ORDER BY ai.id LIMIT 32`, a.owner.AgentID, from, a.Cursor)
		if e != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		items, e := scanAttentionItems(rows)
		rows.Close()
		if e != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		if len(items) == 0 {
			// No execution payload was issued. Preserve the immutable batch and
			// attempt as safely closed without pretending a vendor handoff.
			if _, e = tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='superseded',lease_until=NULL,consumer_fence=consumer_fence+1 WHERE batch_id=?`, a.ResourceID); e != nil {
				return ConsumerPage{}, ErrConsumerStorage
			}
			if _, e = tx.ExecContext(ctx, `UPDATE agent_consumer_attempts SET state='released' WHERE id=?`, a.ID); e != nil || tx.Commit() != nil {
				return ConsumerPage{}, ErrConsumerStorage
			}
			page.Attempt.State = "released"
			return page, nil
		}
		frame, e := attentionFrame(project, a.ResourceID, items)
		if e != nil {
			return ConsumerPage{}, ErrConsumerStorage
		}
		page.Attention = &AttentionPage{Address: a.owner.Address, Cursor: from, NextCursor: a.Cursor, Items: items, Frame: frame, Work: &AttentionDeliveryWork{BatchID: a.ResourceID, Instance: instanceName(), ProjectID: project, State: "leased", Adapter: target.Adapter, TargetKind: target.TargetKind, TargetRef: string(plain), MaximumLevel: target.MaximumLevel}}
	}
	if _, e = tx.ExecContext(ctx, `UPDATE agent_consumer_attempts SET state='executing' WHERE id=? AND state='claimed'`, a.ID); e != nil || tx.Commit() != nil {
		return ConsumerPage{}, ErrConsumerStorage
	}
	page.Attempt.State = "executing"
	return page, nil
}

func (s *Service) CompleteConsumer(ctx context.Context, c ConsumerCredentials, project int64, id, attemptID string, in ConsumerCompletion) (ConsumerResult, error) {
	if in.ExpectedRevision < 1 || (in.Outcome != "applied" && in.Outcome != "outcome_unknown") || in.EffectiveLevel != "simple" {
		return ConsumerResult{}, ErrConsumerInvalid
	}
	switch in.FallbackReason {
	case "", "idle", "unsupported", "policy_capped", "target_missing", "not_steerable", "transport_error":
	default:
		return ConsumerResult{}, ErrConsumerInvalid
	}
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return ConsumerResult{}, ErrConsumerStorage
	}
	defer tx.Rollback()
	if e = consumerPrincipal(ctx, tx, c.Principal, project); e != nil {
		return ConsumerResult{}, e
	}
	a, e := loadAttempt(ctx, tx, attemptID)
	if e != nil || a.StreamID != id || a.owner.ProjectID != project || a.Revision != in.ExpectedRevision || !consumerAttemptProof(c, a) {
		return ConsumerResult{}, ErrConsumerUnavailable
	}
	if _, _, e = consumerRuntime(ctx, tx, c, project, a.owner.Registration.RuntimeID, a.owner.Registration.RuntimeGeneration, false); e != nil {
		return ConsumerResult{}, e
	}
	result := ConsumerResult{SchemaVersion: 1, State: "completed", Cursor: a.Cursor}
	if a.State == "completed" || a.State == "outcome_unknown" {
		if a.State == "outcome_unknown" && a.result == "" {
			return ConsumerResult{}, ErrConsumerUnknown
		}
		if a.result != consumerJSON(in) {
			return ConsumerResult{}, ErrConsumerConflict
		}
		result.State = a.State
		if a.State == "outcome_unknown" {
			result.Cursor = 0
		}
		if tx.Commit() != nil {
			return ConsumerResult{}, ErrConsumerStorage
		}
		return result, nil
	}
	if _, e = authorizeConsumer(ctx, tx, c, project, id, in.ExpectedRevision); e != nil {
		return ConsumerResult{}, e
	}
	if e = expireConsumerAttempt(ctx, tx, &a); e != nil {
		return ConsumerResult{}, e
	}
	if a.State == "outcome_unknown" {
		if tx.Commit() != nil {
			return ConsumerResult{}, ErrConsumerStorage
		}
		return ConsumerResult{}, ErrConsumerUnknown
	}
	if a.State != "executing" {
		return ConsumerResult{}, ErrConsumerConflict
	}
	if e = consumerSelected(ctx, tx, a); e != nil {
		return ConsumerResult{}, e
	}
	if a.owner.Registration.Kind == "attention" && in.FallbackReason != "" {
		return ConsumerResult{}, ErrConsumerInvalid
	}
	if in.Outcome == "outcome_unknown" {
		if _, e = tx.ExecContext(ctx, `UPDATE agent_consumer_attempts SET state='outcome_unknown',result_json=? WHERE id=?`, consumerJSON(in), a.ID); e != nil || tx.Commit() != nil {
			return ConsumerResult{}, ErrConsumerStorage
		}
		return ConsumerResult{SchemaVersion: 1, State: "outcome_unknown"}, nil
	}
	if a.owner.Registration.Kind == "fallback" {
		_, e = tx.ExecContext(ctx, `UPDATE agent_message_deliveries SET state='handed_off',effective_level='simple',fallback_reason=?,handed_off_at=?,lease_until=NULL,last_error_code='',consumer_fence=consumer_fence+1 WHERE delivery_id=? AND state='leased'`, in.FallbackReason, consumerStamp(time.Now()), a.ResourceID)
		if e == nil {
			_, e = ackInboxConsumerTx(ctx, tx, project, a.owner.Address, a.owner.AgentID, a.Cursor, true)
		}
	} else {
		_, e = tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='handed_off',handed_off_at=?,lease_until=NULL,consumer_fence=consumer_fence+1 WHERE batch_id=? AND state='leased'`, consumerStamp(time.Now()), a.ResourceID)
		if e == nil {
			_, e = tx.ExecContext(ctx, `INSERT INTO agent_attention_cursors(receiver_project_id,receiver_project_agent_id,address,cursor,updated_at,consumer_fence) VALUES(?,?,?,?,?,1) ON CONFLICT(receiver_project_agent_id) DO UPDATE SET cursor=excluded.cursor,updated_at=excluded.updated_at,consumer_fence=agent_attention_cursors.consumer_fence+1 WHERE excluded.cursor>agent_attention_cursors.cursor`, project, a.owner.AgentID, a.owner.Address, a.Cursor, consumerStamp(time.Now()))
		}
	}
	if e != nil {
		return ConsumerResult{}, ErrConsumerStorage
	}
	if _, e = tx.ExecContext(ctx, `UPDATE agent_consumer_attempts SET state='completed',result_json=? WHERE id=?`, consumerJSON(in), a.ID); e != nil || tx.Commit() != nil {
		return ConsumerResult{}, ErrConsumerStorage
	}
	return result, nil
}
