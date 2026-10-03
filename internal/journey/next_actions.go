// SPDX-License-Identifier: AGPL-3.0-only
package journey

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type nextActionItem struct {
	ProjectNodeID string            `json:"project_node_id"`
	NextAction    JourneyNextAction `json:"next_action"`
}

type standingGate struct {
	Gate, Release, Approval string
	Live                    bool
}
type actionOffer struct {
	Scope, Resource, ID, Decision, Decider          string
	Expires                                         time.Time
	GrantExpires                                    *time.Time
	Open, GrantExists, Revoked, GrantLive, Consumed bool
}
type actionSnapshot struct {
	Facts                      facts
	Gates                      []standingGate
	Offers                     []actionOffer
	Handoffs                   []handoffRow
	DeployWindow, AccessWindow *time.Time
	Cap                        string
}

func (m *Module) handleNextActions(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "person required")
		return
	}
	ids := strings.Split(r.URL.Query().Get("project_ids"), ",")
	seen := map[string]bool{}
	if len(ids) > 100 {
		writeError(w, 400, "at most 100 projects")
		return
	}
	for i, id := range ids {
		id = strings.ToLower(id)
		if !uuidOK(id) || seen[id] {
			writeError(w, 400, "project_ids must contain unique UUIDs")
			return
		}
		seen[id] = true
		ids[i] = id
	}
	items := []nextActionItem{}
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		check, err := authz.ProjectsTx(r.Context(), tx, p)
		if err != nil {
			return err
		}
		allowed := []string{}
		for _, id := range ids {
			if check("journey.read", id) && check("journey.act", id) {
				allowed = append(allowed, id)
			}
		}
		if len(allowed) == 0 {
			return nil
		}
		snapshots, err := loadActionSnapshots(r.Context(), tx, allowed)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			f := snapshot.Facts
			if !f.ReleasesMode && f.CurrentReleaseRecorded && f.Release == nil {
				return fail(409, "current release is missing")
			}
			foldActionSnapshot(&f, snapshot)
			items = append(items, nextActionItem{ProjectNodeID: f.ProjectID, NextAction: derive(f).NextAction})
		}
		return nil
	})
	writeResult(w, 200, map[string]any{"items": items}, err)
}

// All next-action inputs are selected once, under one MVCC snapshot. The same
// derivation, offer classification and handoff fold as GET /journey are used.
// This read never takes scope locks, initializes a project or changes a gate.
func loadActionSnapshots(ctx context.Context, tx pgx.Tx, ids []string) ([]actionSnapshot, error) {
	query := `WITH RECURSIVE roots AS MATERIALIZED (
 SELECT n.id,n.tenant_id,n.key,
  coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1)) AS project_key,
  coalesce(j.profile,'personal') AS profile,coalesce(j.revision,1) AS revision,
  j.brief_confirmed_at IS NOT NULL AS confirmed,coalesce(j.decision,'pending') AS decision,
  coalesce(j.requirements_revision,0) AS req_revision,coalesce(j.agreed_requirements_revision,0) AS agreed_revision,
  j.current_release_node_id,
  EXISTS(SELECT 1 FROM events e WHERE e.node_id=n.id AND e.type='import.node_created') AS imported,
  EXISTS(SELECT 1 FROM intake_drafts d JOIN intake_draft_acceptances a ON a.tenant_id=d.tenant_id AND a.draft_id=d.id WHERE d.project_node_id=n.id AND d.kind='brief') AS accepted
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 LEFT JOIN journey_projects j ON j.tenant_id=n.tenant_id AND j.project_node_id=n.id
 WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL AND k.slug='project'),
 tree AS (
 SELECT p.id AS root,n.id,n.parent_id,n.kind_id,n.state,0 AS depth FROM roots p JOIN nodes n ON n.id=p.id WHERE p.imported AND p.current_release_node_id IS NULL
 UNION ALL SELECT t.root,n.id,n.parent_id,n.kind_id,n.state,t.depth+1 FROM tree t JOIN nodes n ON n.parent_id=t.id WHERE n.deleted_at IS NULL AND t.depth<64),
 typed AS (SELECT t.*,k.slug FROM tree t JOIN node_kinds k ON k.id=t.kind_id),
 projects AS (
 SELECT p.*,coalesce(p.current_release_node_id,CASE WHEN p.imported THEN latest.release_node_id END) AS release_id,
  CASE WHEN p.current_release_node_id IS NOT NULL AND p.imported THEN 'plan'
       WHEN p.imported AND rc.total>0 AND rc.all_done AND tc.open=0 THEN 'live'
       WHEN p.imported AND rc.total>0 THEN 'build'
       WHEN p.imported AND tc.total>0 THEN 'plan' ELSE '' END AS imported_stage
 FROM roots p
 LEFT JOIN LATERAL (SELECT jr.release_node_id FROM journey_releases jr JOIN nodes n ON n.tenant_id=jr.tenant_id AND n.id=jr.release_node_id WHERE jr.project_node_id=p.id AND n.deleted_at IS NULL ORDER BY n.created_at DESC,n.id DESC LIMIT 1) latest ON p.imported AND p.current_release_node_id IS NULL
 CROSS JOIN LATERAL (SELECT count(*) AS total,coalesce(bool_and(n.state='done'),false) AS all_done FROM journey_releases jr JOIN nodes n ON n.tenant_id=jr.tenant_id AND n.id=jr.release_node_id WHERE jr.project_node_id=p.id) rc
 CROSS JOIN LATERAL (SELECT
  (SELECT count(*) FROM typed t WHERE t.root=p.id AND t.slug='ticket')+(SELECT count(*) FROM journey_tickets jt WHERE jt.project_node_id=p.id) AS total,
  (SELECT count(*) FROM typed t WHERE t.root=p.id AND t.slug='ticket' AND t.state<>'done')+(SELECT count(*) FROM journey_tickets jt JOIN nodes n ON n.tenant_id=jt.tenant_id AND n.id=jt.ticket_node_id WHERE jt.project_node_id=p.id AND n.deleted_at IS NULL AND n.state<>'done') AS open) tc)
 SELECT jsonb_build_object(
 'Facts',jsonb_build_object(
 'ProjectID',p.id,'NodeKey',p.key,'ProjectKey',p.project_key,'Profile',p.profile,'Revision',p.revision,
 'BriefConfirmed',p.confirmed,'AcceptedBrief',p.accepted,'Decision',p.decision,
 'RequirementsRevision',p.req_revision,'AgreedRequirementsRevision',p.agreed_revision,
 'ReleasesMode',EXISTS(SELECT 1 FROM project_delivery WHERE project_node_id=p.id),'CurrentReleaseRecorded',p.current_release_node_id IS NOT NULL,'Imported',p.imported,'ImportedStage',p.imported_stage,
 'RequirementsDigest',s.digest,
 'Release',CASE WHEN r.release_node_id IS NULL THEN NULL ELSE jsonb_build_object('ID',r.release_node_id,'Number',r.number,'State',r.state,'AccessRequired',r.access_required,'Revision',r.revision) END,
 'RequirementCount',(SELECT count(*) FROM journey_requirements q WHERE q.project_node_id=p.id),
 'DraftCount',(SELECT count(*) FROM journey_requirements q WHERE q.project_node_id=p.id AND q.status='draft'),
 'IncludedTickets',stats.total,'OpenReleaseTickets',stats.open,'ScopeRevision',stats.scope>0,'AccessChange',stats.access>0,'MissingEstimate',stats.missing,
 'PlanCents',stats.plan::bigint,'SpentCents',coalesce((SELECT sum(jt.estimated_hours)*100 FROM journey_tickets jt JOIN journey_releases jr ON jr.tenant_id=jt.tenant_id AND jr.release_node_id=jt.release_node_id WHERE jt.project_node_id=p.id AND jr.state IN ('released','superseded') AND jr.release_node_id<>r.release_node_id),0)::bigint,
 'NextReleaseNumber',(SELECT coalesce(max(jr.number),0)+1 FROM journey_releases jr WHERE jr.project_node_id=p.id),
 'PriorReleased',EXISTS(SELECT 1 FROM journey_releases jr WHERE jr.project_node_id=p.id AND jr.state='released' AND (r.release_node_id IS NULL OR jr.release_node_id<>r.release_node_id)),
 'BriefAuthor',coalesce((SELECT actor_principal_id::text FROM events WHERE node_id=p.id AND type='journey.brief_confirmed' ORDER BY id DESC LIMIT 1),''),
 'BuildStarter',coalesce((SELECT actor_principal_id::text FROM events WHERE node_id=p.id AND type='journey.build_started' ORDER BY id DESC LIMIT 1),''),
 'RequirementsDecider',coalesce((SELECT d.decided_by_principal_id::text FROM journey_gates g JOIN approval_decisions d ON d.tenant_id=g.tenant_id AND d.request_id=g.approval_request_id WHERE g.project_node_id=p.id AND g.gate='requirements' ORDER BY g.created_at DESC LIMIT 1),'')),
 'Cap',coalesce((SELECT after->>'approved_cap_hours' FROM events WHERE node_id=p.id AND type='journey.decided' AND after->>'decision' IN ('go','reduce_scope') AND coalesce(after->>'approved_cap_hours','')<>'' ORDER BY id DESC LIMIT 1),''),
 'Gates',coalesce((SELECT jsonb_agg(jsonb_build_object('Gate',g.gate,'Release',g.release,'Approval',g.approval,'Live',g.live) ORDER BY g.gate,nullif(g.release,'')::uuid,g.live DESC,g.created_at DESC,g.id DESC) FROM (

		SELECT g.gate, coalesce(g.release_node_id::text, '') AS release, g.approval_request_id::text AS approval, a_target.target, a_target.target_digest_sha256, g.created_at,g.id,
		       EXISTS (
		         SELECT 1 FROM journey_gates live_gate
		         JOIN approval_requests a ON a.tenant_id=live_gate.tenant_id AND a.id=live_gate.approval_request_id
		         JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id
		         JOIN agent_permission_grants grant_row ON grant_row.tenant_id=a.tenant_id AND grant_row.approval_request_id=a.id
		         WHERE live_gate.tenant_id=g.tenant_id AND live_gate.project_node_id=g.project_node_id
		           AND live_gate.approval_request_id=g.approval_request_id
		           AND live_gate.gate=g.gate AND live_gate.release_node_id IS NOT DISTINCT FROM g.release_node_id
		           AND a.resource_kind='node' AND a.resource_id=coalesce(live_gate.release_node_id,live_gate.project_node_id)
		           AND (g.gate<>'requirements' OR a.scope=s.scope)
		           AND d.decision='approved' AND a.expires_at>now()
		           AND grant_row.revoked_at IS NULL AND grant_row.valid_until>now()
		       ) AS live
		FROM journey_gates g
		JOIN approval_requests a_target ON a_target.tenant_id=g.tenant_id AND a_target.id=g.approval_request_id
		WHERE g.project_node_id = p.id
		ORDER BY g.gate, g.release_node_id, live DESC, g.created_at DESC, g.id DESC
 ) g),'[]'::jsonb),
 'Offers',coalesce((SELECT jsonb_agg(jsonb_build_object('Scope',o.scope,'Resource',o.resource,'ID',o.id,'Decision',o.decision,'Decider',o.decider,'Expires',o.expires_at,'GrantExpires',o.valid_until,'Open',o.open,'GrantExists',o.grant_exists,'Revoked',o.revoked,'GrantLive',o.grant_live,'Consumed',o.consumed) ORDER BY o.proposed_at DESC,o.id DESC) FROM (

		SELECT a.scope, a.resource_id::text AS resource, a.id::text, a.target, a.target_digest_sha256,
		       coalesce(d.decision, '') AS decision, coalesce(d.decided_by_principal_id::text, '') AS decider,
		       a.expires_at, g.valid_until,a.proposed_at,
		       a.expires_at > now() AS open,
		       g.approval_request_id IS NOT NULL AS grant_exists, g.revoked_at IS NOT NULL AS revoked,
		       (g.approval_request_id IS NOT NULL AND g.revoked_at IS NULL AND g.valid_until > now()) AS grant_live,
		       EXISTS (SELECT 1 FROM journey_gates jg
		               WHERE jg.tenant_id = a.tenant_id AND jg.approval_request_id = a.id) AS consumed
		FROM approval_requests a
		LEFT JOIN approval_decisions d
		  ON d.tenant_id = a.tenant_id AND d.request_id = a.id
		LEFT JOIN agent_permission_grants g
		  ON g.tenant_id = a.tenant_id AND g.approval_request_id = a.id
		WHERE a.resource_kind = 'node'
		  AND a.resource_id = ANY(ARRAY[p.id,r.release_node_id])
		  AND a.scope = ANY(ARRAY['journey.shape',s.scope,'journey.build','journey.candidate','journey.deploy','journey.access'])
		ORDER BY a.proposed_at DESC, a.id DESC
 ) o),'[]'::jsonb),
 'DeployWindow',windows.deploy,'AccessWindow',windows.access,
 'Handoffs',coalesce((SELECT jsonb_agg(jsonb_build_object('ID',h.id,'Stage',h.stage,'Operation',h.operation,'State',h.state,'Result',coalesce(result.outcome,''),'Attempt',h.attempt,'Epoch',h.authority_epoch,'At',h.created_at,'Historical',h.journey_revision<windows.renewal)) FROM stage_handoffs h LEFT JOIN stage_handoff_results result ON result.tenant_id=h.tenant_id AND result.handoff_id=h.id WHERE h.release_node_id=r.release_node_id AND h.stage IN ('deploy','access')),'[]'::jsonb))
 FROM projects p
 LEFT JOIN journey_releases r ON r.tenant_id=p.tenant_id AND r.release_node_id=p.release_id
 CROSS JOIN LATERAL (DIGEST_QUERY) content
 CROSS JOIN LATERAL (SELECT encode(sha256(convert_to(content.content,'UTF8')),'hex') AS digest) digest
 CROSS JOIN LATERAL (SELECT digest.digest AS digest,'journey.requirements.r'||p.revision||'.d'||digest.digest AS scope) s
 CROSS JOIN LATERAL (SELECT count(*) FILTER(WHERE jt.release_node_id=r.release_node_id AND n.deleted_at IS NULL) AS total,
 count(*) FILTER(WHERE jt.release_node_id=r.release_node_id AND n.deleted_at IS NULL AND n.state<>'done') AS open,
 count(*) FILTER(WHERE jt.release_node_id=r.release_node_id AND jt.scope_revision_required) AS scope,
 count(*) FILTER(WHERE jt.release_node_id=r.release_node_id AND jt.access_change) AS access,
 coalesce(bool_or(jt.release_node_id=r.release_node_id AND jt.estimated_hours IS NULL),false) AS missing,
 coalesce(sum(jt.estimated_hours) FILTER(WHERE jt.release_node_id=r.release_node_id AND jt.estimated_hours IS NOT NULL),0)*100 AS plan
 FROM journey_tickets jt JOIN nodes n ON n.tenant_id=jt.tenant_id AND n.id=jt.ticket_node_id WHERE jt.project_node_id=p.id) stats
 CROSS JOIN LATERAL (SELECT max(at) FILTER(WHERE type IN ('journey.candidate_approved','journey.deploy_retried')) AS deploy,
 max(at) FILTER(WHERE type='journey.permit_approved') AS access,
 coalesce(max((after->>'revision')::bigint) FILTER(WHERE type IN ('journey.candidate_renewed','journey.deploy_renewed')),0) AS renewal
 FROM events WHERE node_id=p.id AND after->>'current_release_id'=r.release_node_id::text) windows
 ORDER BY array_position($1::uuid[],p.id)`
	query = strings.Replace(query, "DIGEST_QUERY", strings.ReplaceAll(requirements.DigestContentSQL, "$1", "p.id")+" AS content", 1)
	rows, err := tx.Query(ctx, query, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []actionSnapshot{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var s actionSnapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func foldActionSnapshot(f *facts, s actionSnapshot) {
	if cents, ok := parseCents(s.Cap); ok {
		f.HasCap = true
		f.CapCents = cents
	}
	current := ""
	if f.Release != nil {
		current = f.Release.ID
	}
	f.GateLiveByID = map[string]bool{}
	seen := map[string]bool{}
	for _, g := range s.Gates {
		if seen[g.Gate+"\x00"+g.Release] {
			continue
		}
		seen[g.Gate+"\x00"+g.Release] = true
		if g.Release != "" && g.Release != current {
			continue
		}
		f.GateLiveByID[g.Approval] = g.Live
		var id *string
		switch g.Gate {
		case GateShape:
			id = &f.ShapeGateID
		case GateRequirements:
			id = &f.RequirementsGateID
		case GateBuild:
			if g.Release == current {
				id = &f.BuildGateID
			}
		case GateCandidate:
			if g.Release == current {
				id = &f.CandidateGateID
			}
		case GateDeploy:
			if g.Release == current {
				id = &f.DeployGateID
			}
		case GateAccess:
			if g.Release == current {
				id = &f.AccessGateID
			}
		}
		if id != nil && *id == "" {
			*id = g.Approval
		}
	}
	ranked := map[string]int{}
	for _, o := range s.Offers {
		if o.Consumed {
			continue
		}
		state := gateOfferState(o.Decision, o.Open, o.GrantExists, o.Revoked, o.GrantLive)
		expires := o.Expires
		if o.GrantExpires != nil && o.GrantExpires.Before(expires) {
			expires = *o.GrantExpires
		}
		offer := gateOffer{ID: o.ID, DecidedBy: o.Decider, Live: state == "approved_live", State: state, ExpiresAt: expires.UTC().Format(time.RFC3339)}
		rank := 1
		if offer.Live {
			rank = 3
		} else if state == "pending" {
			rank = 2
		}
		key := o.Scope + "\x00" + o.Resource
		if rank > ranked[key] {
			assignOffer(f, o.Scope, o.Resource, offer, true)
			ranked[key] = rank
		}
	}
	var deployAt, accessAt time.Time
	if s.DeployWindow != nil {
		deployAt = *s.DeployWindow
	}
	if s.AccessWindow != nil {
		accessAt = *s.AccessWindow
	}
	f.DeployHandoff, f.AccessHandoff, f.DeployOutcome, f.VerifyOutcome, f.AccessOutcome = foldHandoffs(s.Handoffs, deployAt, accessAt)
}
