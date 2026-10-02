// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/lanedispatch"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

type preparationResultInput struct {
	ExpectedRevision int64    `json:"expected_revision"`
	EstimateHours    float64  `json:"estimate_hours"`
	Criteria         []string `json:"criteria"`
}
type acceptInput struct {
	ExpectedRevision int64 `json:"expected_revision"`
	Accept           *bool `json:"accept"`
}

func (m *Module) getDispatch(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var out lanedispatch.Request
	err := m.transaction(r, p, false, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = lanedispatch.Load(ctx, tx, r.PathValue("dispatchId"), false)
		if err != nil {
			return err
		}
		return permission(ctx, tx, p, "autopilot.read", out.ProjectID)
	})
	respond(w, 200, out, err)
}
func (m *Module) submitPreparation(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var in preparationResultInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 200, nil, err)
		return
	}
	if in.ExpectedRevision < 1 || !validHours(in.EstimateHours, 200) || len(in.Criteria) > 32 {
		respond(w, 200, nil, fail(400, "revision, finite estimate and at most 32 criteria required"))
		return
	}
	for i, s := range in.Criteria {
		in.Criteria[i] = strings.TrimSpace(s)
		if !boundedText(in.Criteria[i], 1000, false) {
			respond(w, 200, nil, fail(400, "criteria must be nonempty and at most 1000 bytes"))
			return
		}
	}
	var out lanedispatch.Request
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		d, l, fields, err := preparationContext(ctx, tx, p, r.PathValue("dispatchId"), in.ExpectedRevision, false)
		if err != nil {
			return err
		}
		if d.Phase != "preparation_requested" {
			return fail(409, "dispatch is not awaiting preparation")
		}
		if in.EstimateHours > l.Policy.MaxTicketEstimateHours {
			return fail(409, "estimate exceeds lane limit")
		}
		phase := "requested"
		changes := []events.Change{}
		if len(workqueue.Criteria(fields)) == 0 {
			if len(in.Criteria) == 0 {
				return fail(400, "missing criteria require a draft")
			}
			raw, _ := json.Marshal(in.Criteria)
			if _, err = tx.Exec(ctx, `UPDATE lane_dispatches SET phase='needs_person',draft_criteria=$2,draft_estimate_hours=$3,revision=revision+1 WHERE id=$1`, d.ID, raw, in.EstimateHours); err != nil {
				return err
			}
		} else {
			// Submitted criteria are deliberately ignored when human criteria exist.
			// Only a missing estimate can be filled automatically (decision 2 B).
			if workqueue.Hours(fields) == 0 {
				change, revision, err := applyPreparationFields(ctx, tx, p, d.TicketNodeID, fields, in.EstimateHours, nil, m.now().UTC())
				if err != nil {
					return err
				}
				changes = append(changes, change)
				d.TicketRevision = revision
			}
			if _, err = tx.Exec(ctx, `UPDATE lane_dispatches SET phase=$2,ticket_revision=$3,revision=revision+1 WHERE id=$1`, d.ID, phase, d.TicketRevision); err != nil {
				return err
			}
		}
		out, err = lanedispatch.Load(ctx, tx, d.ID, false)
		if err != nil {
			return err
		}
		changes = append(changes, events.Change{NodeID: &l.ID, Type: "node.autopilot_preparation_submitted", Before: d, After: out})
		return appendPreparationEvents(ctx, tx, p, changes)
	})
	respond(w, 200, out, err)
}
func (m *Module) acceptPreparation(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in acceptInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 200, nil, err)
		return
	}
	if in.ExpectedRevision < 1 || in.Accept == nil {
		respond(w, 200, nil, fail(400, "revision and accept required"))
		return
	}
	var out lanedispatch.Request
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		d, l, fields, err := preparationContext(ctx, tx, p, r.PathValue("dispatchId"), in.ExpectedRevision, true)
		if err != nil {
			return err
		}
		if d.Phase != "needs_person" {
			return fail(409, "dispatch is not awaiting a person")
		}
		phase := "cancelled"
		changes := []events.Change{}
		if *in.Accept {
			if len(workqueue.Criteria(fields)) > 0 {
				return fail(409, "ticket already has criteria; reload")
			}
			if len(d.DraftCriteria) == 0 || !validHours(d.DraftEstimateHours, l.Policy.MaxTicketEstimateHours) {
				return fail(409, "invalid preparation draft")
			}
			change, revision, err := applyPreparationFields(ctx, tx, p, d.TicketNodeID, fields, d.DraftEstimateHours, d.DraftCriteria, m.now().UTC())
			if err != nil {
				return err
			}
			changes = append(changes, change)
			d.TicketRevision = revision
			phase = "requested"
		}
		if _, err = tx.Exec(ctx, `UPDATE lane_dispatches SET phase=$2,ticket_revision=$3,revision=revision+1 WHERE id=$1`, d.ID, phase, d.TicketRevision); err != nil {
			return err
		}
		out, err = lanedispatch.Load(ctx, tx, d.ID, false)
		if err != nil {
			return err
		}
		changes = append(changes, events.Change{NodeID: &l.ID, Type: "node.autopilot_preparation_decided", Before: d, After: out})
		return appendPreparationEvents(ctx, tx, p, changes)
	})
	respond(w, 200, out, err)
}
func preparationContext(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, revision int64, accept bool) (lanedispatch.Request, Lane, map[string]any, error) {
	d, err := lanedispatch.Load(ctx, tx, id, false)
	if err != nil {
		return d, Lane{}, nil, err
	}
	if d.LaneID == nil {
		return d, Lane{}, nil, fail(409, "coordinator dispatch has no lane preparation")
	}
	l, err := load(ctx, tx, *d.LaneID, true)
	if err != nil {
		return d, l, nil, err
	}
	if err = permission(ctx, tx, p, "nodes.write", l.ProjectID); err != nil {
		return d, l, nil, err
	}
	if accept {
		err = permission(ctx, tx, p, "autopilot.manage", l.ProjectID)
	} else if p.Kind == tenant.Agent {
		err = authz.RequireQueueCoordinatorTx(ctx, tx, p, l.ProjectID)
	} else {
		err = permission(ctx, tx, p, "autopilot.manage", l.ProjectID)
	}
	if err != nil {
		return d, l, nil, err
	}
	if err = ownerAuthority(ctx, tx, p, l); err != nil {
		return d, l, nil, err
	}
	owner := tenant.Principal{ID: l.OwnerPrincipalID, TenantID: p.TenantID, Kind: tenant.Person}
	if err = permission(ctx, tx, owner, "nodes.write", l.ProjectID); err != nil {
		return d, l, nil, err
	}
	if d.LaneRevision != l.Revision {
		return d, l, nil, fail(409, "lane changed; preparation is stale")
	}
	if !l.Enabled || l.Paused {
		return d, l, nil, fail(409, "lane is disabled or paused")
	}
	if err = candidateInScope(ctx, tx, l, d.TicketNodeID); err != nil {
		return d, l, nil, err
	}
	var raw []byte
	var live time.Time
	var kind, state, title, body string
	err = tx.QueryRow(ctx, `SELECT left(n.fields::text,65537),n.updated_at,k.slug,n.state,left(n.title,512),left(n.body,2048) FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL FOR NO KEY UPDATE OF n`, d.TicketNodeID, l.ProjectID).Scan(&raw, &live, &kind, &state, &title, &body)
	if err != nil {
		return d, l, nil, err
	}
	if !live.Equal(d.TicketRevision) || len(raw) > 65536 {
		return d, l, nil, fail(409, "ticket changed; preparation is stale")
	}
	fields := workqueue.Fields(raw)
	item := candidate{id: d.TicketNodeID}
	facts := []candidate{item}
	if err = dependencyFacts(ctx, tx, facts); err != nil {
		return d, l, nil, err
	}
	if facts[0].blocked || workqueue.State(state) == "blocked" || !matches(l.Policy, kind, fields) || !workqueue.Check(kind, state, title, body, fields, false).Queueable {
		return d, l, nil, fail(409, "ticket no longer eligible")
	}
	d, err = lanedispatch.Load(ctx, tx, id, true)
	if err == nil && d.Revision != revision {
		err = fail(409, "dispatch changed; reload")
	}
	return d, l, fields, err
}
func applyPreparationFields(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, fields map[string]any, hours float64, criteria []string, now time.Time) (events.Change, time.Time, error) {
	before := map[string]any{}
	for k, v := range fields {
		before[k] = v
	}
	if workqueue.Hours(fields) == 0 {
		fields["estimate_hours"] = hours
		fields["estimate_source"] = string(p.Kind)
		fields["estimate_by"] = p.ID
		fields["estimate_at"] = now.Format(time.RFC3339Nano)
	}
	if len(criteria) > 0 {
		fields["acceptance_criteria"] = criteria
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return events.Change{}, time.Time{}, err
	}
	if len(raw) > 65536 {
		return events.Change{}, time.Time{}, fail(409, "prepared ticket exceeds field bound")
	}
	var rev time.Time
	err = tx.QueryRow(ctx, `UPDATE nodes SET fields=$2,updated_at=clock_timestamp() WHERE id=$1 RETURNING updated_at`, id, raw).Scan(&rev)
	return events.Change{NodeID: &id, Type: "node.autopilot_prepared", Before: before, After: fields}, rev, err
}
func appendPreparationEvents(ctx context.Context, tx pgx.Tx, p tenant.Principal, changes []events.Change) error {
	// All record writes and loads are done before the first event counter lock.
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return err
		}
	}
	return nil
}
