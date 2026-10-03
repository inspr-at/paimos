// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// journeyTicketSnapshot keeps the ticket ID even when its membership is
// removed by a move to a project without a journey.
type journeyTicketSnapshot struct {
	TicketID   string             `json:"ticket_id"`
	Membership *journeyMembership `json:"membership"`
}

// movedNodeSnapshot retains the node fields at the top level for existing
// node.moved readers and adds membership snapshots only when they change.
type movedNodeSnapshot struct {
	nodeJSON
	JourneyTickets []journeyTicketSnapshot `json:"journey_tickets,omitempty"`
	ShipsIn        []shipsMoveSnapshot     `json:"ships_in_before_move,omitempty"`
}

// subtreeJourneyTickets is called before reparenting while the tree is locked.
func subtreeJourneyTickets(ctx context.Context, tx pgx.Tx, id string) ([]journeyTicketSnapshot, error) {
	rows, err := tx.Query(ctx, `WITH RECURSIVE subtree AS (
	 SELECT id FROM nodes WHERE id=$1::uuid
	 UNION ALL SELECT n.id FROM nodes n JOIN subtree s ON n.parent_id=s.id WHERE n.deleted_at IS NULL
	) SELECT jt.ticket_node_id::text,n.project_id::text FROM journey_tickets jt
	 JOIN subtree s ON s.id=jt.ticket_node_id JOIN nodes n ON n.id=jt.ticket_node_id
 WHERE NOT EXISTS(SELECT 1 FROM project_delivery d WHERE d.project_node_id=jt.project_node_id)
	 ORDER BY jt.ticket_node_id`, id)
	if err != nil {
		return nil, err
	}
	type ticketSource struct {
		id      string
		project *string
	}
	var sources []ticketSource
	for rows.Next() {
		var ticketID string
		var project *string
		if err := rows.Scan(&ticketID, &project); err != nil {
			rows.Close()
			return nil, err
		}
		sources = append(sources, ticketSource{ticketID, project})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var tickets []journeyTicketSnapshot
	for _, source := range sources {
		membership, err := loadJourneyMembership(ctx, tx, source.id)
		if err != nil {
			return nil, err
		}
		if membership == nil || membership.ProjectID != deref(source.project) {
			return nil, conflict("issue journey membership differs from its project")
		}
		tickets = append(tickets, journeyTicketSnapshot{TicketID: source.id, Membership: membership})
	}
	return tickets, nil
}

// transferJourneyMembership is the one forward path for both node move routes.
// The caller holds the tree lock and runs inside db.InTenant.
func transferJourneyMembership(ctx context.Context, tx pgx.Tx, ticketID, sourceProject, targetProject string, before *journeyMembership) (*journeyMembership, error) {
	if before == nil || sourceProject == targetProject {
		return before, nil
	}
	if before.ProjectID != sourceProject {
		return nil, conflict("issue journey membership differs from its project")
	}
	var targetJourney bool
	if targetProject != "" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_projects WHERE project_node_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=$1::uuid))`, targetProject).Scan(&targetJourney); err != nil {
			return nil, err
		}
	}
	if targetJourney {
		if _, err := tx.Exec(ctx, `UPDATE journey_tickets SET project_node_id=$2::uuid,feature_node_id=NULL,release_node_id=NULL,
		 walker_position=(SELECT coalesce(max(walker_position),-1)+1 FROM journey_tickets WHERE project_node_id=$2::uuid),
		 source='manual',scope_revision_required=true WHERE ticket_node_id=$1::uuid`, ticketID, targetProject); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM journey_tickets WHERE ticket_node_id=$1::uuid`, ticketID); err != nil {
			return nil, err
		}
	}
	if err := bumpJourneyProjects(ctx, tx, sourceProject, targetProject); err != nil {
		return nil, err
	}
	return loadJourneyMembership(ctx, tx, ticketID)
}

func bumpJourneyProjects(ctx context.Context, tx pgx.Tx, sourceProject, targetProject string) error {
	var target any
	if targetProject != "" {
		target = targetProject
	}
	_, err := tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,updated_at=now()
	 WHERE project_node_id=$1::uuid OR project_node_id=$2::uuid`, sourceProject, target)
	return err
}

// restoreJourneyMembership checks the saved projection and restores it in the
// same transaction as the node undo. P2's project permission checks remain.
func restoreJourneyMembership(ctx context.Context, tx pgx.Tx, p tenant.Principal, ticketID string, before, after *journeyMembership) error {
	current, err := loadJourneyMembership(ctx, tx, ticketID)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, after) {
		return events.ErrConflict
	}
	var projects []string
	for _, membership := range []*journeyMembership{before, after} {
		if membership != nil {
			projects = append(projects, membership.ProjectID)
		}
	}
	if err := authz.RequireInProjects(ctx, tx, p, "nodes.move", projects...); err != nil {
		return events.ErrForbidden
	}
	if before == nil {
		if after != nil {
			return events.ErrConflict
		}
		return nil
	}
	if after == nil {
		_, err = tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,
		 walker_position,source,scope_revision_required,access_change,estimated_hours)
		 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6,$7,$8,$9,$10::numeric)`,
			p.TenantID, ticketID, before.ProjectID, before.FeatureID, before.ReleaseID,
			before.WalkerPosition, before.Source, before.ScopeRevisionRequired,
			before.AccessChange, before.EstimatedHours)
	} else {
		_, err = tx.Exec(ctx, `UPDATE journey_tickets SET project_node_id=$2::uuid,feature_node_id=$3::uuid,release_node_id=$4::uuid,
		 walker_position=$5,source=$6,scope_revision_required=$7,access_change=$8,estimated_hours=$9::numeric
		 WHERE ticket_node_id=$1::uuid`, ticketID, before.ProjectID, before.FeatureID,
			before.ReleaseID, before.WalkerPosition, before.Source,
			before.ScopeRevisionRequired, before.AccessChange, before.EstimatedHours)
	}
	if err != nil {
		return events.ErrConflict
	}
	targetProject := ""
	if after != nil {
		targetProject = after.ProjectID
	}
	return bumpJourneyProjects(ctx, tx, before.ProjectID, targetProject)
}

func movedJourneyTickets(ctx context.Context, tx pgx.Tx, before []journeyTicketSnapshot) ([]journeyTicketSnapshot, []journeyTicketSnapshot, error) {
	var changedBefore, changedAfter []journeyTicketSnapshot
	for _, ticket := range before {
		var target *string
		if err := tx.QueryRow(ctx, `SELECT project_id::text FROM nodes WHERE id=$1::uuid`, ticket.TicketID).Scan(&target); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, nil, conflict("moved ticket disappeared")
			}
			return nil, nil, err
		}
		if deref(target) == ticket.Membership.ProjectID {
			continue
		}
		after, err := transferJourneyMembership(ctx, tx, ticket.TicketID, ticket.Membership.ProjectID, deref(target), ticket.Membership)
		if err != nil {
			return nil, nil, err
		}
		changedBefore = append(changedBefore, ticket)
		changedAfter = append(changedAfter, journeyTicketSnapshot{TicketID: ticket.TicketID, Membership: after})
	}
	return changedBefore, changedAfter, nil
}
