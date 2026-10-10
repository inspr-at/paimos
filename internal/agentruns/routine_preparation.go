// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/recurrences"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

const routinePreparationCandidates = 32
const routinePreparationFieldBytes = 64 * 1024

type RoutinePreparationInput struct {
	RecurrenceID       string
	DefinitionRevision int64
	TargetAgentID      string
	CandidateLimit     int
}

// RoutinePreparedWork is advice, never an assignment or action approval. The
// executor must recheck it inside the final write, and use the normal action
// broker and shared queue admission. Accepted criteria are never rewritten.
type RoutinePreparedWork struct {
	RoutineRunID     string                     `json:"routine_run_id"`
	QueueRunID       string                     `json:"queue_run_id"`
	NodeID           string                     `json:"node_id"`
	ExpectedRevision time.Time                  `json:"expected_revision"`
	CriteriaDigest   string                     `json:"criteria_digest"`
	Readiness        workqueue.Readiness        `json:"readiness"`
	Estimate         *RoutineEstimateSuggestion `json:"estimate,omitempty"`
}

// An estimate suggestion uses the existing typed queue estimate action. It
// carries the accepted criteria identity and cannot authorize a criteria edit.
type RoutineEstimateSuggestion struct {
	NodeID           string    `json:"node_id"`
	ExpectedRevision time.Time `json:"expected_revision"`
	CriteriaDigest   string    `json:"criteria_digest"`
	EstimateHours    float64   `json:"estimate_hours"`
}

type RoutineCriteriaDraft struct {
	NodeID           string    `json:"node_id"`
	ExpectedRevision time.Time `json:"expected_revision"`
	CriteriaDigest   string    `json:"criteria_digest"`
	Criteria         []string  `json:"acceptance_criteria"`
	NeedsPerson      bool      `json:"needs_person"`
}

type RoutinePreparation struct {
	Candidate  *RoutinePreparedWork `json:"candidate"`
	WaitReason string               `json:"wait_reason"`
	Partial    bool                 `json:"partial"`
	Scanned    int                  `json:"scanned"`
}

// PrepareRoutineWorkTx performs one local-analysis step with a fixed read/time
// budget: <=32 candidates, <=64 KiB per record, <=64 blocker edges and <=5s.
// It makes no network/model calls, spends no provider budget, queues no backlog
// and creates no assignment. S13 owns subsequent budget/action admission.
// Consent is checked before scanning; without S04's qualified project flag it
// remains inert. Enter before resource/event locks; callers own transaction
// rollback on errors and must never consume Partial as a complete scan.
func PrepareRoutineWorkTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RoutinePreparationInput, runtime recurrences.ExecutionRuntimeReader) (out RoutinePreparation, err error) {
	if !workorders.UUID(in.RecurrenceID) || in.DefinitionRevision < 1 || in.TargetAgentID != "" && !workorders.UUID(in.TargetAgentID) || in.CandidateLimit < 0 || in.CandidateLimit > routinePreparationCandidates {
		return out, workorders.Fail(400, "invalid_preparation_input")
	}
	if in.CandidateLimit == 0 {
		in.CandidateLimit = routinePreparationCandidates
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			out.Candidate, out.Partial, out.WaitReason = nil, true, "preparation_time_limit"
			if err == nil {
				err = ctx.Err()
			}
		}
	}()
	// queueLock enters the same tenant/tree fence as human pickup. The consent
	// seam re-enters it before locking the saved definition, never after events.
	if err = queueLock(ctx, tx); err != nil {
		return out, err
	}
	identity, err := recurrences.RequireExecutionConsentTx(ctx, tx, p.TenantID, in.RecurrenceID, in.DefinitionRevision, runtime)
	if err != nil {
		var refusal *workorders.Error
		if errors.As(err, &refusal) && refusal.Status == 409 {
			out.WaitReason = refusal.Message
			return out, nil
		}
		return out, err
	}
	var project string
	if err = tx.QueryRow(ctx, `SELECT project_id::text FROM recurrences WHERE id=$1`, in.RecurrenceID).Scan(&project); err != nil {
		return out, err
	}
	if err = queuePermission(ctx, tx, p, &project, true); err != nil {
		return out, err
	}
	if err = authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return out, err
	}
	// Rank the existing queue before selecting routine lineage. No priority,
	// targeted Start now, manual order or release membership is changed here.
	rows, err := tx.Query(ctx, workqueue.CTE+`SELECT rr.id::text,q.id::text,q.queue_node_id::text,
 n.updated_at,octet_length(n.title)+octet_length(n.body)+octet_length(n.fields::text)
 FROM queue_ordered q JOIN routine_runs rr ON rr.tenant_id=q.tenant_id AND rr.agent_run_id=q.id AND rr.work_node_id=q.queue_node_id
 JOIN nodes n ON n.tenant_id=q.tenant_id AND n.id=q.queue_node_id
 WHERE rr.recurrence_id=$1 AND rr.definition_revision=$2 AND rr.execute_consent
 AND rr.consent_revision=$2 AND rr.execution_principal_id=$3 AND rr.output_project_id=n.project_id
 AND n.project_id=$4 AND n.parent_id=rr.output_parent_id AND n.fields->>'recurrence_id'=$1::text
 AND rr.state='pending' AND q.queue_routed_at IS NULL AND (q.queue_target_agent_id IS NULL OR $5::uuid IS NULL OR q.queue_target_agent_id=$5)
 ORDER BY (q.queue_target_agent_id IS NOT NULL) DESC,q.queue_position,q.id LIMIT $6`, in.RecurrenceID, in.DefinitionRevision, identity, project, optional(in.TargetAgentID), in.CandidateLimit+1)
	if err != nil {
		return out, err
	}
	type candidate struct {
		work  RoutinePreparedWork
		bytes int
	}
	candidates := []candidate{}
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.work.RoutineRunID, &c.work.QueueRunID, &c.work.NodeID, &c.work.ExpectedRevision, &c.bytes); err != nil {
			rows.Close()
			return out, err
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(candidates) > in.CandidateLimit {
		out.Partial, out.WaitReason = true, "candidate_scan_partial"
		return out, nil
	}
	out.WaitReason = "no_authorized_queued_work"
	for _, c := range candidates {
		out.Scanned++
		if c.bytes > routinePreparationFieldBytes {
			out.Partial, out.WaitReason = true, "preparation_fields_partial"
			return out, nil
		}
		t, err := queueLoadTicket(ctx, tx, c.work.NodeID, true)
		if err != nil {
			return out, err
		}
		// Existing live assignment, person pickup or uncertain exit holds this
		// leaf. A lost heartbeat never supplies permission for a second writer.
		if err := requireWriterFree(ctx, tx, t.ID, c.work.QueueRunID); err != nil {
			var refusal *workorders.Error
			if errors.As(err, &refusal) && refusal.Status == 409 {
				out.WaitReason = "writer_unconfirmed"
				continue
			}
			return out, err
		}
		wait, err := workqueue.BlockingTx(ctx, tx, t.ID)
		if err != nil {
			return out, err
		}
		if wait != "" {
			out.WaitReason = wait
			if wait == "blocker_scan_partial" {
				out.Partial = true
				return out, nil
			}
			continue
		}
		ready := readiness(t)
		if workqueue.State(t.State) == "blocked" || !ready.Queueable || t.Stale {
			out.WaitReason = "work_not_eligible"
			continue
		}
		c.work.Readiness = ready
		criteria, err := json.Marshal(t.Fields["acceptance_criteria"])
		if err != nil {
			return out, err
		}
		sum := sha256.Sum256(criteria)
		c.work.CriteriaDigest = hex.EncodeToString(sum[:])
		if slices.Contains(ready.Missing, "criteria") {
			out.Candidate, out.WaitReason = &c.work, "criteria_wait"
			return out, nil
		}
		if slices.Contains(ready.Missing, "estimate") {
			c.work.Estimate = &RoutineEstimateSuggestion{NodeID: t.ID, ExpectedRevision: c.work.ExpectedRevision, CriteriaDigest: c.work.CriteriaDigest, EstimateHours: ready.SuggestedEstimateHours}
			out.WaitReason = "estimate_action_required"
		} else {
			out.WaitReason = ""
		}
		out.Candidate = &c.work
		return out, nil
	}
	return out, nil
}

// DraftRoutineCriteria copies a bounded proposal into a person decision bound
// to the original leaf/revision/criteria. Rejection and replay perform no write;
// acceptance must use the existing person action, then prepare the new revision.
func DraftRoutineCriteria(work RoutinePreparedWork, criteria []string) (RoutineCriteriaDraft, error) {
	out := RoutineCriteriaDraft{}
	if !workorders.UUID(work.NodeID) || work.ExpectedRevision.IsZero() || !digest64(work.CriteriaDigest) || len(criteria) == 0 || len(criteria) > 64 {
		return out, workorders.Fail(400, "invalid_criteria_draft")
	}
	bytes := 0
	for _, criterion := range criteria {
		bytes += len(criterion)
		if strings.TrimSpace(criterion) == "" || len(criterion) > 4096 || bytes > 16384 {
			return out, workorders.Fail(400, "criteria_draft_limit")
		}
	}
	return RoutineCriteriaDraft{NodeID: work.NodeID, ExpectedRevision: work.ExpectedRevision, CriteriaDigest: work.CriteriaDigest, Criteria: slices.Clone(criteria), NeedsPerson: true}, nil
}

// RequireRoutinePreparationTx is the final-write guard for an executor/broker.
// Re-enter before resource/event locks and keep the transaction fenced through
// the typed mutation. This grants neither launch nor person criteria acceptance.
func RequireRoutinePreparationTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, in RoutinePreparationInput, expected RoutinePreparedWork, runtime recurrences.ExecutionRuntimeReader) error {
	current, err := PrepareRoutineWorkTx(ctx, tx, p, in, runtime)
	if err != nil {
		return err
	}
	if current.Partial || current.Candidate == nil {
		return workorders.Fail(409, current.WaitReason)
	}
	actual := current.Candidate
	if actual.NodeID != expected.NodeID || actual.QueueRunID != expected.QueueRunID || actual.RoutineRunID != expected.RoutineRunID || !actual.ExpectedRevision.Equal(expected.ExpectedRevision) || actual.CriteriaDigest != expected.CriteriaDigest {
		return workorders.Fail(409, "stale_preparation")
	}
	if current.WaitReason == "criteria_wait" {
		return workorders.Fail(409, "criteria_wait")
	}
	return nil
}
