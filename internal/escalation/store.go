// SPDX-License-Identifier: AGPL-3.0-only
package escalation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/questions"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func LoadTx(ctx context.Context, tx pgx.Tx, ticket string) (*State, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT state FROM work_escalations WHERE ticket_node_id=$1`, ticket).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	err = json.Unmarshal(raw, &s)
	return &s, err
}
func save(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, project string, s State) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO work_escalations(tenant_id,ticket_node_id,project_id,state) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,ticket_node_id) DO UPDATE SET project_id=EXCLUDED.project_id,state=EXCLUDED.state,updated_at=clock_timestamp()`, p.TenantID, ticket, project, raw)
	return err
}
func route(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket string, s State) (modelregistry.WorkResolution, error) {
	var raw []byte
	var project string
	if err := tx.QueryRow(ctx, `SELECT fields,project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, ticket).Scan(&raw, &project); err != nil {
		return modelregistry.WorkResolution{}, err
	}
	fields := modelprefs.PlacementFields(raw)
	role := fields.RouteRole
	if role == "" {
		role = "build"
	}
	excluded, err := modelregistry.EscalationExclusionsTx(ctx, tx, ticket, s.OriginalProfile, s.EpisodeID, s.UsedProfiles)
	if err != nil {
		return modelregistry.WorkResolution{}, err
	}
	pick, err := modelregistry.ResolveEscalation(ctx, tx, p, modelregistry.WorkQuery{Role: role, Area: fields.Area, ProjectID: project, Complexity: fields.Complexity, ComplexitySource: fields.ComplexitySource, TicketResidency: fields.Residency}, excluded, time.Now().UTC())
	pick.Trace.Role, pick.Trace.ProjectID, pick.Trace.TicketRequirement = pick.Role, project, modelprefs.NormalizeResidency(fields.Residency)
	return pick, err
}

// ObserveTx is called exactly once for each newly inserted outcome, under
// tenant -> pairing -> tree locks and outcome.write authorization. It keeps
// state and events in that same transaction. Idempotent outcome replays skip it.
func ObserveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, project, outcome, kind string, payload json.RawMessage) error {
	if kind != "fix_round" && kind != "review_verdict" && kind != "ci_result" {
		return nil
	}
	if err := authz.RequireTx(ctx, tx, p, "outcome.write", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	s, err := LoadTx(ctx, tx, ticket)
	if err != nil {
		return err
	}
	if s == nil || s.Status == "resolved" {
		var id string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			return err
		}
		s = &State{EpisodeID: id, Status: "observing", MaxAttempts: MaxAttempts, MaxCost: MaxCostMicros}
	}
	if s.OriginalProfile == "" {
		episode := ""
		if s.Attempts > 0 {
			episode = s.EpisodeID
		}
		s.OriginalProfile, err = modelregistry.EscalationOriginalTx(ctx, tx, ticket, episode)
		if err != nil {
			return err
		}
	}
	before := s.Public()
	s.Revision++
	s.LastOutcomeID = outcome
	applyKind := kind
	if kind == "review_verdict" {
		var signal Signal
		if err := json.Unmarshal(payload, &signal); err != nil {
			return err
		}
		if signal.Verdict == "ok" {
			verified := false
			if signal.ReviewID != "" {
				verified, err = reviewgate.VerifiedLatestTx(ctx, tx, signal.ReviewID, ticket)
				if err != nil {
					return err
				}
			}
			if !verified {
				applyKind = ""
			}
		}
	}
	if err := s.apply(applyKind, payload); err != nil {
		return err
	}
	if s.Status == "stuck" {
		if s.Attempts >= MaxAttempts || s.HeldCost >= MaxCostMicros {
			s.Status = "awaiting_decision"
			s.Reason = "escalation_budget_exhausted"
			s.PlannedProfile = ""
		} else {
			pick, err := route(ctx, tx, p, ticket, *s)
			if err != nil {
				return err
			}
			s.RouteRole = pick.Role
			s.RouteArea = pick.Trace.Kind
			s.PrefsRevision = pick.Trace.PrefsRevision
			s.RouteBlock = pick.Trace.Blocked
			if pick.Profile == nil {
				s.PlannedProfile = ""
				if pick.Trace.Blocked == "admission_wait" {
					s.WaitReason = "admission_wait"
				} else {
					s.Status = "awaiting_decision"
					s.Reason = "no_allowed_route"
					s.WaitReason = ""
				}
			} else {
				s.PlannedProfile = pick.Profile.ID
				s.WaitReason = ""
			}
		}
	}
	if err := decisionTx(ctx, tx, p, ticket, project, s); err != nil {
		return err
	}

	if err := save(ctx, tx, p, ticket, project, *s); err != nil {
		return err
	}
	if s.Status == "observing" {
		return nil
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "work.escalation_changed", NodeID: &ticket, Before: before, After: s.Public()})
	return err
}

// decisionTx keeps one durable question identity per episode, even across
// changed callers or repeated failures. Persist FK/resource fences first.
func decisionTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, project string, s *State) error {
	// Establish the escalation row lock and all foreign-key fences before the
	// question service can append its event. Later saves update this same row.
	if err := save(ctx, tx, p, ticket, project, *s); err != nil {
		return err
	}
	// Question authority is a separate grant. Lack of it preserves the stuck
	// evidence and closes retries, instead of rolling back the reported outcome.
	if s.Status == "awaiting_decision" && s.QuestionID == "" {
		canAsk := true
		for _, perm := range []string{"questions.ask", "questions.read"} {
			err := authz.RequireTx(ctx, tx, p, perm, authz.Scope{ProjectID: project})
			if errors.Is(err, authz.ErrForbidden) {
				canAsk = false
				break
			}
			if err != nil {
				return err
			}
		}
		if canAsk {
			in := questions.Input{RequestID: s.EpisodeID, TicketID: ticket, Question: "How should this stuck work continue?", Context: fmt.Sprintf("Bounded model escalation stopped: %s. %d automatic attempts; %d cost micros held. Work item %s.", s.Reason, s.Attempts, s.HeldCost, ticket), Options: []questions.Option{{ID: "park", Title: "Keep parked", Answer: "Keep this work parked until its scope or allowed route is revised."}, {ID: "revise", Title: "Revise scope or policy", Answer: "A person will revise the work scope or its allowed execution policy before another attempt."}}, Recommend: "park", Why: "The automatic attempt or route budget is exhausted; further execution needs a separate authorized lifecycle decision.", Meanwhile: "parked", MeanwhileText: "Automatic retries are parked; current processes retain the existing lifecycle and exit checks.", BlockedNodeIDs: []string{ticket}, SuggestedOutcome: "once"}
			q, _, err := questions.New(nil).AskTx(ctx, tx, p, project, in)
			if err != nil {
				return err
			}
			s.QuestionID = q.ID
			s.QuestionPending = false
		} else {
			s.QuestionPending = true
		}
	}
	return nil
}

// ReserveTx is the AEON-601 retry adapter. Call in the same transaction as
// creating the next managed build, before event append. The caller must hold
// tenant/pairing/tree fences and still run the usual live launch admission.
// The entire order's finite cost ceiling is charged conservatively to this
// episode, so uncertainty never creates free attempts or releases holds.
// No review result is opened or replaced here. A nonnil rejection with nil
// error must be committed and returned as the HTTP failure: it records budget
// exhaustion and the one human question without creating another run.
func ReserveTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, project, previous, profile string, cost *int64) (*workorders.Error, error) {
	_, rejection, err := ReservePlacementTx(ctx, tx, p, ticket, project, previous, profile, cost)
	return rejection, err
}

// ReservePlacementTx returns the exact admitted route before charging it.
// Freeze this placement with the new run; resolving again would select the
// next candidate because the admitted profile is now part of episode history.
func ReservePlacementTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticket, project, previous, profile string, cost *int64) (*modelregistry.WorkPlacement, *workorders.Error, error) {
	var currentProject string
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, ticket).Scan(&currentProject); err != nil {
		return nil, nil, err
	}
	if currentProject != project {
		return nil, nil, workorders.Fail(409, "work project changed")
	}
	s, err := LoadTx(ctx, tx, ticket)
	if err != nil {
		return nil, nil, err
	}
	if s == nil || s.Status == "observing" || s.Status == "resolved" {
		return nil, nil, nil
	}
	if s.OriginalProfile == "" {
		episode := ""
		if s.Attempts > 0 {
			episode = s.EpisodeID
		}
		s.OriginalProfile, err = modelregistry.EscalationOriginalTx(ctx, tx, ticket, episode)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := authz.RequireTx(ctx, tx, p, "run.create", authz.Scope{ProjectID: project}); err != nil {
		return nil, nil, err
	}
	if s.Status != "stuck" {
		return nil, nil, workorders.Fail(409, "stuck work awaits a Decision Desk decision")
	}

	var eligible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id
 WHERE r.id=$1 AND (n.parent_id=$2 OR r.queue_node_id=$2) AND r.status IN ('completed','failed','cancelled') AND r.ended_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM harness_sessions h WHERE h.run_id=r.id AND aeon_work_session_stopped(h.stopped_at,h.stop_reason))
 AND NOT EXISTS(SELECT 1 FROM harness_sessions h WHERE h.run_id=r.id AND NOT aeon_work_session_stopped(h.stopped_at,h.stop_reason)))
 AND NOT EXISTS(SELECT 1 FROM harness_sessions h WHERE h.ticket_node_id=$2 AND NOT aeon_work_session_stopped(h.stopped_at,h.stop_reason))
 AND NOT EXISTS(SELECT 1 FROM agent_runs r JOIN nodes n ON n.id=r.work_order_id WHERE (n.parent_id=$2 OR r.queue_node_id=$2) AND r.status IN ('queued','starting','running','waiting'))`, previous, ticket).Scan(&eligible)
	if err != nil {
		return nil, nil, err
	}
	if !eligible {
		return nil, nil, workorders.Fail(409, "previous writer exit must be proven before escalation")
	}
	if cost == nil || *cost <= 0 || *cost > MaxCostMicros-s.HeldCost || s.Attempts >= MaxAttempts {
		before := s.Public()
		s.Status = "awaiting_decision"
		s.Reason = "escalation_budget_unavailable"
		s.PlannedProfile = ""
		s.Revision++
		if err := decisionTx(ctx, tx, p, ticket, project, s); err != nil {
			return nil, nil, err
		}
		if err := save(ctx, tx, p, ticket, project, *s); err != nil {
			return nil, nil, err
		}
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "work.escalation_changed", NodeID: &ticket, Before: before, After: s.Public()}); err != nil {
			return nil, nil, err
		}
		return nil, &workorders.Error{Status: 409, Message: "finite escalation cost and attempt budget required; work awaits a Decision Desk decision"}, nil
	}
	pick, err := route(ctx, tx, p, ticket, *s)
	if err != nil {
		return nil, nil, err
	}
	if pick.Profile == nil || pick.Profile.ID != profile {
		return nil, nil, workorders.Fail(409, "retry must use the current allowed escalation route")
	}
	s.Attempts++
	s.HeldCost += *cost
	s.UsedProfiles = append(s.UsedProfiles, profile)
	s.PlannedProfile = ""
	s.Revision++
	id := pick.Profile.ID
	placement := &modelregistry.WorkPlacement{PreferenceTrace: pick.Trace, PlannedProfileID: &id}
	placement.Role, placement.ProjectID = pick.Role, project
	return placement, nil, save(ctx, tx, p, ticket, project, *s)
}

// CheckLaunchTx closes every managed dispatch path on the live episode. The
// caller holds the tenant/pairing/tree fences and final mutation authorization.
// Queue entries have no finite retry reservation and remain parked; retries
// must carry the same episode, lineage and charged profile through claim.
func CheckLaunchTx(ctx context.Context, tx pgx.Tx, ticket, profile, previous string, admission json.RawMessage) error {
	state, err := LoadTx(ctx, tx, ticket)
	if err != nil {
		return err
	}
	if state == nil || state.Status == "observing" || state.Status == "resolved" {
		return nil
	}
	if state.Status != "stuck" {
		return workorders.Fail(409, "stuck work awaits a Decision Desk decision")
	}
	var frozen State
	if len(admission) > 0 {
		if err := json.Unmarshal(admission, &frozen); err != nil {
			return workorders.Fail(409, "invalid escalation admission")
		}
	}
	sameEpisode := frozen.EpisodeID == state.EpisodeID
	withinAttempts := frozen.Attempts >= 1 && frozen.Attempts <= state.Attempts && state.Attempts <= MaxAttempts
	withinCost := frozen.HeldCost > 0 && frozen.HeldCost <= state.HeldCost && state.HeldCost <= MaxCostMicros
	chargedProfile := slices.Contains(state.UsedProfiles, profile) && slices.Contains(frozen.UsedProfiles, profile)
	if previous == "" || !sameEpisode || !withinAttempts || !withinCost || !chargedProfile {
		return workorders.Fail(409, "stuck work requires a charged bounded retry lineage")
	}
	return nil
}
