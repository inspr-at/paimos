// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *Module) addTurn(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	projectID, ok := pathUUID(w, r.PathValue("projectId"))
	if !ok {
		return
	}
	var in turnWrite
	if !decode(w, r, &in) {
		return
	}
	if err := validateTurn(&in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	var out turnView
	err := m.intakeTx(r, p, projectID, true, func(tx pgx.Tx, _ []string) error {
		var err error
		out, err = insertTurn(r.Context(), tx, p, projectID, in)
		return err
	})
	writeResult(w, http.StatusCreated, out, err)
}

func insertTurn(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, in turnWrite) (turnView, error) {
	existing, found, err := findTurn(ctx, tx, projectID, in.IdempotencyKey)
	if err != nil {
		return turnView{}, err
	}
	if found {
		if !sameTurn(existing, in) {
			return turnView{}, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for different content")
		}
		return existing, nil
	}
	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM intake_sources WHERE project_node_id = $1::uuid AND id = $2::uuid`,
		projectID, in.SourceID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return turnView{}, fail(http.StatusConflict, "source is not in this project")
	}
	if err != nil {
		return turnView{}, err
	}
	speakerID, err := resolveSpeaker(ctx, tx, p, in.Speaker, in.SpeakerPrincipalID)
	if err != nil {
		return turnView{}, err
	}
	var next int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(ordinal), -1) + 1 FROM intake_transcript_turns
		WHERE source_id = $1::uuid`, in.SourceID).Scan(&next); err != nil {
		return turnView{}, err
	}
	if next != *in.Ordinal {
		return turnView{}, fail(http.StatusConflict, "turn ordinal must append to this source")
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO intake_transcript_turns (
			tenant_id, project_node_id, source_id, ordinal, speaker, speaker_principal_id, body, idempotency_key)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::uuid, $7, $8)
		ON CONFLICT (tenant_id, project_node_id, idempotency_key) DO NOTHING
		RETURNING id::text, source_id::text, ordinal, speaker, speaker_principal_id::text, body, idempotency_key, created_at`,
		p.TenantID, projectID, in.SourceID, *in.Ordinal, in.Speaker, speakerID, in.Body, in.IdempotencyKey)
	view, err := scanTurn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, err = findTurn(ctx, tx, projectID, in.IdempotencyKey)
		if err != nil {
			return turnView{}, err
		}
		if !found || !sameTurn(existing, in) {
			return turnView{}, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for different content")
		}
		return existing, nil
	}
	if err != nil {
		return turnView{}, err
	}
	_, err = appendMeta(ctx, tx, p, nil, evTurnAppended, map[string]any{
		"id":                   view.ID,
		"project_node_id":      projectID,
		"source_id":            view.SourceID,
		"ordinal":              view.Ordinal,
		"speaker":              view.Speaker,
		"speaker_principal_id": view.SpeakerPrincipalID,
		"idempotency_key":      view.IdempotencyKey,
	})
	return view, err
}

func resolveSpeaker(ctx context.Context, tx pgx.Tx, p tenant.Principal, speaker string, given *string) (string, error) {
	id := ""
	if given != nil && *given != "" {
		id = *given
	} else if string(p.Kind) == speaker {
		id = p.ID
	} else {
		return "", fail(http.StatusBadRequest, "speaker principal is required")
	}
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id = $1::uuid`, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fail(http.StatusBadRequest, "speaker principal is not in this tenant")
	}
	if err != nil {
		return "", err
	}
	if kind != speaker {
		return "", fail(http.StatusBadRequest, "speaker does not match the principal")
	}
	if speaker == "agent" && p.Kind == tenant.Agent && id != p.ID {
		return "", fail(http.StatusForbidden, "an agent cannot speak as another agent")
	}
	return id, nil
}

func findTurn(ctx context.Context, tx pgx.Tx, projectID, key string) (turnView, bool, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, source_id::text, ordinal, speaker, speaker_principal_id::text, body, idempotency_key, created_at
		FROM intake_transcript_turns
		WHERE project_node_id = $1::uuid AND idempotency_key = $2`, projectID, key)
	view, err := scanTurn(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return turnView{}, false, nil
	}
	if err != nil {
		return turnView{}, false, err
	}
	return view, true, nil
}

func scanTurn(row pgx.Row) (turnView, error) {
	var v turnView
	var speakerID *string
	err := row.Scan(&v.ID, &v.SourceID, &v.Ordinal, &v.Speaker, &speakerID, &v.Body, &v.IdempotencyKey, &v.CreatedAt)
	if err != nil {
		return turnView{}, err
	}
	if speakerID != nil {
		v.SpeakerPrincipalID = *speakerID
	}
	return v, nil
}

func sameTurn(got turnView, in turnWrite) bool {
	speaker := ""
	if in.SpeakerPrincipalID != nil {
		speaker = *in.SpeakerPrincipalID
	}
	if speaker == "" {
		speaker = got.SpeakerPrincipalID
	}
	return got.SourceID == in.SourceID && got.Ordinal == *in.Ordinal && got.Speaker == in.Speaker &&
		got.SpeakerPrincipalID == speaker && got.Body == in.Body
}
