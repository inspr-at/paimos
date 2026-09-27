// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/tenant"
)

type briefContextKey struct{}

type disposableBrief struct{ title, body string }

// These strings are the fixed AEON disposable-intake-briefs knowledge entries.
var disposableBriefs = map[string]disposableBrief{
	"1": {"Host status page", "A read-only status page for a small fleet of hosts: current state per host, last deploy, open incidents. Success: one page, loads under a second, no write actions."},
	"2": {"Release notes digest", "A weekly digest of released tickets per project: groups by feature and fix, links each ticket, sent nowhere (rendered page only). Success: the digest for last week matches the release history."},
	"3": {"Maintenance window planner", "Plan maintenance windows for hosts: propose a window, check it against the release calendar, record the decision. Success: a window can be proposed, checked and recorded; conflicts are shown."},
}

// checkBriefSeed runs before creating the operator principal or any intake row.
func checkBriefSeed(ctx context.Context, tx pgx.Tx, projectID, brief string) error {
	var live bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_releases WHERE project_node_id=$1::uuid AND state IN ('released','superseded','deploying','access'))`, projectID).Scan(&live); err != nil {
		return err
	}
	if live {
		return errors.New("cannot seed a deployed or released project")
	}
	var accepted, matching int
	err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE d.idempotency_key=$2 AND d.title=$3 AND d.body=$4) FROM intake_drafts d
		JOIN intake_draft_acceptances a ON a.tenant_id=d.tenant_id AND a.draft_id=d.id
		WHERE d.project_node_id=$1::uuid AND d.kind='brief'`, projectID, "operator-brief:"+brief, disposableBriefs[brief].title, disposableBriefs[brief].body).Scan(&accepted, &matching)
	if err != nil {
		return err
	}
	if accepted > 0 {
		if accepted != 1 || matching != 1 {
			return errors.New("intake already uses a different brief; disposable seed refuses to replace it")
		}
		return nil
	}
	var confirmed bool
	if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT brief_confirmed_at IS NOT NULL FROM journey_projects WHERE project_node_id=$1::uuid),false)`, projectID).Scan(&confirmed); err != nil {
		return err
	}
	if confirmed {
		return errors.New("intake is already confirmed without this disposable brief")
	}
	return nil
}

func prepareBriefSeed(ctx context.Context, pool *pgxpool.Pool, m *Module, p tenant.Principal, projectID, brief, target string) (bool, error) {
	selected := disposableBriefs[brief]
	var present bool
	err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if err := ensureJourney(ctx, tx, p, projectID); err != nil {
			return err
		}
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM intake_drafts d JOIN intake_draft_acceptances a ON a.tenant_id=d.tenant_id AND a.draft_id=d.id WHERE d.project_node_id=$1::uuid AND d.idempotency_key=$2)`, projectID, "operator-brief:"+brief).Scan(&present); err != nil {
			return err
		}
		if present {
			return nil
		}
		var base int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM events WHERE node_id=$1::uuid`, projectID).Scan(&base); err != nil {
			return err
		}
		if base == 0 {
			return errors.New("disposable brief needs an initialized journey event")
		}
		var draftID string
		if err := tx.QueryRow(ctx, `INSERT INTO intake_drafts(tenant_id,project_node_id,kind,target_node_id,title,body,base_event_id,proposed_by_principal_id,idempotency_key)
			VALUES($1::uuid,$2::uuid,'brief',$2::uuid,$3,$4,$5,$6::uuid,$7) RETURNING id::text`,
			p.TenantID, projectID, selected.title, selected.body, base, p.ID, "operator-brief:"+brief).Scan(&draftID); err != nil {
			return err
		}
		if _, err := writeEvent(ctx, tx, p, projectID, "journey.seed_brief_proposed", nil, map[string]any{"draft_id": draftID, "title": selected.title}); err != nil {
			return err
		}
		var before, after json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(n)||jsonb_build_object('position',n.position::text) FROM nodes n WHERE id=$1::uuid FOR UPDATE`, projectID).Scan(&before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET title=$2,body=$3,updated_at=clock_timestamp() WHERE id=$1::uuid`, projectID, selected.title, selected.body); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(n)||jsonb_build_object('position',n.position::text) FROM nodes n WHERE id=$1::uuid`, projectID).Scan(&after); err != nil {
			return err
		}
		var beforeMap, afterMap map[string]any
		if err := json.Unmarshal(before, &beforeMap); err != nil {
			return err
		}
		if err := json.Unmarshal(after, &afterMap); err != nil {
			return err
		}
		eventID, err := writeEvent(ctx, tx, p, projectID, "node.updated", beforeMap, afterMap)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO intake_draft_acceptances(tenant_id,draft_id,project_node_id,accepted_by_principal_id,target_node_id,event_id)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$3::uuid,$5)`, p.TenantID, draftID, projectID, p.ID, eventID); err != nil {
			return err
		}
		_, err = writeEvent(ctx, tx, p, projectID, "journey.seed_brief_accepted", nil, map[string]any{"draft_id": draftID, "target_node_id": projectID})
		return err
	})
	if err != nil || present {
		return false, err
	}
	view, err := m.read(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	if view.NextAction.Key != "confirm_brief" {
		return false, fmt.Errorf("disposable brief cannot be confirmed: %s", view.NextAction.Key)
	}
	if _, err := m.actWithMode(ctx, p, projectID, actionWrite{Action: "confirm_brief", ExpectedRevision: view.Revision, IdempotencyKey: "operator-brief:" + brief + ":confirm"}, true); err != nil {
		return false, fmt.Errorf("confirm disposable brief: %w", err)
	}
	if err := seedBriefSpecification(ctx, pool, p, projectID, brief); err != nil {
		return false, fmt.Errorf("seed disposable specification: %w", err)
	}
	view, err = m.read(ctx, p, projectID)
	if err != nil {
		return false, err
	}
	switch view.NextAction.Key {
	case "open_first_release":
		view, err = m.actWithMode(ctx, p, projectID, actionWrite{Action: "open_first_release", ExpectedRevision: view.Revision, IdempotencyKey: "operator-brief:" + brief + ":release"}, true)
		if err != nil {
			return false, err
		}
	case "mark_candidate":
		// An existing building release can already be past Plan.
	case "approve_candidate":
		if target != "candidate" && target != "deploy" {
			return false, fmt.Errorf("disposable specification reached approve_candidate, which is not a pending gate for --to-stage %s", target)
		}
	case "approve_deploy":
		if target != "deploy" {
			return false, fmt.Errorf("disposable specification reached approve_deploy, which is not a pending gate for --to-stage %s", target)
		}
	default:
		return false, fmt.Errorf("disposable specification cannot continue to %s: next action %s (expected open_first_release, mark_candidate, or a pending human gate)", target, view.NextAction.Key)
	}
	if view.CurrentReleaseID == nil {
		return false, fmt.Errorf("disposable specification reached %s without a release", view.NextAction.Key)
	}
	if err := seedBriefTicket(ctx, pool, p, projectID, *view.CurrentReleaseID, brief); err != nil {
		return false, fmt.Errorf("seed disposable release ticket: %w", err)
	}
	return true, nil
}

func seedBriefSpecification(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, projectID, brief string) error {
	selected := disposableBriefs[brief]
	return db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		reqID, err := seedNode(ctx, tx, p, projectID, "requirement", selected.title, selected.body)
		if err != nil {
			return fmt.Errorf("create brief requirement: %w", err)
		}
		var revision, reqRevision int64
		if err := tx.QueryRow(ctx, `UPDATE journey_projects SET revision=revision+1,requirements_revision=requirements_revision+1,updated_at=clock_timestamp()
			WHERE project_node_id=$1::uuid RETURNING revision,requirements_revision`, projectID).Scan(&revision, &reqRevision); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_requirements(tenant_id,requirement_node_id,project_node_id,kind,revision,creation_key)
			VALUES($1::uuid,$2::uuid,$3::uuid,'functional',$4,$5)`, p.TenantID, reqID, projectID, reqRevision, "operator-brief:"+brief); err != nil {
			return err
		}
		ev, err := writeEvent(ctx, tx, p, projectID, "journey.seed_requirement_created", nil, map[string]any{"requirement_node_id": reqID, "revision": revision, "requirements_revision": reqRevision})
		if err != nil {
			return err
		}
		if err := seedReceipt(ctx, tx, p, projectID, brief, "requirement", revision, ev); err != nil {
			return err
		}
		digest, err := requirements.Digest(ctx, tx, projectID)
		if err != nil {
			return fmt.Errorf("digest brief requirements: %w", err)
		}
		featureID, err := seedNode(ctx, tx, p, projectID, "epic", selected.title, selected.body)
		if err != nil {
			return fmt.Errorf("create brief feature: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_features(tenant_id,feature_node_id,project_node_id,requirement_node_id)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, p.TenantID, featureID, projectID, reqID); err != nil {
			return err
		}
		if err := seedLink(ctx, tx, p, featureID, reqID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE journey_requirements SET status='agreed' WHERE requirement_node_id=$1::uuid`, reqID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `UPDATE journey_projects SET revision=revision+1,agreed_requirements_revision=$2,agreed_requirements_digest_sha256=$3,updated_at=clock_timestamp()
			WHERE project_node_id=$1::uuid RETURNING revision`, projectID, reqRevision, digest).Scan(&revision); err != nil {
			return err
		}
		ev, err = writeEvent(ctx, tx, p, projectID, "journey.seed_requirements_agreed", nil, map[string]any{"revision": revision, "requirements_revision": reqRevision, "digest_sha256": digest, "feature_node_id": featureID})
		if err != nil {
			return err
		}
		return seedReceipt(ctx, tx, p, projectID, brief, "agreement", revision, ev)
	})
}

func seedBriefTicket(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, projectID, releaseID, brief string) error {
	selected := disposableBriefs[brief]
	return db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		var featureID string
		if err := tx.QueryRow(ctx, `SELECT f.feature_node_id::text FROM journey_features f JOIN journey_requirements r ON r.tenant_id=f.tenant_id AND r.requirement_node_id=f.requirement_node_id
			WHERE f.project_node_id=$1::uuid AND r.creation_key=$2`, projectID, "operator-brief:"+brief).Scan(&featureID); err != nil {
			return err
		}
		ticketID, err := seedNode(ctx, tx, p, featureID, "ticket", selected.title, selected.body)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,feature_node_id,release_node_id,walker_position,source,estimated_hours)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,0,'requirements',0::numeric)`, p.TenantID, ticketID, projectID, featureID, releaseID); err != nil {
			return err
		}
		if err := seedLink(ctx, tx, p, ticketID, featureID); err != nil {
			return err
		}
		if _, err := writeEvent(ctx, tx, p, projectID, "journey.seed_ticket_selected", nil, map[string]any{"ticket_node_id": ticketID, "release_node_id": releaseID}); err != nil {
			return err
		}
		before, err := seedNodeSnapshot(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done',updated_at=clock_timestamp() WHERE id=$1::uuid`, ticketID); err != nil {
			return err
		}
		after, err := seedNodeSnapshot(ctx, tx, ticketID)
		if err != nil {
			return err
		}
		if _, err := writeEvent(ctx, tx, p, ticketID, "node.updated", before, after); err != nil {
			return err
		}
		_, err = writeEvent(ctx, tx, p, projectID, "journey.seed_ticket_completed", nil, map[string]any{"ticket_node_id": ticketID, "release_node_id": releaseID})
		return err
	})
}

func seedNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, parent, kind, title, body string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,body,position)
		SELECT $1::uuid,k.id,aeon_next_node_key($1::uuid,k.short_prefix),$3::uuid,$4,$5,
		coalesce((SELECT max(position)+1024 FROM nodes WHERE parent_id=$3::uuid AND deleted_at IS NULL),1024)
		FROM node_kinds k WHERE k.tenant_id=$1::uuid AND k.slug=$2 RETURNING id::text`, p.TenantID, kind, parent, title, body).Scan(&id)
	if err != nil {
		return "", err
	}
	snap, err := seedNodeSnapshot(ctx, tx, id)
	if err != nil {
		return "", err
	}
	_, err = writeEvent(ctx, tx, p, id, "node.created", nil, snap)
	return id, err
}

func seedNodeSnapshot(ctx context.Context, tx pgx.Tx, id string) (map[string]any, error) {
	var snap map[string]any
	err := tx.QueryRow(ctx, `SELECT to_jsonb(n)||jsonb_build_object('position',n.position::text) FROM nodes n WHERE id=$1::uuid`, id).Scan(&snap)
	return snap, err
}

func seedLink(ctx context.Context, tx pgx.Tx, p tenant.Principal, source, target string) error {
	var snap map[string]any
	if err := tx.QueryRow(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1::uuid,$2::uuid,$3::uuid,'implements') RETURNING to_jsonb(node_relations)`, p.TenantID, source, target).Scan(&snap); err != nil {
		return err
	}
	_, err := writeEvent(ctx, tx, p, source, "relation.created", nil, snap)
	return err
}

func seedReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, brief, step string, revision, eventID int64) error {
	key := "operator-brief:" + brief + ":" + step
	sum := sha256.Sum256([]byte(key))
	return insertReceipt(ctx, tx, p.TenantID, projectID, key, hex.EncodeToString(sum[:]), revision, eventID)
}
