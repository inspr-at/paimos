// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
)

// ownedExecution is the current durable execution evidence for one batch: the
// lifecycle intent this authority submitted and, once the daemon reported it,
// the owned harness session that intent produced.
type ownedExecution struct {
	IntentState     string
	IntentReason    string
	IntentRevision  int64
	SessionID       string
	SessionPhase    string
	SessionActivity string
	SessionRevision int64
	CanInterrupt    bool
	CanStop         bool
}

func loadOwnedExecution(ctx context.Context, tx *sql.Tx, projectID int64, intentID string) (ownedExecution, error) {
	out := ownedExecution{}
	if intentID == "" {
		return out, nil
	}
	var result sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT state,reason,revision,result_session_id
		FROM lifecycle_intents WHERE id=? AND project_id=?`, intentID, projectID).
		Scan(&out.IntentState, &out.IntentReason, &out.IntentRevision, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if !result.Valid || result.String == "" {
		return out, nil
	}
	out.SessionID = result.String
	var interrupt, stop int
	err = tx.QueryRowContext(ctx, `SELECT phase,activity_state,revision,advertised_interrupt,advertised_stop
		FROM harness_sessions WHERE id=? AND project_id=?`, out.SessionID, projectID).
		Scan(&out.SessionPhase, &out.SessionActivity, &out.SessionRevision, &interrupt, &stop)
	if errors.Is(err, sql.ErrNoRows) {
		out.SessionID = ""
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.CanInterrupt, out.CanStop = interrupt == 1, stop == 1
	return out, nil
}

// batchState derives the workflow state from durable feeds only: the delivery
// attempt's canonical stages, the lifecycle intent, the owned session, and the
// human control record. A worker finishing never marks the product delivered —
// only every required stage being satisfied does.
func (s *Service) batchState(ctx context.Context, tx *sql.Tx, stored storedBatch) (string, Progress, delivery.Snapshot, error) {
	progress := Progress{Stages: []StageView{}}
	if s.Delivery == nil {
		return BatchBlocked, progress, delivery.Snapshot{}, fmt.Errorf("%w: delivery store required", ErrUnavailable)
	}
	snapshot, err := s.Delivery.SnapshotByIssueTx(ctx, tx, stored.IssueID)
	if err != nil {
		return BatchBlocked, progress, snapshot, err
	}
	required, satisfied, observed, fresh, blocked := 0, 0, false, true, false
	latest := ""
	for _, stage := range snapshot.Stages {
		view := StageView{
			StageKey: stage.StageKey, Applicability: stage.Applicability, Weight: stage.Weight,
			State: stage.SemanticState, Phase: stage.Phase, Activity: stage.Activity,
			NeedsInput: stage.NeedsInput, Performed: stage.Performed, Satisfied: stage.PolicySatisfied,
			Stale: stage.Stale,
		}
		// A stage counts as observed only once something real arrived for it:
		// a semantic report, a heartbeat, an estimate, or a performed execution.
		signalled := stage.LastSemanticAt != nil || stage.LastHeartbeatAt != nil ||
			stage.LatestEstimate != nil || stage.Performed
		view.NeverSignaled = !signalled
		if stage.LastSemanticAt != nil {
			view.LastSignalAt = *stage.LastSemanticAt
		} else if stage.LastHeartbeatAt != nil {
			view.LastSignalAt = *stage.LastHeartbeatAt
		}
		if view.LastSignalAt > latest {
			latest = view.LastSignalAt
		}
		if signalled {
			observed = true
			if stage.Stale {
				fresh = false
			}
		}
		if stage.NeedsInput || len(stage.CurrentBlockers) > 0 {
			blocked = true
		}
		if stage.Applicability == "required" {
			required++
			if stage.PolicySatisfied {
				satisfied++
			}
		}
		progress.Stages = append(progress.Stages, view)
	}
	progress.EvidenceObserved = observed
	progress.EvidenceFresh = observed && fresh
	progress.FreshnessAsOf = latest
	execution, err := loadOwnedExecution(ctx, tx, stored.ProjectID, stored.LifecycleIntentID)
	if err != nil {
		return BatchBlocked, progress, snapshot, err
	}
	progress.IntentState = execution.IntentState
	progress.IntentReason = execution.IntentReason
	progress.SessionID = execution.SessionID
	progress.SessionPhase = execution.SessionPhase
	progress.SessionActivity = execution.SessionActivity

	complete := required > 0 && satisfied == required
	switch {
	case stored.ControlState == ControlCancelled:
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchCancelled)
	case complete:
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchCompleted)
	case stored.ControlState == ControlPaused:
		progress.BlockingReason = stored.ControlReason
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchPaused)
	case snapshot.Failed || snapshot.Cancelled:
		progress.BlockingReason = "delivery_attempt_" + snapshot.State
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
	}
	if stored.ExecutionMode == ModeManual {
		if blocked {
			progress.BlockingReason = "stage_needs_input"
			return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
		}
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchActive)
	}
	switch execution.IntentState {
	case "requested", "claimed":
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchQueued)
	case "executing":
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchActive)
	case "completed":
		switch execution.SessionPhase {
		case "working", "yielded":
			if blocked {
				progress.BlockingReason = "stage_needs_input"
				return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
			}
			return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchActive)
		case "":
			progress.BlockingReason = "owned_session_unreported"
			return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
		default:
			// The worker's process is gone while required stages are still
			// unsatisfied. That is unfinished delivery, not a delivered product.
			progress.BlockingReason = "worker_stopped_before_delivery_evidence"
			return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
		}
	case "failed", "expired", "cancelled":
		progress.BlockingReason = "lifecycle_" + execution.IntentState
		if execution.IntentReason != "" {
			progress.BlockingReason += "_" + execution.IntentReason
		}
		return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
	}
	progress.BlockingReason = "start_intent_missing"
	return s.finishBatchState(ctx, tx, stored, snapshot, progress, BatchBlocked)
}

func (s *Service) finishBatchState(ctx context.Context, tx *sql.Tx, stored storedBatch, snapshot delivery.Snapshot, progress Progress, state string) (string, Progress, delivery.Snapshot, error) {
	if err := s.annotateBridge(ctx, tx, stored, snapshot, &progress, &state); err != nil {
		return BatchBlocked, progress, snapshot, err
	}
	return state, progress, snapshot, nil
}

// forecasts always returns the durable educated guess plus everything the real
// feeds currently support. Each carries its own basis and label, and freshness
// is reported separately in Progress so a stale estimate cannot read as fact.
// When a measured or worker forecast has no ETA of its own, a separately labelled
// educated fallback from durable batch planning is attached; it never pretends to
// be observed and never unlocks gates.
func (s *Service) forecasts(ctx context.Context, tx *sql.Tx, stored storedBatch, status string, progress Progress, snapshot delivery.Snapshot, now time.Time) ([]Forecast, error) {
	out, err := loadForecasts(ctx, tx, stored.ID)
	if err != nil {
		return nil, err
	}
	weightTotal, weightSatisfied := 0, 0
	for _, stage := range progress.Stages {
		if stage.Applicability != "required" {
			continue
		}
		weightTotal += stage.Weight
		if stage.Satisfied {
			weightSatisfied += stage.Weight
		}
	}
	if weightTotal > 0 {
		percent := float64(weightSatisfied) / float64(weightTotal) * 100
		measured := Forecast{
			Subject: "overall", Percent: percent, Kind: ForecastMeasured,
			Basis: "satisfied canonical delivery stage weight", AsOf: now.Format(time.RFC3339Nano),
			Label: forecastLabel(ForecastMeasured), Observed: progress.EvidenceObserved, Fresh: progress.EvidenceFresh,
		}
		out = append(out, measured)
	}
	if worker, ok := workerForecast(snapshot); ok {
		out = append(out, worker)
	}
	return attachEducatedETAFallbacks(out, status, progress, stored.Scope, now), nil
}

func attachEducatedETAFallbacks(forecasts []Forecast, status string, progress Progress, scope Scope, now time.Time) []Forecast {
	var planning *Forecast
	for i := range forecasts {
		if forecasts[i].Kind == ForecastGuess {
			planning = &forecasts[i]
			break
		}
	}
	out := make([]Forecast, len(forecasts))
	copy(out, forecasts)
	for i := range out {
		if out[i].Kind == ForecastGuess {
			if out[i].ETASeconds == nil {
				eta, basis, asOf := durablePlanningETA(planning, scope)
				out[i].ETASeconds = &eta
				if out[i].Basis == "" {
					out[i].Basis = basis
				}
				if out[i].AsOf == "" {
					if asOf != "" {
						out[i].AsOf = asOf
					} else {
						out[i].AsOf = now.Format(time.RFC3339Nano)
					}
				}
			}
			continue
		}
		if out[i].ETASeconds != nil {
			continue
		}
		fallback := educatedETAFallback(planning, status, progress, scope, out[i].Percent, out[i].Kind, now)
		out[i].EducatedETASeconds = fallback.eta
		out[i].EducatedETABasis = fallback.basis
		out[i].EducatedETAAsOf = fallback.asOf
		out[i].EducatedETALabel = forecastLabel(ForecastGuess)
	}
	return out
}

type educatedETAFallbackView struct {
	eta   *int64
	basis string
	asOf  string
}

func educatedETAFallback(planning *Forecast, status string, progress Progress, scope Scope, percent float64, kind string, now time.Time) educatedETAFallbackView {
	// Only a completed batch has no remaining work. A worker stage at 100% or a
	// measured weight extrapolation must not claim every required stage satisfied.
	if status == BatchCompleted {
		zero := int64(0)
		asOf := progress.FreshnessAsOf
		if asOf == "" {
			asOf = now.Format(time.RFC3339Nano)
		}
		return educatedETAFallbackView{
			eta:   &zero,
			basis: "all required delivery stages satisfied; no remaining work",
			asOf:  asOf,
		}
	}
	planningETA, planningBasis, planningAsOf := durablePlanningETA(planning, scope)
	remaining := scaleRemainingETA(planningETA, percent)
	if kind == ForecastWorker && percent >= 100 && status != BatchCompleted {
		// A worker finishing one stage is not batch completion.
		remaining = planningETA
		planningBasis += "; worker stage complete — remaining delivery stages may still be open"
	}
	basis := planningBasis
	switch {
	case progress.EvidenceObserved && !progress.EvidenceFresh:
		basis += "; progress evidence stale — educated ETA assumes batch planning unchanged"
	case !progress.EvidenceObserved:
		basis += "; no progress observed yet — educated ETA from batch planning only"
	}
	return educatedETAFallbackView{eta: &remaining, basis: basis, asOf: planningAsOf}
}

func durablePlanningETA(planning *Forecast, scope Scope) (eta int64, basis, asOf string) {
	if planning != nil && planning.ETASeconds != nil {
		return *planning.ETASeconds, planning.Basis, planning.AsOf
	}
	reqs := len(scope.RequirementRefs)
	if reqs < 1 {
		reqs = 1
	}
	return int64(reqs * 1800), fmt.Sprintf("%d selected requirements at batch confirmation", reqs), ""
}

func scaleRemainingETA(total int64, percent float64) int64 {
	if percent <= 0 {
		return total
	}
	if percent >= 100 {
		return 0
	}
	remaining := float64(total) * (100 - percent) / 100
	if remaining < 0 {
		return 0
	}
	return int64(remaining + 0.5)
}

// workerForecast reports the newest stage estimate the delivery read model
// holds for this attempt. It is the worker's own claim: labelled as such, never
// promoted to measured, and never used to satisfy a stage.
func workerForecast(snapshot delivery.Snapshot) (Forecast, bool) {
	var newest *delivery.StageSnapshot
	for i := range snapshot.Stages {
		stage := &snapshot.Stages[i]
		if stage.LatestEstimate == nil || stage.LatestEstimate.Progress == nil {
			continue
		}
		if newest == nil || stage.LatestEstimate.ServerReceivedAt > newest.LatestEstimate.ServerReceivedAt {
			newest = stage
		}
	}
	if newest == nil {
		return Forecast{}, false
	}
	estimate := newest.LatestEstimate
	basis := "worker report"
	if estimate.Source != "" {
		basis += " from " + estimate.Source
	}
	if estimate.Basis != "" {
		basis += ": " + estimate.Basis
	}
	forecast := Forecast{
		Subject: newest.StageKey, Percent: *estimate.Progress * 100, Kind: ForecastWorker,
		Basis: basis, AsOf: estimate.ServerReceivedAt, Label: forecastLabel(ForecastWorker),
		Observed: true, Fresh: !newest.EstimateStale,
	}
	if estimate.ETASeconds != nil {
		eta := *estimate.ETASeconds
		forecast.ETASeconds = &eta
	}
	return forecast, true
}
