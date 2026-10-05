// SPDX-License-Identifier: AGPL-3.0-only

package requirements

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func agree(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in agreeInput) ([]Requirement, error) {
	rev, err := lockProject(ctx, tx, project, true)
	if err != nil {
		return nil, err
	}

	if err = requirePerson(ctx, tx, p); err != nil {
		return nil, err
	}
	requestDigest := requestHash("requirements.agree", p, in)
	repeated, err := replay(ctx, tx, project, in.Key, requestDigest)
	if err != nil {
		return nil, err
	}
	if repeated {
		return load(ctx, tx, project)
	}
	if rev != in.Revision {
		return nil, fail(409, "project revision changed")
	}
	// Lock the current release before any generation/event writes. Handoff
	// transitions also update this row; they cannot start a build between our
	// readiness check and selecting the generated tickets into release one.
	releaseRows, err := tx.Query(ctx, `SELECT r.release_node_id FROM journey_releases r
      JOIN journey_projects j ON j.tenant_id=r.tenant_id AND j.current_release_node_id=r.release_node_id
      WHERE j.project_node_id=$1 FOR UPDATE OF r`, project)
	if err != nil {
		return nil, err
	}
	for releaseRows.Next() {
	}
	err = releaseRows.Err()
	releaseRows.Close()
	if err != nil {
		return nil, err
	}
	contentDigest, err := Digest(ctx, tx, project)
	if err != nil {
		return nil, err
	}
	// Lock the grant against concurrent revocation until this transaction commits.
	var approval string
	err = tx.QueryRow(ctx, `SELECT a.id::text FROM approval_requests a
 JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id
 JOIN agent_permission_grants g ON g.tenant_id=a.tenant_id AND g.approval_request_id=a.id
 JOIN principals person ON person.tenant_id=d.tenant_id AND person.id=d.decided_by_principal_id
 WHERE a.id=$1 AND a.resource_kind='node' AND a.resource_id=$2 AND a.scope=$3
 AND a.expires_at>clock_timestamp() AND d.decision='approved' AND d.decided_by_principal_id=$4 AND person.kind='person'
 AND g.revoked_at IS NULL AND g.valid_until>clock_timestamp()
 AND g.agent_principal_id=a.agent_principal_id AND g.scope=a.scope AND g.resource_kind=a.resource_kind AND g.resource_id=a.resource_id
 FOR UPDATE OF g`, in.ApprovalID, project, ApprovalScope(rev, contentDigest), p.ID).Scan(&approval)
	if err == pgx.ErrNoRows {
		return nil, fail(403, "live person approval for the current requirements digest and revision required")
	}
	if err != nil {
		return nil, err
	}
	// Agreement is a requirements-stage operation, not a means to alter an active
	// build. Personal journeys skip Shape; other profiles require its go decision.
	var ready bool
	err = tx.QueryRow(ctx, `SELECT brief_confirmed_at IS NOT NULL AND (profile='personal' OR decision IN ('go','reduce_scope')) AND decision NOT IN ('park','drop')
 AND NOT EXISTS(SELECT 1 FROM journey_releases r WHERE r.release_node_id=j.current_release_node_id AND r.state<>'planning')
 FROM journey_projects j WHERE project_node_id=$1`, project).Scan(&ready)
	if err != nil {
		return nil, err
	}
	if !ready {
		return nil, fail(409, "journey is not ready for requirements agreement")
	}
	if err = commitAgreement(ctx, tx, p, project, rev, contentDigest, in.Key, requestDigest, in.ApprovalID, true); err != nil {
		return nil, err
	}
	return load(ctx, tx, project)
}

// commitAgreement pins the digest, marks live requirements agreed, and writes
// the normal requirements-agreed event and receipt. withTickets generates
// accepted breakdowns in the same transaction. A non-empty approvalID records
// the person gate; the disposable operator leaves it empty and does not invent
// a decision.
func commitAgreement(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, rev int64, contentDigest, key, requestDigest, approvalID string, withTickets bool) error {
	items, err := load(ctx, tx, project)
	if err != nil {
		return err
	}
	active := 0
	for _, item := range items {
		if item.Status != "superseded" {
			active++
		}
	}
	if active == 0 {
		return fail(409, "no requirements to agree")
	}
	// A direct R1 edit needs a new requirements revision even if it did not touch
	// the projection. Keep monotonically increasing agreement revisions.
	var reqRev int64
	err = tx.QueryRow(ctx, `UPDATE journey_projects SET requirements_revision=greatest(requirements_revision,agreed_requirements_revision+1)
 WHERE project_node_id=$1 RETURNING requirements_revision`, project).Scan(&reqRev)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Status == "superseded" {
			continue
		}
		if err = generateWork(ctx, tx, p, project, item, "", withTickets); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE journey_requirements SET status='agreed' WHERE requirement_node_id=$1`, item.NodeID)
		if err != nil {
			return err
		}
	}
	// The pin records the agreed content, not mutable projection status.
	_, err = tx.Exec(ctx, `UPDATE journey_projects SET revision=revision+1,agreed_requirements_revision=$2,agreed_requirements_digest_sha256=$3,updated_at=now() WHERE project_node_id=$1`, project, reqRev, contentDigest)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE journey_tickets SET scope_revision_required=false WHERE project_node_id=$1 AND source='manual' AND scope_revision_required`, project)
	if err != nil {
		return err
	}
	if approvalID != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO journey_gates(tenant_id,project_node_id,gate,approval_request_id) VALUES($1,$2,'requirements',$3)`, p.TenantID, project, approvalID); err != nil {
			return err
		}
	}
	after := map[string]any{"revision": rev + 1, "requirements_revision": reqRev, "digest_sha256": contentDigest}
	if approvalID != "" {
		after["approval_request_id"] = approvalID
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "journey.requirements_agreed", After: after})
	if err != nil {
		return err
	}
	return receipt(ctx, tx, p, project, key, requestDigest, rev+1, ev.ID)
}

func generateWork(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, item Requirement, bindRelease string, tickets bool) error {
	adopted, modeErr := delivery.ReleasesMode(ctx, tx, project)
	if modeErr != nil {
		return modeErr
	}
	if adopted && bindRelease != "" {
		return fail(409, "This project plans with releases")
	}
	var body string
	var origin *string
	if err := tx.QueryRow(ctx, `SELECT n.body,r.origin_draft_id::text FROM journey_requirements r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.requirement_node_id WHERE r.requirement_node_id=$1`, item.NodeID).Scan(&body, &origin); err != nil {
		return err
	}
	if item.Kind == "nonfunctional" {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM node_relations r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.source_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE r.target_node_id=$1 AND r.type='implements' AND k.slug='memory')`, item.NodeID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		id, err := newNode(ctx, tx, p, "memory", project, item.Title, body, nil)
		if err != nil {
			return err
		}
		return link(ctx, tx, p, id, item.NodeID, "implements")
	}
	feature := item.FeatureID
	if feature == nil {
		id, err := newNode(ctx, tx, p, "epic", project, item.Title, body, nil)
		if err != nil {
			return err
		}
		feature = &id
		if _, err = tx.Exec(ctx, `INSERT INTO journey_features(tenant_id,feature_node_id,project_node_id,requirement_node_id) VALUES($1,$2,$3,$4)`, p.TenantID, id, project, item.NodeID); err != nil {
			return err
		}
		if err = link(ctx, tx, p, id, item.NodeID, "implements"); err != nil {
			return err
		}
	}
	if !tickets || origin == nil {
		return nil
	}
	// Suggestions are immutable, and only a matching explicit acceptance feeds
	// generation. Creation-event lineage survives later re-agreement and edits.
	rows, err := tx.Query(ctx, `SELECT s.ordinal,s.title,s.estimated_hours::float8,s.later,s.access_change
 FROM intake_draft_ticket_suggestions s JOIN intake_draft_acceptances a ON a.tenant_id=s.tenant_id AND a.draft_id=s.draft_id AND a.project_node_id=s.project_node_id
 JOIN intake_drafts d ON d.tenant_id=s.tenant_id AND d.id=s.draft_id
 WHERE s.project_node_id=$1 AND s.draft_id=$2 AND a.target_node_id=$3 AND d.kind='requirement' AND d.requirement_kind='functional' ORDER BY s.ordinal`, project, *origin, item.NodeID)
	if err != nil {
		return err
	}
	type suggestion struct {
		ordinal       int
		title         string
		hours         float64
		later, access bool
	}
	var suggestions []suggestion
	for rows.Next() {
		var s suggestion
		if err = rows.Scan(&s.ordinal, &s.title, &s.hours, &s.later, &s.access); err != nil {
			rows.Close()
			return err
		}
		suggestions = append(suggestions, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range suggestions {
		var exists bool
		// Both lineage and generation provenance are immutable. A generated
		// leaf may leave the live membership projection when it becomes a parent;
		// that must not permit the same accepted suggestion to generate again.
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events generated
 JOIN events created ON created.tenant_id=generated.tenant_id AND created.node_id=generated.node_id AND created.type='node.created'
 WHERE generated.type='journey.ticket_generated' AND generated.after->>'feature_node_id'=$1
 AND generated.after->>'source'='requirements' AND created.after->'fields'->>'intake_draft_id'=$2
 AND created.after->'fields'->>'intake_ordinal'=$3)`, *feature, *origin, fmt.Sprint(s.ordinal)).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		id, err := newNode(ctx, tx, p, "ticket", *feature, s.title, "", map[string]any{"intake_draft_id": *origin, "intake_ordinal": s.ordinal})
		if err != nil {
			return err
		}
		var release *string
		if !s.later && !adopted {
			if bindRelease != "" {
				var bound, state string
				if err = tx.QueryRow(ctx, `SELECT release_node_id::text, state FROM journey_releases WHERE project_node_id=$1 AND release_node_id=$2::uuid`, project, bindRelease).Scan(&bound, &state); err != nil {
					return err
				}
				if state != "planning" && state != "building" {
					return fail(409, "release cannot take generated work")
				}
				release = &bound
			} else {
				// Only an explicitly existing first planning release receives initial work.
				// Creating releases and choosing versions belong to the journey module.
				if err = tx.QueryRow(ctx, `SELECT (SELECT r.release_node_id::text FROM journey_releases r JOIN journey_projects j ON j.tenant_id=r.tenant_id AND j.current_release_node_id=r.release_node_id WHERE r.project_node_id=$1 AND r.number=1 AND r.state='planning')`, project).Scan(&release); err != nil {
					return err
				}
			}
		}
		var raw json.RawMessage
		err = tx.QueryRow(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,walker_position,source,estimated_hours,access_change)
 VALUES($1,$2,$3,$4,$5,coalesce((SELECT max(walker_position)+1 FROM journey_tickets WHERE project_node_id=$3),0),'requirements',$6,$7) RETURNING to_jsonb(journey_tickets)`, p.TenantID, id, project, *feature, release, s.hours, s.access).Scan(&raw)
		if err != nil {
			return err
		}
		if err = link(ctx, tx, p, id, *feature, "implements"); err != nil {
			return err
		}
		if _, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "journey.ticket_generated", After: raw}); err != nil {
			return err
		}
		if release != nil {
			if _, err = tx.Exec(ctx, `UPDATE journey_releases SET revision=revision+1,access_required=access_required OR $2 WHERE release_node_id=$1`, *release, s.access); err != nil {
				return err
			}
		}
	}
	return nil
}
