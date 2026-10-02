// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

type draftRow struct {
	SupersedesDraftID    *string
	Superseded           bool
	RequesterPrincipalID *string
	ID                   string
	Kind                 string
	RequirementKind      *string
	TargetNodeID         *string
	Title                string
	Body                 string
	Extensions           json.RawMessage
	DocumentBytes        *string
	BaseEventID          int64
	IdempotencyKey       string
	ProposedAt           time.Time
	Citations            []citationWrite
	Suggestions          []suggestionView
}

func (m *Module) getIntake(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	projectID, ok := pathUUID(w, r.PathValue("projectId"))
	if !ok {
		return
	}
	nodeID := r.URL.Query().Get("node_id")
	if nodeID != "" && !uuidPattern.MatchString(nodeID) {
		writeError(w, http.StatusBadRequest, "invalid node id")
		return
	}
	var out snapshot
	err := m.intakeTx(r, p, projectID, false, func(tx pgx.Tx, _ []string) error {
		var err error
		if nodeID == "" {
			out, err = loadSnapshot(r.Context(), tx, projectID)
		} else {
			if _, err := nodeInProject(r.Context(), tx, nodeID, projectID); err != nil {
				return err
			}
			out = snapshot{Sources: []sourceView{}, Turns: []turnView{}, Drafts: []draftView{}}
			rows, loadErr := loadDrafts(r.Context(), tx, projectID, "", nodeID)
			if loadErr != nil {
				return loadErr
			}
			for _, row := range rows {
				view, presentErr := presentDraft(r.Context(), tx, projectID, row)
				if presentErr != nil {
					return presentErr
				}
				out.Drafts = append(out.Drafts, view)
			}
		}
		return err
	})
	writeResult(w, http.StatusOK, out, err)
}

func loadSnapshot(ctx context.Context, tx pgx.Tx, projectID string) (snapshot, error) {
	out := snapshot{Sources: []sourceView{}, Turns: []turnView{}, Drafts: []draftView{}}
	sourceRows, err := tx.Query(ctx, `
		SELECT id::text, project_node_id::text, kind, label, locator, file_id::text,
		       content_sha256, idempotency_key, created_at
		FROM intake_sources
		WHERE project_node_id = $1::uuid
		ORDER BY created_at, id LIMIT 201`, projectID)
	if err != nil {
		return snapshot{}, err
	}
	defer sourceRows.Close()
	for sourceRows.Next() {
		view, err := scanSource(sourceRows)
		if err != nil {
			return snapshot{}, err
		}
		if len(out.Sources) == maxSnapshotRows {
			return snapshot{}, fail(422, "intake history exceeds 200 sources")
		}
		out.Sources = append(out.Sources, view)
	}
	if err := sourceRows.Err(); err != nil {
		return snapshot{}, err
	}

	turnRows, err := tx.Query(ctx, `
		SELECT id::text, source_id::text, ordinal, speaker, speaker_principal_id::text, body, idempotency_key, created_at
		FROM intake_transcript_turns
		WHERE project_node_id = $1::uuid
		ORDER BY source_id, ordinal, id LIMIT 201`, projectID)
	if err != nil {
		return snapshot{}, err
	}
	defer turnRows.Close()
	for turnRows.Next() {
		view, err := scanTurn(turnRows)
		if err != nil {
			return snapshot{}, err
		}
		if len(out.Turns) == maxSnapshotRows {
			return snapshot{}, fail(422, "intake history exceeds 200 turns")
		}
		out.Turns = append(out.Turns, view)
	}
	if err := turnRows.Err(); err != nil {
		return snapshot{}, err
	}

	drafts, err := loadDrafts(ctx, tx, projectID, "")
	if err != nil {
		return snapshot{}, err
	}
	for _, row := range drafts {
		view, err := presentDraft(ctx, tx, projectID, row)
		if err != nil {
			return snapshot{}, err
		}
		out.Drafts = append(out.Drafts, view)
	}
	return out, nil
}

func presentDraft(ctx context.Context, tx pgx.Tx, projectID string, row draftRow) (draftView, error) {
	view := draftView{
		RequesterPrincipalID: row.RequesterPrincipalID,
		SupersedesDraftID:    row.SupersedesDraftID,
		Kind:                 row.Kind,
		RequirementKind:      row.RequirementKind,
		TargetNodeID:         row.TargetNodeID,
		Title:                row.Title,
		Body:                 row.Body,
		Extensions:           row.Extensions,
		DocumentBytes:        row.DocumentBytes,
		BaseEventID:          row.BaseEventID,
		Citations:            row.Citations,
		Suggestions:          row.Suggestions,
		IdempotencyKey:       row.IdempotencyKey,
		ID:                   row.ID,
		Status:               "proposed",
		ProposedAt:           row.ProposedAt,
	}
	if view.Citations == nil {
		view.Citations = []citationWrite{}
	}
	if view.Suggestions == nil {
		view.Suggestions = []suggestionView{}
	}
	if row.Superseded {
		view.Status = "superseded"
		return view, nil
	}

	target, at, ok, err := acceptedTarget(ctx, tx, row.ID)
	if err != nil {
		return draftView{}, err
	}
	if ok {
		view.Status = "accepted"
		view.AcceptedAt = &at
		view.TargetNodeID = &target
		return view, nil
	}
	if row.TargetNodeID == nil {
		return view, nil
	}
	rejected, err := draftRejected(ctx, tx, projectID, row)
	if err != nil {
		return draftView{}, err
	}
	if rejected {
		view.Status = "rejected"
	}
	return view, nil
}

func draftRejected(ctx context.Context, tx pgx.Tx, projectID string, row draftRow) (bool, error) {
	last, err := lastEventID(ctx, tx, *row.TargetNodeID)
	if err != nil {
		return false, err
	}
	if last != row.BaseEventID {
		return true, nil
	}
	var other int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM intake_draft_acceptances
		WHERE project_node_id = $1::uuid AND target_node_id = $2::uuid`,
		projectID, *row.TargetNodeID).Scan(&other)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	node, err := loadNode(ctx, tx, *row.TargetNodeID, false)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) && he.status == http.StatusNotFound {
			return true, nil
		}
		return false, err
	}
	content, err := recordedContent(ctx, tx, *row.TargetNodeID)
	if err != nil {
		return false, err
	}
	if content.missing || content.drifted || node.Title != content.title || node.Body != content.body {
		return true, nil
	}
	if content.actor == string(tenant.Person) && (node.Title != row.Title || node.Body != row.Body) {
		return true, nil
	}
	return false, nil
}

func acceptedTarget(ctx context.Context, tx pgx.Tx, draftID string) (string, time.Time, bool, error) {
	var target string
	var at time.Time
	err := tx.QueryRow(ctx, `
		SELECT target_node_id::text, accepted_at FROM intake_draft_acceptances WHERE draft_id = $1::uuid`, draftID).Scan(&target, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, false, nil
	}
	return target, at, err == nil, err
}

func findDraft(ctx context.Context, tx pgx.Tx, projectID, key string) (draftRow, bool, error) {
	rows, err := loadDrafts(ctx, tx, projectID, key)
	if err != nil {
		return draftRow{}, false, err
	}
	if len(rows) == 0 {
		return draftRow{}, false, nil
	}
	return rows[0], true, nil
}

func findDraftByID(ctx context.Context, tx pgx.Tx, projectID, id string) (draftRow, error) {
	rows, err := loadDraftsFiltered(ctx, tx, projectID, "", id, nil)
	if err != nil {
		return draftRow{}, err
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return draftRow{}, fail(http.StatusNotFound, "draft not found")
}

const maxSnapshotRows = 200

func loadDrafts(ctx context.Context, tx pgx.Tx, projectID, key string, nodeID ...string) ([]draftRow, error) {
	return loadDraftsFiltered(ctx, tx, projectID, key, "", nodeID)
}

func loadDraftsFiltered(ctx context.Context, tx pgx.Tx, projectID, key, draftID string, nodeID []string) ([]draftRow, error) {
	q := `
		SELECT id::text, kind, requirement_kind, target_node_id::text, title, body,
		       base_event_id, idempotency_key, proposed_at, extensions, document_bytes, requester_principal_id::text,
               (SELECT r.old_draft_id::text FROM intake_draft_replacements r
                WHERE r.tenant_id=intake_drafts.tenant_id AND r.project_node_id=intake_drafts.project_node_id AND r.new_draft_id=intake_drafts.id),
               EXISTS(SELECT 1 FROM intake_draft_replacements r
                WHERE r.tenant_id=intake_drafts.tenant_id AND r.project_node_id=intake_drafts.project_node_id AND r.old_draft_id=intake_drafts.id)
		FROM intake_drafts
		WHERE project_node_id = $1::uuid`
	args := []any{projectID}
	if key != "" {
		q += ` AND idempotency_key = $2`
		args = append(args, key)
	}
	if draftID != "" {
		args = append(args, draftID)
		q += " AND id = $" + strconv.Itoa(len(args)) + "::uuid"
	}
	if len(nodeID) > 0 {
		q += ` AND EXISTS (
			SELECT 1 FROM intake_draft_acceptances a
			WHERE a.tenant_id = intake_drafts.tenant_id AND a.draft_id = intake_drafts.id
			  AND (a.target_node_id = $2::uuid OR EXISTS (
				SELECT 1 FROM journey_features f JOIN journey_tickets t
				  ON t.tenant_id = f.tenant_id AND t.project_node_id = f.project_node_id AND t.feature_node_id = f.feature_node_id
				WHERE f.tenant_id = a.tenant_id AND f.project_node_id = a.project_node_id
				  AND f.requirement_node_id = a.target_node_id AND t.ticket_node_id = $2::uuid AND t.source = 'requirements'
			  )))`
		args = append(args, nodeID[0])
	}
	q += ` ORDER BY proposed_at DESC, id LIMIT 201`
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drafts []draftRow
	for rows.Next() {
		var row draftRow
		if err := rows.Scan(&row.ID, &row.Kind, &row.RequirementKind, &row.TargetNodeID, &row.Title, &row.Body, &row.BaseEventID, &row.IdempotencyKey, &row.ProposedAt, &row.Extensions, &row.DocumentBytes, &row.RequesterPrincipalID, &row.SupersedesDraftID, &row.Superseded); err != nil {
			return nil, err
		}
		if len(drafts) == maxSnapshotRows {
			return nil, fail(http.StatusUnprocessableEntity, "intake history exceeds 200 drafts; narrow the selection")
		}
		row.Citations = []citationWrite{}
		row.Suggestions = []suggestionView{}
		drafts = append(drafts, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(drafts))
	byID := map[string]*draftRow{}
	for i := range drafts {
		byID[drafts[i].ID] = &drafts[i]
		ids = append(ids, drafts[i].ID)
	}
	citations, err := tx.Query(ctx, `
		SELECT draft_id::text, source_id::text, turn_id::text, locator, quote_sha256
		FROM intake_citations
		WHERE project_node_id = $1::uuid AND draft_id = ANY($2::uuid[])
		ORDER BY draft_id, ordinal`, projectID, ids)
	if err != nil {
		return nil, err
	}
	defer citations.Close()
	for citations.Next() {
		var draftID string
		var c citationWrite
		if err := citations.Scan(&draftID, &c.SourceID, &c.TurnID, &c.Locator, &c.QuoteSHA256); err != nil {
			return nil, err
		}
		if row := byID[draftID]; row != nil {
			row.Citations = append(row.Citations, c)
		}
	}
	if err := citations.Err(); err != nil {
		return nil, err
	}
	suggestions, err := tx.Query(ctx, `
		SELECT draft_id::text, title, estimated_hours::text, later, access_change
		FROM intake_draft_ticket_suggestions
		WHERE project_node_id = $1::uuid AND draft_id = ANY($2::uuid[])
		ORDER BY draft_id, ordinal`, projectID, ids)
	if err != nil {
		return nil, err
	}
	defer suggestions.Close()
	for suggestions.Next() {
		var draftID, hours string
		var s suggestionView
		if err := suggestions.Scan(&draftID, &s.Title, &hours, &s.Later, &s.AccessChange); err != nil {
			return nil, err
		}
		s.EstimatedHours = json.Number(hours)
		if row := byID[draftID]; row != nil {
			row.Suggestions = append(row.Suggestions, s)
		}
	}
	return drafts, suggestions.Err()
}

func sameDraft(got draftRow, in draftWrite) bool {
	if !sameString(got.RequesterPrincipalID, in.RequesterPrincipalID) || got.Kind != in.Kind || !sameString(got.RequirementKind, in.RequirementKind) || !sameString(got.TargetNodeID, in.TargetNodeID) ||
		!bytes.Equal(got.Extensions, in.Extensions) || !sameString(got.DocumentBytes, in.DocumentBytes) ||
		got.Title != in.Title || got.Body != in.Body || got.BaseEventID != *in.BaseEventID || len(got.Citations) != len(in.Citations) ||
		len(got.Suggestions) != len(in.Suggestions) {
		return false
	}
	for i := range in.Citations {
		if got.Citations[i].SourceID != in.Citations[i].SourceID || !sameString(got.Citations[i].TurnID, in.Citations[i].TurnID) ||
			got.Citations[i].Locator != in.Citations[i].Locator || !sameString(got.Citations[i].QuoteSHA256, in.Citations[i].QuoteSHA256) {
			return false
		}
	}
	for i := range in.Suggestions {
		if got.Suggestions[i].Title != in.Suggestions[i].Title || got.Suggestions[i].Later != *in.Suggestions[i].Later ||
			got.Suggestions[i].AccessChange != *in.Suggestions[i].AccessChange || !hoursEqual(got.Suggestions[i].EstimatedHours.String(), in.Suggestions[i].EstimatedHours.String()) {
			return false
		}
	}
	return true
}

func hoursEqual(stored, requested string) bool {
	left, lerr := strconv.ParseFloat(stored, 64)
	right, rerr := strconv.ParseFloat(requested, 64)
	if lerr != nil || rerr != nil {
		return stored == requested
	}
	return math.Abs(left-right) < 0.001
}
