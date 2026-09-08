// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmessage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/lifecyclefence"
)

const maxClosedTargetRecoveries = 8

// RecoveryAuthority reauthorizes the current human operator inside the same
// transaction that inspects or appends recovery evidence.
type RecoveryAuthority func(context.Context, *sql.Tx, int64) (actorUserID int64, err error)

type ClosedTargetRecoveryInput struct {
	ProjectID               int64             `json:"-"`
	DeliveryID              string            `json:"-"`
	ExpectedClosedSessionID string            `json:"expected_closed_session_id"`
	ExpectedTargetID        string            `json:"expected_target_id"`
	ExpectedTargetVersion   int               `json:"expected_target_version"`
	ExpectedConsumerFence   int64             `json:"expected_consumer_fence"`
	ReplacementSessionID    string            `json:"replacement_session_id"`
	Authority               RecoveryAuthority `json:"-"`
}

type ClosedTargetRecoveryTarget struct {
	TargetID          string `json:"target_id"`
	TargetVersion     int    `json:"target_version"`
	HarnessSessionID  string `json:"harness_session_id"`
	SessionGeneration string `json:"session_generation"`
}

// ClosedTargetRecoveryPlan contains only public, content-free binding facts.
// The exact Apply fields can be copied into the mutating request after review.
type ClosedTargetRecoveryPlan struct {
	DeliveryID        string                     `json:"delivery_id"`
	Address           string                     `json:"address"`
	State             string                     `json:"state"`
	AttemptCount      int                        `json:"attempt_count"`
	ConsumerFence     int64                      `json:"consumer_fence"`
	RecoverySequence  int                        `json:"recovery_sequence"`
	OriginalTarget    ClosedTargetRecoveryTarget `json:"original_target"`
	EffectiveTarget   ClosedTargetRecoveryTarget `json:"effective_target"`
	ReplacementTarget ClosedTargetRecoveryTarget `json:"replacement_target"`
	Recovered         bool                       `json:"recovered"`
}

type recoveryFacts struct {
	plan                                                                   ClosedTargetRecoveryPlan
	requestedLevel, fallbackReason                                         string
	originalMaximum, effectiveMaximum, replacementMaximum                  string
	effectiveRole, effectiveAdapter, replacementRole, replacementAdapter   string
	effectiveRuntimeID, replacementRuntimeID, replacementRuntimeGeneration string
	replacementRuntimeUserID, replacementRuntimeAPIKeyID                   int64
	messageDelivered, actionRequest                                        int
	heldReason                                                             string
	leaseUntil, handedOffAt, effectiveLevel                                sql.NullString
}

func normalizeClosedTargetRecovery(in ClosedTargetRecoveryInput) ClosedTargetRecoveryInput {
	in.DeliveryID = strings.TrimSpace(in.DeliveryID)
	in.ExpectedClosedSessionID = strings.TrimSpace(in.ExpectedClosedSessionID)
	in.ExpectedTargetID = strings.TrimSpace(in.ExpectedTargetID)
	in.ReplacementSessionID = strings.TrimSpace(in.ReplacementSessionID)
	return in
}

func validateClosedTargetRecoveryInput(in ClosedTargetRecoveryInput, apply bool) error {
	if in.ProjectID <= 0 || in.DeliveryID == "" || in.ExpectedClosedSessionID == "" || in.ReplacementSessionID == "" ||
		in.ExpectedClosedSessionID == in.ReplacementSessionID {
		return coded("agent_message_delivery_recovery_invalid", "closed-target recovery request is invalid")
	}
	if apply && (in.ExpectedTargetID == "" || in.ExpectedTargetVersion <= 0 || in.ExpectedConsumerFence != 0) {
		return coded("agent_message_delivery_recovery_invalid", "closed-target recovery compare-and-swap fields are invalid")
	}
	return nil
}

// InspectClosedTargetRecovery validates both generations and returns copyable
// compare-and-swap facts without changing the delivery ledger.
func (s *Service) InspectClosedTargetRecovery(ctx context.Context, raw ClosedTargetRecoveryInput) (*ClosedTargetRecoveryPlan, error) {
	in := normalizeClosedTargetRecovery(raw)
	if err := validateClosedTargetRecoveryInput(in, false); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := authorizeClosedTargetRecovery(ctx, tx, in); err != nil {
		return nil, err
	}
	facts, err := loadClosedTargetRecoveryFacts(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if err := validateClosedTargetRecoveryFacts(ctx, tx, in, &facts); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &facts.plan, nil
}

// RecoverClosedTarget appends one immutable effective binding. It never
// rewrites the canonical envelope or the delivery's original target columns.
func (s *Service) RecoverClosedTarget(ctx context.Context, raw ClosedTargetRecoveryInput) (*ClosedTargetRecoveryPlan, error) {
	in := normalizeClosedTargetRecovery(raw)
	if err := validateClosedTargetRecoveryInput(in, true); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	actorUserID, err := authorizeClosedTargetRecovery(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if prior, found, err := loadIdempotentClosedTargetRecovery(ctx, tx, in); err != nil {
		return nil, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return prior, nil
	}
	facts, err := loadClosedTargetRecoveryFacts(ctx, tx, in)
	if err != nil {
		return nil, err
	}
	if facts.plan.EffectiveTarget.TargetID != in.ExpectedTargetID || facts.plan.EffectiveTarget.TargetVersion != in.ExpectedTargetVersion ||
		facts.plan.ConsumerFence != in.ExpectedConsumerFence {
		return nil, coded("agent_message_delivery_recovery_conflict", "closed-target recovery binding changed before apply")
	}
	if err := validateClosedTargetRecoveryFacts(ctx, tx, in, &facts); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_message_delivery_recoveries(
		delivery_id,sequence,project_id,old_target_id,old_target_version,old_harness_session_id,
		old_runtime_id,old_session_generation,new_target_id,new_target_version,new_harness_session_id,
		new_runtime_id,new_runtime_generation,new_session_generation,actor_user_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, in.DeliveryID, facts.plan.RecoverySequence, in.ProjectID,
		facts.plan.EffectiveTarget.TargetID, facts.plan.EffectiveTarget.TargetVersion, facts.plan.EffectiveTarget.HarnessSessionID,
		facts.effectiveRuntimeID, facts.plan.EffectiveTarget.SessionGeneration,
		facts.plan.ReplacementTarget.TargetID, facts.plan.ReplacementTarget.TargetVersion, facts.plan.ReplacementTarget.HarnessSessionID,
		facts.replacementRuntimeID, facts.replacementRuntimeGeneration, facts.plan.ReplacementTarget.SessionGeneration, actorUserID)
	if err != nil {
		return nil, coded("agent_message_delivery_recovery_conflict", "closed-target recovery lost its compare-and-swap")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	facts.plan.EffectiveTarget = facts.plan.ReplacementTarget
	facts.plan.Recovered = true
	return &facts.plan, nil
}

func authorizeClosedTargetRecovery(ctx context.Context, tx *sql.Tx, in ClosedTargetRecoveryInput) (int64, error) {
	if in.Authority == nil {
		return 0, coded("agent_message_unauthorized", "current administrator authority is required")
	}
	actor, err := in.Authority(ctx, tx, in.ProjectID)
	if err != nil {
		return 0, err
	}
	if actor <= 0 {
		return 0, coded("agent_message_forbidden", "current project administrator authority is required")
	}
	return actor, nil
}

func loadClosedTargetRecoveryFacts(ctx context.Context, tx *sql.Tx, in ClosedTargetRecoveryInput) (recoveryFacts, error) {
	var facts recoveryFacts
	var originalID, originalKind, effectiveKind string
	var originalVersion int
	var recoveryCount int
	err := tx.QueryRowContext(ctx, `SELECT d.state,d.attempt_count,d.consumer_fence,
		message.to_address,message.delivered,message.is_action_request,message.held_reason,
		d.requested_level,d.fallback_reason,d.lease_until,d.handed_off_at,d.effective_level,
		original.id,original.version,original.target_kind,original.maximum_level,
		COALESCE((SELECT first.old_harness_session_id FROM agent_message_delivery_recoveries first
		 WHERE first.delivery_id=d.delivery_id ORDER BY first.sequence LIMIT 1),
		 (SELECT session.id FROM harness_sessions session WHERE session.message_target_id=original.id ORDER BY session.id LIMIT 1)),
		COALESCE((SELECT first.old_session_generation FROM agent_message_delivery_recoveries first
		 WHERE first.delivery_id=d.delivery_id ORDER BY first.sequence LIMIT 1),
		 (SELECT binding.generation FROM harness_sessions session JOIN lifecycle_runtime_sessions binding ON binding.session_id=session.id
		  WHERE session.message_target_id=original.id ORDER BY session.id LIMIT 1)),
		effective.id,effective.version,effective.target_kind,effective.maximum_level,effective.role,effective.adapter,
		(SELECT COUNT(*) FROM agent_message_delivery_recoveries recovery WHERE recovery.delivery_id=d.delivery_id)
		FROM agent_message_deliveries d
		JOIN agent_messages message ON message.id=d.message_row_id
		JOIN project_agents receiver ON receiver.id=message.to_agent_id
		JOIN agent_message_targets original ON original.id=d.primary_target_id
		JOIN agent_message_targets effective ON effective.id=`+selectedDeliveryTargetSQL+`
		WHERE d.delivery_id=? AND d.instance=? AND receiver.project_id=?`, in.DeliveryID, instanceName(), in.ProjectID).Scan(
		&facts.plan.State, &facts.plan.AttemptCount, &facts.plan.ConsumerFence, &facts.plan.Address,
		&facts.messageDelivered, &facts.actionRequest, &facts.heldReason,
		&facts.requestedLevel, &facts.fallbackReason, &facts.leaseUntil, &facts.handedOffAt, &facts.effectiveLevel,
		&originalID, &originalVersion, &originalKind, &facts.originalMaximum,
		&facts.plan.OriginalTarget.HarnessSessionID, &facts.plan.OriginalTarget.SessionGeneration,
		&facts.plan.EffectiveTarget.TargetID, &facts.plan.EffectiveTarget.TargetVersion, &effectiveKind,
		&facts.effectiveMaximum, &facts.effectiveRole, &facts.effectiveAdapter, &recoveryCount)
	if errors.Is(err, sql.ErrNoRows) {
		return facts, coded("agent_message_delivery_recovery_unknown", "delivery is unavailable in this project")
	}
	if err != nil {
		return facts, err
	}
	originalSessionID, originalSessionGeneration := facts.plan.OriginalTarget.HarnessSessionID, facts.plan.OriginalTarget.SessionGeneration
	facts.plan = ClosedTargetRecoveryPlan{DeliveryID: in.DeliveryID, Address: facts.plan.Address,
		State: facts.plan.State, AttemptCount: facts.plan.AttemptCount, ConsumerFence: facts.plan.ConsumerFence,
		RecoverySequence: recoveryCount + 1,
		OriginalTarget: ClosedTargetRecoveryTarget{TargetID: originalID, TargetVersion: originalVersion,
			HarnessSessionID: originalSessionID, SessionGeneration: originalSessionGeneration},
		EffectiveTarget: ClosedTargetRecoveryTarget{TargetID: facts.plan.EffectiveTarget.TargetID, TargetVersion: facts.plan.EffectiveTarget.TargetVersion}}
	if originalKind != TargetKindHarnessSession || effectiveKind != TargetKindHarnessSession {
		return facts, coded("agent_message_delivery_recovery_unsupported", "delivery does not use a managed harness target")
	}
	var oldPhase string
	err = tx.QueryRowContext(ctx, `SELECT session.phase,binding.runtime_id,binding.generation
		FROM harness_sessions session JOIN lifecycle_runtime_sessions binding ON binding.session_id=session.id
		WHERE session.id=? AND session.project_id=? AND session.message_target_id=?`, in.ExpectedClosedSessionID,
		in.ProjectID, facts.plan.EffectiveTarget.TargetID).Scan(&oldPhase, &facts.effectiveRuntimeID, &facts.plan.EffectiveTarget.SessionGeneration)
	if err != nil || oldPhase != "stopped" {
		return facts, coded("agent_message_delivery_recovery_closed_generation_invalid", "expected managed generation is not publicly closed for this delivery")
	}
	facts.plan.EffectiveTarget.HarnessSessionID = in.ExpectedClosedSessionID
	var replacementKind string
	err = tx.QueryRowContext(ctx, `SELECT target.id,target.version,target.target_kind,target.maximum_level,target.role,target.adapter,
		binding.runtime_id,runtime.generation,binding.generation,runtime.user_id,runtime.api_key_id
		FROM harness_sessions session
		JOIN agent_message_targets target ON target.id=session.message_target_id
		JOIN lifecycle_runtime_sessions binding ON binding.session_id=session.id
		JOIN lifecycle_runtimes runtime ON runtime.id=binding.runtime_id
		WHERE session.id=? AND session.project_id=? AND session.management_mode='managed' AND session.phase<>'stopped'
		 AND session.steer_mode='owned' AND session.advertised_inbox=1 AND session.advertised_status=1
		 AND session.heartbeat_at>=strftime('%Y-%m-%dT%H:%M:%fZ','now',?)
		 AND target.instance=? AND target.project_id=? AND target.address=? AND target.enabled=1
		 AND target.adapter='managed_harness' AND target.target_kind='harness_session'
		 AND runtime.project_id=? AND runtime.machine_id=session.host
		 AND runtime.expires_at>strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 AND runtime.user_id>0 AND runtime.api_key_id>0
		 AND `+lifecyclefence.RuntimeSessionOwnershipSQL("runtime", "session"),
		in.ReplacementSessionID, in.ProjectID, "-90 seconds", instanceName(), in.ProjectID, facts.plan.Address, in.ProjectID).Scan(
		&facts.plan.ReplacementTarget.TargetID, &facts.plan.ReplacementTarget.TargetVersion, &replacementKind,
		&facts.replacementMaximum, &facts.replacementRole, &facts.replacementAdapter,
		&facts.replacementRuntimeID, &facts.replacementRuntimeGeneration, &facts.plan.ReplacementTarget.SessionGeneration,
		&facts.replacementRuntimeUserID, &facts.replacementRuntimeAPIKeyID)
	if err != nil {
		return facts, coded("agent_message_delivery_recovery_replacement_invalid", "replacement generation lacks a fresh authenticated owned binding")
	}
	if _, _, err = auth.ReauthorizeRuntimeReporterTx(ctx, tx, facts.replacementRuntimeUserID,
		facts.replacementRuntimeAPIKeyID, in.ProjectID, time.Now().UTC()); err != nil {
		return facts, coded("agent_message_delivery_recovery_replacement_invalid", "replacement generation lacks a fresh authenticated owned binding")
	}
	facts.plan.ReplacementTarget.HarnessSessionID = in.ReplacementSessionID
	if replacementKind != TargetKindHarnessSession {
		return facts, coded("agent_message_delivery_recovery_replacement_invalid", "replacement generation target is incompatible")
	}
	return facts, nil
}

func validateClosedTargetRecoveryFacts(ctx context.Context, tx *sql.Tx, in ClosedTargetRecoveryInput, facts *recoveryFacts) error {
	if facts.plan.State != "pending" || facts.plan.AttemptCount != 0 || facts.plan.ConsumerFence != 0 ||
		facts.leaseUntil.Valid || facts.handedOffAt.Valid || facts.effectiveLevel.Valid || facts.messageDelivered != 1 ||
		facts.actionRequest != 0 || facts.heldReason != "" {
		return coded("agent_message_delivery_recovery_effect_unknown", "delivery has execution, lease, completion, or held-message evidence")
	}
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_consumer_attempts WHERE resource_id=?`, in.DeliveryID).Scan(&attempts); err != nil {
		return err
	}
	if attempts != 0 {
		return coded("agent_message_delivery_recovery_effect_unknown", "delivery has consumer attempt evidence")
	}
	if facts.plan.RecoverySequence > maxClosedTargetRecoveries {
		return coded("agent_message_delivery_recovery_limit", "delivery recovery history reached its bound")
	}
	if facts.effectiveAdapter != AdapterManagedHarness || facts.effectiveRole != "primary" ||
		facts.replacementAdapter != AdapterManagedHarness || facts.replacementRole != "primary" ||
		facts.plan.EffectiveTarget.TargetID == facts.plan.ReplacementTarget.TargetID ||
		facts.originalMaximum != facts.effectiveMaximum || facts.effectiveMaximum != facts.replacementMaximum {
		return coded("agent_message_delivery_recovery_replacement_invalid", "replacement generation changes receiver policy or binding role")
	}
	if facts.requestedLevel == "steer" && facts.effectiveMaximum != "steer" ||
		facts.requestedLevel != "simple" && facts.requestedLevel != "steer" || facts.fallbackReason != "" {
		return coded("agent_message_delivery_recovery_unsupported", "delivery fallback or capability policy is not eligible for closed-target recovery")
	}
	return nil
}

func loadIdempotentClosedTargetRecovery(ctx context.Context, tx *sql.Tx, in ClosedTargetRecoveryInput) (*ClosedTargetRecoveryPlan, bool, error) {
	var plan ClosedTargetRecoveryPlan
	err := tx.QueryRowContext(ctx, `SELECT recovery.sequence,message.to_address,d.state,d.attempt_count,d.consumer_fence,
		original.id,original.version,
		COALESCE((SELECT first.old_harness_session_id FROM agent_message_delivery_recoveries first
		 WHERE first.delivery_id=d.delivery_id ORDER BY first.sequence LIMIT 1),recovery.old_harness_session_id),
		COALESCE((SELECT first.old_session_generation FROM agent_message_delivery_recoveries first
		 WHERE first.delivery_id=d.delivery_id ORDER BY first.sequence LIMIT 1),recovery.old_session_generation),
		effective.id,effective.version,
		(SELECT tail.new_harness_session_id FROM agent_message_delivery_recoveries tail
		 WHERE tail.delivery_id=d.delivery_id ORDER BY tail.sequence DESC LIMIT 1),
		(SELECT tail.new_session_generation FROM agent_message_delivery_recoveries tail
		 WHERE tail.delivery_id=d.delivery_id ORDER BY tail.sequence DESC LIMIT 1),
		recovery.new_target_id,recovery.new_target_version,recovery.new_harness_session_id,recovery.new_session_generation
		FROM agent_message_delivery_recoveries recovery
		JOIN agent_message_deliveries d ON d.delivery_id=recovery.delivery_id
		JOIN agent_messages message ON message.id=d.message_row_id
		JOIN agent_message_targets original ON original.id=d.primary_target_id
		JOIN agent_message_targets effective ON effective.id=`+effectivePrimaryDeliveryTargetSQL+`
		WHERE recovery.delivery_id=? AND recovery.project_id=? AND recovery.old_target_id=?
		 AND recovery.old_target_version=? AND recovery.old_harness_session_id=? AND recovery.new_harness_session_id=?`,
		in.DeliveryID, in.ProjectID, in.ExpectedTargetID, in.ExpectedTargetVersion,
		in.ExpectedClosedSessionID, in.ReplacementSessionID).Scan(&plan.RecoverySequence, &plan.Address,
		&plan.State, &plan.AttemptCount, &plan.ConsumerFence,
		&plan.OriginalTarget.TargetID, &plan.OriginalTarget.TargetVersion, &plan.OriginalTarget.HarnessSessionID,
		&plan.OriginalTarget.SessionGeneration, &plan.EffectiveTarget.TargetID, &plan.EffectiveTarget.TargetVersion,
		&plan.EffectiveTarget.HarnessSessionID, &plan.EffectiveTarget.SessionGeneration,
		&plan.ReplacementTarget.TargetID, &plan.ReplacementTarget.TargetVersion,
		&plan.ReplacementTarget.HarnessSessionID, &plan.ReplacementTarget.SessionGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plan.DeliveryID, plan.Recovered = in.DeliveryID, true
	return &plan, true, nil
}
