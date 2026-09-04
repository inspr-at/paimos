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
	ActivityState   string
	ActivityReason  string
	ActivityKind    string
	AssignmentKnown bool
	Assigned        bool
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
			if !in.AssignmentKnown {
				return AttentionDecision{Disposition: AttentionDispositionDeferred}
			}
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
	Authority     TransactionAuthority
}

type AttentionAckInput struct {
	ProjectID int64
	Address   string
	Agent     string
	Cursor    int64
	BatchID   string
	Authority TransactionAuthority
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

const attentionProjectionEpoch = "1970-01-01T00:00:00.000Z"

// activeAttentionItemPredicate keeps the append-only attention history
// separate from authoritative current state. A reply or human resolution
// ends its matching obligation immediately; acknowledgement is deliberately
// not part of that decision.
const activeAttentionItemPredicate = `(ai.source_kind NOT IN ('held_agent_message','reply_obligation')
	OR (ai.source_kind='held_agent_message' AND NOT EXISTS(
		SELECT 1 FROM agent_messages message
		JOIN agent_message_human_resolutions resolution ON resolution.message_row_id=message.id
		WHERE message.message_id=ai.source_id))
	OR (ai.source_kind='reply_obligation' AND EXISTS(
		SELECT 1 FROM agent_messages message
		JOIN agent_reply_obligations obligation ON obligation.message_row_id=message.id
		WHERE message.message_id=ai.source_id AND obligation.state='open')))`

var attentionProjectionSources = []string{
	"harness_session_event",
	"held_agent_message",
	"agent_message_delivery",
	"harness_control",
}

type attentionProjectionCursor struct {
	rowID     int64
	updatedAt string
	present   bool
}

// ProjectAttention reads immutable event sources through durable watermarks,
// then holds the SQLite writer lock only while appending the derived index and
// advancing those watermarks. Mutable failure sources are selected through
// narrow indexes and remain idempotent through their authoritative identity.
func (s *Service) ProjectAttention(ctx context.Context) (int64, error) {
	return s.projectAttention(ctx, nil)
}

func (s *Service) projectAttention(ctx context.Context, authority TransactionAuthority) (int64, error) {
	receiver, err := resolveAttentionReceiver(ctx, s.db)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	cursors := make(map[string]attentionProjectionCursor, len(attentionProjectionSources))
	for _, kind := range attentionProjectionSources {
		var cursor attentionProjectionCursor
		err := s.db.QueryRowContext(ctx, `SELECT source_row_id,source_updated_at FROM agent_attention_projection_cursors
			WHERE receiver_project_agent_id=? AND source_kind=?`, receiver.agentID, kind).Scan(&cursor.rowID, &cursor.updatedAt)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		cursor.present = err == nil
		if cursor.updatedAt == "" {
			cursor.updatedAt = attentionProjectionEpoch
		}
		cursors[kind] = cursor
	}
	highwaters := map[string]attentionProjectionCursor{
		"harness_session_event":  {updatedAt: attentionProjectionEpoch, present: true},
		"held_agent_message":     {updatedAt: attentionProjectionEpoch, present: true},
		"agent_message_delivery": {updatedAt: attentionProjectionEpoch, present: true},
		"harness_control":        {updatedAt: attentionProjectionEpoch, present: true},
	}
	high := highwaters["harness_session_event"]
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM harness_session_events`).Scan(&high.rowID); err != nil {
		return 0, err
	}
	highwaters["harness_session_event"] = high
	high = highwaters["held_agent_message"]
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM agent_messages`).Scan(&high.rowID); err != nil {
		return 0, err
	}
	highwaters["held_agent_message"] = high
	loadMutableHighwater := func(kind, query string, args ...any) error {
		var rowID sql.NullInt64
		var updatedAt sql.NullString
		if err := s.db.QueryRowContext(ctx, query, args...).Scan(&rowID, &updatedAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		highwater := highwaters[kind]
		if rowID.Valid {
			highwater.rowID = rowID.Int64
		}
		if updatedAt.Valid {
			highwater.updatedAt = updatedAt.String
		}
		highwaters[kind] = highwater
		return nil
	}
	if err := loadMutableHighwater("agent_message_delivery", `SELECT rowid,updated_at FROM agent_message_deliveries
		WHERE instance=? ORDER BY updated_at DESC,rowid DESC LIMIT 1`, instanceName()); err != nil {
		return 0, err
	}
	if err := loadMutableHighwater("harness_control", `SELECT c.rowid,c.completed_at FROM harness_session_controls c
		JOIN harness_sessions hs ON hs.id=c.harness_session_id WHERE c.completed_at IS NOT NULL
		AND hs.role='worker' AND hs.project_agent_id<>? ORDER BY c.completed_at DESC,c.rowid DESC LIMIT 1`, receiver.agentID); err != nil {
		return 0, err
	}

	// Enabling projection establishes a cutover boundary rather than replaying
	// the installation's entire operational history. The boundary is written
	// under current authority and the transaction-current orchestrator identity.
	missingCursor := false
	for _, kind := range attentionProjectionSources {
		missingCursor = missingCursor || !cursors[kind].present
	}
	if missingCursor {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		if err := authorizeAttentionTx(ctx, tx, authority); err != nil {
			return 0, err
		}
		writeReceiver, err := resolveAttentionReceiver(ctx, tx)
		if err != nil {
			return 0, err
		}
		if writeReceiver.agentID != receiver.agentID || writeReceiver.address != receiver.address {
			return 0, coded("agent_attention_receiver_changed", "attention receiver changed during projection")
		}
		for _, kind := range attentionProjectionSources {
			if cursors[kind].present {
				continue
			}
			highwater := highwaters[kind]
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_attention_projection_cursors(
				receiver_project_id,receiver_project_agent_id,source_kind,source_row_id,source_updated_at,updated_at)
				VALUES(?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now')) ON CONFLICT DO NOTHING`,
				receiver.projectID, receiver.agentID, kind, highwater.rowID, highwater.updatedAt); err != nil {
				return 0, err
			}
			cursors[kind] = highwater
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return 0, nil
	}

	var candidates []attentionCandidate
	rows, err := s.db.QueryContext(ctx, `SELECT hs.project_id,e.harness_session_id,e.activity_sequence,e.activity_state,
		e.activity_reason,e.activity_event_kind,strftime('%Y-%m-%dT%H:%M:%fZ',e.created_at),e.assignment_present
		FROM harness_session_events e JOIN harness_sessions hs ON hs.id=e.harness_session_id
		WHERE e.id>? AND e.id<=? AND hs.role='worker' AND hs.project_agent_id<>?
		ORDER BY e.id`, cursors["harness_session_event"].rowID, highwaters["harness_session_event"].rowID, receiver.agentID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var projectID, sequence int64
		var sessionID, state, reason, kind, occurredAt string
		var assigned sql.NullBool
		if err := rows.Scan(&projectID, &sessionID, &sequence, &state, &reason, &kind, &occurredAt, &assigned); err != nil {
			rows.Close()
			return 0, err
		}
		decision := ClassifyAttentionTransition(AttentionTransition{
			ActivityState: state, ActivityReason: reason, ActivityKind: kind,
			AssignmentKnown: assigned.Valid, Assigned: assigned.Valid && assigned.Bool,
		})
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
		JOIN project_agents pa ON pa.id=am.to_agent_id WHERE am.id>? AND am.id<=?
		AND NOT EXISTS(SELECT 1 FROM agent_message_human_resolutions resolution WHERE resolution.message_row_id=am.id)
		ORDER BY am.id`,
		cursors["held_agent_message"].rowID, highwaters["held_agent_message"].rowID)
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
		 JOIN project_agents pa ON pa.id=am.to_agent_id WHERE d.instance=? AND d.state IN ('blocked','dead')
		 AND (d.updated_at>? OR (d.updated_at=? AND d.rowid>?))
		 AND NOT EXISTS(SELECT 1 FROM agent_attention_items ai WHERE ai.receiver_project_agent_id=?
		  AND ai.source_kind='agent_message_delivery' AND ai.source_id=d.delivery_id
		  AND ai.source_sequence=d.attempt_count AND ai.reason_code=CASE d.state WHEN 'dead' THEN 'delivery_dead' ELSE 'target_blocked' END)`, "agent_message_delivery", "delivery_failed", func(state string) string {
			if state == "dead" {
				return "delivery_dead"
			}
			return "target_blocked"
		}},
		{`SELECT hs.project_id,c.id,c.sequence,c.state,strftime('%Y-%m-%dT%H:%M:%fZ',c.completed_at) FROM harness_session_controls c
		 JOIN harness_sessions hs ON hs.id=c.harness_session_id WHERE c.state='rejected' AND c.completed_at IS NOT NULL
		 AND hs.role='worker' AND hs.project_agent_id<>?
		 AND (c.completed_at>? OR (c.completed_at=? AND c.rowid>?))
		 AND NOT EXISTS(SELECT 1 FROM agent_attention_items ai WHERE ai.receiver_project_agent_id=?
		  AND ai.source_kind='harness_control' AND ai.source_id=c.id AND ai.source_sequence=c.sequence
		  AND ai.reason_code='control_rejected')`, "harness_control", "control_rejected", func(string) string { return "control_rejected" }},
	}
	for _, source := range queries {
		cursor := cursors[source.sourceKind]
		args := []any{instanceName(), cursor.updatedAt, cursor.updatedAt, cursor.rowID, receiver.agentID}
		if source.sourceKind == "harness_control" {
			args = []any{receiver.agentID, cursor.updatedAt, cursor.updatedAt, cursor.rowID, receiver.agentID}
		}
		rows, err := s.db.QueryContext(ctx, source.sql, args...)
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

	// Reply obligations are authoritative mutable state, not an event cursor.
	// Read only the bounded due frontier; the writer transaction below claims
	// the exact expected generation before appending an immutable attention item.
	now := time.Now().UTC()
	nowText := now.Format("2006-01-02T15:04:05.000Z")
	rows, err = s.db.QueryContext(ctx, `SELECT obligation.project_id,message.message_id,obligation.resurface_count
		FROM agent_reply_obligations obligation JOIN agent_messages message ON message.id=obligation.message_row_id
		WHERE obligation.state='open' AND obligation.next_attention_at<=?
		ORDER BY obligation.next_attention_at,obligation.message_row_id LIMIT ?`, nowText, MaxAttentionItems)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var projectID, currentCount int64
		var messageID string
		if err := rows.Scan(&projectID, &messageID, &currentCount); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, attentionCandidate{
			projectID, "reply_obligation", messageID, currentCount + 1, "reply_overdue", "reply_expected", nowText,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	watermarkChanged := false
	for _, kind := range attentionProjectionSources {
		watermarkChanged = watermarkChanged || cursors[kind].rowID != highwaters[kind].rowID || cursors[kind].updatedAt != highwaters[kind].updatedAt
	}
	if len(candidates) == 0 && !watermarkChanged {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if err := authorizeAttentionTx(ctx, tx, authority); err != nil {
		return 0, err
	}
	writeReceiver, err := resolveAttentionReceiver(ctx, tx)
	if err != nil {
		return 0, err
	}
	if writeReceiver.agentID != receiver.agentID || writeReceiver.address != receiver.address {
		return 0, coded("agent_attention_receiver_changed", "attention receiver changed during projection")
	}
	var inserted int64
	for _, candidate := range candidates {
		if candidate.sourceKind == "held_agent_message" {
			var resolved int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_messages message
				JOIN agent_message_human_resolutions resolution ON resolution.message_row_id=message.id
				WHERE message.message_id=?`, candidate.sourceID).Scan(&resolved); err != nil {
				return 0, err
			}
			if resolved != 0 {
				continue
			}
		}
		if candidate.sourceKind == "reply_obligation" {
			var nextAttention any
			if candidate.sourceSequence < replyObligationMaxResurfaces {
				nextAttention = now.Add(replyObligationDelay(candidate.sourceSequence)).Format("2006-01-02T15:04:05.000Z")
			}
			result, claimErr := tx.ExecContext(ctx, `UPDATE agent_reply_obligations SET resurface_count=?,next_attention_at=?
				WHERE message_row_id=(SELECT id FROM agent_messages WHERE message_id=?) AND project_id=? AND state='open'
				 AND resurface_count=? AND next_attention_at<=?`, candidate.sourceSequence, nextAttention,
				candidate.sourceID, candidate.projectID, candidate.sourceSequence-1, nowText)
			if claimErr != nil {
				return 0, claimErr
			}
			claimed, _ := result.RowsAffected()
			if claimed == 0 {
				continue
			}
			if _, eventErr := tx.ExecContext(ctx, `INSERT INTO agent_reply_obligation_events(
				message_row_id,event_sequence,event_kind,occurred_at)
				SELECT id,?,'resurfaced',? FROM agent_messages WHERE message_id=?`, candidate.sourceSequence, nowText, candidate.sourceID); eventErr != nil {
				return 0, eventErr
			}
		}
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
	for _, kind := range attentionProjectionSources {
		highwater := highwaters[kind]
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_attention_projection_cursors(
			receiver_project_id,receiver_project_agent_id,source_kind,source_row_id,source_updated_at,updated_at)
			VALUES(?,?,?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'))
			ON CONFLICT(receiver_project_agent_id,source_kind) DO UPDATE SET
			source_row_id=CASE WHEN excluded.source_updated_at>source_updated_at THEN excluded.source_row_id
			 WHEN excluded.source_updated_at=source_updated_at THEN MAX(source_row_id,excluded.source_row_id) ELSE source_row_id END,
			source_updated_at=MAX(source_updated_at,excluded.source_updated_at),updated_at=excluded.updated_at`,
			receiver.projectID, receiver.agentID, kind, highwater.rowID, highwater.updatedAt); err != nil {
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
		ORDER BY CASE WHEN adapter IN ('codex','claude_resume') THEN 0 ELSE 1 END,
		 CASE role WHEN 'primary' THEN 0 ELSE 1 END,version DESC`, instance, receiver.projectID)
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
		if err == nil && name == receiver.name {
			receiver.address = address
			return &receiver, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, sql.ErrNoRows
}

// IsOrchestratorAttentionAddress reports whether address names the configured
// instance orchestrator. It deliberately keys on the stable project-agent
// identity rather than on a currently enabled target version: rotating or
// disabling the target must not temporarily downgrade its administration to
// an ordinary project-admin operation.
func (s *Service) IsOrchestratorAttentionAddress(ctx context.Context, projectID int64, address string) (bool, error) {
	return IsOrchestratorAttentionAddressTx(ctx, s.db, projectID, address)
}

type orchestratorIdentityQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// IsOrchestratorAttentionAddressTx is the transaction-scoped form used by
// target and harness mutations so orchestrator reassignment cannot race an
// authorization decision made against an older snapshot.
func IsOrchestratorAttentionAddressTx(ctx context.Context, q orchestratorIdentityQueryer, projectID int64, address string) (bool, error) {
	_, agent, err := parseAddress(address)
	if err != nil {
		return false, err
	}
	var protected int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM instance_orchestrator io
		JOIN project_agents pa ON pa.id=io.project_agent_id
		WHERE io.singleton_id=1 AND pa.project_id=? AND pa.name=?)`, projectID, agent).Scan(&protected); err != nil {
		return false, err
	}
	return protected == 1, nil
}

func authorizeAttentionTx(ctx context.Context, tx *sql.Tx, authority TransactionAuthority) error {
	if authority == nil {
		return nil
	}
	superAdmin, err := authority(ctx, tx)
	if err != nil {
		return err
	}
	if !superAdmin {
		return coded("agent_attention_forbidden", "orchestrator attention requires current super-admin authority")
	}
	return nil
}

func resolveAuthorizedAttentionReceiverTx(ctx context.Context, tx *sql.Tx, projectID int64, address, agent string) (string, int64, error) {
	address, agentID, err := resolveAttributedInboxQuery(ctx, tx, projectID, address, agent)
	if err != nil {
		return "", 0, err
	}
	var receiverProjectID, receiverAgentID int64
	err = tx.QueryRowContext(ctx, `SELECT pa.project_id,pa.id
		FROM instance_orchestrator io JOIN project_agents pa ON pa.id=io.project_agent_id
		WHERE io.singleton_id=1 AND io.project_agent_id IS NOT NULL`).Scan(&receiverProjectID, &receiverAgentID)
	if err != nil {
		return "", 0, err
	}
	// The project-agent identity is authoritative. The inbox address and its
	// wake-capable target are deliberately separate: a missing, steer-only, or
	// rotated target must yield durable blocked/requeue work rather than make
	// the configured orchestrator disappear from the authorization check.
	if receiverProjectID != projectID || receiverAgentID != agentID {
		return "", 0, coded("agent_attention_receiver_changed", "attention receiver changed during the operation")
	}
	return address, agentID, nil
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
	// Validate the attributed inbox before scanning immutable sources. Current
	// credential authority and orchestrator identity are resolved in the
	// projection and delivery transactions below, so this early shape check is
	// never an authorization decision.
	if _, _, err := s.resolveAttributedInbox(ctx, in.ProjectID, in.Address, in.Agent); err != nil {
		return nil, err
	}
	if _, err := s.projectAttention(ctx, in.Authority); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := authorizeAttentionTx(ctx, tx, in.Authority); err != nil {
		return nil, err
	}
	address, agentID, err := resolveAuthorizedAttentionReceiverTx(ctx, tx, in.ProjectID, in.Address, in.Agent)
	if err != nil {
		return nil, err
	}
	// If every immutable item in an open batch has ceased to be actionable,
	// close that delivery generation without pretending it was handed off.
	// The cursor remains unchanged, so unrelated later work cannot be skipped.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_attention_batches
		SET state='superseded',blocked_reason='',lease_until=NULL,
			superseded_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
		WHERE receiver_project_id=? AND receiver_project_agent_id=? AND state IN ('pending','leased','blocked')
		AND NOT EXISTS(SELECT 1 FROM agent_attention_items ai
			WHERE ai.receiver_project_id=agent_attention_batches.receiver_project_id
			AND ai.receiver_project_agent_id=agent_attention_batches.receiver_project_agent_id
			AND ai.id>agent_attention_batches.from_cursor AND ai.id<=agent_attention_batches.to_cursor
			AND `+activeAttentionItemPredicate+`)`, in.ProjectID, agentID); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT ai.created_at FROM agent_attention_items ai
		WHERE ai.receiver_project_id=? AND ai.receiver_project_agent_id=? AND ai.id>?
		AND `+activeAttentionItemPredicate+` ORDER BY ai.id LIMIT 1`,
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
	rows, err := tx.QueryContext(ctx, `SELECT ai.id,printf('attention-%d',ai.id),ai.source_project_id,ai.source_kind,ai.source_id,ai.source_sequence,ai.attention_kind,ai.reason_code,ai.occurred_at
		FROM agent_attention_items ai WHERE ai.receiver_project_id=? AND ai.receiver_project_agent_id=? AND ai.id>? AND ai.created_at<=?
		AND `+activeAttentionItemPredicate+` ORDER BY ai.id LIMIT ?`, in.ProjectID, agentID, after, windowEnd, limit)
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

	var batchID, state, blockedReason, selectedTargetID, batchAdapter, batchAddress string
	var fromCursor, toCursor int64
	err = tx.QueryRowContext(ctx, `SELECT batch_id,state,blocked_reason,COALESCE(target_id,''),worker_adapter,from_cursor,to_cursor,address
		FROM agent_attention_batches WHERE receiver_project_agent_id=? AND state IN ('pending','leased','blocked')`, agentID).Scan(
		&batchID, &state, &blockedReason, &selectedTargetID, &batchAdapter, &fromCursor, &toCursor, &batchAddress)
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
			if err := tx.QueryRowContext(ctx, `SELECT batch_id,state,blocked_reason,COALESCE(target_id,''),worker_adapter,from_cursor,to_cursor,address
				FROM agent_attention_batches WHERE receiver_project_agent_id=? AND state IN ('pending','leased','blocked')`,
				agentID).Scan(&batchID, &state, &blockedReason, &selectedTargetID, &batchAdapter, &fromCursor, &toCursor, &batchAddress); err != nil {
				return nil, err
			}
		}
	} else if err != nil {
		return nil, err
	}
	if batchAddress != "" && batchAddress != address {
		return nil, coded("agent_attention_batch_requeue_required", "the open attention batch belongs to a previous address; an authorized operator must requeue it")
	}

	// The open batch, not a later page, owns delivery order.
	rows, err = tx.QueryContext(ctx, `SELECT ai.id,printf('attention-%d',ai.id),ai.source_project_id,ai.source_kind,ai.source_id,ai.source_sequence,ai.attention_kind,ai.reason_code,ai.occurred_at
		FROM agent_attention_items ai WHERE ai.receiver_project_id=? AND ai.receiver_project_agent_id=? AND ai.id>? AND ai.id<=?
		AND `+activeAttentionItemPredicate+` ORDER BY ai.id`,
		in.ProjectID, agentID, fromCursor, toCursor)
	if err != nil {
		return nil, err
	}
	items, err = scanAttentionItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		page.Items, page.Frame, page.Work, page.NextCursor = nil, "", nil, cursor
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return page, nil
	}
	page.Items, page.NextCursor = items, toCursor
	work := &AttentionDeliveryWork{BatchID: batchID, Instance: instanceName(), ProjectID: in.ProjectID, State: state, BlockedReason: blockedReason}
	if state != "blocked" {
		var cipher []byte
		if err := tx.QueryRowContext(ctx, `SELECT adapter,target_kind,maximum_level,target_ref_cipher FROM agent_message_targets
			WHERE id=? AND instance=? AND project_id=? AND address=? AND enabled=1`, selectedTargetID, instanceName(), in.ProjectID, address).Scan(
			&work.Adapter, &work.TargetKind, &work.MaximumLevel, &cipher); errors.Is(err, sql.ErrNoRows) {
			return nil, coded("agent_attention_batch_requeue_required", "the open attention batch target is no longer enabled; an authorized operator must requeue it")
		} else if err != nil {
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
	if err := authorizeAttentionTx(ctx, tx, in.Authority); err != nil {
		return nil, err
	}
	address, agentID, err := resolveAuthorizedAttentionReceiverTx(ctx, tx, in.ProjectID, in.Address, in.Agent)
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
		if state == "superseded" {
			// Authoritative state closed the work before handoff. A stale worker
			// acknowledgement is harmless and must not resurrect the batch.
		} else if state == "blocked" {
			return nil, coded("agent_attention_delivery_blocked", "attention delivery is blocked")
		} else if state != "handed_off" {
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
