// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/secretvault"
)

const (
	MaxAttentionItems       = 32
	attentionCoalesceWindow = 2 * time.Second
	attentionCodexLease     = 2 * time.Minute
	attentionClaudeLease    = 15 * time.Minute

	AttentionDispositionActionable = "actionable"
	AttentionDispositionAbsorbed   = "absorbed"
	AttentionDispositionDeferred   = "deferred"
)

// AttentionTransition is the closed, content-free input to the attention
// policy. Unknown combinations are deferred and therefore cannot wake a
// receiver merely because a producer added an enum elsewhere.
type AttentionTransition struct {
	ActivityState  string
	ActivityReason string
	ActivityKind   string
	Assigned       bool
}

type AttentionDecision struct {
	Disposition string
	Kind        string
	Reason      string
}

// ClassifyAttentionTransition is deliberately exhaustive over PAI-901's
// native activity contract. Busy activity is routine. An idle turn end is
// actionable only while an existing product session remains assigned.
func ClassifyAttentionTransition(in AttentionTransition) AttentionDecision {
	switch in.ActivityState {
	case "busy":
		if in.ActivityReason == "adapter_activity" && (in.ActivityKind == "turn_started" || in.ActivityKind == "tool_started" || in.ActivityKind == "control_applied") {
			return AttentionDecision{Disposition: AttentionDispositionAbsorbed}
		}
	case "idle":
		if in.ActivityReason == "turn_completed" && in.ActivityKind == "turn_completed" {
			if in.Assigned {
				return AttentionDecision{Disposition: AttentionDispositionActionable, Kind: "assignment_turn_ended", Reason: "turn_completed_open_assignment"}
			}
			return AttentionDecision{Disposition: AttentionDispositionAbsorbed}
		}
	case "unknown":
		switch in.ActivityReason {
		case "heartbeat_stale", "stale_evidence", "malformed_evidence":
			return AttentionDecision{Disposition: AttentionDispositionActionable, Kind: "worker_unknown", Reason: in.ActivityReason}
		case "unreported", "unmanaged_evidence":
			return AttentionDecision{Disposition: AttentionDispositionDeferred}
		}
	case "dead":
		switch in.ActivityReason {
		case "process_exited", "process_failed", "ownership_lost", "stopped":
			return AttentionDecision{Disposition: AttentionDispositionActionable, Kind: "worker_dead", Reason: in.ActivityReason}
		}
	}
	return AttentionDecision{Disposition: AttentionDispositionDeferred}
}

type AttentionItem struct {
	Cursor          int64  `json:"cursor"`
	AttentionID     string `json:"attention_id"`
	SourceProjectID int64  `json:"source_project_id"`
	SourceKind      string `json:"source_kind"`
	SourceID        string `json:"source_id"`
	SourceSequence  int64  `json:"source_sequence"`
	Kind            string `json:"kind"`
	Reason          string `json:"reason"`
	OccurredAt      string `json:"occurred_at"`
}

type AttentionDeliveryWork struct {
	BatchID       string `json:"batch_id"`
	Instance      string `json:"instance"`
	ProjectID     int64  `json:"project_id"`
	State         string `json:"state"`
	Adapter       string `json:"adapter,omitempty"`
	TargetKind    string `json:"target_kind,omitempty"`
	TargetRef     string `json:"target_ref,omitempty"`
	MaximumLevel  string `json:"maximum_level,omitempty"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}

type AttentionPage struct {
	Address    string                 `json:"address"`
	Cursor     int64                  `json:"cursor"`
	NextCursor int64                  `json:"next_cursor"`
	Items      []AttentionItem        `json:"items"`
	Frame      string                 `json:"frame,omitempty"`
	Work       *AttentionDeliveryWork `json:"delivery_work,omitempty"`
}

type AttentionInput struct {
	ProjectID     int64
	Address       string
	Agent         string
	WorkerAdapter string
	AfterID       int64
	Limit         int
}

type AttentionAckInput struct {
	ProjectID int64
	Address   string
	Agent     string
	Cursor    int64
	BatchID   string
}

type attentionReceiver struct {
	projectID int64
	agentID   int64
	name      string
	address   string
}

// isAttentionWakeAdapter is the closed list of local plugins that implement
// an actual simple handoff. Managed/agentd adapters are steer-only (or leased
// by another worker) even though they are local plugins, so selecting them for
// an attention wake would create a false capability claim.
func isAttentionWakeAdapter(adapter string) bool {
	return adapter == AdapterCodex || adapter == AdapterClaudeResume
}

func attentionLeaseDuration(adapter string) time.Duration {
	if adapter == AdapterClaudeResume {
		return attentionClaudeLease
	}
	return attentionCodexLease
}

type attentionCandidate struct {
	projectID                int64
	sourceKind, sourceID     string
	sourceSequence           int64
	kind, reason, occurredAt string
}

type attentionQueryer interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// ProjectAttention reads immutable event sources through durable watermarks,
// then holds the SQLite writer lock only while appending the derived index and
// advancing those watermarks. Mutable failure sources are selected through
// narrow indexes and remain idempotent through their authoritative identity.
func (s *Service) ProjectAttention(ctx context.Context) (int64, error) {
	receiver, err := resolveAttentionReceiver(ctx, s.db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	watermarks := map[string]int64{"harness_session_event": 0, "held_agent_message": 0}
	for kind := range watermarks {
		var watermark int64
		err := s.db.QueryRowContext(ctx, `SELECT source_row_id FROM agent_attention_projection_cursors
			WHERE receiver_project_agent_id=? AND source_kind=?`, receiver.agentID, kind).Scan(&watermark)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		watermarks[kind] = watermark
	}
	var harnessHighwater, messageHighwater int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM harness_session_events`).Scan(&harnessHighwater); err != nil {
		return 0, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM agent_messages`).Scan(&messageHighwater); err != nil {
		return 0, err
	}
	highwaters := map[string]int64{"harness_session_event": harnessHighwater, "held_agent_message": messageHighwater}

	var candidates []attentionCandidate
	rows, err := s.db.QueryContext(ctx, `SELECT hs.project_id,e.harness_session_id,e.activity_sequence,e.activity_state,
		e.activity_reason,e.activity_event_kind,strftime('%Y-%m-%dT%H:%M:%fZ',e.created_at),EXISTS(
		 SELECT 1 FROM product_sessions ps JOIN issues i ON i.id=ps.node_id
		 WHERE ps.target_project_agent_id=hs.project_agent_id AND i.deleted_at IS NULL
		 AND i.status NOT IN ('done','delivered','accepted','invoiced','cancelled')
		 AND strftime('%Y-%m-%dT%H:%M:%fZ',ps.created_at)<=strftime('%Y-%m-%dT%H:%M:%fZ',e.created_at)
		 AND strftime('%Y-%m-%dT%H:%M:%fZ',ps.updated_at)<=strftime('%Y-%m-%dT%H:%M:%fZ',e.created_at)
		 AND strftime('%Y-%m-%dT%H:%M:%fZ',i.updated_at)<=strftime('%Y-%m-%dT%H:%M:%fZ',e.created_at))
		FROM harness_session_events e JOIN harness_sessions hs ON hs.id=e.harness_session_id
		WHERE e.id>? AND e.id<=? AND hs.role='worker' AND hs.project_agent_id<>?
		ORDER BY e.id`, watermarks["harness_session_event"], highwaters["harness_session_event"], receiver.agentID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var projectID, sequence int64
		var sessionID, state, reason, kind, occurredAt string
		var assigned bool
		if err := rows.Scan(&projectID, &sessionID, &sequence, &state, &reason, &kind, &occurredAt, &assigned); err != nil {
			rows.Close()
			return 0, err
		}
		decision := ClassifyAttentionTransition(AttentionTransition{ActivityState: state, ActivityReason: reason, ActivityKind: kind, Assigned: assigned})
		if decision.Disposition == AttentionDispositionActionable {
			candidates = append(candidates, attentionCandidate{projectID, "harness_session_event", sessionID, sequence, decision.Kind, decision.Reason, occurredAt})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	// Every immutable message row advances the watermark, including messages
	// that are not action requests. That makes polling proportional to new rows.
	rows, err = s.db.QueryContext(ctx, `SELECT pa.project_id,am.id,am.message_id,am.is_action_request,am.delivered,
		strftime('%Y-%m-%dT%H:%M:%fZ',am.created_at) FROM agent_messages am
		JOIN project_agents pa ON pa.id=am.to_agent_id WHERE am.id>? AND am.id<=? ORDER BY am.id`,
		watermarks["held_agent_message"], highwaters["held_agent_message"])
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var projectID, rowID int64
		var messageID, occurredAt string
		var action, delivered bool
		if err := rows.Scan(&projectID, &rowID, &messageID, &action, &delivered, &occurredAt); err != nil {
			rows.Close()
			return 0, err
		}
		if action && !delivered {
			candidates = append(candidates, attentionCandidate{projectID, "held_agent_message", messageID, 0, "held_action", "action_request_held", occurredAt})
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	queries := []struct {
		sql        string
		sourceKind string
		kind       string
		reason     func(string) string
	}{
		{`SELECT pa.project_id,d.delivery_id,d.attempt_count,d.state,strftime('%Y-%m-%dT%H:%M:%fZ',d.updated_at)
		 FROM agent_message_deliveries d JOIN agent_messages am ON am.id=d.message_row_id
		 JOIN project_agents pa ON pa.id=am.to_agent_id WHERE d.instance=? AND d.state IN ('blocked','dead')`, "agent_message_delivery", "delivery_failed", func(state string) string {
			if state == "dead" {
				return "delivery_dead"
			}
			return "target_blocked"
		}},
		{`SELECT hs.project_id,c.id,c.sequence,c.state,strftime('%Y-%m-%dT%H:%M:%fZ',c.completed_at) FROM harness_session_controls c
		 JOIN harness_sessions hs ON hs.id=c.harness_session_id WHERE c.state='rejected' AND c.completed_at IS NOT NULL
		 AND hs.role='worker' AND hs.project_agent_id<>?`, "harness_control", "control_rejected", func(string) string { return "control_rejected" }},
	}
	for _, source := range queries {
		arg := any(instanceName())
		if source.sourceKind == "harness_control" {
			arg = receiver.agentID
		}
		rows, err := s.db.QueryContext(ctx, source.sql, arg)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var projectID, sequence int64
			var sourceID, state, occurredAt string
			if err := rows.Scan(&projectID, &sourceID, &sequence, &state, &occurredAt); err != nil {
				rows.Close()
				return 0, err
			}
			candidates = append(candidates, attentionCandidate{projectID, source.sourceKind, sourceID, sequence, source.kind, source.reason(state), occurredAt})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, err
		}
		if err := rows.Close(); err != nil {
			return 0, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	writeReceiver, err := resolveAttentionReceiver(ctx, tx)
	if err != nil {
		return 0, err
	}
	if writeReceiver.agentID != receiver.agentID || writeReceiver.address != receiver.address {
		return 0, coded("agent_attention_receiver_changed", "attention receiver changed during projection")
	}
	var inserted int64
	for _, candidate := range candidates {
		result, execErr := tx.ExecContext(ctx, `INSERT INTO agent_attention_items(
			receiver_project_id,receiver_project_agent_id,address,source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(receiver_project_agent_id,source_kind,source_id,source_sequence,attention_kind,reason_code) DO NOTHING`,
			receiver.projectID, receiver.agentID, receiver.address, candidate.projectID, candidate.sourceKind, candidate.sourceID,
			candidate.sourceSequence, candidate.kind, candidate.reason, candidate.occurredAt)
		if execErr != nil {
			return 0, execErr
		}
		changed, _ := result.RowsAffected()
		inserted += changed
	}
	for _, kind := range []string{"harness_session_event", "held_agent_message"} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_attention_projection_cursors(
			receiver_project_id,receiver_project_agent_id,source_kind,source_row_id,updated_at)
			VALUES(?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			ON CONFLICT(receiver_project_agent_id,source_kind) DO UPDATE SET
			source_row_id=MAX(source_row_id,excluded.source_row_id),updated_at=excluded.updated_at`,
			receiver.projectID, receiver.agentID, kind, highwaters[kind]); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return inserted, nil
}

func selectAttentionTarget(ctx context.Context, q attentionQueryer, projectID int64, address string) (targetID, state, blockedReason string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT id,adapter FROM agent_message_targets
		WHERE instance=? AND project_id=? AND address=? AND enabled=1
		ORDER BY CASE role WHEN 'primary' THEN 0 ELSE 1 END,version DESC`, instanceName(), projectID, address)
	if err != nil {
		return "", "", "", err
	}
	defer rows.Close()
	hasTarget := false
	for rows.Next() {
		var id, adapter string
		if err := rows.Scan(&id, &adapter); err != nil {
			return "", "", "", err
		}
		hasTarget = true
		if targetID == "" && isAttentionWakeAdapter(adapter) {
			targetID = id
		}
	}
	if err := rows.Err(); err != nil {
		return "", "", "", err
	}
	if targetID != "" {
		return targetID, "pending", "", nil
	}
	if hasTarget {
		return "", "blocked", "capability_missing", nil
	}
	return "", "blocked", "target_missing", nil
}

func resolveAttentionReceiver(ctx context.Context, q attentionQueryer) (*attentionReceiver, error) {
	instance := instanceName()
	var receiver attentionReceiver
	err := q.QueryRowContext(ctx, `SELECT pa.project_id,pa.id,pa.name
		FROM instance_orchestrator io JOIN project_agents pa ON pa.id=io.project_agent_id
		WHERE io.singleton_id=1 AND io.project_agent_id IS NOT NULL`).Scan(
		&receiver.projectID, &receiver.agentID, &receiver.name)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT address,adapter FROM agent_message_targets
		WHERE instance=? AND project_id=? AND enabled=1
		ORDER BY CASE role WHEN 'primary' THEN 0 ELSE 1 END,version DESC`, instance, receiver.projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var address, adapter string
		if err := rows.Scan(&address, &adapter); err != nil {
			return nil, err
		}
		_, name, err := parseAddress(address)
		if err == nil && name == receiver.name && isAttentionWakeAdapter(adapter) {
			receiver.address = address
			return &receiver, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, sql.ErrNoRows
}

func scanAttentionItems(rows *sql.Rows) ([]AttentionItem, error) {
	var items []AttentionItem
	for rows.Next() {
		var item AttentionItem
		if err := rows.Scan(&item.Cursor, &item.AttentionID, &item.SourceProjectID, &item.SourceKind, &item.SourceID,
			&item.SourceSequence, &item.Kind, &item.Reason, &item.OccurredAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func attentionFrame(projectID int64, batchID string, items []AttentionItem) (string, error) {
	payload, err := json.Marshal(struct {
		BatchID string          `json:"batch_id"`
		Items   []AttentionItem `json:"items"`
	}{BatchID: batchID, Items: items})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("<paimos-attention project_id=\"%d\" delivery_level=\"simple\">\n%s\n</paimos-attention>", projectID, payload), nil
}

// ListAttention returns the durable per-address feed. A delivery worker gets
// one recoverable coalesced batch; a read-only caller gets the same bounded
// items without mutating delivery state.
func (s *Service) ListAttention(ctx context.Context, in AttentionInput) (*AttentionPage, error) {
	if in.WorkerAdapter != "" && !isAttentionWakeAdapter(in.WorkerAdapter) {
		return nil, coded("agent_attention_worker_adapter_invalid", "delivery must name a registered local simple-handoff adapter")
	}
	// Authorize the attributed inbox before projecting any durable attention
	// rows. The same attribution is resolved again inside the delivery
	// transaction below so a target disabled between these steps fails closed.
	if _, _, err := s.resolveAttributedInbox(ctx, in.ProjectID, in.Address, in.Agent); err != nil {
		return nil, err
	}
	if _, err := s.ProjectAttention(ctx); err != nil {
		return nil, err
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
	var cursor int64
	err = tx.QueryRowContext(ctx, `SELECT cursor FROM agent_attention_cursors WHERE receiver_project_agent_id=?`, agentID).Scan(&cursor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	after := in.AfterID
	// Delivery workers may never advance from caller memory alone: only the
	// durable acknowledged cursor can move their FIFO boundary after a crash.
	if in.WorkerAdapter != "" || after < cursor {
		after = cursor
	}
	limit := in.Limit
	if limit <= 0 || limit > MaxAttentionItems {
		limit = MaxAttentionItems
	}
	var firstCreatedAt string
	err = tx.QueryRowContext(ctx, `SELECT created_at FROM agent_attention_items
		WHERE receiver_project_id=? AND receiver_project_agent_id=? AND id>? ORDER BY id LIMIT 1`,
		in.ProjectID, agentID, after).Scan(&firstCreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &AttentionPage{Address: address, Cursor: cursor, NextCursor: after}, nil
	}
	if err != nil {
		return nil, err
	}
	firstCreated, err := time.Parse("2006-01-02T15:04:05.000Z", firstCreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parse attention coalescing timestamp: %w", err)
	}
	windowEnd := firstCreated.Add(attentionCoalesceWindow).UTC().Format("2006-01-02T15:04:05.000Z")
	rows, err := tx.QueryContext(ctx, `SELECT id,printf('attention-%d',id),source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at
		FROM agent_attention_items WHERE receiver_project_id=? AND receiver_project_agent_id=? AND id>? AND created_at<=?
		ORDER BY id LIMIT ?`, in.ProjectID, agentID, after, windowEnd, limit)
	if err != nil {
		return nil, err
	}
	items, err := scanAttentionItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	next := after
	if len(items) > 0 {
		next = items[len(items)-1].Cursor
	}
	page := &AttentionPage{Address: address, Cursor: cursor, NextCursor: next, Items: items}
	if len(items) == 0 || in.WorkerAdapter == "" {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return page, nil
	}

	var batchID, state, blockedReason, selectedTargetID, batchAdapter, batchAddress, batchLeaseUntil string
	var fromCursor, toCursor int64
	err = tx.QueryRowContext(ctx, `SELECT batch_id,state,blocked_reason,COALESCE(target_id,''),worker_adapter,from_cursor,to_cursor,address,COALESCE(lease_until,'')
		FROM agent_attention_batches WHERE receiver_project_agent_id=? AND state IN ('pending','leased','blocked')`, agentID).Scan(
		&batchID, &state, &blockedReason, &selectedTargetID, &batchAdapter, &fromCursor, &toCursor, &batchAddress, &batchLeaseUntil)
	if errors.Is(err, sql.ErrNoRows) {
		id := uuid.Must(uuid.NewRandom())
		batchID, fromCursor, toCursor = id.String(), cursor, next
		selectedTargetID, state, blockedReason, err = selectAttentionTarget(ctx, tx, in.ProjectID, address)
		if err != nil {
			return nil, err
		}
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO agent_attention_batches(batch_id,receiver_project_id,receiver_project_agent_id,address,
			from_cursor,to_cursor,item_count,state,target_id,worker_adapter,blocked_reason)
			VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, batchID, in.ProjectID, agentID, address, fromCursor, toCursor, len(items), state,
			nullableString(selectedTargetID), "", blockedReason)
		if insertErr != nil {
			return nil, insertErr
		}
		created, _ := result.RowsAffected()
		if created == 0 {
			if err := tx.QueryRowContext(ctx, `SELECT batch_id,state,blocked_reason,COALESCE(target_id,''),worker_adapter,from_cursor,to_cursor,address,COALESCE(lease_until,'')
				FROM agent_attention_batches WHERE receiver_project_agent_id=? AND state IN ('pending','leased','blocked')`,
				agentID).Scan(&batchID, &state, &blockedReason, &selectedTargetID, &batchAdapter, &fromCursor, &toCursor, &batchAddress, &batchLeaseUntil); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	if batchAddress != "" && batchAddress != address {
		now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		if state == "leased" && batchLeaseUntil > now {
			page.Items, page.NextCursor = nil, cursor
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return page, nil
		}
		selectedTargetID, state, blockedReason, err = selectAttentionTarget(ctx, tx, in.ProjectID, address)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE agent_attention_batches SET address=?,state=?,target_id=?,worker_adapter='',
			blocked_reason=?,lease_until=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE batch_id=?`,
			address, state, nullableString(selectedTargetID), blockedReason, batchID); err != nil {
			return nil, err
		}
		batchAddress, batchAdapter = address, ""
	}

	// The open batch, not a later page, owns delivery order.
	rows, err = tx.QueryContext(ctx, `SELECT id,printf('attention-%d',id),source_project_id,source_kind,source_id,source_sequence,attention_kind,reason_code,occurred_at
		FROM agent_attention_items WHERE receiver_project_id=? AND receiver_project_agent_id=? AND id>? AND id<=? ORDER BY id`,
		in.ProjectID, agentID, fromCursor, toCursor)
	if err != nil {
		return nil, err
	}
	items, err = scanAttentionItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	page.Items, page.NextCursor = items, toCursor
	work := &AttentionDeliveryWork{BatchID: batchID, Instance: instanceName(), ProjectID: in.ProjectID, State: state, BlockedReason: blockedReason}
	if state != "blocked" {
		var cipher []byte
		if err := tx.QueryRowContext(ctx, `SELECT adapter,target_kind,maximum_level,target_ref_cipher FROM agent_message_targets
			WHERE id=? AND instance=? AND project_id=? AND address=?`, selectedTargetID, instanceName(), in.ProjectID, address).Scan(
			&work.Adapter, &work.TargetKind, &work.MaximumLevel, &cipher); err != nil {
			return nil, err
		}
		if work.Adapter != in.WorkerAdapter {
			work.State = "pending"
			page.Work = work
			page.Frame, err = attentionFrame(in.ProjectID, batchID, items)
			if err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return page, nil
		}
		leaseUntil := time.Now().UTC().Add(attentionLeaseDuration(in.WorkerAdapter)).Format("2006-01-02T15:04:05.000Z")
		result, err := tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='leased',worker_adapter=?,blocked_reason='',
			lease_until=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
			WHERE batch_id=? AND (state='pending' OR (state='leased' AND lease_until<=strftime('%Y-%m-%dT%H:%M:%fZ','now')))`, in.WorkerAdapter, leaseUntil, batchID)
		if err != nil {
			return nil, err
		}
		changed, _ := result.RowsAffected()
		if changed == 1 {
			plain, err := secretvault.Decrypt(targetSecretDomain, cipher)
			if err != nil {
				return nil, fmt.Errorf("decrypt attention target: %w", err)
			}
			work.State, work.TargetRef = "leased", string(plain)
		} else {
			// Another listener still owns the live lease. Do not disclose the
			// same batch as deliverable work or advance this process's cursor.
			page.Items, page.Frame, page.Work, page.NextCursor = nil, "", nil, cursor
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return page, nil
		}
	}
	page.Work = work
	page.Frame, err = attentionFrame(in.ProjectID, batchID, items)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return page, nil
}

// AckAttention advances a read-only attention cursor monotonically. If the
// acknowledgement names a delivery batch, the batch is completed atomically.
func (s *Service) AckAttention(ctx context.Context, in AttentionAckInput) (*CursorState, error) {
	if in.Cursor <= 0 {
		return nil, coded("agent_attention_cursor_invalid", "cursor must be greater than zero")
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
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_attention_items
		WHERE receiver_project_id=? AND receiver_project_agent_id=? AND id=?`, in.ProjectID, agentID, in.Cursor).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, coded("agent_attention_cursor_unknown", "cursor is not an attention item in this inbox")
	}
	if strings.TrimSpace(in.BatchID) != "" {
		var toCursor int64
		var state string
		if err := tx.QueryRowContext(ctx, `SELECT to_cursor,state FROM agent_attention_batches
			WHERE batch_id=? AND receiver_project_id=? AND receiver_project_agent_id=?`, strings.TrimSpace(in.BatchID), in.ProjectID, agentID).Scan(&toCursor, &state); err != nil {
			return nil, coded("agent_attention_batch_unknown", "attention batch does not belong to this inbox")
		}
		if toCursor > in.Cursor {
			return nil, coded("agent_attention_cursor_invalid", "cursor does not reach attention batch")
		}
		if state == "blocked" {
			return nil, coded("agent_attention_delivery_blocked", "attention delivery is blocked")
		}
		if state != "handed_off" {
			if state != "leased" {
				return nil, coded("agent_attention_delivery_not_leased", "attention delivery is not leased")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='handed_off',handed_off_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
				lease_until=NULL,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE batch_id=? AND state='leased'`, in.BatchID); err != nil {
				return nil, err
			}
		}
	}
	// A cursor acknowledgement is authoritative even when the caller does not
	// retain a batch ID. Complete every reached open batch atomically so a
	// manual or higher-cursor acknowledgement cannot leave a sticky lease.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_attention_batches SET state='handed_off',blocked_reason='',
		handed_off_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),lease_until=NULL,
		updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE receiver_project_id=? AND receiver_project_agent_id=? AND to_cursor<=?
		AND state IN ('pending','leased','blocked')`, in.ProjectID, agentID, in.Cursor); err != nil {
		return nil, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT cursor FROM agent_attention_cursors WHERE receiver_project_agent_id=?`, agentID).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if in.Cursor > current {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_attention_cursors(receiver_project_id,receiver_project_agent_id,address,cursor,updated_at)
			VALUES(?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			ON CONFLICT(receiver_project_agent_id) DO UPDATE SET address=excluded.address,cursor=excluded.cursor,updated_at=excluded.updated_at
			WHERE excluded.cursor>agent_attention_cursors.cursor`, in.ProjectID, agentID, address, in.Cursor); err != nil {
			return nil, err
		}
		current = in.Cursor
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &CursorState{Address: address, Cursor: current}, nil
}
