// SPDX-License-Identifier: AGPL-3.0-only

package agentruns

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/accountuse"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/escalation"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Run struct {
	ReservationsSettled       *bool   `json:"reservations_settled,omitempty"`
	RecoveryBrief             string  `json:"recovery_brief,omitempty"`
	RecoveryTier              string  `json:"recovery_service_tier,omitempty"`
	RecoveryLabel             *string `json:"recovery_display_label,omitempty"`
	recoveryHandover          json.RawMessage
	Trace                     json.RawMessage             `json:"trace,omitempty"`
	QueueNodeID               *string                     `json:"queue_node_id,omitempty"`
	QueueTargetAgentID        *string                     `json:"queue_target_agent_id,omitempty"`
	QueueRoutedAt             *time.Time                  `json:"queue_routed_at,omitempty"`
	ReadOnlyReview            bool                        `json:"read_only_review,omitempty"`
	VerificationError         string                      `json:"verification_error,omitempty"`
	VerificationReason        string                      `json:"verification_reason,omitempty"`
	CapacityHandoff           bool                        `json:"capacity_handoff,omitempty"`
	CapacityOverride          string                      `json:"capacity_override"`
	Wait                      *agentaccounts.CapacityWait `json:"wait,omitempty"`
	Purpose                   string                      `json:"purpose"`
	VerificationTask          string                      `json:"verification_task,omitempty"`
	MaxDurationSeconds        int                         `json:"max_duration_seconds,omitempty"`
	VerificationPolicy        string                      `json:"verification_policy,omitempty"`
	RepositoryMutationAllowed bool                        `json:"repository_mutation_allowed"`
	RowVersion                int64                       `json:"row_version,omitempty"`
	ID                        string                      `json:"id"`
	OrderID                   string                      `json:"work_order_id"`
	AgentID                   string                      `json:"agent_principal_id"`
	ProfileID                 *string                     `json:"model_profile_id"`
	AccountID                 *string                     `json:"account_id"`
	RequestedAccountID        *string                     `json:"requested_account_id"`
	Outcome                   *string                     `json:"outcome"`
	DurationMS                *int64                      `json:"duration_ms"`
	WaitingMS                 *int64                      `json:"waiting_ms"`
	ActiveMS                  *int64                      `json:"active_ms"`
	OutcomeDetail             *string                     `json:"outcome_detail"`
	RetryOfRunID              *string                     `json:"retry_of_run_id"`
	Status                    string                      `json:"status"`
	RequestedModel            *string                     `json:"requested_model"`
	EffectiveModel            *string                     `json:"effective_model"`
	ModelEvidence             string                      `json:"model_evidence"`
	InputTokens               int64                       `json:"input_tokens"`
	OutputTokens              int64                       `json:"output_tokens"`
	CachedInputTokens         int64                       `json:"cached_input_tokens"`
	ReasoningTokens           int64                       `json:"reasoning_tokens"`
	Cost                      int64                       `json:"cost_micros"`
	StartedAt                 *time.Time                  `json:"started_at"`
	EndedAt                   *time.Time                  `json:"ended_at"`
	CreatedAt                 time.Time                   `json:"created_at"`
	DaemonID                  *string                     `json:"-"`
	Generation                *string                     `json:"-"`
}

// UsageRecorder lets the account module settle its own allowance projections
// atomically with accepted telemetry. It runs once for each new sequence, never
// for replay, inside the existing tenant transaction. It must not commit or
// start a second transaction or append events itself. It adds changes to the
// caller-owned batch. The coordinator supplies it when mounting both
// modules. A nil recorder performs no account settlement; production wiring
// must supply the account module's recorder when account allowances are enabled.
type UsageRecorder func(context.Context, pgx.Tx, tenant.Principal, Run, Telemetry, *[]events.Change) error
type CompletionReviewer func(context.Context, pgx.Tx, tenant.Principal, string, reviewgate.CommitRange, *[]events.Change) error
type CompletionPreparer func(context.Context, pgx.Tx, tenant.Principal, string, reviewgate.CommitRange) (bool, error)
type module struct {
	queueTimeout  func(context.Context, time.Duration) (context.Context, context.CancelFunc)
	leadAdmission harness.LeadAdmission
	reviews       CompletionReviewer
	prepare       CompletionPreparer
	pool          *pgxpool.Pool
	usage         UsageRecorder
}

// New returns the /api/runs and /api/work-orders/{id}/runs module. The optional
// recorder integrates account settlement without importing another worker's
// package. Mount behind auth.Middleware; see doc.go for scopes and fencing.
func New(pool *pgxpool.Pool, recorder ...UsageRecorder) httpapi.Module {
	m := &module{pool: pool}
	if len(recorder) > 0 {
		m.usage = recorder[0]
	}
	return m
}

// NewWithLeadAdmission connects the qualified existing start checks to explicit
// project leads. The default constructor waits for configured project dispatch.
func NewWithLeadAdmission(pool *pgxpool.Pool, admission harness.LeadAdmission, recorder ...UsageRecorder) httpapi.Module {
	m := New(pool, recorder...).(*module)
	m.leadAdmission = admission
	return m
}

// NewWithReviews atomically requests a review when a builder reports its range.
func NewWithReviews(pool *pgxpool.Pool, usage UsageRecorder, reviews CompletionReviewer, prepare CompletionPreparer) httpapi.Module {
	return &module{pool: pool, usage: usage, reviews: reviews, prepare: prepare}
}
func (m *module) Mount(mux *http.ServeMux) {
	m.mountQueue(mux)
	for _, route := range []struct {
		pattern, scope string
		agent          bool
		status         int
		fn             func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"POST /api/work-orders/{workOrderId}/runs", "run.create", false, 201, m.create},
		{"GET /api/runs", "run.read", false, 200, m.list},
		{"GET /api/runs/queued", "run.read", true, 200, m.queued},
		{"GET /api/runs/{runId}", "run.read", false, 200, m.get},
		{"GET /api/runs/{runId}/handoff", "run.read", false, 200, m.getHandoff},
		{"POST /api/runs/{runId}/capacity-override", "run.create", false, 200, m.runNow},
		{"POST /api/runs/{runId}/cancel", "run.create", false, 200, m.cancel},
		{"POST /api/runs/{runId}/claim", "run.claim", true, 200, m.claim},
	} {
		mux.HandleFunc(route.pattern, workorders.Endpoint(m.pool, route.scope, route.agent, route.status, func(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
			var lockErr error
			if r.Method == http.MethodGet && route.pattern != "GET /api/runs/queued" {
				lockErr = agentpairing.LockRead(r.Context(), tx)
			} else {
				lockErr = agentpairing.Lock(r.Context(), tx)
			}
			if lockErr != nil {
				return nil, lockErr
			}
			if r.Method != http.MethodGet {
				if err := accountuse.LockShared(r.Context(), tx); err != nil {
					return nil, err
				}
			}
			if route.pattern == "POST /api/runs/{runId}/claim" {
				if err := queueLock(r.Context(), tx); err != nil {
					return nil, err
				}
			}
			v, err := route.fn(r, tx, p)
			return v, pairingRunError(err)
		}))
	}
	mux.HandleFunc("POST /api/runs/{runId}/telemetry", workorders.EndpointPrepared(m.pool, "run.telemetry", true, 200,
		func(r *http.Request, p tenant.Principal) (*http.Request, error) {
			prepared, err := m.prepareTelemetry(r, p)
			return prepared, pairingRunError(err)
		},
		func(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
			out, err := m.telemetry(r, tx, p)
			return out, pairingRunError(err)
		}))
}

// Keep lifecycle refusals identical across prepared and ordinary run entries.
func pairingRunError(err error) error {
	var pe *agentpairing.Error
	if errors.As(err, &pe) {
		return workorders.Fail(pe.Status, pe.Code)
	}
	return err
}

// row_version is the run's own revision (AEON-449): a trigger bumps it inside every
// statement that changes the row, so of two copies the larger is the newer one.
const columns = `id::text,work_order_id::text,agent_principal_id::text,model_profile_id::text,account_id::text,status,
 requested_model,effective_model,model_evidence,input_tokens,output_tokens,cached_input_tokens,reasoning_tokens,cost_micros,started_at,ended_at,created_at,daemon_id,daemon_generation,requested_account_id::text,purpose,active_ms,outcome_detail,retry_of_run_id::text,capacity_override,(retry_account_id IS NOT NULL),verification_unavailable_reason,
 EXISTS(SELECT 1 FROM work_orders review_order WHERE review_order.node_id=agent_runs.work_order_id AND review_order.kind='review'),row_version,queue_node_id::text,queue_target_agent_id::text,queue_routed_at,trace,waiting_ms,
 coalesce((SELECT q.handover FROM harness_recoveries q WHERE q.next_run_id=agent_runs.id),'null'::jsonb),
 coalesce((SELECT q.service_tier FROM harness_recoveries q WHERE q.next_run_id=agent_runs.id),''),
 (SELECT q.display_label FROM harness_recoveries q WHERE q.next_run_id=agent_runs.id)`

func scan(row pgx.Row) (Run, error) {
	var v Run
	err := row.Scan(&v.ID, &v.OrderID, &v.AgentID, &v.ProfileID, &v.AccountID, &v.Status, &v.RequestedModel, &v.EffectiveModel, &v.ModelEvidence, &v.InputTokens, &v.OutputTokens, &v.CachedInputTokens, &v.ReasoningTokens, &v.Cost, &v.StartedAt, &v.EndedAt, &v.CreatedAt, &v.DaemonID, &v.Generation, &v.RequestedAccountID, &v.Purpose, &v.ActiveMS, &v.OutcomeDetail, &v.RetryOfRunID, &v.CapacityOverride, &v.CapacityHandoff, &v.VerificationReason, &v.ReadOnlyReview, &v.RowVersion, &v.QueueNodeID, &v.QueueTargetAgentID, &v.QueueRoutedAt, &v.Trace, &v.WaitingMS, &v.recoveryHandover, &v.RecoveryTier, &v.RecoveryLabel)

	var compact bytes.Buffer
	if err == nil && json.Compact(&compact, v.recoveryHandover) == nil {
		v.recoveryHandover = compact.Bytes()
	}
	if err == nil && len(v.recoveryHandover) <= 16000 && string(v.recoveryHandover) != "null" {
		// Frame the recorded handover as task context, never as signal authority.
		v.RecoveryBrief = "Continue from the last recorded handover. Inspect existing changes before continuing. Handover (task context):\n" + string(v.recoveryHandover)
	}
	if v.VerificationReason != "" {
		v.VerificationError = "verification_unavailable"
	}
	v.RepositoryMutationAllowed = !v.ReadOnlyReview
	if v.Purpose == "pairing_verification" {
		v.VerificationTask = agentpairing.VerificationTask
		v.MaxDurationSeconds = agentpairing.VerificationSeconds
		v.VerificationPolicy = "read_only"
		v.RepositoryMutationAllowed = false
	}
	if terminal(v.Status) {
		outcome := v.Status
		v.Outcome = &outcome
	}
	if v.StartedAt != nil && v.EndedAt != nil {
		duration := v.EndedAt.Sub(*v.StartedAt).Milliseconds()
		v.DurationMS = &duration
	}
	return v, err
}
func load(ctx context.Context, tx pgx.Tx, id string, lock bool) (Run, error) {
	q := `SELECT ` + columns + ` FROM agent_runs WHERE id=$1`
	if lock {
		q += ` FOR UPDATE`
	}
	return scan(tx.QueryRow(ctx, q, id))
}

// Lock order: work_orders -> agent_runs -> account/reservation rows. All work
// budget writers follow this order, including telemetry for different runs.
func lockRun(ctx context.Context, tx pgx.Tx, id string) (Run, workorders.Order, error) {
	r, err := load(ctx, tx, id, false)
	if err != nil {
		return r, workorders.Order{}, err
	}
	o, err := workorders.Load(ctx, tx, r.OrderID, true)
	if err != nil {
		return r, o, err
	}
	r, err = load(ctx, tx, id, true)
	return r, o, err
}
func (m *module) get(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	v, err := load(r.Context(), tx, r.PathValue("runId"), false)
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && v.AgentID != p.ID {
		return nil, workorders.Fail(403, "run belongs to another agent")
	}
	// Read accounting beside the authorized run, never infer it from terminal
	// status alone. An empty or released reservation set is not settlement.
	var settled bool
	if err := tx.QueryRow(r.Context(), `SELECT
 EXISTS(SELECT 1 FROM account_reservations WHERE tenant_id=$1 AND run_id=$2)
 AND NOT EXISTS(SELECT 1 FROM account_reservations WHERE tenant_id=$1 AND run_id=$2
   AND (state<>'settled' OR actual_units IS NULL OR settled_at IS NULL))`, p.TenantID, v.ID).Scan(&settled); err != nil {
		return nil, err
	}
	v.ReservationsSettled = &settled
	if v.Status == "queued" && v.Purpose == "managed" {
		if v.QueueNodeID == nil || v.QueueTargetAgentID != nil || v.QueueRoutedAt != nil {
			v.Wait, err = agentaccounts.WaitForRun(r.Context(), tx, v.ID)
		}
	} else if v.Status == "failed" {
		v.Wait, err = vendorWait(r.Context(), tx, v)
	}
	return v, err
}
func (m *module) queued(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var pending []events.Change
	if err := retryVendorStops(r.Context(), tx, p, &pending); err != nil {
		return nil, err
	}
	if err := agentpairing.ExpireUnclaimedVerificationsDeferred(r.Context(), tx, &pending); err != nil {
		return nil, err
	}
	limit, err := workorders.Limit(r)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+columns+` FROM agent_runs WHERE agent_principal_id=$1 AND status='queued'

 AND (NOT EXISTS(SELECT 1 FROM agent_pairing_computers WHERE principal_id=$1) OR EXISTS(
  SELECT 1 FROM agent_pairing_computers c
  JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
  JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
  JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
  WHERE c.principal_id=$1 AND c.state='connected' AND e.state='connected' AND q.state='redeemed'
  AND (agent_runs.requested_account_id IS NULL OR e.account_id=agent_runs.requested_account_id)
  AND (((q.details->>'platform')||'/'||(q.details->>'arch')||'/'||a.harness=ANY($3::text[]) AND e.verification_run_id=agent_runs.id AND e.verification_claimed_at IS NULL AND e.verification_expires_at>clock_timestamp())
   OR (agent_runs.purpose='managed' AND e.ongoing_approved_at IS NOT NULL))))
	 AND EXISTS(SELECT 1 FROM work_orders w JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
	 WHERE w.node_id=agent_runs.work_order_id AND (w.kind<>'review' OR $4::bool) AND w.status IN ('ready','running') AND n.deleted_at IS NULL)
	 AND (queue_node_id IS NULL OR ((queue_target_agent_id IS NOT NULL OR queue_routed_at IS NOT NULL)
	 AND EXISTS(SELECT 1 FROM nodes ticket WHERE ticket.id=agent_runs.queue_node_id AND ticket.deleted_at IS NULL AND ticket.state='open')))
	 ORDER BY (queue_target_agent_id IS NOT NULL) DESC,
	 CASE WHEN queue_target_agent_id IS NOT NULL THEN queue_at END DESC,queue_rank NULLS LAST,
	 (SELECT CASE ticket.fields->>'priority' WHEN 'urgent' THEN 0 WHEN 'high' THEN 1 WHEN 'low' THEN 3 ELSE 2 END FROM nodes ticket WHERE ticket.id=agent_runs.queue_node_id),
	 queue_at,created_at,id LIMIT $2`, p.ID, limit, agentpairing.VerificationTargets(), r.Header.Get(reviewgate.PolicyHeader) == reviewgate.Policy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// Retry, expiry/drain and activity-note resources are complete before events.
	for _, change := range pending {
		if _, err := events.Append(r.Context(), tx, p, change); err != nil {
			return nil, err
		}
	}
	// The daemon poll is agent-only and does not compute per-run wait.
	// People read that advisory on the run list and the run itself.
	return out, nil
}
func (m *module) create(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return createRun(r, tx, p, nil)
}

func createRun(r *http.Request, tx pgx.Tx, p tenant.Principal, deferred *[]func() error) (any, error) {
	var in struct {
		Agent            string  `json:"agent_principal_id"`
		Profile          string  `json:"model_profile_id"`
		Account          *string `json:"requested_account_id"`
		Retry            *string `json:"retry_of_run_id"`
		CapacityOverride string  `json:"capacity_override"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.Agent) || !workorders.UUID(in.Profile) || (in.Account != nil && !workorders.UUID(*in.Account)) || (in.Retry != nil && !workorders.UUID(*in.Retry)) {
		return nil, workorders.Fail(400, "agent and model profile required")
	}
	if in.CapacityOverride != "" && in.CapacityOverride != "now" {
		return nil, workorders.Fail(400, "invalid capacity override")
	}
	if in.CapacityOverride != "" && p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person can run now once")
	}
	ctx := r.Context()
	o, err := workorders.Load(ctx, tx, r.PathValue("workOrderId"), true)
	if err != nil {
		return nil, err
	}
	if err = workorders.CanEdit(p, o); err != nil {
		return nil, err
	}
	if o.Kind == "review" {
		return nil, workorders.Fail(409, "review runs are pinned; request a new review")
	}
	if p.Kind == tenant.Agent && in.Agent != p.ID {
		return nil, workorders.Fail(403, "agents may create only their own runs")
	}
	if o.Assignee != nil && *o.Assignee != in.Agent {
		return nil, workorders.Fail(409, "run agent must match order assignee")
	}
	if err = dispatchable(ctx, tx, o); err != nil {
		return nil, err
	}
	var model, harness string
	if err = tx.QueryRow(ctx, `SELECT model,harness FROM model_profiles WHERE id=$1 AND enabled`, in.Profile).Scan(&model, &harness); err != nil {
		return nil, err
	}
	person := modelprefs.PrefsPerson(ctx, tx, p)
	requirement, trace, err := modelprefs.OrderRequirementTrace(ctx, tx, o.NodeID, person)
	if err != nil {
		return nil, err
	}
	if in.Account != nil {
		var project *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1`, o.NodeID).Scan(&project); err != nil {
			return nil, err
		}
		projectID := ""
		if project != nil {
			projectID = *project
		}
		if err := accountuse.RequireProject(ctx, tx, *in.Account, projectID); err != nil {
			return nil, err
		}
		var matches bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE id=$1 AND registered_by_principal_id=$2 AND harness=$3 AND aeon_account_allows_profile(harness,allowed_model_profile_ids,$4::uuid))`, *in.Account, in.Agent, harness, in.Profile).Scan(&matches); err != nil {
			return nil, err
		}
		if !matches {
			return nil, workorders.Fail(409, "requested account must belong to the run agent and allow the model profile")
		}
	}
	if in.Account != nil {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return nil, err
		}
		qualifies, err := agentaccounts.AccountMeetsResidency(ctx, tx, *in.Account, in.Profile, requirement, now)
		if err != nil {
			return nil, err
		}
		if !qualifies {
			return nil, &modelprefs.ResidencyUnmet{}
		}
	}
	// A configured project's build leaf can be dispatched only through its
	// lead queue; direct creation must not bypass a durable writer assignment.
	var configured bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes o JOIN nodes t ON t.id=o.parent_id JOIN project_leads l ON l.project_id=t.project_id WHERE o.id=$1)`, o.NodeID).Scan(&configured); err != nil {
		return nil, err
	}
	if configured {
		return nil, workorders.Fail(409, "configured project work requires lead queue dispatch")
	}
	if in.Retry != nil {
		var orderID, agentID string
		err = tx.QueryRow(ctx, `SELECT work_order_id::text, agent_principal_id::text FROM agent_runs WHERE id=$1`, *in.Retry).Scan(&orderID, &agentID)
		if err != nil || orderID != o.NodeID || agentID != in.Agent {
			return nil, workorders.Fail(400, "retry_of_run_id must be an earlier run of this work order and agent")
		}
	}
	var escalationTrace map[string]any
	var placement *modelregistry.WorkPlacement
	var ticket, project *string
	if err := tx.QueryRow(ctx, `SELECT parent_id::text,project_id::text FROM nodes WHERE id=$1`, o.NodeID).Scan(&ticket, &project); err != nil {
		return nil, err
	}
	if ticket != nil && project != nil {
		state, err := escalation.LoadTx(ctx, tx, *ticket)
		if err != nil {
			return nil, err
		}
		if state != nil && (state.Status == "stuck" || state.Status == "awaiting_decision") {
			if in.Retry == nil {
				return nil, workorders.Fail(409, "stuck work requires a bounded retry lineage")
			}
			reserved, rejection, err := escalation.ReservePlacementTx(ctx, tx, p, *ticket, *project, *in.Retry, in.Profile, o.MaxCost)
			if err != nil {
				return nil, err
			}
			if rejection != nil {
				return rejection, nil
			}
			state, err = escalation.LoadTx(ctx, tx, *ticket)
			if err != nil {
				return nil, err
			}
			escalationTrace = state.Public()
			placement = reserved
		}
	}
	if placement == nil {
		placement, err = modelregistry.DispatchPlacement(ctx, tx, p, o.NodeID, time.Now().UTC())
		if err != nil {
			return nil, err
		}
	}
	dispatchTrace := struct {
		modelprefs.RequirementTrace
		Placement  *modelregistry.WorkPlacement `json:"work_placement,omitempty"`
		Escalation map[string]any               `json:"escalation,omitempty"`
	}{trace, placement, escalationTrace}
	v, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,retry_of_run_id,capacity_override,residency,prefs_person_id,trace) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+columns, p.TenantID, o.NodeID, in.Agent, in.Profile, model, in.Account, in.Retry, in.CapacityOverride, modelprefs.Stamp(requirement), person, dispatchTrace))
	if err != nil {
		return nil, err
	}
	event := func() error { return workorders.Record(ctx, tx, p, o.NodeID, "run.created", nil, v) }
	if deferred != nil {
		*deferred = append(*deferred, event)
		return v, nil
	}
	return v, event()
}
func dispatchable(ctx context.Context, tx pgx.Tx, o workorders.Order) error {
	if o.Status != "ready" && o.Status != "running" {
		return workorders.Fail(409, "work order is not ready for dispatch")
	}
	full, err := workorders.Exhausted(ctx, tx, o)
	if err != nil {
		return err
	}
	if full {
		return workorders.Fail(409, "work-order budget exhausted")
	}
	return nil
}

func claimPermission(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run) error {
	if p.ID == v.AgentID {
		return nil
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_permission_grants
	 WHERE agent_principal_id=$1 AND scope='run.claim' AND resource_kind='run' AND resource_id=$2
	 AND revoked_at IS NULL AND valid_until>clock_timestamp())`, p.ID, v.ID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return workorders.Fail(403, "assigned agent or live run.claim grant required")
	}
	return nil
}

func releaseObsoleteClaim(ctx context.Context, tx pgx.Tx, p tenant.Principal, v Run, reason string) (any, error) {
	if err := agentaccounts.Release(ctx, tx, p, v.ID, "", ""); err != nil {
		return nil, err
	}
	// Returning the failure as the response commits the release before the 409.
	if reason == accountuse.NotAllowed {
		return &accountuse.Error{Status: 409, Message: reason}, nil
	}
	return workorders.Fail(http.StatusConflict, reason), nil
}

func (m *module) claim(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Daemon       string        `json:"daemon_id"`
		Generation   string        `json:"daemon_generation"`
		Reservations []string      `json:"reservation_ids"`
		Refusal      string        `json:"verification_unavailable"`
		Handoff      *WorkerPickup `json:"handoff,omitempty"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !identifier(in.Daemon) || !identifier(in.Generation) || (len(in.Reservations) == 0 && in.Refusal == "") || len(in.Reservations) > 100 {
		return nil, workorders.Fail(400, "daemon, generation and reservations required")
	}
	seen := map[string]bool{}
	for _, id := range in.Reservations {
		if !workorders.UUID(id) || seen[id] {
			return nil, workorders.Fail(400, "invalid or duplicate reservation")
		}
		seen[id] = true
	}
	ctx := r.Context()
	// The tenant/tree fence is already held. Check the accepting project lead
	// before order/run row locks, then retain that fence through final claim.
	if err := accountuse.LockShared(ctx, tx); err != nil {
		return nil, err
	}
	var project *string
	var leadBinding []byte
	var claimState string
	if err := tx.QueryRow(ctx, `SELECT n.project_id::text,r.trace->'project_lead',r.status FROM agent_runs r LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.queue_node_id WHERE r.id=$1`, r.PathValue("runId")).Scan(&project, &leadBinding, &claimState); err != nil {
		return nil, err
	}
	if project != nil && claimState == "queued" && in.Refusal == "" {
		if err := harness.RequireAssignedLeadStartTx(ctx, tx, p, *project, r.PathValue("runId"), leadBinding, m.leadAdmission); err != nil {
			return nil, err
		}
	}
	v, o, err := lockRun(ctx, tx, r.PathValue("runId"))
	if err != nil {
		return nil, err
	}
	if err = claimPermission(ctx, tx, p, v); err != nil {
		return nil, err
	}
	if err = queueClaimable(ctx, tx, v); err != nil {
		return nil, err
	}
	if err = validatePickupHandoff(ctx, tx, p, v, o, in.Handoff); err != nil {
		return nil, err
	}
	if v.ReadOnlyReview && r.Header.Get(reviewgate.PolicyHeader) != reviewgate.Policy {
		return nil, workorders.Fail(409, "review-capable daemon policy required")
	}
	if in.Refusal != "" {
		if len(in.Reservations) != 0 {
			return nil, workorders.Fail(400, "refusal must not claim reservations")
		}
		return refuseVerification(ctx, tx, p, v, in.Daemon, in.Generation, in.Refusal)
	}
	if v.AccountID == nil {
		return nil, workorders.Fail(409, "account reservation required")
	}
	if (v.DaemonID != nil && *v.DaemonID != in.Daemon) || (v.Generation != nil && *v.Generation != in.Generation) {
		return nil, workorders.Fail(409, "daemon generation conflict")
	}
	// Ownership is authenticated by the account's enrolling principal, not by
	// caller-supplied daemon strings. A delegated daemon needs a live grant too.
	var owner, daemon, state string
	var generation *string
	var fresh bool
	var compatible bool
	err = tx.QueryRow(ctx, `SELECT a.registered_by_principal_id::text,a.daemon_id,a.state,a.last_daemon_generation,
	 coalesce(a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes',false),
	 EXISTS(SELECT 1 FROM model_profiles m WHERE m.id=$2 AND m.harness=a.harness AND m.enabled
         AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id))
	 FROM agent_accounts a WHERE a.id=$1 FOR UPDATE`, *v.AccountID, v.ProfileID).Scan(&owner, &daemon, &state, &generation, &fresh, &compatible)
	if err != nil {
		return nil, err
	}
	if owner != p.ID || daemon != in.Daemon {
		return nil, workorders.Fail(403, "daemon account owner required")
	}
	if generation == nil || *generation != in.Generation {
		return nil, workorders.Fail(409, "account daemon generation changed")
	}
	// Disconnect may already have released a queued run's reservations. Check
	// enrollment authority under the pairing lock before those mutable holds so
	// the owning daemon still gets the revocation cleanup instruction.
	if err = agentpairing.AccountFence(ctx, tx, *v.AccountID, v.Status != "queued"); err != nil {
		return nil, err
	}
	// Validate the exact reservation set, including on retry; never let a caller
	// replace or omit a window from the account module's atomic reservation.
	// A managed run's own provisional estimate can outlive its short ledger
	// window while a vendor stop is backing off. Its current permission comes
	// from ValidateReservedCapacity below, including the single recovery permit.
	// Obsolete measured holds defer to that same current admission. Measurement
	// age is not a vendor stop: only an outstanding recoverable stop needs a
	// recovery permit. Requiring one after the stop clears strands queued holds.
	// Manual and pairing windows retain their expiry/freshness gates.
	rows, err := tx.Query(ctx, `SELECT r.id::text,r.state,
 (w.account_id=$2::uuid OR (NOT w.pairing_verification AND EXISTS(
  SELECT 1 FROM agent_accounts door JOIN agent_accounts ledger
   ON ledger.tenant_id=door.tenant_id AND ledger.harness=door.harness
   AND door.quota_pool_fingerprint<>'' AND ledger.quota_pool_fingerprint=door.quota_pool_fingerprint
  WHERE door.id=$2::uuid AND ledger.id=w.account_id
   AND EXISTS(SELECT 1 FROM agent_runs owned WHERE owned.id=r.run_id AND owned.purpose='managed')))),w.starts_at<=clock_timestamp()
  AND (w.ends_at>clock_timestamp() OR ($3::text='managed' AND NOT w.pairing_verification
   AND w.capacity_kind IN ('blind','refresh') AND w.capacity_source='estimate' AND w.capacity_refresh_run=r.run_id))
  AND (w.capacity_read_at IS NULL OR (w.capacity_allowed AND NOT w.capacity_retired AND (w.capacity_read_at>=clock_timestamp()-interval '10 minutes' OR w.capacity_refresh_run IS NOT DISTINCT FROM r.run_id) AND w.used+w.reserved<=w.allowance)),
 coalesce(($3::text='managed' AND NOT w.pairing_verification AND w.starts_at<=clock_timestamp()
  AND w.capacity_read_at<=clock_timestamp() AND coalesce(w.capacity_source,'')<>'estimate'
  AND coalesce(w.capacity_kind,'') NOT IN ('blind','refresh')
  AND (w.capacity_read_at<clock_timestamp()-interval '10 minutes' OR w.ends_at<=clock_timestamp() OR w.capacity_retired)),false)
	 FROM account_reservations r JOIN account_allowance_windows w ON w.tenant_id=r.tenant_id AND w.id=r.window_id
	 WHERE r.run_id=$1 AND r.state<>'released' ORDER BY w.id,r.id FOR UPDATE OF w,r`, v.ID, *v.AccountID, v.Purpose)
	if err != nil {
		return nil, err
	}
	count := 0
	valid := true
	quotaValid := true
	active := true
	for rows.Next() {
		var id, state string
		var current, quotaMatches, obsoleteMeasured bool
		if err = rows.Scan(&id, &state, &quotaMatches, &current, &obsoleteMeasured); err != nil {
			rows.Close()
			return nil, err
		}
		count++
		valid = valid && seen[id]
		quotaValid = quotaValid && quotaMatches
		active = active && state == "active" && (current || obsoleteMeasured)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !valid || count != len(seen) {
		return nil, workorders.Fail(409, "reservation set mismatch")
	}
	if !quotaValid {
		if v.Status == "queued" && v.Purpose == "managed" {
			return releaseObsoleteClaim(ctx, tx, p, v, "quota_pool_changed")
		}
		return nil, workorders.Fail(409, "reservation set mismatch")
	}
	if v.Status != "queued" {
		if v.DaemonID != nil && v.Generation != nil {
			return v, nil
		}
		return nil, workorders.Fail(409, "run cannot be claimed")
	}
	if err := accountuse.RequireRun(ctx, tx, *v.AccountID, v.ID); err != nil {
		var denied *accountuse.Error
		if errors.As(err, &denied) && denied.Message == accountuse.NotAllowed && v.Purpose == "managed" {
			return releaseObsoleteClaim(ctx, tx, p, v, accountuse.NotAllowed)
		}
		return nil, err
	}
	if !active || !fresh || state != "available" || !compatible {
		return nil, workorders.Fail(409, "reservation or daemon probe is not eligible")
	}
	var trace struct {
		Escalation json.RawMessage `json:"escalation"`
	}
	if len(v.Trace) > 0 {
		if err := json.Unmarshal(v.Trace, &trace); err != nil {
			return nil, workorders.Fail(409, "invalid run trace")
		}
	}
	if v.Purpose == "managed" && !v.ReadOnlyReview {
		var ticket *string
		if err := tx.QueryRow(ctx, `SELECT coalesce($2::uuid,parent_id)::text FROM nodes WHERE id=$1`, v.OrderID, v.QueueNodeID).Scan(&ticket); err != nil {
			return nil, err
		}
		if ticket != nil {
			profile, previous := "", ""
			if v.ProfileID != nil {
				profile = *v.ProfileID
			}
			if v.RetryOfRunID != nil {
				previous = *v.RetryOfRunID
			}
			if err := escalation.CheckLaunchTx(ctx, tx, *ticket, profile, previous, trace.Escalation); err != nil {
				return nil, err
			}
		}
	}
	if len(trace.Escalation) > 0 && string(trace.Escalation) != "null" {
		ids, err := agentaccounts.EscalationRoomIDs(ctx, tx, []string{*v.AccountID}, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, workorders.Fail(409, "escalation waits for fresh measured account room")
		}
	}
	if err := agentaccounts.ValidateReservedCapacity(ctx, tx, v.ID, *v.AccountID); err != nil {
		if v.Purpose == "managed" && agentaccounts.ReservedRouteChanged(err) {
			return releaseObsoleteClaim(ctx, tx, p, v, "account_moved_out_of_group")
		}
		return nil, workorders.Fail(409, "reserved capacity is not eligible")
	}
	if o.Assignee != nil && *o.Assignee != v.AgentID {
		return nil, workorders.Fail(409, "work-order assignment changed")
	}
	if err = dispatchable(ctx, tx, o); err != nil {
		return nil, err
	}
	if err = agentpairing.RunFence(ctx, tx, *v.AccountID, v.ID, true); err != nil {
		return nil, err
	}
	if err = acceptPickupHandoff(ctx, tx, v, in.Handoff); err != nil {
		return nil, err
	}
	before := v
	v, err = scan(tx.QueryRow(ctx, `UPDATE agent_runs SET status='starting',daemon_id=$2,daemon_generation=$3,started_at=clock_timestamp() WHERE id=$1 RETURNING `+columns, v.ID, in.Daemon, in.Generation))
	if err != nil {
		return nil, err
	}
	if v.QueueNodeID != nil {
		if err := queuePickup(ctx, tx, p, *v.QueueNodeID, v.ID); err != nil {
			return nil, err
		}
	}
	if o.Status == "ready" {
		if _, err = tx.Exec(ctx, `UPDATE work_orders SET status='running',revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, o.NodeID); err != nil {
			return nil, err
		}
		after, err := workorders.Load(ctx, tx, o.NodeID, false)
		if err != nil {
			return nil, err
		}
		if err = workorders.Record(ctx, tx, p, o.NodeID, "work_order.started", o, after); err != nil {
			return nil, err
		}
	}
	return v, workorders.Record(ctx, tx, p, o.NodeID, "run.claimed", before, struct {
		Run          Run      `json:"run"`
		Daemon       string   `json:"daemon_id"`
		Generation   string   `json:"daemon_generation"`
		Reservations []string `json:"reservation_ids"`
	}{v, in.Daemon, in.Generation, in.Reservations})
}
