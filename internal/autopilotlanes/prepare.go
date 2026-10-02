// SPDX-License-Identifier: AGPL-3.0-only
package autopilotlanes

import (
	"context"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

type prepareInput struct {
	ExpectedRevision       int64     `json:"expected_revision"`
	TicketNodeID           string    `json:"ticket_node_id"`
	ExpectedTicketRevision time.Time `json:"expected_ticket_revision"`
}
type PreparationRequest struct {
	ID                     string    `json:"id"`
	LaneID                 string    `json:"lane_id"`
	ProjectID              string    `json:"project_id"`
	TicketNodeID           string    `json:"ticket_node_id"`
	TicketRevision         time.Time `json:"ticket_revision"`
	LaneRevision           int64     `json:"lane_revision"`
	RequestedByPrincipalID string    `json:"requested_by_principal_id"`
	Preparation            string    `json:"preparation"`
	Status                 string    `json:"status"`
	Launched               bool      `json:"launched"`
	CreatedAt              time.Time `json:"created_at"`
}

func (m *Module) prepare(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in prepareInput
	if err := decode(w, r, &in); err != nil {
		respond(w, 202, nil, err)
		return
	}
	if in.ExpectedRevision < 1 || in.ExpectedTicketRevision.IsZero() || !uuid.MatchString(in.TicketNodeID) {
		respond(w, 202, nil, fail(400, "lane revision, ticket id and ticket revision required"))
		return
	}
	var out PreparationRequest
	err := m.transaction(r, p, true, func(ctx context.Context, tx pgx.Tx) error {
		l, err := load(ctx, tx, r.PathValue("laneId"), true)
		if err != nil {
			return err
		}
		if err = permission(ctx, tx, p, "autopilot.manage", l.ProjectID); err != nil {
			return err
		}
		if l.Revision != in.ExpectedRevision {
			return fail(409, "lane changed; reload")
		}
		if err = executionAuthority(ctx, tx, p, l); err != nil {
			return err
		}
		if l.Scope.Kind == "release" {
			if err = candidateInScope(ctx, tx, l, in.TicketNodeID); err != nil {
				return err
			}
		}
		var kind, state, title, body string
		var fields []byte
		var revision time.Time
		if err = tx.QueryRow(ctx, `SELECT k.slug,n.state,left(n.title,512),left(n.body,2048),left(n.fields::text,65537),n.updated_at FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL FOR NO KEY UPDATE OF n`, in.TicketNodeID, l.ProjectID).Scan(&kind, &state, &title, &body, &fields, &revision); err != nil {
			return err
		}
		if !revision.Equal(in.ExpectedTicketRevision) {
			return fail(409, "ticket changed; reload")
		}
		if len(fields) > 65536 {
			return fail(409, "ticket fields exceed preparation bound")
		}
		f := workqueue.Fields(fields)
		if !matches(l.Policy, kind, f) || !workqueue.Check(kind, state, title, body, f, false).Queueable || workqueue.Hours(f) > l.Policy.MaxTicketEstimateHours {
			return fail(409, "ticket outside lane policy")
		}
		how := preparation(f)
		if how == "none" {
			return fail(409, "ticket already has an estimate and criteria")
		}
		// Immutable intent dedupes the reviewed lane/ticket revisions. It does not
		// claim dispatch ownership; the scheduler must qualify it before acting.
		var inserted bool
		err = tx.QueryRow(ctx, `INSERT INTO autopilot_preparation_requests(tenant_id,project_id,lane_id,ticket_node_id,ticket_revision,lane_revision,requested_by_principal_id,preparation) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(tenant_id,lane_id,ticket_node_id,ticket_revision,lane_revision) DO NOTHING RETURNING true`, p.TenantID, l.ProjectID, l.ID, in.TicketNodeID, revision, l.Revision, p.ID, how).Scan(&inserted)
		if err != nil && err != pgx.ErrNoRows {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id::text,lane_id::text,project_id::text,ticket_node_id::text,ticket_revision,lane_revision,requested_by_principal_id::text,preparation,status,created_at FROM autopilot_preparation_requests WHERE lane_id=$1 AND ticket_node_id=$2 AND ticket_revision=$3 AND lane_revision=$4`, l.ID, in.TicketNodeID, revision, l.Revision).Scan(&out.ID, &out.LaneID, &out.ProjectID, &out.TicketNodeID, &out.TicketRevision, &out.LaneRevision, &out.RequestedByPrincipalID, &out.Preparation, &out.Status, &out.CreatedAt)
		if err != nil || !inserted {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &l.ID, Type: "node.autopilot_preparation_requested", After: out})
		return err
	})
	respond(w, 202, out, err)
}
