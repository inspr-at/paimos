// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type stageExecutionKey struct {
	attempt   int64
	execution int64
	stage     string
}

func (s *Store) SnapshotByIssue(ctx context.Context, issueID int64) (Snapshot, error) {
	return s.SnapshotByIssueTx(ctx, s.db, issueID)
}

// SnapshotByIssueTx lets PAI-803/804 combine delivery truth, trust inputs, and
// aggregates under one caller-owned read snapshot.
func (s *Store) SnapshotByIssueTx(ctx context.Context, q DBTX, issueID int64) (Snapshot, error) {
	if issueID <= 0 {
		return Snapshot{}, ErrInvalid
	}
	snapshots, err := s.BulkSnapshotsTx(ctx, q, []int64{issueID})
	if err != nil {
		return Snapshot{}, err
	}
	return snapshots[0], nil
}

func (s *Store) BulkSnapshots(ctx context.Context, issueIDs []int64) ([]Snapshot, error) {
	return s.BulkSnapshotsTx(ctx, s.db, issueIDs)
}

// BulkSnapshotsTx is the sole snapshot loader/reducer. It performs one SQL
// round trip for 1..1,000 already-authorized issue roots and preserves caller
// ordering while the caller controls transaction isolation.
func (s *Store) BulkSnapshotsTx(ctx context.Context, q DBTX, issueIDs []int64) ([]Snapshot, error) {
	if len(issueIDs) == 0 {
		return []Snapshot{}, nil
	}
	if len(issueIDs) > 1000 {
		return nil, fmt.Errorf("%w: bulk snapshot limit is 1000", ErrInvalid)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(issueIDs)), ",")
	args := make([]any, len(issueIDs))
	for i, id := range issueIDs {
		if id <= 0 {
			return nil, ErrInvalid
		}
		args[i] = id
	}
	calculatedAt := s.now()
	// One SQL round-trip returns bounded, typed JSON aggregates for every
	// requested root. Correlated lookups remain index-backed; query count is
	// constant whether the caller asks for one delivery or one thousand.
	query := `SELECT i.id,i.title,i.description,i.acceptance_criteria,d.id,d.delivery_key,d.spec_revision,a.id,a.attempt_number,a.plan_revision,
		COALESCE((SELECT MAX(delivery_revision) FROM delivery_events WHERE delivery_id=d.id),0),
		COALESCE(d.change_sequence_high_water,0),
		COALESCE((SELECT json_group_array(json_object(
		 'stage_key',p.stage_key,'sort_order',p.sort_order,'applicability',p.applicability,'weight',p.weight,
		 'execution_number',COALESCE(l.execution_number,0),'authority_epoch',COALESCE(l.authority_epoch,0),
		 'current_reporter_id',COALESCE(l.current_reporter_id,0),'reporter_type',COALESCE(r.reporter_type,''),
		 'authority_anchor_id',COALESCE(l.authority_stage_event_id,0),
		 'authority_source_cutoff',COALESCE((SELECT owner.authority_source_sequence_cutoff
		   FROM delivery_stage_events owner WHERE owner.attempt_id=p.attempt_id AND owner.stage_key=p.stage_key
		    AND owner.execution_number=l.execution_number AND owner.authority_epoch=l.authority_epoch
		    AND owner.reporter_id=l.current_reporter_id AND owner.event_type IN ('execution_started','handoff')
		   ORDER BY owner.event_sequence DESC LIMIT 1),0),
		 'authority_activated_at',COALESCE((SELECT owner.server_received_at
		   FROM delivery_stage_events owner WHERE owner.attempt_id=p.attempt_id AND owner.stage_key=p.stage_key
		    AND owner.execution_number=l.execution_number AND owner.authority_epoch=l.authority_epoch
		    AND owner.reporter_id=l.current_reporter_id AND owner.event_type IN ('execution_started','handoff')
		   ORDER BY owner.event_sequence DESC LIMIT 1),''),
		 'start_id',COALESCE(l.execution_start_stage_event_id,0),'based_on_id',COALESCE(start.based_on_stage_event_id,0),
		 'start_at',COALESCE(start.server_received_at,''),'semantic_id',COALESCE(l.semantic_stage_event_id,0),
		 'semantic_state',COALESCE(sem.semantic_state,'pending'),'activity',COALESCE(sem.activity,''),
		 'needs_input',COALESCE(sem.needs_input,0),'semantic_authority',COALESCE(sem.authority_epoch,0),
		 'semantic_reporter_id',COALESCE(sem.reporter_id,0),'semantic_spec_revision',COALESCE(sem.spec_revision,0),
		 'heartbeat_at',COALESCE(hb.server_received_at,''),
		 'run_json',json_object('run_id',COALESCE(ar.id,0),'status',COALESCE(ar.status,''),
		   'supervised',COALESCE(ar.expects_supervisor_telemetry,0),'heartbeat_at',COALESCE(tl.last_heartbeat_at,''),
		   'heartbeat_sequence',COALESCE(ht.sequence,0),'activation_cutoff',COALESCE(act.telemetry_sequence_cutoff,0),
		   'activation_at',COALESCE(act.created_at,''),
		   'started_at',COALESCE(NULLIF(ar.started_at,''),ar.created_at,''),
		   'semantic_id',COALESCE(st.id,0),'semantic_sequence',COALESCE(st.sequence,0),
		   'phase',COALESCE(st.phase,''),'activity',COALESCE(st.activity,''),'needs_input',COALESCE(st.needs_input,0),
		   'blocker_state',COALESCE(st.blocker_state,''),'semantic_at',COALESCE(st.server_received_at,'')),
		 'external_estimate_json',json_object('id',COALESCE(est.id,0),'sequence',COALESCE(est.source_sequence,0),
		   'revision',COALESCE(est.estimate_revision,0),'progress',est.progress_percent,'eta',est.eta_seconds,
		   'eta_min',est.eta_min_seconds,'eta_max',est.eta_max_seconds,'source',COALESCE(est.estimate_source,''),
		   'confidence',COALESCE(est.estimate_confidence,0),'basis',COALESCE(est.estimate_basis,''),
		   'received_at',COALESCE(est.server_received_at,'')),
		 'run_estimate_json',json_object('id',COALESCE(rte.id,0),'sequence',COALESCE(rte.sequence,0),
		   'revision',COALESCE(rte.estimate_revision,0),'progress',rte.progress_percent,'eta',rte.eta_seconds,
		   'eta_min',rte.eta_min_seconds,'eta_max',rte.eta_max_seconds,'source',COALESCE(rte.estimate_source,''),
		   'confidence',COALESCE(rte.estimate_confidence,0),'basis',COALESCE(rte.estimate_basis,''),
		   'received_at',COALESCE(rte.server_received_at,'')),
		 'reset_json',json_object('id',COALESCE(reset.id,0),'epoch',COALESCE(reset.reset_epoch,0),
		   'authority_anchor_id',COALESCE(reset.reset_authority_anchor_stage_event_id,0),
		   'stage_cutoff',COALESCE(reset.reset_source_cutoff,0),'source_kind',COALESCE(reset.reset_source_kind,''),
		   'run_id',reset.reset_telemetry_run_id,'telemetry_cutoff',reset.reset_telemetry_sequence_cutoff),
		 'blockers_json',COALESCE((SELECT json_group_array(json_object('key',ob.blocker_key,'class',ob.blocker_class,
		   'summary',ob.summary,'human_wait',ob.is_human_wait)) FROM (SELECT * FROM delivery_stage_blockers
		   WHERE stage_event_id=l.semantic_stage_event_id AND is_current=1 ORDER BY ordinal) ob),'[]'),
		 'evidence_json',COALESCE((SELECT json_group_array(json_object('type',oe.evidence_type,'outcome',oe.outcome,
		   'reference_kind',oe.reference_kind,'reference_value',oe.reference_value,'digest',oe.digest_sha256,
		   'attachment_id',oe.attachment_id)) FROM (SELECT * FROM delivery_evidence
		   WHERE stage_event_id=l.semantic_stage_event_id ORDER BY ordinal) oe),'[]')
		 )) FROM delivery_attempt_stage_policy p
		 LEFT JOIN delivery_stage_latest l ON l.attempt_id=p.attempt_id AND l.stage_key=p.stage_key
		 LEFT JOIN delivery_reporters r ON r.id=l.current_reporter_id AND r.delivery_id=l.delivery_id
		 LEFT JOIN delivery_stage_events start ON start.id=l.execution_start_stage_event_id
		 LEFT JOIN delivery_stage_events sem ON sem.id=l.semantic_stage_event_id
		 LEFT JOIN delivery_stage_events hb ON hb.id=l.heartbeat_stage_event_id
		 LEFT JOIN delivery_stage_events est ON est.id=l.estimate_stage_event_id
		 LEFT JOIN delivery_agent_run_links link ON link.attempt_id=p.attempt_id AND link.stage_key=p.stage_key
		  AND link.execution_number=l.execution_number AND link.reporter_id=l.current_reporter_id
		 LEFT JOIN delivery_agent_run_activations act ON act.attempt_id=p.attempt_id AND act.stage_key=p.stage_key
		  AND act.execution_number=l.execution_number AND act.authority_epoch=l.authority_epoch
		  AND act.agent_run_id=link.agent_run_id AND act.reporter_id=l.current_reporter_id
		 LEFT JOIN agent_runs ar ON ar.id=act.agent_run_id
		 LEFT JOIN agent_run_telemetry_latest tl ON tl.run_id=ar.id
		 LEFT JOIN agent_run_telemetry ht ON ht.id=tl.heartbeat_telemetry_id
		 LEFT JOIN agent_run_telemetry st ON st.id=tl.semantic_telemetry_id
		 LEFT JOIN agent_run_telemetry rte ON rte.id=tl.estimate_telemetry_id
		 LEFT JOIN delivery_stage_events reset ON reset.id=(SELECT rs.id FROM delivery_stage_events rs
		   WHERE rs.attempt_id=p.attempt_id AND rs.stage_key=p.stage_key
		    AND rs.execution_number=l.execution_number AND rs.event_type='progress_reset_authorized'
		    AND ((link.agent_run_id IS NULL AND rs.authority_epoch=l.authority_epoch)
		      OR (link.agent_run_id IS NOT NULL AND rs.reset_telemetry_run_id=link.agent_run_id))
		   ORDER BY rs.event_sequence DESC LIMIT 1)
		 WHERE p.attempt_id=a.id ORDER BY p.sort_order),'[]'),
		COALESCE((SELECT json_group_array(json_object('run_id',ar.id,'status',ar.status,'agent_name',ar.agent_name))
		 FROM agent_runs ar WHERE ar.issue_id=i.id AND ar.delivery_instrumentation_version=0
		 AND ar.status IN ('queued','running') ORDER BY ar.id),'[]'),
		(SELECT COUNT(*) FROM agent_runs active_run
		 WHERE active_run.issue_id=i.id AND active_run.delivery_instrumentation_version=1
		  AND NOT EXISTS(SELECT 1 FROM delivery_agent_run_links active_link
		   WHERE active_link.agent_run_id=active_run.id AND active_link.root_issue_id=i.id))
	FROM issues i
	LEFT JOIN deliveries d ON d.issue_id=i.id
	LEFT JOIN delivery_attempts a ON a.id=(SELECT ca.id FROM delivery_attempts ca
	 JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=ca.delivery_id AND seal.attempt_id=ca.id
	 WHERE ca.delivery_id=d.id ORDER BY ca.attempt_number DESC LIMIT 1)
	WHERE i.deleted_at IS NULL AND i.id IN (` + placeholders + `) ORDER BY i.id`
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byIssue := make(map[int64]Snapshot, len(issueIDs))
	for rows.Next() {
		var issueID int64
		var title, description, criteria string
		var deliveryID, specRevision, attemptID, attemptNumber, planRevision sql.NullInt64
		var deliveryKey sql.NullString
		var revision, changeSequence int64
		var stagesJSON, legacyJSON string
		var unlinkedActiveRuns int
		if err := rows.Scan(&issueID, &title, &description, &criteria, &deliveryID, &deliveryKey, &specRevision, &attemptID, &attemptNumber, &planRevision,
			&revision, &changeSequence, &stagesJSON, &legacyJSON, &unlinkedActiveRuns); err != nil {
			return nil, err
		}
		if unlinkedActiveRuns != 0 {
			return nil, fmt.Errorf("%w: active instrumented run lacks a delivery link", ErrInvariant)
		}
		snapshot, err := s.assembleBulkSnapshot(issueID, title, description, criteria, deliveryID, deliveryKey, specRevision, attemptID, attemptNumber,
			planRevision, revision, changeSequence, stagesJSON, legacyJSON, calculatedAt)
		if err != nil {
			return nil, err
		}
		byIssue[issueID] = snapshot
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(issueIDs))
	for _, issueID := range issueIDs {
		snapshot, ok := byIssue[issueID]
		if !ok {
			return nil, ErrNotFound
		}
		out = append(out, snapshot)
	}
	return out, nil
}

type bulkBlocker struct {
	Key       string `json:"key"`
	Class     string `json:"class"`
	Summary   string `json:"summary"`
	HumanWait int    `json:"human_wait"`
}

type bulkEvidence struct {
	Type           string `json:"type"`
	Outcome        string `json:"outcome"`
	ReferenceKind  string `json:"reference_kind"`
	ReferenceValue string `json:"reference_value"`
	Digest         string `json:"digest"`
	AttachmentID   *int64 `json:"attachment_id"`
}

type bulkRun struct {
	RunID             int64  `json:"run_id"`
	Status            string `json:"status"`
	Supervised        int    `json:"supervised"`
	HeartbeatAt       string `json:"heartbeat_at"`
	HeartbeatSequence int64  `json:"heartbeat_sequence"`
	ActivationCutoff  int64  `json:"activation_cutoff"`
	ActivationAt      string `json:"activation_at"`
	StartedAt         string `json:"started_at"`
	SemanticID        int64  `json:"semantic_id"`
	SemanticSequence  int64  `json:"semantic_sequence"`
	Phase             string `json:"phase"`
	Activity          string `json:"activity"`
	NeedsInput        int    `json:"needs_input"`
	BlockerState      string `json:"blocker_state"`
	SemanticAt        string `json:"semantic_at"`
}

type bulkEstimate struct {
	ID         int64    `json:"id"`
	Sequence   int64    `json:"sequence"`
	Revision   int64    `json:"revision"`
	Progress   *float64 `json:"progress"`
	ETA        *int64   `json:"eta"`
	ETAMin     *int64   `json:"eta_min"`
	ETAMax     *int64   `json:"eta_max"`
	Source     string   `json:"source"`
	Confidence float64  `json:"confidence"`
	Basis      string   `json:"basis"`
	ReceivedAt string   `json:"received_at"`
}

type bulkReset struct {
	ID                int64  `json:"id"`
	Epoch             int64  `json:"epoch"`
	AuthorityAnchorID int64  `json:"authority_anchor_id"`
	StageCutoff       int64  `json:"stage_cutoff"`
	SourceKind        string `json:"source_kind"`
	RunID             *int64 `json:"run_id"`
	TelemetryCutoff   *int64 `json:"telemetry_cutoff"`
}

type bulkStage struct {
	StageKey              string          `json:"stage_key"`
	SortOrder             int             `json:"sort_order"`
	Applicability         string          `json:"applicability"`
	Weight                int             `json:"weight"`
	ExecutionNumber       int64           `json:"execution_number"`
	AuthorityEpoch        int64           `json:"authority_epoch"`
	CurrentReporterID     int64           `json:"current_reporter_id"`
	ReporterType          string          `json:"reporter_type"`
	AuthorityAnchorID     int64           `json:"authority_anchor_id"`
	AuthoritySourceCutoff int64           `json:"authority_source_cutoff"`
	AuthorityActivatedAt  string          `json:"authority_activated_at"`
	StartID               int64           `json:"start_id"`
	BasedOnID             int64           `json:"based_on_id"`
	StartAt               string          `json:"start_at"`
	SemanticID            int64           `json:"semantic_id"`
	SemanticState         string          `json:"semantic_state"`
	Activity              string          `json:"activity"`
	NeedsInput            int             `json:"needs_input"`
	SemanticAuthority     int64           `json:"semantic_authority"`
	SemanticReporterID    int64           `json:"semantic_reporter_id"`
	SemanticSpecRev       int64           `json:"semantic_spec_revision"`
	HeartbeatAt           string          `json:"heartbeat_at"`
	Run                   bulkRun         `json:"run_json"`
	ExternalEstimate      bulkEstimate    `json:"external_estimate_json"`
	RunEstimate           bulkEstimate    `json:"run_estimate_json"`
	Reset                 bulkReset       `json:"reset_json"`
	BlockersJSON          json.RawMessage `json:"blockers_json"`
	EvidenceJSON          json.RawMessage `json:"evidence_json"`
}

func (s *Store) assembleBulkSnapshot(issueID int64, title, description, criteria string, deliveryID sql.NullInt64, deliveryKey sql.NullString,
	specRevision, attemptID, attemptNumber, planRevision sql.NullInt64, revision, changeSequence int64, stagesJSON, legacyJSON string,
	calculatedAt time.Time) (Snapshot, error) {
	var legacy []LegacyRun
	if err := json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
		return Snapshot{}, err
	}
	if !deliveryID.Valid {
		return Snapshot{IssueID: issueID, DeliveryKey: fmt.Sprintf("issue:%d", issueID), Instrumented: false,
			State: "unknown", AttentionFlags: []string{"unknown_reporter"}, PrimaryAttention: "unknown_reporter",
			LegacyActiveRuns: legacy}, nil
	}
	delivery := deliveryID.Int64
	snapshot := Snapshot{IssueID: issueID, DeliveryKey: deliveryKey.String, DeliveryID: &delivery, Instrumented: true,
		DeliveryRevision: revision, ChangeSequence: changeSequence, SpecRevision: specRevision.Int64,
		State: "unknown", LegacyActiveRuns: legacy}
	if !attemptID.Valid {
		snapshot.AttentionFlags, snapshot.PrimaryAttention = []string{"unknown_reporter"}, "unknown_reporter"
		return snapshot, nil
	}
	aid := attemptID.Int64
	snapshot.AttemptID, snapshot.AttemptNumber, snapshot.PlanRevision, snapshot.State = &aid, attemptNumber.Int64, planRevision.Int64, "pending"
	var rawStages []bulkStage
	if err := json.Unmarshal([]byte(stagesJSON), &rawStages); err != nil {
		return Snapshot{}, err
	}
	if len(rawStages) != len(CanonicalStages) {
		return Snapshot{}, fmt.Errorf("%w: active attempt does not have five policy facts", ErrInvariant)
	}
	sort.Slice(rawStages, func(i, j int) bool { return rawStages[i].SortOrder < rawStages[j].SortOrder })
	flags := map[string]bool{}
	allRequired, anyExecution, anyFailed, retryFailed, anyCancelled := true, false, false, false, false
	requiredCount, weightTotal := 0, 0
	var predecessor int64
	predecessorEligible := true
	currentSpecDigest := canonicalIssueSpecDigestValues(title, description, criteria)
	for index, raw := range rawStages {
		if raw.StageKey != CanonicalStages[index] || raw.SortOrder != index+1 {
			return Snapshot{}, fmt.Errorf("%w: active attempt policy order is not canonical", ErrInvariant)
		}
		stage := StageSnapshot{StageKey: raw.StageKey, SortOrder: raw.SortOrder, Applicability: raw.Applicability,
			Weight: raw.Weight, ExecutionNumber: raw.ExecutionNumber, AuthorityEpoch: raw.AuthorityEpoch,
			ReporterType: raw.ReporterType, SemanticState: raw.SemanticState, Activity: raw.Activity,
			NeedsInput: raw.NeedsInput == 1}
		weightTotal += raw.Weight
		if raw.Applicability == "required" {
			requiredCount++
		}
		stage.AuthorityActivatedAt = stringPtr(raw.AuthorityActivatedAt)
		if raw.StartID > 0 {
			stage.ExecutionStartEventID = int64Pointer(raw.StartID)
			anyExecution = true
		}
		if raw.BasedOnID > 0 {
			stage.BasedOnStageEventID = int64Pointer(raw.BasedOnID)
		}
		if raw.SemanticID > 0 {
			stage.SemanticEventID = int64Pointer(raw.SemanticID)
		}
		if raw.Applicability == "not_applicable" {
			stage.SemanticState = "not_applicable"
			stage.PolicySatisfied = true
			stage.CurrentLineage = predecessorEligible
			snapshot.Stages = append(snapshot.Stages, stage)
			continue
		}
		applyRunSemantic(raw, &stage)
		var blockers []bulkBlocker
		if err := unmarshalNested(raw.BlockersJSON, &blockers); err != nil {
			return Snapshot{}, err
		}
		for _, blocker := range blockers {
			stage.CurrentBlockers = append(stage.CurrentBlockers, Blocker{Key: blocker.Key, Class: blocker.Class,
				Summary: blocker.Summary, HumanWait: blocker.HumanWait == 1})
		}
		var evidence []bulkEvidence
		if err := unmarshalNested(raw.EvidenceJSON, &evidence); err != nil {
			return Snapshot{}, err
		}
		passed, failedEvidence := false, false
		for _, item := range evidence {
			stage.Evidence = append(stage.Evidence, Evidence{Type: item.Type, Outcome: item.Outcome,
				ReferenceKind: item.ReferenceKind, ReferenceValue: item.ReferenceValue, DigestSHA256: item.Digest,
				AttachmentID: item.AttachmentID})
			eligibleEvidence := item.Outcome == "passed" && evidenceAllowed(raw.StageKey, item.Type)
			if raw.StageKey == StageSpecification {
				eligibleEvidence = eligibleEvidence && item.Digest == currentSpecDigest && raw.SemanticSpecRev == snapshot.SpecRevision
			}
			passed = passed || eligibleEvidence
			failedEvidence = failedEvidence || (item.Outcome == "failed" && evidenceAllowed(raw.StageKey, item.Type))
		}
		lineage := (predecessor == 0 && raw.BasedOnID == 0) || (predecessor > 0 && raw.BasedOnID == predecessor)
		stage.CurrentLineage = predecessorEligible && lineage
		semanticCurrent := raw.SemanticID > 0 && raw.SemanticAuthority == raw.AuthorityEpoch && raw.SemanticReporterID == raw.CurrentReporterID
		stage.EligibleSuccess = stage.CurrentLineage && semanticCurrent && raw.SemanticState == "succeeded" &&
			len(blockers) == 0 && passed && !failedEvidence
		stage.PolicySatisfied, stage.Performed = stage.EligibleSuccess, stage.EligibleSuccess
		if stage.EligibleSuccess {
			predecessor = raw.SemanticID
		} else {
			predecessor, predecessorEligible, allRequired = 0, false, false
		}
		applyStageSourceTruth(s, raw, &stage, calculatedAt)
		if !stage.CurrentLineage {
			suppressInvalidLineageTruth(&stage)
		}
		if stage.CurrentLineage {
			if stage.NeedsInput {
				flags["waiting_on_human"] = true
			}
			for _, blocker := range stage.CurrentBlockers {
				flags["blocked"] = true
				flags["waiting_on_human"] = flags["waiting_on_human"] || blocker.HumanWait
			}
			if semanticCurrent {
				if stage.SemanticState == "failed" {
					anyFailed, retryFailed = true, true
				}
				anyCancelled = anyCancelled || stage.SemanticState == "cancelled"
			}
			if raw.StartID > 0 && (raw.ReporterType == "" || (raw.ReporterType == "agent_run" && raw.Run.RunID == 0)) {
				flags["unknown_reporter"] = true
			}
			if stage.Stale || stage.EstimateStale {
				flags["stale"] = true
			}
		}
		snapshot.Stages = append(snapshot.Stages, stage)
	}
	if requiredCount == 0 || weightTotal != 100 {
		return Snapshot{}, fmt.Errorf("%w: active attempt policy must retain a required stage and weight 100", ErrInvariant)
	}
	deployment := snapshotStage(snapshot.Stages, StageDeployment)
	verification := snapshotStage(snapshot.Stages, StageVerification)
	snapshot.Deployed, snapshot.Verified = deployment.Performed, verification.Performed
	snapshot.DeploymentPolicySatisfied, snapshot.VerificationPolicySatisfied = deployment.PolicySatisfied, verification.PolicySatisfied
	snapshot.Unverified = snapshot.Deployed && !snapshot.VerificationPolicySatisfied
	if snapshot.Unverified {
		flags["unverified"] = true
	}
	snapshot.Failed, snapshot.FailedNeedsRetry, snapshot.Cancelled = anyFailed, retryFailed, anyCancelled
	if anyFailed && anyCancelled {
		return Snapshot{}, fmt.Errorf("%w: current lineage cannot be both failed and cancelled", ErrInvariant)
	}
	if allRequired {
		snapshot.State = "completed"
	} else if anyCancelled {
		snapshot.State = "cancelled"
	} else if anyFailed {
		// Every sealed current-lineage failure requires a new execution; retry
		// status is independent of deployment, verification, and N/A axes.
		snapshot.State = "failed_needs_retry"
	} else if snapshot.Unverified {
		snapshot.State = "deployed_unverified"
	} else if anyExecution {
		snapshot.State = "active"
	}
	if len(legacy) > 0 {
		flags["unknown_reporter"] = true
	}
	snapshot.AttentionFlags = orderedAttention(flags)
	if len(snapshot.AttentionFlags) > 0 {
		snapshot.PrimaryAttention = snapshot.AttentionFlags[0]
	}
	return snapshot, nil
}

// An execution whose predecessor is no longer current remains addressable as
// immutable history, but none of its owner-supplied facts are current delivery
// truth. Keeping these fields empty also means revoked run telemetry cannot
// alter a snapshot without a matching delivery revision/change invalidation.
func suppressInvalidLineageTruth(stage *StageSnapshot) {
	stage.ReporterType = ""
	stage.SemanticState = "pending"
	stage.Phase = ""
	stage.Activity = ""
	stage.NeedsInput = false
	stage.TelemetryBlockerState = ""
	stage.CurrentBlockers = nil
	stage.Evidence = nil
	stage.EligibleSuccess = false
	stage.SemanticEventID = nil
	stage.OwnerRunID = nil
	stage.AuthorityActivationCutoff = 0
	stage.AuthorityActivatedAt = nil
	stage.LastHeartbeatAt = nil
	stage.LastSemanticAt = nil
	stage.LatestEstimateAt = nil
	stage.LatestEstimate = nil
	stage.ProgressReset = nil
	stage.SignalState = ""
	stage.NeverSignaled = false
	stage.HeartbeatStale = false
	stage.EstimateStale = false
	stage.Stale = false
	stage.NextFreshnessTransitionAt = nil
}

func applyRunSemantic(raw bulkStage, stage *StageSnapshot) {
	if raw.SemanticID > 0 || raw.Run.RunID == 0 || raw.Run.SemanticID == 0 ||
		raw.Run.SemanticSequence <= raw.Run.ActivationCutoff ||
		(raw.Run.Status != "queued" && raw.Run.Status != "running") {
		return
	}
	stage.Phase = raw.Run.Phase
	stage.Activity = raw.Run.Activity
	stage.NeedsInput = raw.Run.NeedsInput == 1
	stage.TelemetryBlockerState = raw.Run.BlockerState
	stage.LastSemanticAt = stringPtr(raw.Run.SemanticAt)
	if stage.NeedsInput || (raw.Run.BlockerState != "" && raw.Run.BlockerState != "none") || raw.Run.Phase == "waiting" {
		stage.SemanticState = "waiting"
	} else {
		stage.SemanticState = "active"
	}
	if raw.Run.BlockerState != "" && raw.Run.BlockerState != "none" {
		stage.CurrentBlockers = append(stage.CurrentBlockers, Blocker{Key: "agent-run-blocker", Class: raw.Run.BlockerState,
			HumanWait: stage.NeedsInput || raw.Run.BlockerState == "input"})
	} else if stage.NeedsInput {
		stage.CurrentBlockers = append(stage.CurrentBlockers, Blocker{Key: "agent-run-needs-input", Class: "input", HumanWait: true})
	}
}

func applyStageSourceTruth(s *Store, raw bulkStage, stage *StageSnapshot, calculatedAt time.Time) {
	if raw.Reset.ID > 0 {
		stage.ProgressReset = &ProgressResetBoundary{StageEventID: raw.Reset.ID, ResetEpoch: raw.Reset.Epoch,
			AuthorityAnchorStageEventID: raw.Reset.AuthorityAnchorID, StageSourceSequenceCutoff: raw.Reset.StageCutoff,
			SourceKind: raw.Reset.SourceKind, AgentRunID: raw.Reset.RunID, TelemetrySequenceCutoff: raw.Reset.TelemetryCutoff}
	}
	var estimate bulkEstimate
	if raw.Run.RunID > 0 {
		id := raw.Run.RunID
		stage.OwnerRunID = &id
		stage.AuthorityActivationCutoff = raw.Run.ActivationCutoff
		stage.AuthorityActivatedAt = stringPtr(raw.Run.ActivationAt)
		estimate = raw.RunEstimate
		if estimate.Sequence <= raw.Run.ActivationCutoff {
			estimate = bulkEstimate{}
		}
		if raw.Reset.RunID != nil && *raw.Reset.RunID == raw.Run.RunID && raw.Reset.TelemetryCutoff != nil &&
			estimate.Sequence <= *raw.Reset.TelemetryCutoff {
			estimate = bulkEstimate{}
		}
	} else {
		stage.AuthorityActivationCutoff = raw.AuthoritySourceCutoff
		estimate = raw.ExternalEstimate
		if raw.Reset.ID > 0 && estimate.Sequence <= raw.Reset.StageCutoff {
			estimate = bulkEstimate{}
		}
	}
	if estimate.ID > 0 && estimate.Confidence <= 0 {
		estimate = bulkEstimate{}
	}
	terminalExternal := raw.SemanticID > 0 && raw.SemanticAuthority == raw.AuthorityEpoch &&
		raw.SemanticReporterID == raw.CurrentReporterID &&
		(raw.SemanticState == "succeeded" || raw.SemanticState == "failed" || raw.SemanticState == "cancelled" || raw.SemanticState == "draft_ready")
	externalActive := raw.Run.RunID == 0 && raw.ReporterType == "external" && raw.StartID > 0 && !terminalExternal
	activeOwner := (raw.Run.RunID > 0 && raw.Run.Status == "running") || externalActive
	if estimate.ID > 0 {
		sourceKind, sourceID := "stage_event", estimate.ID
		if raw.Run.RunID > 0 {
			sourceKind, sourceID = "agent_run_telemetry", raw.Run.RunID
		}
		stage.LatestEstimate = &EstimateSnapshot{SourceKind: sourceKind, SourceID: sourceID,
			SourceSequence: estimate.Sequence, Revision: estimate.Revision, Progress: estimate.Progress,
			ETASeconds: estimate.ETA, ETAMin: estimate.ETAMin, ETAMax: estimate.ETAMax,
			Source: estimate.Source, Confidence: estimate.Confidence, Basis: estimate.Basis, ServerReceivedAt: estimate.ReceivedAt}
		stage.LatestEstimateAt = stringPtr(estimate.ReceivedAt)
		stage.EstimateStale = activeOwner && isOlderThan(estimate.ReceivedAt, calculatedAt, s.freshness.EstimateTimeout)
		if activeOwner && !stage.EstimateStale {
			setNextFreshnessTransition(stage, estimate.ReceivedAt, s.freshness.EstimateTimeout, calculatedAt)
		}
	}
	if raw.Run.RunID > 0 && raw.Run.Supervised == 1 {
		heartbeatAt := raw.Run.HeartbeatAt
		if raw.Run.HeartbeatSequence <= raw.Run.ActivationCutoff {
			heartbeatAt = ""
		}
		switch raw.Run.Status {
		case "queued":
			stage.SignalState = "queued"
		case "running":
			classifyHeartbeat(stage, laterFreshnessAnchor(raw.Run.ActivationAt, raw.Run.StartedAt), heartbeatAt, calculatedAt, s.freshness)
		default:
			stage.SignalState = "ended"
		}
	} else if externalActive {
		classifyHeartbeat(stage, raw.AuthorityActivatedAt, raw.HeartbeatAt, calculatedAt, s.freshness)
	}
}

func laterFreshnessAnchor(activationAt, startedAt string) string {
	activation, activationErr := parseStoredTime(activationAt)
	started, startedErr := parseStoredTime(startedAt)
	if activationErr != nil {
		return startedAt
	}
	if startedErr != nil || !started.After(activation) {
		return activationAt
	}
	return startedAt
}

func classifyHeartbeat(stage *StageSnapshot, startAt, heartbeatAt string, now time.Time, policy FreshnessPolicy) {
	if heartbeatAt == "" {
		if isOlderThan(startAt, now, policy.FirstSignalTimeout) {
			stage.SignalState, stage.NeverSignaled, stage.Stale = "no_signal", true, true
		} else {
			stage.SignalState = "awaiting_first_signal"
			setNextFreshnessTransition(stage, startAt, policy.FirstSignalTimeout, now)
		}
		return
	}
	stage.LastHeartbeatAt = stringPtr(heartbeatAt)
	if isOlderThan(heartbeatAt, now, policy.HeartbeatTimeout) {
		stage.SignalState, stage.HeartbeatStale, stage.Stale = "stale", true, true
	} else {
		stage.SignalState = "live"
		setNextFreshnessTransition(stage, heartbeatAt, policy.HeartbeatTimeout, now)
	}
}

func setNextFreshnessTransition(stage *StageSnapshot, raw string, threshold time.Duration, now time.Time) {
	stamp, err := parseStoredTime(raw)
	if err != nil {
		return
	}
	next := stamp.Add(threshold)
	if !next.After(now) {
		return
	}
	formatted := formatTime(next)
	if stage.NextFreshnessTransitionAt == nil || formatted < *stage.NextFreshnessTransitionAt {
		stage.NextFreshnessTransitionAt = &formatted
	}
}

func snapshotStage(stages []StageSnapshot, key string) StageSnapshot {
	for _, stage := range stages {
		if stage.StageKey == key {
			return stage
		}
	}
	return StageSnapshot{}
}

func stringPtr(v string) *string {
	if v == "" {
		return nil
	}
	out := v
	return &out
}

func int64Pointer(v int64) *int64 {
	out := v
	return &out
}

func unmarshalNested(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return json.Unmarshal([]byte("[]"), out)
	}
	if raw[0] == '"' {
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return err
		}
		return json.Unmarshal([]byte(encoded), out)
	}
	return json.Unmarshal(raw, out)
}

func isOlderThan(raw string, now time.Time, threshold time.Duration) bool {
	t, err := parseStoredTime(raw)
	return err != nil || !now.Before(t.Add(threshold))
}

func parseStoredTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse("2006-01-02 15:04:05", raw)
	return parsed.UTC(), err
}

func stageEligible(stages []StageSnapshot, key string) bool {
	for _, stage := range stages {
		if stage.StageKey == key {
			return stage.EligibleSuccess
		}
	}
	return false
}

func orderedAttention(flags map[string]bool) []string {
	precedence := []string{"waiting_on_human", "blocked", "stale", "unknown_reporter", "unverified"}
	var out []string
	for _, flag := range precedence {
		if flags[flag] {
			out = append(out, flag)
		}
	}
	return out
}

func loadLegacyRuns(ctx context.Context, q DBTX, issueID int64) ([]LegacyRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,status,agent_name FROM agent_runs
		WHERE issue_id=? AND delivery_instrumentation_version=0 AND status IN ('queued','running') ORDER BY id`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LegacyRun
	for rows.Next() {
		var run LegacyRun
		if err := rows.Scan(&run.RunID, &run.Status, &run.AgentName); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) AttemptHistory(ctx context.Context, issueID int64) ([]Attempt, error) {
	return s.AttemptHistoryTx(ctx, s.db, issueID)
}

func (s *Store) AttemptHistoryTx(ctx context.Context, q DBTX, issueID int64) ([]Attempt, error) {
	if issueID <= 0 {
		return nil, ErrInvalid
	}
	rows, err := q.QueryContext(ctx, `SELECT a.id,a.delivery_id,a.attempt_number,a.plan_revision,a.previous_attempt_id,
		a.project_id_at_start,a.reason_code,a.reason_text,a.created_at,p.stage_key,p.applicability,p.weight,
		p.policy_reference,p.reason_code,p.reason_text,
		COALESCE((SELECT json_group_array(json_object('run_id',ordered.agent_run_id,'stage_key',ordered.stage_key,
		 'execution_number',ordered.execution_number,'status',ordered.status,'finished_at',ordered.finished_at,
		 'linked_at',ordered.created_at,'lifecycle_event_id',ordered.lifecycle_event_id)) FROM (
		 SELECT link.agent_run_id,link.stage_key,link.execution_number,run.status,run.finished_at,link.created_at,
		  (SELECT observed.id FROM delivery_events observed
		   WHERE observed.delivery_id=link.delivery_id AND observed.reporter_id=link.reporter_id
		    AND observed.kind IN ('run_normalized','run_lifecycle_observed')
		   ORDER BY observed.delivery_revision DESC LIMIT 1) lifecycle_event_id
		 FROM delivery_agent_run_links link
		 JOIN agent_runs run ON run.id=link.agent_run_id WHERE link.attempt_id=a.id ORDER BY link.agent_run_id) ordered),'[]')
		FROM deliveries d JOIN delivery_attempts a ON a.delivery_id=d.id
		JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=a.delivery_id AND seal.attempt_id=a.id
		JOIN delivery_attempt_stage_policy p ON p.attempt_id=a.id
		WHERE d.issue_id=? ORDER BY a.attempt_number,p.sort_order`, issueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Attempt
	var currentID int64
	for rows.Next() {
		var a Attempt
		var previous, project sql.NullInt64
		var policy Policy
		var linkedRunsJSON string
		if err := rows.Scan(&a.ID, &a.DeliveryID, &a.AttemptNumber, &a.PlanRevision, &previous, &project,
			&a.ReasonCode, &a.ReasonText, &a.CreatedAt, &policy.StageKey, &policy.Applicability, &policy.Weight,
			&policy.PolicyReference, &policy.ReasonCode, &policy.ReasonText, &linkedRunsJSON); err != nil {
			return nil, err
		}
		if a.ID != currentID {
			a.PreviousAttemptID, a.ProjectIDAtStart = nullInt64Ptr(previous), nullInt64Ptr(project)
			var linked []struct {
				RunID            int64   `json:"run_id"`
				StageKey         string  `json:"stage_key"`
				ExecutionNumber  int64   `json:"execution_number"`
				Status           string  `json:"status"`
				FinishedAt       *string `json:"finished_at"`
				LinkedAt         string  `json:"linked_at"`
				LifecycleEventID *int64  `json:"lifecycle_event_id"`
			}
			if err := json.Unmarshal([]byte(linkedRunsJSON), &linked); err != nil {
				return nil, err
			}
			for _, run := range linked {
				a.LinkedRuns = append(a.LinkedRuns, AttemptRunOutcome{RunID: run.RunID, StageKey: run.StageKey,
					ExecutionNumber: run.ExecutionNumber, Status: run.Status, FinishedAt: run.FinishedAt,
					LinkedAt: run.LinkedAt, LifecycleEventID: run.LifecycleEventID})
			}
			out = append(out, a)
			currentID = a.ID
		}
		out[len(out)-1].Policies = append(out[len(out)-1].Policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, attempt := range out {
		if len(attempt.Policies) != len(CanonicalStages) {
			return nil, fmt.Errorf("%w: attempt history has incomplete policy", ErrInvariant)
		}
	}
	return out, nil
}

func (s *Store) RebuildLatest(ctx context.Context, issueID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d, err := loadDeliveryByIssue(ctx, tx, issueID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM delivery_stage_latest WHERE delivery_id=?`, d.ID); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id,stage_key,MAX(execution_number)
		FROM delivery_stage_events WHERE delivery_id=? GROUP BY attempt_id,stage_key`, d.ID)
	if err != nil {
		return err
	}
	var keys []stageExecutionKey
	for rows.Next() {
		var k stageExecutionKey
		if err := rows.Scan(&k.attempt, &k.stage, &k.execution); err != nil {
			rows.Close()
			return err
		}
		keys = append(keys, k)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, k := range keys {
		var startID, authorityID, reporterID, epoch int64
		var updated string
		if err := tx.QueryRowContext(ctx, `SELECT id,server_received_at FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND event_type='execution_started'`, k.attempt, k.stage, k.execution).
			Scan(&startID, &updated); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT authority_epoch FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? ORDER BY authority_epoch DESC,event_sequence DESC LIMIT 1`,
			k.attempt, k.stage, k.execution).Scan(&epoch); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT id,reporter_id,server_received_at FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
			AND event_type IN ('execution_started','handoff') ORDER BY event_sequence DESC LIMIT 1`,
			k.attempt, k.stage, k.execution, epoch).Scan(&authorityID, &reporterID, &updated); err != nil {
			return err
		}
		// A reset is the latest authority-related fact but does not transfer reporter ownership.
		if err := tx.QueryRowContext(ctx, `SELECT id,server_received_at FROM delivery_stage_events
			WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=?
			AND event_type IN ('execution_started','handoff','progress_reset_authorized') ORDER BY event_sequence DESC LIMIT 1`,
			k.attempt, k.stage, k.execution, epoch).Scan(&authorityID, &updated); err != nil {
			return err
		}
		semantic, semanticAt, err := latestStageEventID(ctx, tx, k, epoch, reporterID, []string{"semantic_report", "lifecycle_normalized"})
		if err != nil {
			return err
		}
		heartbeat, heartbeatAt, err := latestStageEventID(ctx, tx, k, epoch, reporterID, []string{"heartbeat"})
		if err != nil {
			return err
		}
		estimate, estimateAt, err := latestEstimateStageEventID(ctx, tx, k, epoch, reporterID)
		if err != nil {
			return err
		}
		updated, err = latestParsedStoredTime(updated, semanticAt, heartbeatAt, estimateAt)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_stage_latest(delivery_id,attempt_id,stage_key,
			execution_number,authority_epoch,current_reporter_id,execution_start_stage_event_id,
			authority_stage_event_id,semantic_stage_event_id,heartbeat_stage_event_id,estimate_stage_event_id,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, d.ID, k.attempt, k.stage, k.execution, epoch, reporterID,
			startID, authorityID, semantic, heartbeat, estimate, updated); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func latestEstimateStageEventID(ctx context.Context, q DBTX, k stageExecutionKey, epoch, reporterID int64) (any, string, error) {
	var id int64
	var receivedAt string
	err := q.QueryRowContext(ctx, `SELECT estimate.id,estimate.server_received_at FROM delivery_stage_events estimate
		WHERE estimate.attempt_id=? AND estimate.stage_key=? AND estimate.execution_number=?
		 AND estimate.authority_epoch=? AND estimate.reporter_id=? AND estimate.event_type='estimate'
		 AND estimate.source_sequence>COALESCE((SELECT reset.reset_source_cutoff FROM delivery_stage_events reset
		   WHERE reset.attempt_id=estimate.attempt_id AND reset.stage_key=estimate.stage_key
		    AND reset.execution_number=estimate.execution_number AND reset.authority_epoch=estimate.authority_epoch
		    AND reset.event_type='progress_reset_authorized' ORDER BY reset.event_sequence DESC LIMIT 1),0)
		ORDER BY estimate.event_sequence DESC LIMIT 1`, k.attempt, k.stage, k.execution, epoch, reporterID).Scan(&id, &receivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return id, receivedAt, nil
}

func latestStageEventID(ctx context.Context, q DBTX, k stageExecutionKey, epoch, reporterID int64, kinds []string) (any, string, error) {
	if len(kinds) == 0 {
		return nil, "", nil
	}
	query := `SELECT id,server_received_at FROM delivery_stage_events WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=? AND reporter_id=? AND event_type=?`
	args := []any{k.attempt, k.stage, k.execution, epoch, reporterID, kinds[0]}
	if len(kinds) == 2 {
		query = `SELECT id,server_received_at FROM delivery_stage_events WHERE attempt_id=? AND stage_key=? AND execution_number=? AND authority_epoch=? AND reporter_id=? AND event_type IN (?,?)`
		args = append(args, kinds[1])
	}
	query += ` ORDER BY event_sequence DESC LIMIT 1`
	var id int64
	var receivedAt string
	if err := q.QueryRowContext(ctx, query, args...).Scan(&id, &receivedAt); errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", err
	}
	return id, receivedAt, nil
}

func latestParsedStoredTime(values ...string) (string, error) {
	var latest time.Time
	for _, raw := range values {
		if raw == "" {
			continue
		}
		parsed, err := parseStoredTime(raw)
		if err != nil {
			return "", fmt.Errorf("%w: malformed latest projection time", ErrInvariant)
		}
		if latest.IsZero() || parsed.After(latest) {
			latest = parsed
		}
	}
	if latest.IsZero() {
		return "", fmt.Errorf("%w: latest projection has no receipt time", ErrInvariant)
	}
	return formatTime(latest), nil
}

func (s *Store) RetainChangesThrough(ctx context.Context, floorID int64) error {
	if floorID < 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, maxID int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(floor_id),0) FROM delivery_change_retention`).Scan(&current); err != nil {
		return err
	}
	if floorID == current {
		// Exact retention retries remain idempotent even when the prior call
		// pruned the former tail and MAX(delivery_change_log.id) is now zero.
		return nil
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM delivery_change_log`).Scan(&maxID); err != nil {
		return err
	}
	if floorID < current || floorID > maxID {
		return fmt.Errorf("%w: invalid retention floor", ErrConflict)
	}
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_change_retention(floor_id,advanced_at) VALUES(?,?)`, floorID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM delivery_change_log WHERE id<=?`, floorID); err != nil {
		return err
	}
	return tx.Commit()
}

func SortedSnapshots(in []Snapshot) []Snapshot {
	out := append([]Snapshot(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].IssueID < out[j].IssueID })
	return out
}
