// SPDX-License-Identifier: AGPL-3.0-only

package journey

import (
	"context"
	"errors"
	"net/http"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

var actionNames = map[string]bool{
	"confirm_brief": true, "go": true, "reduce_scope": true, "park": true, "drop": true,
	"reopen": true, "start_build": true, "approve_candidate": true, "reject_candidate": true,
	"open_first_release": true, "mark_candidate": true,
	"approve_deploy": true, "retry_deploy": true, "approve_permit": true, "plan_next_release": true,
}

func (m *Module) read(ctx context.Context, p tenant.Principal, projectID string) (Journey, error) {
	var out Journey
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		f, err := loadFacts(ctx, tx, projectID, false)
		if err != nil {
			return err
		}
		out = derive(f)
		return nil
	})
	return out, err
}

func (m *Module) setProfile(ctx context.Context, p tenant.Principal, projectID string, in profileWrite) (Journey, error) {
	if in.Profile != "personal" && in.Profile != "professional" && in.Profile != "enterprise" {
		return Journey{}, fail(http.StatusBadRequest, "invalid profile")
	}
	if in.ExpectedRevision < 1 {
		return Journey{}, fail(http.StatusBadRequest, "invalid revision")
	}
	var out Journey
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := requirePerson(ctx, tx, p); err != nil {
			return err
		}
		if err := ensureJourney(ctx, tx, p, projectID); err != nil {
			return err
		}
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		before, err := loadFacts(ctx, tx, projectID, false)
		if err != nil {
			return err
		}
		if before.Revision != in.ExpectedRevision {
			return fail(http.StatusConflict, "journey revision is stale")
		}
		if before.Profile == in.Profile {
			out = derive(before)
			return nil
		}
		var next int64
		err = tx.QueryRow(ctx, `
			UPDATE journey_projects
			SET profile = $3, revision = revision + 1, updated_at = clock_timestamp()
			WHERE project_node_id = $1::uuid AND revision = $2
			RETURNING revision`, projectID, before.Revision, in.Profile).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusConflict, "journey revision is stale")
		}
		if err != nil {
			return err
		}
		after, err := loadFacts(ctx, tx, projectID, false)
		if err != nil {
			return err
		}
		view := derive(after)
		if _, err := writeEvent(ctx, tx, p, projectID, "journey.profile_set",
			snapFrom(before, derive(before), "", "", "", "", nil),
			snapFrom(after, view, "profile", "", "", "", nil)); err != nil {
			return err
		}
		out = view
		return nil
	})
	return out, err
}

func (m *Module) act(ctx context.Context, p tenant.Principal, projectID string, in actionWrite) (Journey, error) {
	return m.actWithMode(ctx, p, projectID, in, false)
}

// actWithMode keeps operator seeding on the same revision, receipt, projection
// and event path as person actions. The seed mode is reachable only from the
// host CLI after the disposable and development guards.
func (m *Module) actWithMode(ctx context.Context, p tenant.Principal, projectID string, in actionWrite, seed bool) (Journey, error) {
	if err := validateAction(in); err != nil {
		return Journey{}, err
	}
	hash, err := canonicalHash(in)
	if err != nil {
		return Journey{}, err
	}
	var out Journey
	err = m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if seed {
			brief, _ := ctx.Value(briefContextKey{}).(string)
			if in.Action != "open_first_release" && in.Action != "start_build" && in.Action != "mark_candidate" && !(brief != "" && in.Action == "confirm_brief") {
				return fail(http.StatusForbidden, "operator seed cannot perform a person decision")
			}
			var operator bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id=$1::uuid AND kind='agent' AND 'operator'=ANY(roles))`, p.ID).Scan(&operator); err != nil {
				return err
			}
			if !operator {
				return fail(http.StatusForbidden, "operator principal required")
			}
		} else {
			if err := requirePerson(ctx, tx, p); err != nil {
				return err
			}
		}
		if err := ensureJourney(ctx, tx, p, projectID); err != nil {
			return err
		}
		if err := lockJourney(ctx, tx, projectID); err != nil {
			return err
		}
		prev, found, err := lookupReceipt(ctx, tx, projectID, in.IdempotencyKey)
		if err != nil {
			return err
		}
		if found {
			if prev != hash {
				return fail(http.StatusConflict, "idempotency key was used for a different action")
			}
			f, err := loadFacts(ctx, tx, projectID, false)
			if err != nil {
				return err
			}
			out = derive(f)
			return nil
		}
		before, err := loadFacts(ctx, tx, projectID, true)
		if err != nil {
			return err
		}
		if seed && !before.Disposable {
			return fail(http.StatusForbidden, "project is not disposable")
		}
		if before.Revision != in.ExpectedRevision {
			return fail(http.StatusConflict, "journey revision is stale")
		}
		if err := pinRelease(before, in); err != nil {
			return err
		}
		view := derive(before)
		if !actionMatches(view.NextAction.Key, in.Action) {
			return fail(http.StatusConflict, "that action is not available")
		}
		if scope, resource, gated := gateTarget(before, in.Action); gated && ptrVal(in.ApprovalRequestID) != "" {
			if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), scope, resource); err != nil {
				return err
			}
		}
		seedGate := seed && (in.Action == "start_build" && view.NextAction.Reason == reasonBuildGate ||
			in.Action == "mark_candidate" && view.NextAction.Reason == reasonCompletionGate)
		if in.Action != "reject_candidate" && !view.NextAction.Available && !seedGate {
			msg := view.NextAction.Reason
			if msg == "" {
				msg = "that action is not available"
			}
			return fail(http.StatusConflict, msg)
		}
		cap, superseded, err := applyAction(ctx, tx, p, before, in, seed)
		if err != nil {
			return err
		}
		after, err := loadFacts(ctx, tx, projectID, false)
		if err != nil {
			return err
		}
		adjustFacts(&after, in.Action)
		next := derive(after)
		kind, action := eventType(in.Action), in.Action
		if seed {
			kind, action = "journey.seed_"+in.Action, "seed:"+in.Action
		}
		eventID, err := writeEvent(ctx, tx, p, projectID, kind,
			snapFrom(before, view, "", "", "", "", nil),
			snapFrom(after, next, action, ptrVal(in.ApprovalRequestID), cap, ptrVal(in.Reason), superseded))
		if err != nil {
			return err
		}
		if err := insertReceipt(ctx, tx, p.TenantID, projectID, in.IdempotencyKey, hash, after.Revision, eventID); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

func validateAction(in actionWrite) error {
	if !actionNames[in.Action] {
		return fail(http.StatusBadRequest, "invalid action")
	}
	if in.ExpectedRevision < 1 {
		return fail(http.StatusBadRequest, "invalid revision")
	}
	n := utf8.RuneCountInString(in.IdempotencyKey)
	if n < 1 || n > 128 {
		return fail(http.StatusBadRequest, "invalid idempotency key")
	}
	if ptrVal(in.ApprovalRequestID) != "" && !uuidOK(ptrVal(in.ApprovalRequestID)) {
		return fail(http.StatusBadRequest, "invalid approval_request_id")
	}
	if ptrVal(in.ReleaseID) != "" && !uuidOK(ptrVal(in.ReleaseID)) {
		return fail(http.StatusBadRequest, "invalid release_id")
	}
	return nil
}

func gateTarget(f facts, action string) (string, string, bool) {
	switch action {
	case "go", "reduce_scope", "park", "drop", "reopen":
		return ScopeShape, f.ProjectID, true
	case "start_build":
		if f.Release == nil {
			return "", "", false
		}
		return ScopeBuild, f.Release.ID, true
	case "mark_candidate":
		if f.Release == nil {
			return "", "", false
		}
		return ScopeBuild, f.Release.ID, true
	case "approve_candidate", "reject_candidate":
		if f.Release == nil {
			return "", "", false
		}
		return ScopeCandidate, f.Release.ID, true
	case "approve_deploy", "retry_deploy":
		if f.Release == nil {
			return "", "", false
		}
		return ScopeDeploy, f.Release.ID, true
	case "approve_permit":
		if f.Release == nil {
			return "", "", false
		}
		return ScopeAccess, f.Release.ID, true
	default:
		return "", "", false
	}
}

func pinRelease(f facts, in actionWrite) error {
	id := ptrVal(in.ReleaseID)
	if id == "" {
		return nil
	}
	switch in.Action {
	case "confirm_brief", "go", "reduce_scope", "park", "drop", "reopen":
		return fail(http.StatusBadRequest, "release_id is not used by this action")
	}
	if f.Release == nil || f.Release.ID != id {
		return fail(http.StatusConflict, "release does not match the current release")
	}
	return nil
}

func applyAction(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite, seed bool) (string, []string, error) {
	switch in.Action {
	case "confirm_brief":
		return "", nil, confirmBrief(ctx, tx, f, in)
	case "go", "reduce_scope", "park", "drop":
		capHours, err := decide(ctx, tx, p, f, in)
		return capHours, nil, err
	case "reopen":
		return "", nil, reopen(ctx, tx, p, f, in)
	case "start_build":
		return "", nil, startBuild(ctx, tx, p, f, in, seed)
	case "open_first_release":
		return "", nil, openFirstRelease(ctx, tx, p, f, in)
	case "mark_candidate":
		return "", nil, markCandidate(ctx, tx, p, f, in, seed)
	case "approve_candidate":
		return "", nil, decideCandidate(ctx, tx, p, f, in, "deploying")
	case "reject_candidate":
		return "", nil, decideCandidate(ctx, tx, p, f, in, "building")
	case "approve_deploy":
		return "", nil, approveDeploy(ctx, tx, p, f, in)
	case "retry_deploy":
		return "", nil, retryDeploy(ctx, tx, p, f, in)
	case "approve_permit":
		return "", nil, approvePermit(ctx, tx, p, f, in)
	case "plan_next_release":
		return planNext(ctx, tx, p, f, in)
	default:
		return "", nil, fail(http.StatusBadRequest, "invalid action")
	}
}

func confirmBrief(ctx context.Context, tx pgx.Tx, f facts, in actionWrite) error {
	if ptrVal(in.ApprovalRequestID) != "" {
		return fail(http.StatusBadRequest, "approval_request_id is not used by this action")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_projects
		SET brief_confirmed_at = clock_timestamp(), revision = revision + 1, updated_at = clock_timestamp()
		WHERE project_node_id = $1::uuid AND revision = $2 AND brief_confirmed_at IS NULL`,
		f.ProjectID, f.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "brief is already confirmed")
	}
	return nil
}

func decide(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) (string, error) {
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeShape, f.ProjectID); err != nil {
		return "", err
	}
	if _, err := cleanReason(ptrVal(in.Reason), in.Action == "park" || in.Action == "drop"); err != nil {
		return "", err
	}
	cap := ""
	if in.Action == "go" || in.Action == "reduce_scope" {
		if needsCap(f.Profile) {
			cents, ok, err := projectCap(ctx, tx, f.ProjectID)
			if err != nil {
				return "", err
			}
			if !ok || cents <= 0 {
				return "", fail(http.StatusConflict, "Professional and enterprise decisions need a positive cap_hours on the project.")
			}
			cap = formatCents(cents)
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_projects
		SET decision = $3, revision = revision + 1, updated_at = clock_timestamp()
		WHERE project_node_id = $1::uuid AND revision = $2`,
		f.ProjectID, f.Revision, in.Action)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", fail(http.StatusConflict, "journey revision is stale")
	}
	if err := insertGate(ctx, tx, p.TenantID, f.ProjectID, "", GateShape, ptrVal(in.ApprovalRequestID)); err != nil {
		return "", err
	}
	return cap, nil
}

func reopen(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) error {
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeShape, f.ProjectID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_projects
		SET decision = 'pending', revision = revision + 1, updated_at = clock_timestamp()
		WHERE project_node_id = $1::uuid AND revision = $2`,
		f.ProjectID, f.Revision)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "journey revision is stale")
	}
	return insertGate(ctx, tx, p.TenantID, f.ProjectID, "", GateShape, ptrVal(in.ApprovalRequestID))
}

func startBuild(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite, seed bool) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if !seed {
		if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeBuild, f.Release.ID); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_releases
		SET state = 'building', revision = revision + 1,
		    access_required = access_required OR $2
		WHERE release_node_id = $1::uuid AND state = 'planning'`,
		f.Release.ID, f.AccessChange)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "the release is not in planning")
	}
	if _, err := bumpProject(ctx, tx, f.ProjectID, f.Revision); err != nil {
		return err
	}
	if seed {
		return nil
	}
	return insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateBuild, ptrVal(in.ApprovalRequestID))
}

func openFirstRelease(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) error {
	if f.Release != nil {
		return fail(http.StatusConflict, "a release already exists")
	}
	if ptrVal(in.ApprovalRequestID) != "" {
		return fail(http.StatusBadRequest, "approval_request_id is not used by this action")
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_releases WHERE project_node_id=$1::uuid`, f.ProjectID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fail(http.StatusConflict, "a release already exists")
	}
	_, _, err := createNextRelease(ctx, tx, p, f.ProjectID, f.Revision)
	return err
}

func markCandidate(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite, seed bool) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if !seed {
		if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeBuild, f.Release.ID); err != nil {
			return err
		}
	}
	rows, err := tx.Query(ctx, `SELECT n.id FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.release_node_id=$1::uuid AND n.deleted_at IS NULL ORDER BY n.id FOR SHARE OF n`, f.Release.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var total, open int
	if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE n.state<>'done') FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id WHERE t.release_node_id=$1::uuid AND n.deleted_at IS NULL`, f.Release.ID).Scan(&total, &open); err != nil {
		return err
	}
	if total == 0 {
		return fail(http.StatusConflict, reasonEmptyRelease)
	}
	if open > 0 {
		return fail(http.StatusConflict, reasonOpenTickets)
	}
	tag, err := tx.Exec(ctx, `UPDATE journey_releases SET state='candidate',revision=revision+1 WHERE release_node_id=$1::uuid AND state IN ('building','planning')`, f.Release.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "the release is not building")
	}
	if _, err := tx.Exec(ctx, `UPDATE journey_projects SET current_release_node_id=$2::uuid WHERE project_node_id=$1::uuid AND current_release_node_id IS NULL`, f.ProjectID, f.Release.ID); err != nil {
		return err
	}
	if _, err := bumpProject(ctx, tx, f.ProjectID, f.Revision); err != nil {
		return err
	}
	if seed {
		return nil
	}
	return insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateBuild, ptrVal(in.ApprovalRequestID))
}

func decideCandidate(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite, state string) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeCandidate, f.Release.ID); err != nil {
		return err
	}
	if in.Action == "approve_candidate" && f.Profile == "enterprise" {
		if f.BuildStarter == "" || f.BriefAuthor == "" {
			return fail(http.StatusForbidden, reasonReviewerUnknown)
		}
		if f.DraftCount > 0 {
			return fail(http.StatusForbidden, reasonDrafts)
		}
		if samePerson(p.ID, f.BuildStarter, f.BriefAuthor, f.RequirementsDecider) {
			return fail(http.StatusForbidden, reasonReviewer)
		}
	}
	if in.Action == "reject_candidate" {
		if _, err := cleanReason(ptrVal(in.Reason), true); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_releases
		SET state = $2, revision = revision + 1
		WHERE release_node_id = $1::uuid AND state = 'candidate'`, f.Release.ID, state)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "the release is not a candidate")
	}
	if _, err := bumpProject(ctx, tx, f.ProjectID, f.Revision); err != nil {
		return err
	}
	return insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateCandidate, ptrVal(in.ApprovalRequestID))
}

func approveDeploy(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeDeploy, f.Release.ID); err != nil {
		return err
	}
	if err := insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateDeploy, ptrVal(in.ApprovalRequestID)); err != nil {
		return err
	}
	if deployPhase(f) == outcomeSucceeded {
		if accessNeeded(f) {
			tag, err := tx.Exec(ctx, `
				UPDATE journey_releases
				SET state = 'access', revision = revision + 1
				WHERE release_node_id = $1::uuid AND state IN ('deploying', 'refused')`, f.Release.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return fail(http.StatusConflict, "the release is not waiting for deployment")
			}
		} else if _, err := settleReleased(ctx, tx, f.ProjectID, f.Release.ID); err != nil {
			return err
		}
	}
	_, err := bumpProject(ctx, tx, f.ProjectID, f.Revision)
	return err
}

func retryDeploy(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeDeploy, f.Release.ID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE journey_releases
		SET state = 'deploying', revision = revision + 1
		WHERE release_node_id = $1::uuid AND state IN ('deploying', 'refused')`, f.Release.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fail(http.StatusConflict, "the release is not waiting for deployment")
	}
	if _, err := bumpProject(ctx, tx, f.ProjectID, f.Revision); err != nil {
		return err
	}
	return insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateDeploy, ptrVal(in.ApprovalRequestID))
}

func approvePermit(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) error {
	if f.Release == nil {
		return fail(http.StatusConflict, reasonNoRelease)
	}
	if err := requireGate(ctx, tx, p.ID, ptrVal(in.ApprovalRequestID), ScopeAccess, f.Release.ID); err != nil {
		return err
	}
	if err := insertGate(ctx, tx, p.TenantID, f.ProjectID, f.Release.ID, GateAccess, ptrVal(in.ApprovalRequestID)); err != nil {
		return err
	}
	if f.AccessOutcome == outcomeSucceeded {
		if _, err := settleReleased(ctx, tx, f.ProjectID, f.Release.ID); err != nil {
			return err
		}
	} else {
		tag, err := tx.Exec(ctx, `
			UPDATE journey_releases
			SET state = 'access', revision = revision + 1
			WHERE release_node_id = $1::uuid AND state IN ('deploying', 'access', 'refused')`, f.Release.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fail(http.StatusConflict, "the release is not waiting for access")
		}
	}
	_, err := bumpProject(ctx, tx, f.ProjectID, f.Revision)
	return err
}

func planNext(ctx context.Context, tx pgx.Tx, p tenant.Principal, f facts, in actionWrite) (string, []string, error) {
	if ptrVal(in.ApprovalRequestID) != "" {
		return "", nil, fail(http.StatusBadRequest, "approval_request_id is not used by this action")
	}
	if f.Release == nil {
		return "", nil, fail(http.StatusConflict, reasonNoRelease)
	}
	var superseded []string
	if f.ImportedStage == stageLive && f.Release.State == "planning" {
		var err error
		superseded, err = settleReleased(ctx, tx, f.ProjectID, f.Release.ID)
		if err != nil {
			return "", nil, err
		}
		_, _, err = createNextRelease(ctx, tx, p, f.ProjectID, f.Revision)
		return "", superseded, err
	}
	if f.Release.State != "released" && f.Release.State != "superseded" {
		ready := f.DeployGateID != "" && deployPhase(f) == outcomeSucceeded &&
			(!accessNeeded(f) || (f.AccessGateID != "" && f.AccessOutcome == outcomeSucceeded))
		if !ready {
			return "", nil, fail(http.StatusConflict, "the release is not live")
		}
		var err error
		superseded, err = settleReleased(ctx, tx, f.ProjectID, f.Release.ID)
		if err != nil {
			return "", nil, err
		}
	}
	if _, _, err := createNextRelease(ctx, tx, p, f.ProjectID, f.Revision); err != nil {
		return "", nil, err
	}
	return "", superseded, nil
}

func adjustFacts(f *facts, action string) {
	switch action {
	case "approve_candidate", "retry_deploy":
		f.DeployOutcome = ""
		f.VerifyOutcome = ""
		if action == "approve_candidate" && f.Release != nil {
			f.Release.State = "deploying"
		}
		if action == "retry_deploy" && f.Release != nil {
			f.Release.State = "deploying"
		}
	case "approve_permit":
		if f.AccessOutcome == outcomeFailed {
			f.AccessOutcome = ""
		}
	}
}

func eventType(action string) string {
	switch action {
	case "confirm_brief":
		return "journey.brief_confirmed"
	case "go", "reduce_scope", "park", "drop":
		return "journey.decided"
	case "reopen":
		return "journey.reopened"
	case "start_build":
		return "journey.build_started"
	case "open_first_release":
		return "journey.release_opened"
	case "mark_candidate":
		return "journey.candidate_marked"
	case "approve_candidate":
		return "journey.candidate_approved"
	case "reject_candidate":
		return "journey.candidate_rejected"
	case "approve_deploy":
		return "journey.deploy_approved"
	case "retry_deploy":
		return "journey.deploy_retried"
	case "approve_permit":
		return "journey.permit_approved"
	case "plan_next_release":
		return "journey.release_planned"
	default:
		return "journey.updated"
	}
}
