// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/accountuse"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/escalation"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

type queueTarget = workqueue.RouteTarget

type queueUndoToken struct {
	RunID    string    `json:"run_id"`
	Revision time.Time `json:"revision"`
}
type queueEntry struct {
	Undo       *queueUndoToken   `json:"undo,omitempty"`
	NodeID     string            `json:"node_id"`
	ProjectID  *string           `json:"project_id"`
	Key        string            `json:"key"`
	Title      string            `json:"title"`
	State      string            `json:"state"`
	Priority   string            `json:"priority"`
	Hours      float64           `json:"estimate_hours"`
	Queued     *workqueue.Queued `json:"queued"`
	Run        Run               `json:"-"`
	VisibleRun *Run              `json:"run,omitempty"`
}
type queueCapacity struct {
	QueuedHours float64  `json:"queued_hours"`
	Parallel    int      `json:"parallel_runs"`
	WorkHours   *float64 `json:"work_hours"`
	Warning     bool     `json:"warning"`
}
type queuePage struct {
	Items    []queueEntry  `json:"items"`
	Count    int           `json:"count"`
	Manual   bool          `json:"manual_order"`
	Capacity queueCapacity `json:"capacity"`
}
type queueTicket struct {
	ID, Key, Title, Body, Kind, State string
	ProjectID                         *string
	Fields                            map[string]any
	Raw                               json.RawMessage
	Stale                             bool
	NamedBlocker                      bool
}
type queueError struct {
	Status    int
	Message   string
	Code      string
	Readiness *workqueue.Readiness
}

func (e *queueError) Error() string     { return e.Message }
func (e *queueError) HTTPStatus() int   { return e.Status }
func (e *queueError) ErrorCode() string { return e.Code }

// Same tree lock as nodes, work orders and status autopilot, after the shared tenant/tree/pairing entry and
// before order/run/account rows and the tenant event counter. Claim and queue edits
// serialize; an entry cannot be removed while pickup commits.
func queueLock(ctx context.Context, tx pgx.Tx) error {
	return db.LockWorkTreeTx(ctx, tx)
}
func (m *module) queueDeadline(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
	if m.queueTimeout != nil {
		return m.queueTimeout(ctx, duration)
	}
	return context.WithTimeout(ctx, duration)
}
func (m *module) mountQueue(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern string
		write   bool
		fn      func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"GET /api/queue", false, m.queueList},
		{"GET /api/queue/{nodeId}/readiness", false, m.queueReadiness},
		{"POST /api/queue/{nodeId}/estimate", true, m.queueEstimate},
		{"POST /api/queue/{nodeId}/undo", true, m.queueUndo},
		{"POST /api/queue", true, m.queueAdd},
		{"POST /api/queue/{nodeId}/snapshots", true, m.queueSnapshotCapture},
		{"GET /api/queue-snapshots/{snapshotId}", false, m.queueSnapshotGet},
		{"POST /api/queue-snapshots/{snapshotId}/apply", true, m.queueSnapshotApply},
		{"DELETE /api/queue-snapshots/{snapshotId}", true, m.queueSnapshotCancel},
		{"DELETE /api/queue/{nodeId}", true, m.queueRemove},
		{"POST /api/queue/{nodeId}/move", true, m.queueMove},
		{"POST /api/queue/reset", true, m.queueReset},
		{"POST /api/queue/next", true, m.queueNext},
	} {
		mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if strings.Contains(route.pattern, "snapshot") {
				ctx, cancel := m.queueDeadline(r.Context(), 5*time.Second)
				defer cancel()
				r = r.WithContext(ctx)
				if id := r.PathValue("snapshotId"); id != "" && !workorders.UUID(id) {
					httpapi.WriteError(w, 400, "invalid snapshot id")
					return
				}
			}
			p, ok := tenant.PrincipalFrom(r.Context())
			if !ok {
				httpapi.WriteError(w, 401, "unauthorized")
				return
			}
			if id := r.PathValue("nodeId"); id != "" && !workorders.UUID(id) {
				httpapi.WriteError(w, 400, "invalid node id")
				return
			}
			ctx, cancel := m.queueDeadline(r.Context(), 30*time.Second)
			defer cancel()
			r = r.WithContext(ctx)
			// Decode only buffered memory inside the write, never transport
			// input while retaining tenant/tree/pairing fences.
			if err := workorders.BufferEndpointBody(w, r); err != nil {
				workorders.WriteError(w, err)
				return
			}
			var out any
			err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
				if err := authz.RequireTx(r.Context(), tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
					return err
				}
				if p.Kind == tenant.Agent {
					if err := authz.RequireQueueCoordinatorEntryTx(r.Context(), tx, p); err != nil {
						return err
					}
				}
				if route.write {
					if err := agentpairing.LockMutation(r.Context(), tx); err != nil {
						return err
					}
				}
				if route.write {
					if err := accountuse.LockShared(r.Context(), tx); err != nil {
						return err
					}
				}
				var err error
				out, err = route.fn(r, tx, p)
				return err
			})
			if err != nil {
				var qe *queueError
				if errors.As(err, &qe) && qe.Readiness != nil {
					httpapi.WriteJSON(w, qe.Status, map[string]any{"error": qe.Message, "code": qe.Code, "readiness": qe.Readiness})
					return
				}
				workorders.WriteError(w, err)
				return
			}
			httpapi.WriteJSON(w, 200, out)
		})
	}
}
func queuePermission(ctx context.Context, tx pgx.Tx, p tenant.Principal, project *string, write bool) error {
	id := ""
	if project != nil {
		id = *project
	}
	if p.Kind == tenant.Agent {
		return authz.RequireQueueCoordinatorTx(ctx, tx, p, id)
	}
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	permission := "nodes.read"
	if write {
		permission = "run.create"
	}
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: id})
}
func queueLoadTicket(ctx context.Context, tx pgx.Tx, id string, lock bool) (queueTicket, error) {
	q := `SELECT n.id::text,n.key,n.title,n.body,n.state,n.project_id::text,n.fields,k.slug,to_jsonb(n),
 EXISTS(SELECT 1 FROM node_relations r JOIN nodes b ON b.tenant_id=r.tenant_id AND b.id=r.source_node_id WHERE r.target_node_id=n.id AND r.type='blocks' AND b.deleted_at IS NULL)
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF n`
	}
	var t queueTicket
	var fields []byte
	err := tx.QueryRow(ctx, q, id).Scan(&t.ID, &t.Key, &t.Title, &t.Body, &t.State, &t.ProjectID, &fields, &t.Kind, &t.Raw, &t.NamedBlocker)
	t.Fields = workqueue.Fields(fields)
	for _, key := range []string{"blocker", "blocked_by"} {
		if s, ok := t.Fields[key].(string); ok {
			s = strings.TrimSpace(s)
			if s != "" && !strings.EqualFold(s, "unnamed") {
				t.NamedBlocker = true
			}
		}
	}
	if err == nil && workqueue.Progress(t.State) {
		var stale map[string]bool
		stale, err = workqueue.Stale(ctx, tx, []string{id})
		t.Stale = stale[id]
	}
	if err == nil && t.Kind == "work" {
		var leaf bool
		err = tx.QueryRow(ctx, `SELECT aeon_work_leaf($1::uuid) AND aeon_work_pending($1::uuid) IS NULL`, id).Scan(&leaf)
		if !leaf {
			t.Kind = "parent"
		}
	}
	return t, err
}
func readiness(t queueTicket) workqueue.Readiness {
	return workqueue.Check(t.Kind, t.State, t.Title, t.Body, t.Fields, t.NamedBlocker, t.Stale)
}
func (m *module) queueReadiness(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	t, err := queueLoadTicket(r.Context(), tx, r.PathValue("nodeId"), false)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(r.Context(), tx, p, t.ProjectID, false); err != nil {
		return nil, err
	}
	return readiness(t), nil
}
func (m *module) queueEstimate(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Hours float64 `json:"estimate_hours"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	t, err := queueLoadTicket(r.Context(), tx, r.PathValue("nodeId"), true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(r.Context(), tx, p, t.ProjectID, true); err != nil {
		return nil, err
	}
	if !readiness(t).Queueable {
		return nil, workorders.Fail(409, "ticket is not queueable")
	}
	if workqueue.Hours(map[string]any{"estimate_hours": in.Hours}) == 0 {
		return nil, workorders.Fail(400, "estimate_hours must be greater than 0 and at most 200")
	}
	if workqueue.Hours(t.Fields) > 0 {
		return readiness(t), nil
	}
	t.Fields["estimate_hours"] = in.Hours
	t.Fields["estimate_source"] = string(p.Kind)
	t.Fields["estimate_by"] = p.ID
	t.Fields["estimate_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	t.Fields["estimate_confirmed"] = p.Kind == tenant.Person
	if err = queueUpdateTicket(r.Context(), tx, p, t, "queue.estimate_applied"); err != nil {
		return nil, err
	}
	return readiness(t), nil
}
func queueUpdateTicket(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, event string, runID ...string) error {
	change, _, err := queueUpdateTicketDeferred(ctx, tx, t, event, runID...)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, change)
	return err
}

func queueUpdateTicketDeferred(ctx context.Context, tx pgx.Tx, t queueTicket, event string, runID ...string) (events.Change, time.Time, error) {
	raw, err := json.Marshal(t.Fields)
	if err != nil {
		return events.Change{}, time.Time{}, err
	}
	var after json.RawMessage
	var revision time.Time
	err = tx.QueryRow(ctx, `UPDATE nodes SET state=$2,fields=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes),updated_at`, t.ID, t.State, raw).Scan(&after, &revision)
	if err != nil {
		return events.Change{}, time.Time{}, err
	}
	change := events.Change{NodeID: &t.ID, Type: event, Before: t.Raw, After: after}
	if len(runID) > 0 {
		change.Metadata, _ = json.Marshal(map[string]string{"run_id": runID[0]})
	}
	return change, revision, nil
}

func (m *module) queueAdd(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		NodeID string `json:"node_id"`
		queueTarget
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.NodeID) {
		return nil, workorders.Fail(400, "node_id required")
	}
	if err := validateQueueTarget(in.queueTarget, false); err != nil {
		return nil, err
	}
	ctx := r.Context()
	t, err := queueLoadTicket(ctx, tx, in.NodeID, true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return nil, err
	}
	prepared, err := m.queuePrepare(ctx, tx, p, t, in.queueTarget)
	if err != nil {
		return nil, err
	}
	for _, change := range prepared.Changes {
		if _, err = events.Append(ctx, tx, p, change); err != nil {
			return nil, err
		}
	}
	entry, err := m.queueEntry(ctx, tx, t.ID)
	entry.Undo = prepared.Undo
	return entry, err
}

type queuePrepared struct {
	Run     Run
	Changes []events.Change
	Undo    *queueUndoToken
}

// queuePrepare shares single-leaf eligibility and creation with parent snapshots.
// It does not acquire the event counter; composite callers flush audit last.
func (m *module) queuePrepare(ctx context.Context, tx pgx.Tx, p tenant.Principal, t queueTicket, in queueTarget) (queuePrepared, error) {
	if err := queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return queuePrepared{}, err
	}
	if err := escalation.CheckLaunchTx(ctx, tx, t.ID, "", "", nil); err != nil {
		return queuePrepared{}, err
	}
	existing, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM agent_runs WHERE queue_node_id=$1 AND status IN ('queued','starting','running','waiting')`, t.ID))
	if err == nil {
		if existing.Status != "queued" {
			return queuePrepared{}, workorders.Fail(409, "ticket already has an active run")
		}
		if !sameTarget(existing, in) {
			return queuePrepared{}, workorders.Fail(409, "ticket already queued with another target; remove it first")
		}
		return queuePrepared{Run: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return queuePrepared{}, err
	}
	// Preserve the existing readiness refusal before writer exclusion. Both
	// checks remain under the tenant/tree fence and precede every queue write.
	ready := readiness(t)
	if !ready.Ready {
		return queuePrepared{}, &queueError{Status: 422, Message: "Not ready to queue: " + strings.Join(ready.Missing, ", "), Code: "queue_not_ready", Readiness: &ready}
	}
	if err = requireWriterFree(ctx, tx, t.ID, ""); err != nil {
		return queuePrepared{}, err
	}
	if in.Account != nil {
		projectID := ""
		if t.ProjectID != nil {
			projectID = *t.ProjectID
		}
		if err := accountuse.RequireProject(ctx, tx, *in.Account, projectID); err != nil {
			return queuePrepared{}, err
		}
	}
	if in.Agent != "" {
		if err = queueValidateTarget(ctx, tx, in); err != nil {
			return queuePrepared{}, err
		}
	}
	agent := in.Agent
	if agent == "" {
		// A tenant-keyed, keyless principal preserves the legacy NOT NULL contract.
		// Unrouted ticket runs are fenced out of polling, reservation and claim.
		agent = p.TenantID
		_, err = tx.Exec(ctx, `INSERT INTO principals(id,tenant_id,kind,name) VALUES($1,$1,'agent','Next free agent (queue holder)') ON CONFLICT(id) DO NOTHING`, agent)
		if err != nil {
			return queuePrepared{}, err
		}
		var inert bool
		err = tx.QueryRow(ctx, `SELECT kind='agent' AND name='Next free agent (queue holder)' AND NOT EXISTS(SELECT 1 FROM agent_keys WHERE principal_id=$1) FROM principals WHERE id=$1`, agent).Scan(&inert)
		if err != nil {
			return queuePrepared{}, err
		}
		if !inert {
			return queuePrepared{}, workorders.Fail(409, "queue holder identity conflicts")
		}
	}
	o, changes, err := workorders.CreateDeferred(ctx, tx, p, workorders.CreateInput{Title: t.Title, Body: t.Body, Parent: &t.ID, Criteria: workqueue.Criteria(t.Fields)})
	if err != nil {
		return queuePrepared{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE work_orders SET status='ready',revision=revision+1 WHERE node_id=$1`, o.NodeID)
	if err != nil {
		return queuePrepared{}, err
	}
	person := modelprefs.PrefsPerson(ctx, tx, p)
	requirement, trace, err := modelprefs.OrderRequirementTrace(ctx, tx, o.NodeID, person)
	if err != nil {
		return queuePrepared{}, err
	}
	if in.Account != nil {
		var now time.Time
		if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
			return queuePrepared{}, err
		}
		qualifies, err := agentaccounts.AccountMeetsResidency(ctx, tx, *in.Account, in.Profile, requirement, now)
		if err != nil {
			return queuePrepared{}, err
		}
		if !qualifies {
			return queuePrepared{}, &modelprefs.ResidencyUnmet{}
		}
	}
	var model *string
	if in.Profile != "" {
		if err = tx.QueryRow(ctx, `SELECT model FROM model_profiles WHERE id=$1 AND enabled`, in.Profile).Scan(&model); err != nil {
			return queuePrepared{}, err
		}
	}
	var rank *int
	if in.Agent == "" {
		if rank, err = workqueue.AppendRank(ctx, tx); err != nil {
			return queuePrepared{}, err
		}
	}
	v, err := scan(tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,queue_node_id,queue_by_principal_id,queue_at,queue_target_agent_id,queue_rank,queue_security_review_required,residency,prefs_person_id,trace)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp(),$9,$10,$11,$12,$13,$14) RETURNING `+columns, p.TenantID, o.NodeID, agent, optional(in.Profile), model, in.Account, t.ID, p.ID, optional(in.Agent), rank, ready.SecurityReviewRequired, modelprefs.Stamp(requirement), person, trace))
	if err != nil {
		return queuePrepared{}, err
	}
	if workqueue.State(t.State) != "blocked" {
		t.State = "open"
	}
	if ready.SecurityReviewRequired {
		t.Fields["security_review_required"] = true
		t.Fields["needs_review"] = true
		t.Fields["review_route"] = "review-gate"
	}
	change, revision, err := queueUpdateTicketDeferred(ctx, tx, t, "queue.added", v.ID)
	if err != nil {
		return queuePrepared{}, err
	}
	changes = append(changes, change)
	changes = append(changes, events.Change{NodeID: &o.NodeID, Type: "run.created", After: v})
	prepared := queuePrepared{Run: v, Changes: changes}
	if ready.Stale {
		prepared.Undo = &queueUndoToken{RunID: v.ID, Revision: revision}
	}
	return prepared, nil
}

func sameTarget(v Run, in queueTarget) bool {
	agent := ""
	if v.QueueTargetAgentID != nil {
		agent = *v.QueueTargetAgentID
	}
	if agent != in.Agent {
		return false
	}
	if agent == "" {
		return true
	}
	profile := ""
	if v.ProfileID != nil {
		profile = *v.ProfileID
	}
	account := ""
	if v.RequestedAccountID != nil {
		account = *v.RequestedAccountID
	}
	requested := ""
	if in.Account != nil {
		requested = *in.Account
	}
	return profile == in.Profile && account == requested
}
func validateQueueTarget(in queueTarget, next bool) error {
	if in.Agent != "" && !workorders.UUID(in.Agent) || in.Profile != "" && !workorders.UUID(in.Profile) || in.Account != nil && !workorders.UUID(*in.Account) {
		return workorders.Fail(400, "invalid queue target")
	}
	if in.Account != nil && in.Agent == "" || in.Agent != "" && in.Profile == "" || !next && in.Profile != "" && in.Agent == "" {
		return workorders.Fail(400, "targeted work requires agent and model profile")
	}
	return nil
}
func queueValidateTarget(ctx context.Context, tx pgx.Tx, in queueTarget) error {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals p JOIN model_profiles m ON m.tenant_id=p.tenant_id
 WHERE p.id=$1 AND p.kind='agent' AND m.id=$2 AND m.enabled AND EXISTS(SELECT 1 FROM agent_accounts a
 WHERE a.registered_by_principal_id=p.id AND a.harness=m.harness AND ($3::uuid IS NULL OR a.id=$3)
 AND aeon_account_allows_profile(a.harness,a.allowed_model_profile_ids,m.id)))`, in.Agent, in.Profile, in.Account).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return workorders.Fail(409, "target agent and account must allow the model profile")
	}
	return nil
}
func (m *module) queueRemove(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	t, err := queueLoadTicket(ctx, tx, r.PathValue("nodeId"), true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return nil, err
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM agent_runs WHERE queue_node_id=$1 AND status IN ('queued','starting','running','waiting')`, t.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]bool{"removed": false}, nil
	}
	if err != nil {
		return nil, err
	}
	v, _, err := lockRun(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if v.Status != "queued" {
		return nil, workorders.Fail(409, "run has already started")
	}
	removed, err := workqueue.RemoveQueued(ctx, tx, p, t.ID)
	return map[string]bool{"removed": removed}, err
}
