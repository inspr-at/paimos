// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *Module) addSource(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	projectID, ok := pathUUID(w, r.PathValue("projectId"))
	if !ok {
		return
	}
	var in sourceWrite
	if !decode(w, r, &in) {
		return
	}
	if err := validateSource(&in); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	var out sourceView
	err := m.intakeTx(r, p, projectID, true, func(tx pgx.Tx, _ []string) error {
		var err error
		out, err = insertSource(r.Context(), tx, p, projectID, in)
		return err
	})
	writeResult(w, http.StatusCreated, out, err)
}

func insertSource(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID string, in sourceWrite) (sourceView, error) {
	existing, found, err := findSource(ctx, tx, projectID, in.IdempotencyKey)
	if err != nil {
		return sourceView{}, err
	}
	if found {
		if !sameSource(existing, in) {
			return sourceView{}, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for different content")
		}
		return existing, nil
	}
	if in.FileID != nil {
		if err := fileOwned(ctx, tx, *in.FileID); err != nil {
			return sourceView{}, err
		}
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO intake_sources (
			tenant_id, project_node_id, kind, label, locator, file_id, content_sha256,
			idempotency_key, created_by_principal_id)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7, $8, $9::uuid)
		ON CONFLICT (tenant_id, project_node_id, idempotency_key) DO NOTHING
		RETURNING id::text, project_node_id::text, kind, label, locator, file_id::text,
		          content_sha256, idempotency_key, created_at`,
		p.TenantID, projectID, in.Kind, in.Label, in.Locator, in.FileID, in.ContentSHA256, in.IdempotencyKey, p.ID)
	view, err := scanSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, found, err = findSource(ctx, tx, projectID, in.IdempotencyKey)
		if err != nil {
			return sourceView{}, err
		}
		if !found || !sameSource(existing, in) {
			return sourceView{}, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for different content")
		}
		return existing, nil
	}
	if err != nil {
		return sourceView{}, err
	}
	_, err = appendMeta(ctx, tx, p, nil, evSourceRecorded, map[string]any{
		"id":              view.ID,
		"project_node_id": projectID,
		"kind":            view.Kind,
		"content_sha256":  view.ContentSHA256,
		"file_id":         view.FileID,
		"idempotency_key": view.IdempotencyKey,
	})
	return view, err
}

func findSource(ctx context.Context, tx pgx.Tx, projectID, key string) (sourceView, bool, error) {
	row := tx.QueryRow(ctx, `
		SELECT id::text, project_node_id::text, kind, label, locator, file_id::text,
		       content_sha256, idempotency_key, created_at
		FROM intake_sources
		WHERE project_node_id = $1::uuid AND idempotency_key = $2`, projectID, key)
	view, err := scanSource(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return sourceView{}, false, nil
	}
	return view, err == nil, err
}

func scanSource(row pgx.Row) (sourceView, error) {
	var v sourceView
	err := row.Scan(&v.ID, &v.ProjectNodeID, &v.Kind, &v.Label, &v.Locator, &v.FileID, &v.ContentSHA256, &v.IdempotencyKey, &v.CreatedAt)
	return v, err
}

func sameSource(got sourceView, in sourceWrite) bool {
	return got.Kind == in.Kind && got.Label == in.Label && sameString(got.Locator, in.Locator) &&
		sameString(got.FileID, in.FileID) && got.ContentSHA256 == in.ContentSHA256
}
