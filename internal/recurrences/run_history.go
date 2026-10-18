// SPDX-License-Identifier: AGPL-3.0-only
package recurrences

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RunState is closed at the Go write/read boundary. Older released writers
// still use pending, so a database CHECK would violate expand-only compatibility.
type RunState string

const (
	RunCreated          RunState = "created"
	RunRunning          RunState = "running"
	RunWaitingForPerson RunState = "waiting_for_person"
	RunHeld             RunState = "held"
	RunFailed           RunState = "failed"
	RunDone             RunState = "done"
	RunSkipped          RunState = "skipped"
)

func runState(raw string) (RunState, string) {
	switch raw {
	case "pending", "queued", "created":
		return RunCreated, "The occurrence was created; execution has not started."
	case "starting", "running":
		return RunRunning, "Execution is in progress."
	case "needs_person", "waiting_for_person":
		return RunWaitingForPerson, "A person must decide before execution can continue."
	case "waiting", "held", "blocked", "paused":
		return RunHeld, "Execution is held until its required conditions are confirmed."
	case "failed", "ownership_lost":
		return RunFailed, "Execution failed and has not completed successfully."
	case "succeeded", "completed", "done":
		return RunDone, ""
	case "cancelled", "skipped":
		return RunSkipped, "The run was skipped or cancelled."
	default:
		return RunHeld, "The recorded outcome is unknown and requires reconciliation."
	}
}

type historyCursor struct {
	Scope string    `json:"s"`
	At    time.Time `json:"t"`
	ID    string    `json:"i"`
}

func parseHistoryCursor(raw, scope string) (historyCursor, error) {
	v := historyCursor{Scope: scope}
	if raw == "" {
		return v, nil
	}
	v = historyCursor{}
	if len(raw) > 256 {
		return v, workorders.Fail(400, "invalid run history cursor")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || json.Unmarshal(b, &v) != nil || v.Scope != scope || v.At.IsZero() || !workorders.UUID(v.ID) {
		return historyCursor{}, workorders.Fail(400, "invalid run history cursor")
	}
	return v, nil
}

func historyNext(scope string, at time.Time, id string) *string {
	b, _ := json.Marshal(historyCursor{scope, at.UTC(), id})
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func historyQuery(r *http.Request, keys ...string) (int, error) {
	if len(r.URL.RawQuery) > 1024 {
		return 0, workorders.Fail(400, "run history query too large")
	}
	q := r.URL.Query()
	for key, values := range q {
		valid := key == "limit"
		for _, k := range keys {
			valid = valid || key == k
		}
		if !valid || len(values) != 1 || values[0] == "" {
			return 0, workorders.Fail(400, "invalid run history query")
		}
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			return 0, workorders.Fail(400, "limit must be 1..50")
		}
	}
	return limit, nil
}

// Historical reads intentionally retain retired definitions. Definition scope
// and output authority are checked together, without returning assignments.
func historyDefinition(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string) error {
	r, err := scanRecurrence(tx.QueryRow(ctx, `SELECT `+recurrenceColumns+` FROM recurrences WHERE id=$1`, id))
	if err != nil {
		return err
	}
	if err = definitions(ctx, tx, []Recurrence{r}, func(items []Recurrence) { r = items[0] }); err != nil {
		return err
	}
	return readDefinition(ctx, tx, p, r.Input)
}

type RunHistoryEntry struct {
	ID                 string          `json:"id"`
	RecurrenceID       string          `json:"recurrence_id"`
	DefinitionRevision int64           `json:"definition_revision"`
	OwnerID            string          `json:"owner_principal_id"`
	State              RunState        `json:"state"`
	Reason             string          `json:"reason"`
	WorkNodeID         *string         `json:"work_node_id"`
	WorkOrderID        *string         `json:"work_order_id"`
	AgentRunID         *string         `json:"agent_run_id"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Usage              RunHistoryUsage `json:"usage"`
	project            string
	workProject        *string
}

type HistoryMeasure struct {
	Measured *string `json:"measured"`
	Known    int     `json:"known"`
	Unknown  int     `json:"unknown"`
}

type RunHistoryUsage struct {
	Tokens             HistoryMeasure `json:"tokens"`
	PaidMicroUSD       *string        `json:"paid_microusd"`
	EstimatedAPIUSD    *string        `json:"estimated_api_usd"`
	ReservedTokens     *string        `json:"reserved_tokens"`
	ReservedMicroUSD   *string        `json:"reserved_microusd"`
	ReservedRecoveryMS *string        `json:"reserved_recovery_ms"`
	Partial            bool           `json:"partial"`
	Truncated          bool           `json:"truncated"`
	Gaps               []string       `json:"gaps"`
}

type HistoryPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

const historyRunColumns = `r.id::text,r.recurrence_id::text,r.definition_revision,r.owner_principal_id::text,r.state,
 n.id::text,w.node_id::text,a.id::text,r.created_at,r.updated_at,r.output_project_id::text,n.project_id::text`
const historyRunJoins = ` LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_node_id
 LEFT JOIN work_orders w ON w.tenant_id=r.tenant_id AND w.node_id=r.work_order_id
 LEFT JOIN agent_runs a ON a.tenant_id=r.tenant_id AND a.id=r.agent_run_id`

func scanHistoryRun(row pgx.Row) (RunHistoryEntry, error) {
	var out RunHistoryEntry
	var raw string
	err := row.Scan(&out.ID, &out.RecurrenceID, &out.DefinitionRevision, &out.OwnerID, &raw, &out.WorkNodeID, &out.WorkOrderID, &out.AgentRunID, &out.CreatedAt, &out.UpdatedAt, &out.project, &out.workProject)
	out.State, out.Reason = runState(raw)
	out.CreatedAt, out.UpdatedAt = out.CreatedAt.UTC(), out.UpdatedAt.UTC()
	return out, err
}

func (m *Module) listRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	limit, err := historyQuery(r, "before")
	scope := r.PathValue("recurrenceId")
	cursor, e := parseHistoryCursor(r.URL.Query().Get("before"), scope)
	if err == nil {
		err = e
	}
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	out := HistoryPage[RunHistoryEntry]{Items: []RunHistoryEntry{}}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err = db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := historyDefinition(ctx, tx, p, scope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+historyRunColumns+` FROM routine_runs r `+historyRunJoins+`
 WHERE r.recurrence_id=$1 AND ($2::uuid IS NULL OR (r.created_at,r.id)<($3,$2::uuid)) ORDER BY r.created_at DESC,r.id DESC LIMIT $4`, scope, nullID(cursor.ID), cursor.At, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			v, err := scanHistoryRun(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out.Items = append(out.Items, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			last := out.Items[limit-1]
			out.NextCursor = historyNext(scope, last.CreatedAt, last.ID)
		}
		for i := range out.Items {
			if err := readRunUsage(ctx, tx, p, &out.Items[i]); err != nil {
				return err
			}
			if err := confirmRunOutcome(ctx, tx, &out.Items[i]); err != nil {
				return err
			}
		}
		return nil
	})
	reply(w, 200, out, err)
}

func nullID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

type AttemptHistory struct {
	ID            string    `json:"id"`
	Role          string    `json:"role"`
	State         RunState  `json:"state"`
	Reason        string    `json:"reason"`
	AgentRunID    *string   `json:"agent_run_id"`
	ExitConfirmed bool      `json:"exit_confirmed"`
	ExitCode      *int64    `json:"exit_code"`
	CreatedAt     time.Time `json:"created_at"`
}

type ActionHistory struct {
	ID                string    `json:"id"`
	AttemptID         *string   `json:"attempt_id"`
	Kind              string    `json:"kind"`
	State             string    `json:"state"`
	TargetID          *string   `json:"target_node_id"`
	EvaluationID      *string   `json:"evaluation_id"`
	EvaluationResult  *string   `json:"evaluation_result"`
	ApprovalID        *string   `json:"approval_id"`
	ApprovalState     *string   `json:"approval_state"`
	EffectID          *string   `json:"effect_id"`
	PullRequestNumber *int64    `json:"pull_request_number"`
	CreatedAt         time.Time `json:"created_at"`
}

type EffectHistory struct {
	ID        string    `json:"id"`
	ActionID  *string   `json:"action_id"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

type RunHistory struct {
	Run      RunHistoryEntry             `json:"run"`
	Attempts HistoryPage[AttemptHistory] `json:"attempts"`
	Actions  HistoryPage[ActionHistory]  `json:"actions"`
	Effects  HistoryPage[EffectHistory]  `json:"effects"`
}

func (m *Module) getRun(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("runId")
	if !workorders.UUID(id) {
		reply(w, 200, nil, workorders.Fail(400, "invalid run id"))
		return
	}
	limit, err := historyQuery(r, "attempt_before", "action_before", "effect_before")
	cursors := map[string]historyCursor{}
	for _, kind := range []string{"attempt", "action", "effect"} {
		v, e := parseHistoryCursor(r.URL.Query().Get(kind+"_before"), id+":"+kind)
		if err == nil {
			err = e
		}
		cursors[kind] = v
	}
	if err != nil {
		reply(w, 200, nil, err)
		return
	}
	out := RunHistory{Attempts: HistoryPage[AttemptHistory]{Items: []AttemptHistory{}}, Actions: HistoryPage[ActionHistory]{Items: []ActionHistory{}}, Effects: HistoryPage[EffectHistory]{Items: []EffectHistory{}}}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	err = db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
			return err
		}
		if err := historyDefinition(ctx, tx, p, r.PathValue("recurrenceId")); err != nil {
			return err
		}
		var err error
		out.Run, err = scanHistoryRun(tx.QueryRow(ctx, `SELECT `+historyRunColumns+` FROM routine_runs r `+historyRunJoins+` WHERE r.id=$1 AND r.recurrence_id=$2`, id, r.PathValue("recurrenceId")))
		if err != nil {
			return err
		}
		if err = readRunUsage(ctx, tx, p, &out.Run); err != nil {
			return err
		}
		allowed, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		if err = readAttempts(ctx, tx, id, limit, cursors["attempt"], allowed, &out.Attempts); err != nil {
			return err
		}
		if err = readActions(ctx, tx, p, id, limit, cursors["action"], allowed, &out.Actions); err != nil {
			return err
		}
		if err = readEffects(ctx, tx, id, limit, cursors["effect"], &out.Effects); err != nil {
			return err
		}
		return confirmRunOutcome(ctx, tx, &out.Run)
	})
	reply(w, 200, out, err)
}

func confirmRunOutcome(ctx context.Context, tx pgx.Tx, run *RunHistoryEntry) error {
	if run.State != RunDone {
		return nil
	}
	var proven, failed, runConfirmed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1),
 EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND (state IN ('failed','ownership_lost') OR (process_receipt->>'exit_confirmed'='true' AND CASE WHEN process_receipt->>'exit_code' ~ '^-?[0-9]{1,9}$' THEN (process_receipt->>'exit_code')::bigint<>0 ELSE false END))),
 NOT EXISTS(SELECT 1 FROM routine_attempts WHERE run_id=$1 AND (state NOT IN ('done','completed','succeeded') OR agent_run_id IS NULL OR process_receipt->>'exit_confirmed' IS DISTINCT FROM 'true' OR process_receipt->>'exit_code' IS DISTINCT FROM '0'))`, run.ID).Scan(&proven, &failed, &runConfirmed)
	if err == nil && failed {
		run.State, run.Reason = runState("failed")
		return nil
	}
	if err == nil && (!proven || !runConfirmed) {
		run.State, run.Reason = RunHeld, "Successful execution has not been confirmed by process evidence."
	}
	return err
}

func readAttempts(ctx context.Context, tx pgx.Tx, id string, limit int, c historyCursor, allowed authz.ProjectCheck, out *HistoryPage[AttemptHistory]) error {
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.role,a.state,r.id::text,n.project_id::text,
 a.process_receipt->>'exit_confirmed'='true',
 CASE WHEN a.process_receipt->>'exit_code' ~ '^-?[0-9]{1,9}$' THEN (a.process_receipt->>'exit_code')::bigint END,a.created_at FROM routine_attempts a
 LEFT JOIN agent_runs r ON r.tenant_id=a.tenant_id AND r.id=a.agent_run_id
 LEFT JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id
 WHERE a.run_id=$1 AND ($2::uuid IS NULL OR (a.created_at,a.id)<($3,$2::uuid)) ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, id, nullID(c.ID), c.At, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v AttemptHistory
		var raw string
		var project *string
		var exited *bool
		if err = rows.Scan(&v.ID, &v.Role, &raw, &v.AgentRunID, &project, &exited, &v.ExitCode, &v.CreatedAt); err != nil {
			return err
		}
		v.State, v.Reason = runState(raw)
		v.CreatedAt = v.CreatedAt.UTC()
		if project == nil || !allowed("harness.read", *project) {
			v.AgentRunID = nil
			v.ExitCode = nil
		} else {
			v.ExitConfirmed = exited != nil && *exited
		}
		if v.ExitConfirmed && v.ExitCode != nil && *v.ExitCode != 0 {
			v.State, v.Reason = runState("failed")
		} else if v.State == RunDone && (!v.ExitConfirmed || v.ExitCode == nil) {
			v.State, v.Reason = RunHeld, "Process exit has not been confirmed."
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = historyNext(id+":attempt", last.CreatedAt, last.ID)
	}
	return rows.Err()
}

func readActions(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, limit int, c historyCursor, allowed authz.ProjectCheck, out *HistoryPage[ActionHistory]) error {
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.attempt_id::text,a.kind,a.state,n.id::text,n.project_id::text,
 e.id::text,e.result,ap.id::text,
 CASE WHEN ap.id IS NULL THEN NULL WHEN g.revoked_at IS NOT NULL THEN 'revoked' WHEN ad.decision IS NOT NULL THEN ad.decision WHEN ap.expires_at<=now() THEN 'expired' ELSE 'pending' END,
 n.project_id::text,ap.agent_principal_id::text,f.id::text,
 CASE WHEN a.kind='pr.open' AND a.result->>'pull_request_number' ~ '^[1-9][0-9]{0,8}$' THEN (a.result->>'pull_request_number')::bigint END,a.created_at
 FROM routine_actions a LEFT JOIN nodes n ON n.tenant_id=a.tenant_id AND n.id=a.target_node_id
 LEFT JOIN routine_guard_evaluations e ON e.tenant_id=a.tenant_id AND e.id=a.evaluation_id AND e.recurrence_id=(SELECT recurrence_id FROM routine_runs WHERE id=$1)
 LEFT JOIN approval_requests ap ON ap.tenant_id=a.tenant_id AND ap.id=a.approval_id AND ap.resource_kind='node' AND ap.resource_id=a.target_node_id
 LEFT JOIN approval_decisions ad ON ad.tenant_id=ap.tenant_id AND ad.request_id=ap.id
 LEFT JOIN agent_permission_grants g ON g.tenant_id=ap.tenant_id AND g.approval_request_id=ap.id
 LEFT JOIN LATERAL (SELECT id FROM routine_effect_outbox WHERE tenant_id=a.tenant_id AND action_id=a.id ORDER BY created_at,id LIMIT 1) f ON true
 WHERE a.run_id=$1 AND ($2::uuid IS NULL OR (a.created_at,a.id)<($3,$2::uuid)) ORDER BY a.created_at DESC,a.id DESC LIMIT $4`, id, nullID(c.ID), c.At, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v ActionHistory
		var project, approvalProject, approvalAgent *string
		if err = rows.Scan(&v.ID, &v.AttemptID, &v.Kind, &v.State, &v.TargetID, &project, &v.EvaluationID, &v.EvaluationResult, &v.ApprovalID, &v.ApprovalState, &approvalProject, &approvalAgent, &v.EffectID, &v.PullRequestNumber, &v.CreatedAt); err != nil {
			return err
		}
		v.CreatedAt = v.CreatedAt.UTC()
		if project == nil || !allowed("nodes.read", *project) {
			v.TargetID, v.EvaluationID, v.EvaluationResult, v.ApprovalID, v.ApprovalState = nil, nil, nil, nil, nil
			v.PullRequestNumber = nil
		}
		if approvalProject == nil || !allowed("approvals.read", *approvalProject) || p.Kind == tenant.Agent && (approvalAgent == nil || *approvalAgent != p.ID) {
			v.ApprovalID, v.ApprovalState = nil, nil
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = historyNext(id+":action", last.CreatedAt, last.ID)
	}
	return rows.Err()
}

func readEffects(ctx context.Context, tx pgx.Tx, id string, limit int, c historyCursor, out *HistoryPage[EffectHistory]) error {
	rows, err := tx.Query(ctx, `SELECT id::text,action_id::text,kind,state,created_at FROM routine_effect_outbox
 WHERE run_id=$1 AND ($2::uuid IS NULL OR (created_at,id)<($3,$2::uuid)) ORDER BY created_at DESC,id DESC LIMIT $4`, id, nullID(c.ID), c.At, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v EffectHistory
		if err = rows.Scan(&v.ID, &v.ActionID, &v.Kind, &v.State, &v.CreatedAt); err != nil {
			return err
		}
		v.CreatedAt = v.CreatedAt.UTC()
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		out.NextCursor = historyNext(id+":effect", last.CreatedAt, last.ID)
	}
	return rows.Err()
}

func addHistoryNumber(total **string, raw *string) error {
	if raw == nil {
		return nil
	}
	n, ok := new(big.Rat).SetString(*raw)
	if !ok || n.Sign() < 0 {
		return errors.New("invalid stored usage measurement")
	}
	if *total != nil {
		v, ok := new(big.Rat).SetString(**total)
		if !ok {
			return errors.New("invalid usage aggregate")
		}
		n.Add(n, v)
	}
	s := strings.TrimRight(strings.TrimRight(n.FloatString(12), "0"), ".")
	if s == "" {
		s = "0"
	}
	*total = &s
	return nil
}

func readRunUsage(ctx context.Context, tx pgx.Tx, p tenant.Principal, run *RunHistoryEntry) error {
	u := RunHistoryUsage{Partial: true, Gaps: []string{}}
	run.Usage = u
	allowed, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return err
	}
	if run.workProject == nil || !allowed("nodes.read", *run.workProject) {
		run.WorkNodeID = nil
	}
	if run.workProject == nil || !allowed("work_orders.read", *run.workProject) {
		run.WorkOrderID = nil
	}
	if run.workProject == nil || !allowed("harness.read", *run.workProject) {
		run.AgentRunID = nil
	}
	if !allowed("harness.read", run.project) || !allowed("outcome.read", run.project) {
		run.Usage.Gaps = []string{"usage_visibility_withheld"}
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM (
 SELECT agent_run_id AS id FROM routine_runs WHERE id=$1 AND agent_run_id IS NOT NULL
 UNION SELECT agent_run_id FROM routine_attempts WHERE run_id=$1 AND agent_run_id IS NOT NULL
 UNION SELECT agent_run_id FROM routine_budget_grants WHERE run_id=$1 AND parent_id IS NULL) lineage ORDER BY id LIMIT 201`, run.ID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(ids) > 200 {
		u.Truncated = true
		u.Gaps = append(u.Gaps, "run_lineage_limit")
		ids = ids[:200]
	}
	measurements, err := harness.ReadRunUsageTx(ctx, tx, p, ids)
	if err != nil {
		return err
	}
	u.Partial = u.Truncated || len(ids) == 0 || len(measurements) != len(ids)
	if len(ids) == 0 {
		u.Tokens.Unknown = 1
		u.Gaps = append(u.Gaps, "execution_usage_unknown")
	}
	if len(measurements) != len(ids) {
		u.Tokens.Unknown += len(ids) - len(measurements)
		u.Gaps = append(u.Gaps, "source_visibility_incomplete")
	}
	for _, v := range measurements {
		if err = addHistoryNumber(&u.Tokens.Measured, v.Tokens); err != nil {
			return err
		}
		if err = addHistoryNumber(&u.EstimatedAPIUSD, v.EstimatedAPIUSD); err != nil {
			return err
		}
		u.Tokens.Known += v.KnownModels
		u.Tokens.Unknown += v.UnknownModels
		u.Partial = u.Partial || v.Partial
		u.Truncated = u.Truncated || v.Truncated
	}
	if u.Partial {
		u.Gaps = append(u.Gaps, "measurement_incomplete")
	}
	// The balance contains settled ROOT grants only, already deduplicated by
	// immutable settlement receipts. Child/provider totals must not be added.
	var paid string
	var settled int
	var unsettled bool
	err = tx.QueryRow(ctx, `SELECT settled_microusd::text,held_tokens::text,held_microusd::text,held_ms::text,
 (SELECT count(*) FROM routine_budget_grants g WHERE g.run_id=b.run_id AND g.parent_id IS NULL AND g.state='settled'),
 EXISTS(SELECT 1 FROM routine_budget_grants g WHERE g.run_id=b.run_id AND g.parent_id IS NULL AND g.state<>'settled')
 FROM routine_budget_balances b WHERE run_id=$1`, run.ID).Scan(&paid, &u.ReservedTokens, &u.ReservedMicroUSD, &u.ReservedRecoveryMS, &settled, &unsettled)
	if errors.Is(err, pgx.ErrNoRows) {
		u.Partial = true
		u.Gaps = append(u.Gaps, "paid_usage_unknown")
	} else if err != nil {
		return err
	} else {
		if settled > 0 {
			u.PaidMicroUSD = &paid
		} else {
			u.Partial = true
			u.Gaps = append(u.Gaps, "paid_usage_unknown")
		}
		if unsettled {
			u.Partial = true
			u.Gaps = append(u.Gaps, "unsettled_allowance")
		}
	}
	run.Usage = u
	return nil
}
