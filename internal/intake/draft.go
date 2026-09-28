// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/approvals"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *Module) proposeDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		writeError(w, http.StatusForbidden, "only an agent may propose a draft")
		return
	}
	projectID, ok := pathUUID(w, r.PathValue("projectId"))
	if !ok {
		return
	}
	var in draftWrite
	if !decode(w, r, &in) {
		return
	}
	if err := validateDraft(&in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	var out draftView
	err := m.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		scopes, err := authorize(r.Context(), r, tx, p, true)
		if err != nil {
			return err
		}
		if err := lockProject(r.Context(), tx, projectID); err != nil {
			return err
		}
		live, err := approvals.LiveGrant(r.Context(), tx, p.ID, scopeWrite, "node", &projectID, scopes)
		if err != nil {
			return err
		}
		if !live {
			return fail(http.StatusForbidden, "grant required: "+scopeWrite)
		}
		out, err = insertDraft(r.Context(), tx, p, projectID, in)
		return err
	})
	writeResult(w, http.StatusCreated, out, err)
}

func insertDraft(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, in draftWrite) (draftView, error) {
	existing, found, err := findDraft(ctx, tx, projectID, in.IdempotencyKey)
	if err != nil {
		return draftView{}, err
	}
	if found {
		if !sameDraft(existing, in) {
			return draftView{}, fail(http.StatusConflict, "idempotency key was used for different content")
		}
		return presentDraft(ctx, tx, projectID, existing)
	}
	if err := validateDraftLinks(ctx, tx, projectID, in); err != nil {
		return draftView{}, err
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO intake_drafts (
			tenant_id, project_node_id, kind, requirement_kind, target_node_id, title, body,
			base_event_id, proposed_by_principal_id, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, $6, $7, $8, $9::uuid, $10)
		ON CONFLICT (tenant_id, project_node_id, idempotency_key) DO NOTHING
		RETURNING id::text`,
		p.TenantID, projectID, in.Kind, in.RequirementKind, in.TargetNodeID, in.Title, in.Body,
		*in.BaseEventID, p.ID, in.IdempotencyKey)
	var id string
	err = row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, err = findDraft(ctx, tx, projectID, in.IdempotencyKey)
		if err != nil {
			return draftView{}, err
		}
		if !found || !sameDraft(existing, in) {
			return draftView{}, fail(http.StatusConflict, "idempotency key was used for different content")
		}
		return presentDraft(ctx, tx, projectID, existing)
	}
	if err != nil {
		return draftView{}, err
	}
	for i, c := range in.Citations {
		if _, err := tx.Exec(ctx, `
			INSERT INTO intake_citations (
				tenant_id, draft_id, project_node_id, ordinal, source_id, turn_id, locator, quote_sha256)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, $7, $8)`,
			p.TenantID, id, projectID, i, c.SourceID, c.TurnID, c.Locator, c.QuoteSHA256); err != nil {
			return draftView{}, err
		}
	}
	for i, s := range in.Suggestions {
		if _, err := tx.Exec(ctx, `
			INSERT INTO intake_draft_ticket_suggestions (
				tenant_id, draft_id, project_node_id, ordinal, title, estimated_hours, later, access_change)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::numeric, $7, $8)`,
			p.TenantID, id, projectID, i, s.Title, s.EstimatedHours.String(), *s.Later, *s.AccessChange); err != nil {
			return draftView{}, err
		}
	}
	stored, found, err := findDraft(ctx, tx, projectID, in.IdempotencyKey)
	if err != nil || !found {
		if err == nil {
			err = fail(http.StatusInternalServerError, "internal")
		}
		return draftView{}, err
	}
	if _, err := appendMeta(ctx, tx, p, nil, evDraftProposed, map[string]any{
		"id":               stored.ID,
		"project_node_id":  projectID,
		"kind":             stored.Kind,
		"requirement_kind": stored.RequirementKind,
		"target_node_id":   stored.TargetNodeID,
		"base_event_id":    stored.BaseEventID,
		"citation_count":   len(stored.Citations),
		"suggestion_count": len(stored.Suggestions),
		"idempotency_key":  stored.IdempotencyKey,
	}); err != nil {
		return draftView{}, err
	}
	return presentDraft(ctx, tx, projectID, stored)
}

func validateDraftLinks(ctx context.Context, tx pgx.Tx, projectID string, in draftWrite) error {
	if in.TargetNodeID != nil {
		slug, err := nodeInProject(ctx, tx, *in.TargetNodeID, projectID)
		if err != nil {
			return err
		}
		if err := targetKindAllowed(in.Kind, slug); err != nil {
			return err
		}
		if _, err := loadNode(ctx, tx, *in.TargetNodeID, true); err != nil {
			return err
		}
		last, err := lastEventID(ctx, tx, *in.TargetNodeID)
		if err != nil {
			return err
		}
		if last != *in.BaseEventID {
			return fail(http.StatusConflict, "target changed")
		}
	}
	for _, c := range in.Citations {
		var one int
		err := tx.QueryRow(ctx, `
			SELECT 1 FROM intake_sources WHERE project_node_id = $1::uuid AND id = $2::uuid`,
			projectID, c.SourceID).Scan(&one)
		if errors.Is(err, pgx.ErrNoRows) {
			return fail(http.StatusConflict, "citation source is not in this project")
		}
		if err != nil {
			return err
		}
		if c.TurnID == nil {
			continue
		}
		var turnSource string
		err = tx.QueryRow(ctx, `
			SELECT source_id::text FROM intake_transcript_turns
			WHERE project_node_id = $1::uuid AND id = $2::uuid`, projectID, *c.TurnID).Scan(&turnSource)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && turnSource != c.SourceID) {
			return fail(http.StatusConflict, "citation turn is not part of the cited source")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func targetKindAllowed(draftKind, slug string) error {
	switch draftKind {
	case "brief":
		if slug == "project" || slug == "memory" {
			return nil
		}
	case "requirement":
		if slug == "requirement" {
			return nil
		}
	}
	return fail(http.StatusConflict, "draft cannot target this node")
}

func (m *Module) acceptDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, http.StatusForbidden, "only a person may accept a draft")
		return
	}
	projectID, ok := pathUUID(w, r.PathValue("projectId"))
	if !ok {
		return
	}
	draftID, ok := pathUUID(w, r.PathValue("draftId"))
	if !ok {
		return
	}
	var in acceptWrite
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedBaseEventID == nil || *in.ExpectedBaseEventID < 0 {
		writeError(w, http.StatusBadRequest, "invalid base event")
		return
	}
	var out draftView
	err := m.tx(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := lockProject(r.Context(), tx, projectID); err != nil {
			return err
		}
		var err error
		out, err = acceptOne(r.Context(), tx, p, projectID, draftID, *in.ExpectedBaseEventID)
		return err
	})
	writeResult(w, http.StatusOK, out, err)
}

func acceptOne(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, draftID string, expected int64) (draftView, error) {
	return acceptOneMode(ctx, tx, p, projectID, draftID, expected, false)
}

func acceptOneMode(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, draftID string, expected int64, disposable bool) (draftView, error) {
	if disposable {
		if err := requireDisposableOperator(ctx, tx, p, projectID); err != nil {
			return draftView{}, err
		}
	}
	stored, err := findDraftByID(ctx, tx, projectID, draftID)
	if err != nil {
		return draftView{}, err
	}
	if expected != stored.BaseEventID {
		return draftView{}, fail(http.StatusConflict, "target changed")
	}
	if _, ok, err := acceptanceOf(ctx, tx, draftID); err != nil {
		return draftView{}, err
	} else if ok {
		return presentDraft(ctx, tx, projectID, stored)
	}
	var targetID string
	var nodeEvent int64
	if stored.TargetNodeID == nil {
		targetID, nodeEvent, err = createAcceptedNode(ctx, tx, p, projectID, stored)
	} else {
		targetID, nodeEvent, err = applyAcceptedNode(ctx, tx, p, projectID, stored, disposable)
	}
	if err != nil {
		return draftView{}, err
	}
	acceptEvent := nodeEvent
	if acceptEvent == 0 {
		ev, err := appendMeta(ctx, tx, p, &targetID, evDraftAccepted, map[string]any{
			"draft_id":        stored.ID,
			"project_node_id": projectID,
			"target_node_id":  targetID,
		})
		if err != nil {
			return draftView{}, err
		}
		acceptEvent = ev.ID
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO intake_draft_acceptances (
			tenant_id, draft_id, project_node_id, accepted_by_principal_id, target_node_id, event_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6)`,
		p.TenantID, stored.ID, projectID, p.ID, targetID, acceptEvent); err != nil {
		return draftView{}, err
	}
	if nodeEvent != 0 {
		if _, err := appendMeta(ctx, tx, p, &targetID, evDraftAccepted, map[string]any{
			"draft_id":        stored.ID,
			"project_node_id": projectID,
			"target_node_id":  targetID,
			"node_event_id":   nodeEvent,
		}); err != nil {
			return draftView{}, err
		}
	}
	return presentDraft(ctx, tx, projectID, stored)
}

func createAcceptedNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, stored draftRow) (string, int64, error) {
	slug := "memory"
	if stored.Kind == "requirement" {
		slug = "requirement"
		if _, err := tx.Exec(ctx, `SELECT aeon_seed_requirement_kind(NULLIF(current_setting('aeon.tenant_id', true), '')::uuid)`); err != nil {
			return "", 0, err
		}
	}
	node, ev, err := insertChildNode(ctx, tx, p, projectID, slug, stored.Title, stored.Body)
	if err != nil {
		return "", 0, err
	}
	if stored.Kind == "requirement" {
		kind := ""
		if stored.RequirementKind != nil {
			kind = *stored.RequirementKind
		}
		var revision int64
		if err := tx.QueryRow(ctx, `
			UPDATE journey_projects
			SET requirements_revision = requirements_revision + 1, updated_at = now()
			WHERE project_node_id = $1::uuid
			RETURNING requirements_revision`, projectID).Scan(&revision); err != nil {
			return "", 0, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO journey_requirements (
				tenant_id, requirement_node_id, project_node_id, kind, revision, status, origin_draft_id, creation_key)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 'draft', $6::uuid, $7)`,
			p.TenantID, node.ID, projectID, kind, revision, stored.ID, stored.IdempotencyKey); err != nil {
			return "", 0, err
		}
	}
	return node.ID, ev.ID, nil
}

func applyAcceptedNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, stored draftRow, disposable bool) (string, int64, error) {
	targetID := *stored.TargetNodeID
	node, err := loadNode(ctx, tx, targetID, true)
	if err != nil {
		return "", 0, err
	}
	slug, err := nodeInProject(ctx, tx, targetID, projectID)
	if err != nil {
		return "", 0, err
	}
	if err := targetKindAllowed(stored.Kind, slug); err != nil {
		return "", 0, err
	}
	last, err := lastEventID(ctx, tx, targetID)
	if err != nil {
		return "", 0, err
	}
	if last != stored.BaseEventID {
		return "", 0, fail(http.StatusConflict, "target changed")
	}
	var other string
	err = tx.QueryRow(ctx, `
		SELECT draft_id::text FROM intake_draft_acceptances
		WHERE project_node_id = $1::uuid AND target_node_id = $2::uuid AND draft_id <> $3::uuid`,
		projectID, targetID, stored.ID).Scan(&other)
	if err == nil {
		return "", 0, fail(http.StatusConflict, "accepted node is unchanged")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, err
	}
	content, err := recordedContent(ctx, tx, targetID)
	if err != nil {
		return "", 0, err
	}
	if content.missing {
		if !disposable {
			return "", 0, fail(http.StatusConflict, "target has no recorded content")
		}
	} else if content.drifted || node.Title != content.title || node.Body != content.body {
		if !disposable {
			return "", 0, fail(http.StatusConflict, "target changed")
		}
	} else if content.actor == string(tenant.Person) && (node.Title != stored.Title || node.Body != stored.Body) {
		if !disposable {
			return "", 0, fail(http.StatusConflict, "person edit is preserved")
		}
	}
	if node.Title == stored.Title && node.Body == stored.Body {
		return targetID, 0, nil
	}
	ev, err := updateNodeText(ctx, tx, p, node, stored.Title, stored.Body)
	if err != nil {
		return "", 0, err
	}
	return targetID, ev.ID, nil
}

type recorded struct {
	actor   string
	title   string
	body    string
	eventID int64
	drifted bool
	missing bool
}

func recordedContent(ctx context.Context, tx pgx.Tx, nodeID string) (recorded, error) {
	var out recorded
	var after []byte
	err := tx.QueryRow(ctx, `
		SELECT e.id, pr.kind, e.after
		FROM events e
		JOIN principals pr ON pr.tenant_id = e.tenant_id AND pr.id = e.actor_principal_id
		WHERE e.node_id = $1::uuid AND e.type IN ('node.created', 'node.updated')
		ORDER BY e.id DESC
		LIMIT 1`, nodeID).Scan(&out.eventID, &out.actor, &after)
	if errors.Is(err, pgx.ErrNoRows) {
		out.missing = true
		return out, nil
	}
	if err != nil {
		return recorded{}, err
	}
	var snap struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if len(after) == 0 || json.Unmarshal(after, &snap) != nil {
		out.drifted = true
		return out, nil
	}
	out.title = snap.Title
	out.body = snap.Body
	return out, nil
}

func insertChildNode(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, slug, title, body string) (nodeSnap, events.Event, error) {
	var kindID, prefix string
	err := tx.QueryRow(ctx, `
		SELECT id::text, short_prefix FROM node_kinds WHERE slug = $1`, slug).Scan(&kindID, &prefix)
	if errors.Is(err, pgx.ErrNoRows) {
		return nodeSnap{}, events.Event{}, fail(http.StatusConflict, "node kind is not available")
	}
	if err != nil {
		return nodeSnap{}, events.Event{}, err
	}
	var key string
	if err := tx.QueryRow(ctx, `
		SELECT aeon_next_node_key(NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1)`, prefix).Scan(&key); err != nil {
		return nodeSnap{}, events.Event{}, err
	}
	node, err := scanNode(tx.QueryRow(ctx, `
		INSERT INTO nodes (tenant_id, key, kind_id, title, body, fields, state, parent_id, position)
		VALUES (
			$1::uuid, $2, $3::uuid, $4, $5, '{}'::jsonb, 'open', $6::uuid,
			(SELECT COALESCE(MAX(position), 0) + 1 FROM nodes WHERE parent_id = $6::uuid AND deleted_at IS NULL)
		)
		RETURNING `+nodeReturning, p.TenantID, key, kindID, title, body, projectID))
	if err != nil {
		return nodeSnap{}, events.Event{}, err
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &node.ID, Type: evNodeCreated, After: node})
	return node, ev, err
}

func updateNodeText(ctx context.Context, tx pgx.Tx, p tenant.Principal, before nodeSnap, title, body string) (events.Event, error) {
	after, err := scanNode(tx.QueryRow(ctx, `
		UPDATE nodes SET title = $2, body = $3, updated_at = now()
		WHERE id = $1::uuid AND deleted_at IS NULL
		RETURNING `+nodeReturning, before.ID, title, body))
	if err != nil {
		return events.Event{}, err
	}
	return events.Append(ctx, tx, p, events.Change{NodeID: &after.ID, Type: evNodeUpdated, Before: before, After: after})
}

func acceptanceOf(ctx context.Context, tx pgx.Tx, draftID string) (time.Time, bool, error) {
	var at time.Time
	err := tx.QueryRow(ctx, `SELECT accepted_at FROM intake_draft_acceptances WHERE draft_id = $1::uuid`, draftID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	return at, err == nil, err
}
