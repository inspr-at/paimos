// SPDX-License-Identifier: AGPL-3.0-only

package requirements

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Digest fingerprints current requirement content, accepted breakdown identity,
// and manually added scope. Call inside db.InTenant, holding the tenant's R1
// tree advisory lock and project row lock when authorizing a mutation. Digest
// locks scope node rows against concurrent R1 content edits. Status is
// excluded so agreeing does not itself invalidate the pin. Direct R1 changes,
// including deletion, fields and manual ticket edits, change this digest.
func Digest(ctx context.Context, tx pgx.Tx, project string) (string, error) {
	// R1 title/body patches take row locks, not the tree advisory lock. Lock
	// every scope node before reading content and hold these locks through the
	// caller's commit; otherwise an edit could race approval verification.
	rows, err := tx.Query(ctx, `SELECT n.id FROM nodes n WHERE
      EXISTS(SELECT 1 FROM journey_requirements r WHERE r.project_node_id=$1 AND r.requirement_node_id=n.id)
      OR EXISTS(SELECT 1 FROM journey_tickets t WHERE t.project_node_id=$1 AND t.source='manual' AND t.ticket_node_id=n.id)
      ORDER BY n.id FOR SHARE OF n`, project)
	if err != nil {
		return "", err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	var content string
	err = tx.QueryRow(ctx, DigestContentSQL, project).Scan(&content)
	if err != nil {
		return "", err
	}
	return hash([]byte(content)), nil
}

// DigestContentSQL is the read-only content projection shared by snapshot
// readers. Mutations still use Digest's scope-row locks before reading it.
const DigestContentSQL = `SELECT jsonb_build_object(
 'requirements',coalesce((SELECT jsonb_agg(jsonb_build_object('id',n.id,'kind',r.kind,'revision',r.revision,'title',n.title,'body',n.body,'fields',n.fields,'state',n.state,'origin',r.origin_draft_id,'accepted',EXISTS(SELECT 1 FROM intake_draft_acceptances a WHERE a.draft_id=r.origin_draft_id AND a.target_node_id=n.id),'suggestions',coalesce((SELECT jsonb_agg(jsonb_build_object('ordinal',s.ordinal,'title',s.title,'hours',s.estimated_hours,'later',s.later,'access',s.access_change) ORDER BY s.ordinal) FROM intake_draft_ticket_suggestions s WHERE s.draft_id=r.origin_draft_id),'[]'::jsonb)) ORDER BY n.id)
 FROM journey_requirements r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.requirement_node_id WHERE r.project_node_id=$1 AND r.status<>'superseded' AND n.deleted_at IS NULL),'[]'::jsonb),
 'manual',coalesce((SELECT jsonb_agg(jsonb_build_object('id',n.id,'title',n.title,'body',n.body,'fields',n.fields,'feature',t.feature_node_id,'access_change',t.access_change,'estimated_hours',t.estimated_hours) ORDER BY n.id)
 FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.project_node_id=$1 AND t.source='manual' AND n.deleted_at IS NULL),'[]'::jsonb))::text`

// ApprovalScope binds an R2 request to the exact project revision and Digest.
// Agents propose this scope on resource_kind=node, resource_id=project ID;
// the approving person must be the person who applies the agreement. R2's
// dotted scope refinements allow a journey.requirements API-key ceiling.
// The coordinator can use this helper when presenting the requirements gate.
func ApprovalScope(revision int64, digest string) string {
	return fmt.Sprintf("journey.requirements.r%d.d%s", revision, digest)
}
func hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func requestHash(action string, p tenant.Principal, in any) string {
	b, _ := json.Marshal(struct {
		Action, Actor string
		Input         any
	}{action, p.ID, in})
	return hash(b)
}
func replay(ctx context.Context, tx pgx.Tx, project, key, digest string) (bool, error) {
	var saved string
	err := tx.QueryRow(ctx, `SELECT request_sha256 FROM journey_action_receipts WHERE project_node_id=$1 AND idempotency_key=$2`, project, key).Scan(&saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if saved != digest {
		return false, fail(409, "idempotency key already used for a different request")
	}
	return true, nil
}
func receipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, key, digest string, revision, eventID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO journey_action_receipts(tenant_id,project_node_id,idempotency_key,request_sha256,resulting_revision,event_id) VALUES ($1,$2,$3,$4,$5,$6)`, p.TenantID, project, key, digest, revision, eventID)
	return err
}
func requirePerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1 AND kind='person')`, p.ID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return fail(403, "person required")
	}
	return nil
}
func create(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, in createInput) (Requirement, error) {
	var out Requirement
	rev, err := lockProject(ctx, tx, project, true)
	if err != nil {
		return out, err
	}
	if err = requirePerson(ctx, tx, p); err != nil {
		return out, err
	}
	digest := requestHash("requirement.create", p, in)
	repeated, err := replay(ctx, tx, project, in.Key, digest)
	if err != nil {
		return out, err
	}
	if repeated {
		items, err := load(ctx, tx, project)
		if err != nil {
			return out, err
		}
		var id string
		err = tx.QueryRow(ctx, `SELECT requirement_node_id::text FROM journey_requirements WHERE project_node_id=$1 AND creation_key=$2`, project, in.Key).Scan(&id)
		if err != nil {
			return out, err
		}
		for _, item := range items {
			if item.NodeID == id {
				return item, nil
			}
		}
		return out, fail(409, "created requirement is no longer live")
	}
	if rev != in.Revision {
		return out, fail(409, "project revision changed")
	}
	var reqRev int64
	err = tx.QueryRow(ctx, `UPDATE journey_projects SET revision=revision+1,requirements_revision=requirements_revision+1,updated_at=now() WHERE project_node_id=$1 RETURNING requirements_revision`, project).Scan(&reqRev)
	if err != nil {
		return out, err
	}
	id, err := newNode(ctx, tx, p, "requirement", project, in.Title, *in.Body, nil)
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO journey_requirements(tenant_id,requirement_node_id,project_node_id,kind,revision,creation_key) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, id, project, in.Kind, reqRev, in.Key)
	if err != nil {
		return out, err
	}
	out = Requirement{NodeID: id, ProjectID: project, Kind: in.Kind, Revision: reqRev, Status: "draft", Title: in.Title, TicketIDs: []string{}}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &project, Type: "journey.requirement_created", After: map[string]any{"requirement_node_id": id, "revision": rev + 1, "requirements_revision": reqRev}})
	if err != nil {
		return out, err
	}
	err = receipt(ctx, tx, p, project, in.Key, digest, rev+1, ev.ID)
	return out, err
}

// newNode uses R1's allocator and complete event snapshot (including textual
// position, as expected by R1 undo). It never introduces a parallel key counter.
func newNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind, parent, title, body string, fields any) (string, error) {
	if fields == nil {
		fields = map[string]any{}
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,parent_id,title,body,fields,position)
 SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$3,$4,$5,$6::jsonb,
 coalesce((SELECT max(position)+1024 FROM nodes WHERE parent_id=$3 AND deleted_at IS NULL),1024)
 FROM node_kinds k WHERE k.slug=CASE WHEN $2 IN ('epic','ticket','task') AND EXISTS(SELECT 1 FROM node_kinds WHERE slug='work') THEN 'work' ELSE $2 END RETURNING id::text`, p.TenantID, kind, parent, title, body, raw).Scan(&id)
	if err != nil {
		return "", err
	}
	snapshot, err := nodeSnapshot(ctx, tx, id)
	if err != nil {
		return "", err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: "node.created", After: snapshot})
	return id, err
}
func nodeSnapshot(ctx context.Context, tx pgx.Tx, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT to_jsonb(n)||jsonb_build_object('position',n.position::text) FROM nodes n WHERE id=$1`, id).Scan(&raw)
	return raw, err
}
func link(ctx context.Context, tx pgx.Tx, p tenant.Principal, source, target, kind string) error {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `INSERT INTO node_relations(tenant_id,source_node_id,target_node_id,type) VALUES($1,$2,$3,$4) RETURNING to_jsonb(node_relations)`, p.TenantID, source, target, kind).Scan(&raw)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, p, events.Change{NodeID: &source, Type: "relation.created", After: raw})
	return err
}
