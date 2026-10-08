// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const (
	flowItemLimit     = 50
	flowStepLimit     = 4000
	flowIncidentLimit = 200
)

// flowItemInput names a run. Unset optional fields keep the stored value.
type flowItemInput struct {
	Project, Kind, Ref, Title string
	Ticket                    *string
	PRs                       []int64
	Started, Ended            *time.Time
	Target                    *FlowTarget
	// SetGate and SetETA replace the stored gate and OPS estimate, also with nil.
	SetGate        bool
	Gate           *FlowGate
	SetETA         bool
	OpsP50, OpsP90 *time.Time
	// NoCreate records nothing for a run never seen open: a close, end or
	// merge of work that started before the Flow observed it.
	NoCreate bool
	// Repository scopes pull-request numbers. A number alone is not a delivery.
	Repository string
}

// flowStepInput is one reported step. Merge keeps the earliest start and an
// already recorded end or outcome when this report carries none: PAIMOS and
// GitHub report a step's start and end in separate events. CloseOnly never
// creates a row (an end without a recorded start is not a fact).
type flowStepInput struct {
	Source, Key string
	StepKey     string
	Round       int
	Kind        string
	Actor       FlowActor
	Started     time.Time
	Ended       *time.Time
	Outcome     *string
	WaitReason  *string
	WaitsFor    *string
	Side        bool
	Merge       bool
	CloseOnly   bool
	// Progress is a live report of one step. While the stored step is open,
	// the report replaces it. A report with no end never reopens a step that
	// has already ended, so a late queued delivery stays idempotent.
	Progress bool
	// Phase is the live rank: queued, then running, then completed. A queued
	// report never overwrites a row a later phase already wrote.
	Phase string
}

type flowIncidentInput struct {
	Source, Key       string
	Started           time.Time
	Ended             *time.Time
	Severity, Summary string
	RecoveryKeys      []string
}

type flowBatch struct {
	Item      flowItemInput
	Steps     []flowStepInput
	Incidents []flowIncidentInput
}

// flowWrite runs one flow write: tenant fence, then items, steps and
// incidents, then one event per changed row, last. build runs under the
// fence and may authorize or read (the API checks RequireTx there).
// actor nil appends as the workspace system actor.
func (m *Module) flowWrite(ctx context.Context, tid string, actor *tenant.Principal, build func(context.Context, pgx.Tx, func([]flowBatch) error) error) (int, error) {
	changed := 0
	err := db.InTenant(ctx, m.pool, tid, func(tx pgx.Tx) error {
		changed = 0
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		var changes []events.Change
		apply := func(batches []flowBatch) error {
			out, err := applyFlowTx(ctx, tx, tid, batches, m.now())
			changes = append(changes, out...)
			return err
		}
		if err := build(ctx, tx, apply); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		p := tenant.Principal{}
		if actor != nil {
			p = *actor
		} else {
			var err error
			if p, err = systemactor.Ensure(ctx, tx, tid); err != nil {
				return err
			}
		}
		// Event counter last: every row above is already written.
		for _, c := range changes {
			if _, err := events.Append(ctx, tx, p, c); err != nil {
				return err
			}
		}
		changed = len(changes)
		return nil
	})
	return changed, err
}

// flowChange is a value-free live hint: ids only, never step content.
func flowChange(project, typ string, after map[string]string) events.Change {
	return events.Change{NodeID: &project, Type: typ, After: after}
}

func applyFlowTx(ctx context.Context, tx pgx.Tx, tid string, batches []flowBatch, now time.Time) ([]events.Change, error) {
	var changes []events.Change
	for _, b := range batches {
		in := b.Item
		id := flowItemID(tid, in.Project, in.Kind, in.Ref)
		before, err := loadFlowItemTx(ctx, tx, id, true)
		if err != nil {
			return changes, err
		}
		opens := len(b.Incidents) > 0 || slices.ContainsFunc(b.Steps, func(s flowStepInput) bool { return !s.CloseOnly })
		if before == nil && !opens && (in.NoCreate || len(b.Steps) > 0) {
			// Only closing reports for a run never seen start: nothing to record.
			continue
		}
		after := flowItemRow{ID: id, Project: in.Project, Kind: in.Kind, Ref: in.Ref, Title: in.Title, Ticket: in.Ticket, PRs: in.PRs}
		if before != nil {
			after = *before
			if in.Title != "" {
				after.Title = in.Title
			}
			if in.Ticket != nil {
				after.Ticket = in.Ticket
			}
			after.PRs = append(slices.Clone(before.PRs), in.PRs...)
		}
		after.PRs = sortedPRs(after.PRs)
		if len(after.PRs) > 20 {
			after.PRs = after.PRs[len(after.PRs)-20:]
		}
		if after.Title == "" {
			after.Title = in.Ref
		}
		if in.Started != nil && (after.Started == nil || in.Started.Before(*after.Started)) {
			v := flowTime(*in.Started)
			after.Started = &v
		}
		if in.Ended != nil {
			v := flowTime(*in.Ended)
			after.Ended = &v
		}
		if in.Target != nil {
			t := *in.Target
			after.Target = &t
		}
		if in.SetGate {
			after.Gate = in.Gate
		}
		if in.SetETA {
			after.OpsP50, after.OpsP90 = flowTimePtr(in.OpsP50), flowTimePtr(in.OpsP90)
		}
		// A merge projected before this row existed is still the linked
		// delivery's current state. Late creation must not leave it open.
		if before == nil && in.Kind == "change" && after.Ended == nil {
			mergedAt, err := flowMergedAtTx(ctx, tx, in.Project, in.Repository, in.Ticket, after.PRs)
			if err != nil {
				return changes, err
			}
			if mergedAt != nil {
				v := flowTime(*mergedAt)
				after.Ended = &v
			}
		}
		for _, s := range b.Steps {
			// A late queued report must not pull a running change back to the
			// run's creation time. The step write rejects that phase too.
			if s.Phase == "queued" && after.Started != nil && flowTime(s.Started).Before(*after.Started) {
				continue
			}
			if !s.CloseOnly && (after.Started == nil || s.Started.Before(*after.Started)) {
				v := flowTime(s.Started)
				after.Started = &v
			}
		}
		itemChanged := before == nil || !reflect.DeepEqual(*before, after)
		if itemChanged {
			if err := saveFlowItemTx(ctx, tx, tid, after, now); err != nil {
				return changes, err
			}
		}
		for _, s := range b.Steps {
			changed, stepID, err := applyFlowStepTx(ctx, tx, tid, after, s, now)
			if err != nil {
				return changes, err
			}
			if changed {
				changes = append(changes, flowChange(after.Project, "delivery.step", map[string]string{"item_id": id, "step_id": stepID}))
			}
		}
		for _, inc := range b.Incidents {
			changed, incID, err := applyFlowIncidentTx(ctx, tx, tid, after, inc, now)
			if err != nil {
				return changes, err
			}
			if changed {
				changes = append(changes, flowChange(after.Project, "delivery.incident", map[string]string{"item_id": id, "incident_id": incID}))
			}
		}
		if itemChanged {
			changes = append(changes, flowChange(after.Project, "delivery.item", map[string]string{"item_id": id}))
		}
	}
	return changes, nil
}

func saveFlowItemTx(ctx context.Context, tx pgx.Tx, tid string, i flowItemRow, now time.Time) error {
	var minutes *int
	var from, source, gateWhat *string
	var gatePrincipal *string
	if i.Target != nil {
		minutes, from, source = &i.Target.Minutes, &i.Target.FromStep, &i.Target.Source
	}
	if i.Gate != nil {
		gatePrincipal, gateWhat = i.Gate.PrincipalID, &i.Gate.What
	}
	_, err := tx.Exec(ctx, `INSERT INTO delivery_flow_items(tenant_id,project_id,id,kind,ref,ticket_node_id,title,prs,started_at,ended_at,target_minutes,target_from_step,target_source,gate_principal_id,gate_what,ops_eta_p50_at,ops_eta_p90_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT(tenant_id,id) DO UPDATE SET ticket_node_id=EXCLUDED.ticket_node_id,title=EXCLUDED.title,prs=EXCLUDED.prs,started_at=EXCLUDED.started_at,ended_at=EXCLUDED.ended_at,
		target_minutes=EXCLUDED.target_minutes,target_from_step=EXCLUDED.target_from_step,target_source=EXCLUDED.target_source,gate_principal_id=EXCLUDED.gate_principal_id,gate_what=EXCLUDED.gate_what,
		ops_eta_p50_at=EXCLUDED.ops_eta_p50_at,ops_eta_p90_at=EXCLUDED.ops_eta_p90_at,updated_at=EXCLUDED.updated_at`,
		tid, i.Project, i.ID, i.Kind, i.Ref, i.Ticket, i.Title, i.PRs, i.Started, i.Ended, minutes, from, source, gatePrincipal, gateWhat, i.OpsP50, i.OpsP90, now)
	return err
}

const flowItemColumns = `id::text,project_id::text,kind,ref,ticket_node_id::text,title,prs,started_at,ended_at,target_minutes,target_from_step,target_source,gate_principal_id::text,gate_what,ops_eta_p50_at,ops_eta_p90_at`

func scanFlowItem(row pgx.Row) (flowItemRow, error) {
	var i flowItemRow
	var minutes *int
	var from, source, gatePrincipal, gateWhat *string
	err := row.Scan(&i.ID, &i.Project, &i.Kind, &i.Ref, &i.Ticket, &i.Title, &i.PRs, &i.Started, &i.Ended, &minutes, &from, &source, &gatePrincipal, &gateWhat, &i.OpsP50, &i.OpsP90)
	if err != nil {
		return i, err
	}
	if minutes != nil && from != nil && source != nil {
		i.Target = &FlowTarget{Minutes: *minutes, FromStep: *from, Source: *source}
	}
	if gateWhat != nil {
		i.Gate = &FlowGate{PrincipalID: gatePrincipal, What: *gateWhat}
	}
	if i.PRs == nil {
		i.PRs = []int64{}
	}
	for _, t := range []*time.Time{i.Started, i.Ended, i.OpsP50, i.OpsP90} {
		if t != nil {
			*t = t.UTC()
		}
	}
	return i, nil
}

// flowMergedAtTx is the linked delivery's merge time, when that delivery is
// already merged. A pull-request number is repository-scoped, so the row is
// the delivery for this repository, project and ticket. Another repository's
// merged pull request must not close this change. Without a pull request,
// the ticket's own merged delivery is the link.
func flowMergedAtTx(ctx context.Context, tx pgx.Tx, project, repository string, ticket *string, prs []int64) (*time.Time, error) {
	var at time.Time
	var err error
	switch {
	case len(prs) > 0 && repository != "" && project != "" && ticket != nil && *ticket != "":
		err = tx.QueryRow(ctx, `SELECT state_since FROM delivery_items WHERE state='merged' AND repository=$1 AND project_id=$2 AND ticket_node_id=$3 AND pull_request = ANY($4::bigint[]) ORDER BY state_since DESC LIMIT 1`, repository, project, *ticket, prs).Scan(&at)
	case len(prs) == 0 && ticket != nil && *ticket != "":
		err = tx.QueryRow(ctx, `SELECT state_since FROM delivery_items WHERE state='merged' AND ticket_node_id=$1 ORDER BY state_since DESC LIMIT 1`, *ticket).Scan(&at)
	default:
		return nil, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at = at.UTC()
	return &at, nil
}

func loadFlowItemTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*flowItemRow, error) {
	q := `SELECT ` + flowItemColumns + ` FROM delivery_flow_items WHERE id=$1`
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	i, err := scanFlowItem(tx.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

const flowStepColumns = `id::text,item_id::text,step_key,round,kind,actor_type,actor_principal_id::text,actor_label,actor_model,started_at,ended_at,outcome,wait_reason,waits_for::text,side,source`

func scanFlowStep(row pgx.Row) (FlowStep, error) {
	var s FlowStep
	err := row.Scan(&s.ID, &s.ItemID, &s.StepKey, &s.Round, &s.Kind, &s.Actor.Type, &s.Actor.PrincipalID, &s.Actor.Label, &s.Actor.Model, &s.StartedAt, &s.EndedAt, &s.Outcome, &s.WaitReason, &s.WaitsFor, &s.Side, &s.Source)
	s.StartedAt = s.StartedAt.UTC()
	if s.EndedAt != nil {
		v := s.EndedAt.UTC()
		s.EndedAt = &v
	}
	return s, err
}

func applyFlowStepTx(ctx context.Context, tx pgx.Tx, tid string, item flowItemRow, in flowStepInput, now time.Time) (bool, string, error) {
	id := flowStepID(tid, item.ID, in.Source, in.Key)
	before, err := scanFlowStep(tx.QueryRow(ctx, `SELECT `+flowStepColumns+` FROM delivery_flow_steps WHERE id=$1 FOR NO KEY UPDATE`, id))
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, id, err
	}
	if !exists && in.CloseOnly {
		return false, id, nil
	}
	// queued → running → completed. A queued redelivery must not replace a
	// running start with the run's creation time, or reopen a finished step.
	if exists && in.Phase == "queued" {
		return false, id, nil
	}
	if exists && in.Progress && before.EndedAt != nil && in.Ended == nil {
		return false, id, nil
	}
	after := FlowStep{ID: id, ItemID: item.ID, StepKey: in.StepKey, Round: in.Round, Kind: in.Kind, Actor: in.Actor, StartedAt: flowTime(in.Started), EndedAt: flowTimePtr(in.Ended), Outcome: in.Outcome, WaitReason: in.WaitReason, WaitsFor: in.WaitsFor, Side: in.Side, Source: in.Source}
	if exists && (in.Merge || in.CloseOnly) {
		if in.CloseOnly || before.StartedAt.Before(after.StartedAt) {
			after.StartedAt = before.StartedAt
		}
		if in.CloseOnly {
			after.StepKey, after.Round, after.Kind, after.Actor, after.WaitReason, after.WaitsFor, after.Side = before.StepKey, before.Round, before.Kind, before.Actor, before.WaitReason, before.WaitsFor, before.Side
		}
		// A close keeps the first recorded end and outcome: later events of
		// the same round (a verdict after the claim) must not move it.
		if after.EndedAt == nil || in.CloseOnly && before.EndedAt != nil {
			after.EndedAt = before.EndedAt
		}
		if after.Outcome == nil || in.CloseOnly && before.EndedAt != nil {
			after.Outcome = before.Outcome
		}
	}
	// Reports may arrive out of order; an end never precedes its start.
	if after.EndedAt != nil && after.EndedAt.Before(after.StartedAt) {
		v := after.StartedAt
		after.EndedAt = &v
	}
	if exists && reflect.DeepEqual(before, after) {
		return false, id, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_flow_steps(tenant_id,project_id,id,item_id,source,source_key,step_key,round,kind,actor_type,actor_principal_id,actor_label,actor_model,started_at,ended_at,outcome,wait_reason,waits_for,side,recorded_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20)
		ON CONFLICT(tenant_id,id) DO UPDATE SET step_key=EXCLUDED.step_key,round=EXCLUDED.round,kind=EXCLUDED.kind,actor_type=EXCLUDED.actor_type,actor_principal_id=EXCLUDED.actor_principal_id,
		actor_label=EXCLUDED.actor_label,actor_model=EXCLUDED.actor_model,started_at=EXCLUDED.started_at,ended_at=EXCLUDED.ended_at,outcome=EXCLUDED.outcome,wait_reason=EXCLUDED.wait_reason,
		waits_for=EXCLUDED.waits_for,side=EXCLUDED.side,updated_at=EXCLUDED.updated_at`,
		tid, item.Project, id, item.ID, in.Source, in.Key, after.StepKey, after.Round, after.Kind, after.Actor.Type, after.Actor.PrincipalID, after.Actor.Label, after.Actor.Model,
		after.StartedAt, after.EndedAt, after.Outcome, after.WaitReason, after.WaitsFor, after.Side, now)
	return err == nil, id, err
}

const flowIncidentColumns = `id::text,item_id::text,started_at,ended_at,severity,summary,recovery_step_ids::text[]`

func scanFlowIncident(row pgx.Row) (FlowIncident, error) {
	var i FlowIncident
	err := row.Scan(&i.ID, &i.ItemID, &i.StartedAt, &i.EndedAt, &i.Severity, &i.Summary, &i.RecoveryStepIDs)
	i.StartedAt = i.StartedAt.UTC()
	if i.EndedAt != nil {
		v := i.EndedAt.UTC()
		i.EndedAt = &v
	}
	if i.RecoveryStepIDs == nil {
		i.RecoveryStepIDs = []string{}
	}
	return i, err
}

func applyFlowIncidentTx(ctx context.Context, tx pgx.Tx, tid string, item flowItemRow, in flowIncidentInput, now time.Time) (bool, string, error) {
	id := flowIncidentID(tid, item.ID, in.Source, in.Key)
	before, err := scanFlowIncident(tx.QueryRow(ctx, `SELECT `+flowIncidentColumns+` FROM delivery_flow_incidents WHERE id=$1 FOR NO KEY UPDATE`, id))
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, id, err
	}
	after := FlowIncident{ID: id, ItemID: item.ID, StartedAt: flowTime(in.Started), EndedAt: flowTimePtr(in.Ended), Severity: in.Severity, Summary: in.Summary, RecoveryStepIDs: []string{}}
	for _, k := range in.RecoveryKeys {
		after.RecoveryStepIDs = append(after.RecoveryStepIDs, flowStepID(tid, item.ID, in.Source, k))
	}
	if exists && reflect.DeepEqual(before, after) {
		return false, id, nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO delivery_flow_incidents(tenant_id,project_id,id,item_id,source,source_key,started_at,ended_at,severity,summary,recovery_step_ids,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::uuid[],$12)
		ON CONFLICT(tenant_id,id) DO UPDATE SET started_at=EXCLUDED.started_at,ended_at=EXCLUDED.ended_at,severity=EXCLUDED.severity,summary=EXCLUDED.summary,recovery_step_ids=EXCLUDED.recovery_step_ids,updated_at=EXCLUDED.updated_at`,
		tid, item.Project, id, item.ID, in.Source, in.Key, after.StartedAt, after.EndedAt, after.Severity, after.Summary, after.RecoveryStepIDs, now)
	return err == nil, id, err
}

// loadFlowHistoryTx aggregates finished steps of the project in the 30 days
// before at. Percentiles are computed in SQL over every finished step, so the
// result is not bounded by a fetch limit.
func loadFlowHistoryTx(ctx context.Context, tx pgx.Tx, project string, at time.Time) (flowHistory, error) {
	h := flowHistory{}
	rows, err := tx.Query(ctx, `SELECT step_key,kind='wait',count(*),
		percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM ended_at-started_at)/60),
		percentile_cont(0.9) WITHIN GROUP (ORDER BY extract(epoch FROM ended_at-started_at)/60)
		FROM delivery_flow_steps WHERE project_id=$1 AND ended_at IS NOT NULL AND ended_at>$2 AND ended_at<=$3 GROUP BY 1,2`,
		project, at.Add(-flowHistoryDays*24*time.Hour), at)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k flowNormKey
		var s flowNormStat
		if err := rows.Scan(&k.Step, &k.Wait, &s.N, &s.P50, &s.P90); err != nil {
			return nil, err
		}
		s.P50, s.P90 = roundTenth(s.P50), roundTenth(s.P90)
		h[k] = s
	}
	return h, rows.Err()
}

// flowTime is the stored precision: UTC microseconds, so a replay compares equal.
func flowTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func flowTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := flowTime(*t)
	return &v
}

func roundTenth(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

// loadFlowStepsTx reads the steps of the given items, oldest first, bounded.
func loadFlowStepsTx(ctx context.Context, tx pgx.Tx, items []string, limit int) ([]FlowStep, bool, error) {
	out := []FlowStep{}
	rows, err := tx.Query(ctx, `SELECT `+flowStepColumns+` FROM delivery_flow_steps WHERE item_id=ANY($1::uuid[]) ORDER BY started_at,id LIMIT $2`, items, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		s, err := scanFlowStep(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, s)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

func loadFlowIncidentsTx(ctx context.Context, tx pgx.Tx, items []string) ([]FlowIncident, bool, error) {
	out := []FlowIncident{}
	rows, err := tx.Query(ctx, `SELECT `+flowIncidentColumns+` FROM delivery_flow_incidents WHERE item_id=ANY($1::uuid[]) ORDER BY started_at,id LIMIT $2`, items, flowIncidentLimit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		i, err := scanFlowIncident(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, i)
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > flowIncidentLimit {
		return out[:flowIncidentLimit], true, nil
	}
	return out, false, nil
}
