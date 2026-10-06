// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/deploytarget"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasesettlement"
	"github.com/inspr-at/paimos/internal/requirements"
	"github.com/inspr-at/paimos/internal/tenant"
)

type eventSnap struct {
	ProjectNodeID              string               `json:"project_node_id"`
	Profile                    string               `json:"profile"`
	Revision                   int64                `json:"revision"`
	Decision                   string               `json:"decision"`
	BriefConfirmed             bool                 `json:"brief_confirmed"`
	Disposable                 bool                 `json:"disposable"`
	RequirementsRevision       int64                `json:"requirements_revision"`
	AgreedRequirementsRevision int64                `json:"agreed_requirements_revision"`
	CurrentReleaseID           *string              `json:"current_release_id"`
	ReleaseState               string               `json:"release_state,omitempty"`
	Stage                      string               `json:"stage"`
	Action                     string               `json:"action,omitempty"`
	ApprovalRequestID          *string              `json:"approval_request_id,omitempty"`
	ApprovedCapHours           string               `json:"approved_cap_hours,omitempty"`
	Reason                     string               `json:"reason,omitempty"`
	SupersededReleaseIDs       []string             `json:"superseded_release_ids,omitempty"`
	DeployTarget               *deploytarget.Target `json:"deploy_target,omitempty"`
	DeployTargetDigestSHA256   string               `json:"deploy_target_digest_sha256,omitempty"`
}

type approvalRow struct {
	Scope        string
	ResourceKind string
	ResourceID   string
	Decision     string
	DecidedBy    string
	Open         bool
	GrantLive    bool
	Consumed     bool
}

func (row approvalRow) live() bool {
	return row.Decision == "approved" && row.Open && row.GrantLive && !row.Consumed
}

type handoffRow struct {
	ID         string
	Stage      string
	Operation  string
	State      string
	Result     string
	Attempt    int
	Epoch      int64
	At         time.Time
	Historical bool
}

type handoffIdentity struct {
	ID      string
	Attempt int
	Epoch   int64
}

type nodeSnap struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	KindID    string          `json:"kind_id"`
	Title     string          `json:"title"`
	Body      string          `json:"body"`
	Fields    json.RawMessage `json:"fields"`
	State     string          `json:"state"`
	ParentID  *string         `json:"parent_id"`
	Position  string          `json:"position"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
	DeletedAt *time.Time      `json:"deleted_at"`
}

func ensureJourney(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) error {
	// Match permit renewal, handoff completion and release membership even
	// without parent-status serialization: tenant -> pairing -> tree -> rows.
	if err := db.LockWorkTreeTx(ctx, tx); err != nil {
		return err
	}
	var one int
	err := tx.QueryRow(ctx, `
		SELECT 1
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = $1::uuid AND n.deleted_at IS NULL AND k.slug = 'project'`, projectID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "project not found")
	}
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM journey_projects WHERE project_node_id = $1::uuid)`, projectID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return recordDerivation(ctx, tx, p, projectID)
	}
	if _, err := tx.Exec(ctx, `SELECT aeon_seed_requirement_kind($1::uuid)`, p.TenantID); err != nil {
		return err
	}
	var inserted bool
	err = tx.QueryRow(ctx, `
		INSERT INTO journey_projects (tenant_id, project_node_id)
		VALUES ($1::uuid, $2::uuid)
		ON CONFLICT (tenant_id, project_node_id) DO NOTHING
		RETURNING true`, p.TenantID, projectID).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return recordDerivation(ctx, tx, p, projectID)
	}
	if err != nil {
		return err
	}
	_, err = writeEvent(ctx, tx, p, projectID, "journey.initialized", nil, eventSnap{
		ProjectNodeID: projectID,
		Profile:       "personal",
		Revision:      1,
		Decision:      "pending",
		Stage:         stageInspire,
		Action:        "initialize",
	})
	if err != nil {
		return err
	}
	return recordDerivation(ctx, tx, p, projectID)
}

// The first successful person action records imported data interpretation once.
// GET deliberately does not call this function.
func recordDerivation(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string) error {
	var locked string
	if err := tx.QueryRow(ctx, `SELECT project_node_id::text FROM journey_projects WHERE project_node_id=$1::uuid FOR UPDATE`, projectID).Scan(&locked); err != nil {
		return err
	}
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1::uuid AND type='journey.derived')`, projectID).Scan(&seen); err != nil {
		return err
	}
	if seen {
		return nil
	}
	f, err := loadFacts(ctx, tx, projectID, false)
	if err != nil {
		return err
	}
	if f.ImportedStage == "" {
		return nil
	}
	_, err = writeEvent(ctx, tx, p, projectID, "journey.derived", nil, snapFrom(f, derive(f), "derive", "", "", "", nil))
	return err
}

func lockJourney(ctx context.Context, tx pgx.Tx, projectID string) error {
	var revision int64
	err := tx.QueryRow(ctx, `
		SELECT revision FROM journey_projects WHERE project_node_id = $1::uuid FOR UPDATE`, projectID).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "project not found")
	}
	return err
}

func loadFacts(ctx context.Context, tx pgx.Tx, projectID string, lockRelease bool) (facts, error) {
	var f facts
	var confirmed bool
	var releaseID *string
	f.ProjectID = projectID
	err := tx.QueryRow(ctx, `
		SELECT profile, revision, brief_confirmed_at IS NOT NULL, decision,
		       requirements_revision, agreed_requirements_revision, current_release_node_id::text,
		       EXISTS (
		         SELECT 1
		         FROM intake_drafts d
		         JOIN intake_draft_acceptances a
		           ON a.tenant_id = d.tenant_id AND a.draft_id = d.id
		         WHERE d.project_node_id = journey_projects.project_node_id AND d.kind = 'brief')
		FROM journey_projects
		WHERE project_node_id = $1::uuid`, projectID).Scan(
		&f.Profile, &f.Revision, &confirmed, &f.Decision,
		&f.RequirementsRevision, &f.AgreedRequirementsRevision, &releaseID, &f.AcceptedBrief)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug='project')`, projectID).Scan(&exists); err != nil {
			return facts{}, err
		}
		if !exists {
			return facts{}, fail(404, "project not found")
		}
		f.Profile, f.Revision, f.Decision = "personal", 1, "pending"
		releaseID = nil
		err = nil
	}
	if err != nil {
		return facts{}, err
	}
	f.BriefConfirmed = confirmed
	f.CurrentReleaseRecorded = releaseID != nil
	if err := loadImported(ctx, tx, &f, &releaseID); err != nil {
		return facts{}, err
	}
	if releaseID != nil && *releaseID != "" {
		rel, err := loadRelease(ctx, tx, *releaseID, lockRelease)
		if err != nil {
			return facts{}, err
		}
		f.Release = &rel
	}
	if err := loadTicketStats(ctx, tx, &f); err != nil {
		return facts{}, err
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'draft')
		FROM journey_requirements WHERE project_node_id = $1::uuid`, projectID).Scan(&f.RequirementCount, &f.DraftCount); err != nil {
		return facts{}, err
	}
	current := ""
	if f.Release != nil {
		current = f.Release.ID
	}
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM journey_releases
			WHERE project_node_id = $1::uuid AND state = 'released'
			  AND ($2::uuid IS NULL OR release_node_id <> $2::uuid))`, projectID, nullableUUID(current)).Scan(&f.PriorReleased); err != nil {
		return facts{}, err
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number),0)+1 FROM journey_releases WHERE project_node_id=$1::uuid`, projectID).Scan(&f.NextReleaseNumber); err != nil {
		return facts{}, err
	}
	if err := loadCap(ctx, tx, &f); err != nil {
		return facts{}, err
	}
	if err := loadPeople(ctx, tx, &f); err != nil {
		return facts{}, err
	}
	f.RequirementsDigest, err = requirements.Digest(ctx, tx, projectID)
	if err != nil {
		return facts{}, err
	}
	if err := loadGates(ctx, tx, &f); err != nil {
		return facts{}, err
	}
	if err := loadOffers(ctx, tx, &f); err != nil {
		return facts{}, err
	}
	if f.Release != nil {
		if err := loadHandoffs(ctx, tx, &f); err != nil {
			return facts{}, err
		}
	}
	// The binding is the node's own key and the tenant row. Callers cannot
	// supply either; a missing row fails closed rather than echoing blanks.
	if err := tx.QueryRow(ctx, `
		SELECT n.key,
		       coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1)),
		       t.slug
		FROM nodes n
		JOIN tenants t ON t.id = n.tenant_id
		WHERE n.id = $1::uuid`, projectID).Scan(&f.NodeKey, &f.ProjectKey, &f.TenantSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return facts{}, fail(404, "project not found")
		}
		return facts{}, err
	}
	if f.NodeKey == "" || f.ProjectKey == "" || f.TenantSlug == "" {
		return facts{}, fail(404, "project not found")
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM journey_disposable_projects WHERE project_node_id=$1::uuid)`, projectID).Scan(&f.Disposable); err != nil {
		return facts{}, err
	}
	return f, nil
}

func loadImported(ctx context.Context, tx pgx.Tx, f *facts, releaseID **string) error {
	var imported bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE node_id=$1::uuid AND type='import.node_created')`, f.ProjectID).Scan(&imported); err != nil {
		return err
	}
	if !imported {
		return nil
	}
	f.Imported = true
	if *releaseID != nil {
		f.ImportedStage = stagePlan
		return nil
	}
	var latest *string
	var releaseCount, ticketCount, openTickets int
	var allDone bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE tree AS (
		SELECT n.id,n.parent_id,n.kind_id,n.state,n.created_at,0 AS depth
		FROM nodes n WHERE n.id=$1::uuid AND n.deleted_at IS NULL
		UNION ALL
		SELECT n.id,n.parent_id,n.kind_id,n.state,n.created_at,t.depth+1
		FROM nodes n JOIN tree t ON n.parent_id=t.id AND n.tenant_id=current_setting('aeon.tenant_id')::uuid
		WHERE n.deleted_at IS NULL AND t.depth<64
	), typed AS (
		SELECT t.*,k.slug FROM tree t JOIN node_kinds k ON k.id=t.kind_id AND k.tenant_id=current_setting('aeon.tenant_id')::uuid
	), imported_work AS (
		SELECT id,state FROM typed WHERE slug='ticket'
		 OR (slug='work' AND aeon_work_is_release_leaf(current_setting('aeon.tenant_id')::uuid,id))
		UNION
		SELECT n.id,n.state FROM journey_tickets jt JOIN nodes n ON n.tenant_id=jt.tenant_id AND n.id=jt.ticket_node_id
		 WHERE jt.project_node_id=$1::uuid AND aeon_work_is_release_leaf(n.tenant_id,n.id)
	)
		SELECT (SELECT r.release_node_id::text FROM journey_releases r JOIN nodes n ON n.id=r.release_node_id AND n.tenant_id=r.tenant_id WHERE r.project_node_id=$1::uuid AND n.deleted_at IS NULL ORDER BY n.created_at DESC,n.id DESC LIMIT 1),
		       (SELECT count(*) FROM journey_releases r WHERE r.project_node_id=$1::uuid),
		       (SELECT count(*) FROM imported_work),
		       (SELECT count(*) FROM imported_work WHERE state<>'done'),
		       coalesce((SELECT bool_and(n.state='done') FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id WHERE r.project_node_id=$1::uuid),false)`, f.ProjectID).Scan(&latest, &releaseCount, &ticketCount, &openTickets, &allDone)
	if err != nil {
		return err
	}
	if latest != nil {
		*releaseID = latest
	}
	switch {
	case releaseCount > 0 && allDone && openTickets == 0:
		f.ImportedStage = stageLive
	case releaseCount > 0:
		f.ImportedStage = stageBuild
	case ticketCount > 0:
		f.ImportedStage = stagePlan
	}
	return nil
}

func loadRelease(ctx context.Context, tx pgx.Tx, id string, lock bool) (releaseFacts, error) {
	q := `
		SELECT release_node_id::text, number, state, access_required, revision
		FROM journey_releases WHERE release_node_id = $1::uuid`
	if lock {
		q += ` FOR UPDATE`
	}
	var rel releaseFacts
	err := tx.QueryRow(ctx, q, id).Scan(&rel.ID, &rel.Number, &rel.State, &rel.AccessRequired, &rel.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return releaseFacts{}, fail(409, "current release is missing")
	}
	return rel, err
}

func loadTicketStats(ctx context.Context, tx pgx.Tx, f *facts) error {
	if f.Release == nil {
		return nil
	}
	var scopeN, accessN int
	var plan, spent string
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE release_node_id = $2::uuid AND n.deleted_at IS NULL),
		       count(*) FILTER (WHERE release_node_id = $2::uuid AND n.deleted_at IS NULL AND n.state <> 'done'),
		       count(*) FILTER (WHERE release_node_id = $2::uuid AND scope_revision_required),
		       count(*) FILTER (WHERE release_node_id = $2::uuid AND access_change),
		       coalesce(bool_or(release_node_id = $2::uuid AND estimated_hours IS NULL), false),
		       coalesce(sum(estimated_hours) FILTER (
		         WHERE release_node_id = $2::uuid AND estimated_hours IS NOT NULL), 0)::text
		FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id
		WHERE t.project_node_id = $1::uuid AND t.release_node_id = $2::uuid AND n.deleted_at IS NULL AND aeon_work_is_release_leaf(n.tenant_id,n.id)`, f.ProjectID, f.Release.ID).Scan(
		&f.IncludedTickets, &f.OpenReleaseTickets, &scopeN, &accessN, &f.MissingEstimate, &plan); err != nil {
		return err
	}
	f.ScopeRevision = scopeN > 0
	f.AccessChange = accessN > 0
	cents, ok := parseCents(plan)
	if !ok {
		return fmt.Errorf("journey: plan hours %q", plan)
	}
	f.PlanCents = cents
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(sum(t.estimated_hours), 0)::text
		FROM journey_tickets t
		JOIN journey_releases r
		  ON r.tenant_id = t.tenant_id AND r.release_node_id = t.release_node_id
		WHERE t.project_node_id = $1::uuid
		  AND r.state IN ('released', 'superseded')
		  AND r.release_node_id <> $2::uuid`, f.ProjectID, f.Release.ID).Scan(&spent); err != nil {
		return err
	}
	spentCents, ok := parseCents(spent)
	if !ok {
		return fmt.Errorf("journey: spent hours %q", spent)
	}
	f.SpentCents = spentCents
	return nil
}

func loadCap(ctx context.Context, tx pgx.Tx, f *facts) error {
	var raw *string
	err := tx.QueryRow(ctx, `
		SELECT after->>'approved_cap_hours'
		FROM events
		WHERE node_id = $1::uuid AND type = 'journey.decided'
		  AND after->>'decision' IN ('go', 'reduce_scope')
		  AND coalesce(after->>'approved_cap_hours', '') <> ''
		ORDER BY id DESC
		LIMIT 1`, f.ProjectID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	cents, ok := parseCents(*raw)
	if !ok {
		return nil
	}
	f.HasCap = true
	f.CapCents = cents
	return nil
}

func loadPeople(ctx context.Context, tx pgx.Tx, f *facts) error {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (type) type, actor_principal_id::text
		FROM events
		WHERE node_id = $1::uuid
		  AND type IN ('journey.brief_confirmed', 'journey.build_started')
		ORDER BY type, id DESC`, f.ProjectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, actor string
		if err := rows.Scan(&kind, &actor); err != nil {
			return err
		}
		switch kind {
		case "journey.brief_confirmed":
			f.BriefAuthor = actor
		case "journey.build_started":
			f.BuildStarter = actor
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var decider *string
	err = tx.QueryRow(ctx, `
		SELECT d.decided_by_principal_id::text
		FROM journey_gates g
		JOIN approval_decisions d
		  ON d.tenant_id = g.tenant_id AND d.request_id = g.approval_request_id
		WHERE g.project_node_id = $1::uuid AND g.gate = 'requirements'
		ORDER BY g.created_at DESC
		LIMIT 1`, f.ProjectID).Scan(&decider)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if decider != nil {
		f.RequirementsDecider = *decider
	}
	return nil
}

func loadGates(ctx context.Context, tx pgx.Tx, f *facts) error {
	// Keep this liveness predicate aligned with stagehandoff.gateLive: approved
	// decision, unexpired request, and an unrevoked, unexpired grant. Prefer the
	// live gate's identity over newer expired or revoked history.
	rows, err := tx.Query(ctx, `
		SELECT g.gate, coalesce(g.release_node_id::text, ''), g.approval_request_id::text, a_target.target, a_target.target_digest_sha256,
		       EXISTS (
		         SELECT 1 FROM journey_gates live_gate
		         JOIN approval_requests a ON a.tenant_id=live_gate.tenant_id AND a.id=live_gate.approval_request_id
		         JOIN approval_decisions d ON d.tenant_id=a.tenant_id AND d.request_id=a.id
		         JOIN agent_permission_grants grant_row ON grant_row.tenant_id=a.tenant_id AND grant_row.approval_request_id=a.id
		         WHERE live_gate.tenant_id=g.tenant_id AND live_gate.project_node_id=g.project_node_id
		           AND live_gate.approval_request_id=g.approval_request_id
		           AND live_gate.gate=g.gate AND live_gate.release_node_id IS NOT DISTINCT FROM g.release_node_id
		           AND a.resource_kind='node' AND a.resource_id=coalesce(live_gate.release_node_id,live_gate.project_node_id)
		           AND (g.gate<>'requirements' OR a.scope=$2)
		           AND d.decision='approved' AND a.expires_at>now()
		           AND grant_row.revoked_at IS NULL AND grant_row.valid_until>now()
		       ) AS live
		FROM journey_gates g
		JOIN approval_requests a_target ON a_target.tenant_id=g.tenant_id AND a_target.id=g.approval_request_id
		WHERE g.project_node_id = $1::uuid
		ORDER BY g.gate, g.release_node_id, live DESC, g.created_at DESC, g.id DESC`, f.ProjectID, requirementsScope(f.Revision, f.RequirementsDigest))
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]bool{}
	f.GateLiveByID = make(map[string]bool)
	current := ""
	if f.Release != nil {
		current = f.Release.ID
	}
	for rows.Next() {
		var gate, release, approval string
		var target *deploytarget.Target
		var targetDigest *string
		var live bool
		if err := rows.Scan(&gate, &release, &approval, &target, &targetDigest, &live); err != nil {
			return err
		}
		if seen[gate+"\x00"+release] {
			continue
		}
		seen[gate+"\x00"+release] = true
		matches := release == "" || release == current
		if !matches {
			continue
		}
		f.GateLiveByID[approval] = live
		switch gate {
		case GateShape:
			if f.ShapeGateID == "" {
				f.ShapeGateID = approval
			}
		case GateRequirements:
			if f.RequirementsGateID == "" {
				f.RequirementsGateID = approval
			}
		case GateBuild:
			if release == current && f.BuildGateID == "" {
				f.BuildGateID = approval
			}
		case GateCandidate:
			if release == current && f.CandidateGateID == "" {
				f.CandidateGateID = approval
			}
		case GateDeploy:
			if release == current && f.DeployGateID == "" {
				f.DeployGateID = approval
				f.DeployTarget = target
				if targetDigest != nil {
					f.DeployTargetDigestSHA256 = *targetDigest
				}
			}
		case GateAccess:
			if release == current && f.AccessGateID == "" {
				f.AccessGateID = approval
			}
		}
	}
	return rows.Err()
}

func loadOffers(ctx context.Context, tx pgx.Tx, f *facts) error {
	ids := []string{f.ProjectID}
	if f.Release != nil {
		ids = append(ids, f.Release.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT r.scope, r.resource_id::text, r.id::text, r.target, r.target_digest_sha256,
		       coalesce(d.decision, ''), coalesce(d.decided_by_principal_id::text, ''),
		       r.expires_at, g.valid_until,
		       r.expires_at > now(),
		       g.approval_request_id IS NOT NULL, g.revoked_at IS NOT NULL,
		       (g.approval_request_id IS NOT NULL AND g.revoked_at IS NULL AND g.valid_until > now()),
		       EXISTS (SELECT 1 FROM journey_gates jg
		               WHERE jg.tenant_id = r.tenant_id AND jg.approval_request_id = r.id)
		FROM approval_requests r
		LEFT JOIN approval_decisions d
		  ON d.tenant_id = r.tenant_id AND d.request_id = r.id
		LEFT JOIN agent_permission_grants g
		  ON g.tenant_id = r.tenant_id AND g.approval_request_id = r.id
		WHERE r.resource_kind = 'node'
		  AND r.resource_id = ANY($1::uuid[])
		  AND r.scope = ANY($2::text[])
		ORDER BY r.proposed_at DESC, r.id DESC`, ids, []string{
		ScopeShape, requirementsScope(f.Revision, f.RequirementsDigest), ScopeBuild, ScopeCandidate, ScopeDeploy, ScopeAccess,
	})
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var scope, resource, id, decision, decider string
		var target *deploytarget.Target
		var targetDigest *string
		var requestExpiry time.Time
		var grantExpiry *time.Time
		var open, grantExists, revoked, grantLive, consumed bool
		if err := rows.Scan(&scope, &resource, &id, &target, &targetDigest, &decision, &decider, &requestExpiry, &grantExpiry, &open, &grantExists, &revoked, &grantLive, &consumed); err != nil {
			return err
		}
		// loadGates owns consumed standing evidence. It must never hide a
		// pending retry or be published as fresh journey/1.1 offer authority.
		if consumed {
			continue
		}
		key := scope + "\x00" + resource
		state := gateOfferState(decision, open, grantExists, revoked, grantLive)
		expires := requestExpiry
		if grantExpiry != nil && grantExpiry.Before(expires) {
			expires = *grantExpiry
		}
		offer := gateOffer{ID: id, DecidedBy: decider, Live: state == "approved_live", Consumed: consumed, State: state, ExpiresAt: expires.UTC().Format(time.RFC3339)}
		if scope == ScopeDeploy {
			offer.Target = target
			if targetDigest != nil {
				offer.TargetDigestSHA256 = *targetDigest
			}
		}
		// The newest unconsumed request wins within each class.
		rank := 1
		switch {
		case offer.Live:
			rank = 3
		case state == "pending":
			rank = 2
		}
		if rank > seen[key] {
			assignOffer(f, scope, resource, offer, true)
			seen[key] = rank
		}
	}
	return rows.Err()
}

func gateOfferState(decision string, requestOpen, grantExists, revoked, grantLive bool) string {
	if decision == "denied" {
		return "rejected"
	}
	if revoked {
		return "revoked"
	}
	if !requestOpen || (decision == "approved" && grantExists && !grantLive) {
		return "expired"
	}
	if decision == "approved" {
		if !grantExists {
			return "grant_missing"
		}
		return "approved_live"
	}
	return "pending"
}

func assignOffer(f *facts, scope, resource string, offer gateOffer, replace bool) {
	switch scope {
	case ScopeShape:
		if resource == f.ProjectID && (replace || f.Shape.ID == "") {
			f.Shape = offer
		}
	case requirementsScope(f.Revision, f.RequirementsDigest):
		if resource == f.ProjectID && (replace || f.Requirements.ID == "") {
			f.Requirements = offer
		}
	case ScopeBuild:
		if f.Release != nil && resource == f.Release.ID && (replace || f.Build.ID == "") {
			f.Build = offer
		}
	case ScopeCandidate:
		if f.Release != nil && resource == f.Release.ID && (replace || f.Candidate.ID == "") {
			f.Candidate = offer
		}
	case ScopeDeploy:
		if f.Release != nil && resource == f.Release.ID && (replace || f.Deploy.ID == "") {
			f.Deploy = offer
		}
	case ScopeAccess:
		if f.Release != nil && resource == f.Release.ID && (replace || f.Access.ID == "") {
			f.Access = offer
		}
	}
}

func loadHandoffs(ctx context.Context, tx pgx.Tx, f *facts) error {
	var deployWindow, accessWindow *time.Time
	var renewalRevision, permitRevision int64
	err := tx.QueryRow(ctx, `
		SELECT max(at) FILTER (WHERE type IN ('journey.candidate_approved', 'journey.deploy_retried')),
		       max(at) FILTER (WHERE type IN ('journey.permit_approved','journey.permit_renewed')),
		       coalesce(max((after->>'revision')::bigint) FILTER (WHERE type IN ('journey.candidate_renewed', 'journey.deploy_renewed')),0),
		       coalesce(max((after->>'revision')::bigint) FILTER (WHERE type='journey.permit_renewed'),0)
		FROM events
		WHERE node_id = $1::uuid AND after->>'current_release_id' = $2`, f.ProjectID, f.Release.ID).Scan(&deployWindow, &accessWindow, &renewalRevision, &permitRevision)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT h.id::text, h.stage, h.operation, h.state, coalesce(r.outcome, ''), h.attempt, h.authority_epoch, h.created_at, (h.journey_revision < $2 OR (h.stage='access' AND h.journey_revision < $3))
		FROM stage_handoffs h
		LEFT JOIN stage_handoff_results r
		  ON r.tenant_id = h.tenant_id AND r.handoff_id = h.id
		WHERE h.release_node_id = $1::uuid AND h.stage IN ('deploy', 'access')`, f.Release.ID, renewalRevision, permitRevision)
	if err != nil {
		return err
	}
	defer rows.Close()
	var all []handoffRow
	for rows.Next() {
		var row handoffRow
		if err := rows.Scan(&row.ID, &row.Stage, &row.Operation, &row.State, &row.Result, &row.Attempt, &row.Epoch, &row.At, &row.Historical); err != nil {
			return err
		}
		all = append(all, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var deployAt, accessAt time.Time
	if deployWindow != nil {
		deployAt = *deployWindow
	}
	if accessWindow != nil {
		accessAt = *accessWindow
	}
	f.DeployHandoff, f.AccessHandoff, f.DeployOutcome, f.VerifyOutcome, f.AccessOutcome = foldHandoffs(all, deployAt, accessAt)
	return nil
}

func foldHandoffs(rows []handoffRow, deployWindow, accessWindow time.Time) (deploy, access handoffIdentity, deployOutcome, verifyOutcome, accessOutcome string) {
	type best struct {
		row     handoffRow
		present bool
	}
	latest := map[string]handoffRow{}
	ops := map[string]best{}
	for _, row := range rows {
		if prev, ok := latest[row.Stage]; !ok || row.At.After(prev.At) || (row.At.Equal(prev.At) && row.ID > prev.ID) {
			latest[row.Stage] = row
		}
		if row.Historical {
			continue
		}
		window := deployWindow
		if row.Stage == "access" {
			window = accessWindow
		}
		if !window.IsZero() && row.At.Before(window) {
			continue
		}
		key := row.Stage + "\x00" + row.Operation
		cur, ok := ops[key]
		if !ok || row.Attempt > cur.row.Attempt || (row.Attempt == cur.row.Attempt && row.At.After(cur.row.At)) {
			ops[key] = best{row: row, present: true}
		}
	}
	if row, ok := latest["deploy"]; ok {
		deploy = handoffIdentity{ID: row.ID, Attempt: row.Attempt, Epoch: row.Epoch}
	}
	if row, ok := latest["access"]; ok {
		access = handoffIdentity{ID: row.ID, Attempt: row.Attempt, Epoch: row.Epoch}
	}
	if row, ok := ops["deploy\x00deploy"]; ok {
		deployOutcome = terminalOutcome(row.row)
	}
	if row, ok := ops["deploy\x00verify"]; ok {
		verifyOutcome = terminalOutcome(row.row)
	}
	if row, ok := ops["access\x00apply"]; ok {
		accessOutcome = terminalOutcome(row.row)
	}
	return deploy, access, deployOutcome, verifyOutcome, accessOutcome
}

func terminalOutcome(row handoffRow) string {
	if row.State == "succeeded" && row.Result == "succeeded" {
		return outcomeSucceeded
	}
	if row.Result == "failed" || row.State == "failed" || row.State == "revoked" || row.State == "blocked" {
		return outcomeFailed
	}
	return outcomePending
}

func loadApproval(ctx context.Context, tx pgx.Tx, id string) (approvalRow, error) {
	var row approvalRow
	var resource *string
	err := tx.QueryRow(ctx, `
		SELECT r.scope, r.resource_kind, r.resource_id::text,
		       coalesce(d.decision, ''), coalesce(d.decided_by_principal_id::text, ''),
		       r.expires_at > clock_timestamp(),
		       (g.approval_request_id IS NOT NULL AND g.revoked_at IS NULL AND g.valid_until > clock_timestamp()),
		       EXISTS (
		         SELECT 1 FROM journey_gates jg
		         WHERE jg.tenant_id = r.tenant_id AND jg.approval_request_id = r.id)
		FROM approval_requests r
		LEFT JOIN approval_decisions d
		  ON d.tenant_id = r.tenant_id AND d.request_id = r.id
		LEFT JOIN agent_permission_grants g
		  ON g.tenant_id = r.tenant_id AND g.approval_request_id = r.id
		WHERE r.id = $1::uuid`, id).Scan(
		&row.Scope, &row.ResourceKind, &resource, &row.Decision, &row.DecidedBy,
		&row.Open, &row.GrantLive, &row.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return approvalRow{}, fail(403, "approval does not grant this action")
	}
	if err != nil {
		return approvalRow{}, err
	}
	if resource != nil {
		row.ResourceID = *resource
	}
	return row, nil
}

func requireGate(ctx context.Context, tx pgx.Tx, actor, approvalID, scope, resourceID string) error {
	if approvalID == "" {
		return fail(400, "approval_request_id is required")
	}
	if !uuidOK(approvalID) {
		return fail(400, "invalid approval_request_id")
	}
	row, err := loadApproval(ctx, tx, approvalID)
	if err != nil {
		return err
	}
	if row.Scope != scope || row.ResourceKind != "node" || row.ResourceID != resourceID {
		return fail(403, "approval does not grant this action")
	}
	if row.Consumed && row.Decision == "approved" {
		return fail(409, "approval is already used")
	}
	if !row.live() {
		return fail(403, "approval does not grant this action")
	}
	if row.DecidedBy != actor {
		return fail(403, "approval was decided by someone else")
	}
	return nil
}

func insertGate(ctx context.Context, tx pgx.Tx, tenantID, projectID, releaseID, gate, approvalID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO journey_gates (tenant_id, project_node_id, release_node_id, gate, approval_request_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid)`,
		tenantID, projectID, nullableUUID(releaseID), gate, approvalID)
	return err
}

func bumpProject(ctx context.Context, tx pgx.Tx, projectID string, revision int64) (int64, error) {
	var next int64
	err := tx.QueryRow(ctx, `
		UPDATE journey_projects
		SET revision = revision + 1, updated_at = clock_timestamp()
		WHERE project_node_id = $1::uuid AND revision = $2
		RETURNING revision`, projectID, revision).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, fail(409, "journey revision is stale")
	}
	return next, err
}

func requirePerson(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return fail(403, "only a person can change the journey")
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id = $1::uuid`, p.ID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != string(tenant.Person)) {
		return fail(403, "only a person can change the journey")
	}
	return err
}

func writeEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, eventType string, before, after any) (int64, error) {
	production, _ := ctx.Value(productionContextKey{}).(bool)
	brief, _ := ctx.Value(briefContextKey{}).(string)
	if production || brief != "" {
		encoded, err := json.Marshal(after)
		if err != nil {
			return 0, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			return 0, err
		}
		if fields == nil {
			return 0, errors.New("operator event requires an object snapshot")
		}
		if production {
			fields["production"] = json.RawMessage("true")
		}
		if brief != "" {
			fields["disposable"] = json.RawMessage("true")
			fields["brief"] = json.RawMessage(brief)
		}
		after = fields
	}
	ev, err := events.Append(ctx, tx, p, events.Change{
		NodeID: &projectID,
		Type:   eventType,
		Before: before,
		After:  after,
	})
	if err != nil {
		return 0, err
	}
	return ev.ID, nil
}

func snapFrom(f facts, view Journey, action, approvalID, cap, reason string, superseded []string) eventSnap {
	state := ""
	if f.Release != nil {
		state = f.Release.State
	}
	return eventSnap{
		ProjectNodeID:              f.ProjectID,
		Profile:                    f.Profile,
		Revision:                   f.Revision,
		Decision:                   f.Decision,
		BriefConfirmed:             f.BriefConfirmed,
		Disposable:                 f.Disposable,
		RequirementsRevision:       f.RequirementsRevision,
		AgreedRequirementsRevision: f.AgreedRequirementsRevision,
		CurrentReleaseID:           view.CurrentReleaseID,
		ReleaseState:               state,
		Stage:                      view.Stage,
		Action:                     action,
		ApprovalRequestID:          strPtr(approvalID),
		ApprovedCapHours:           cap,
		Reason:                     reason,
		SupersededReleaseIDs:       superseded,
		DeployTarget:               f.DeployTarget,
		DeployTargetDigestSHA256:   f.DeployTargetDigestSHA256,
	}
}

func canonicalHash(in actionWrite) (string, error) {
	body, err := json.Marshal(struct {
		Action            string   `json:"action"`
		ExpectedRevision  int64    `json:"expected_revision"`
		IdempotencyKey    string   `json:"idempotency_key"`
		ApprovalRequestID string   `json:"approval_request_id"`
		ReleaseID         string   `json:"release_id"`
		Reason            string   `json:"reason"`
		TicketNodeIDs     []string `json:"ticket_node_ids,omitempty"`
	}{
		Action:            in.Action,
		ExpectedRevision:  in.ExpectedRevision,
		IdempotencyKey:    in.IdempotencyKey,
		ApprovalRequestID: ptrVal(in.ApprovalRequestID),
		ReleaseID:         ptrVal(in.ReleaseID),
		Reason:            ptrVal(in.Reason),
		TicketNodeIDs:     in.TicketNodeIDs,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

func lookupReceipt(ctx context.Context, tx pgx.Tx, projectID, key string) (string, bool, error) {
	var hash string
	err := tx.QueryRow(ctx, `
		SELECT request_sha256
		FROM journey_action_receipts
		WHERE project_node_id = $1::uuid AND idempotency_key = $2`, projectID, key).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	return hash, err == nil, err
}

func insertReceipt(ctx context.Context, tx pgx.Tx, tenantID, projectID, key, hash string, revision, eventID int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO journey_action_receipts (
			tenant_id, project_node_id, idempotency_key, request_sha256, resulting_revision, event_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
		tenantID, projectID, key, hash, revision, eventID)
	return err
}

func projectCap(ctx context.Context, tx pgx.Tx, projectID string) (int64, bool, error) {
	var raw string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(fields->>'cap_hours', '') FROM nodes WHERE id = $1::uuid`, projectID).Scan(&raw); err != nil {
		return 0, false, err
	}
	cents, ok := parseCents(raw)
	return cents, ok, nil
}

func settleReleased(ctx context.Context, tx pgx.Tx, projectID, releaseID string) ([]string, error) {
	ids, err := releasesettlement.SettleTx(ctx, tx, projectID, releaseID)
	if errors.Is(err, releasesettlement.ErrVersionConflict) {
		return nil, fail(409, err.Error())
	}
	return ids, err
}

func createNextRelease(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, revision int64) (string, int64, error) {
	var allowed []string
	var unrestricted bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(k.allowed_child_kinds, ARRAY[]::text[]), k.allowed_child_kinds IS NULL
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE n.id = $1::uuid`, projectID).Scan(&allowed, &unrestricted); err != nil {
		return "", 0, err
	}
	if !unrestricted && !containsStr(allowed, "release") {
		return "", 0, fail(409, "project cannot contain a release")
	}
	var number int
	if err := tx.QueryRow(ctx, `
		SELECT coalesce(max(number), 0) + 1 FROM journey_releases WHERE project_node_id = $1::uuid`, projectID).Scan(&number); err != nil {
		return "", 0, err
	}
	var key string
	if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key($1::uuid, 'REL')`, p.TenantID).Scan(&key); err != nil {
		return "", 0, err
	}
	title := fmt.Sprintf("Release %d", number)
	var node nodeSnap
	var fields []byte
	err := tx.QueryRow(ctx, `
		INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, position)
		SELECT $1::uuid, $2, k.id, $3, $4::uuid, $5::numeric
		FROM node_kinds k
		WHERE k.tenant_id = $1::uuid AND k.slug = 'release'
		RETURNING id::text, key, kind_id::text, title, body, fields, state, parent_id::text,
		          position::text, created_at, updated_at, deleted_at`,
		p.TenantID, key, title, projectID, number).Scan(
		&node.ID, &node.Key, &node.KindID, &node.Title, &node.Body, &fields, &node.State,
		&node.ParentID, &node.Position, &node.CreatedAt, &node.UpdatedAt, &node.DeletedAt)
	if err != nil {
		return "", 0, err
	}
	if len(fields) == 0 {
		fields = []byte(`{}`)
	}
	node.Fields = fields
	if _, err := writeEvent(ctx, tx, p, node.ID, "node.created", nil, node); err != nil {
		return "", 0, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO journey_releases (tenant_id, release_node_id, project_node_id, number, state)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'planning')`,
		p.TenantID, node.ID, projectID, number); err != nil {
		return "", 0, err
	}
	var next int64
	err = tx.QueryRow(ctx, `
		UPDATE journey_projects
		SET current_release_node_id = $2::uuid, revision = revision + 1, updated_at = clock_timestamp()
		WHERE project_node_id = $1::uuid AND revision = $3
		RETURNING revision`, projectID, node.ID, revision).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, fail(409, "journey revision is stale")
	}
	if err != nil {
		return "", 0, err
	}
	return node.ID, next, nil
}

func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func containsStr(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func cleanReason(raw string, required bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if required && trimmed == "" {
		return "", fail(400, "reason is required")
	}
	if utf8.RuneCountInString(trimmed) > 2048 {
		return "", fail(400, "reason is too long")
	}
	return trimmed, nil
}
