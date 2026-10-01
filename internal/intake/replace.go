// SPDX-License-Identifier: AGPL-3.0-only

package intake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

func (m *Module) replaceDraft(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Agent {
		writeError(w, http.StatusForbidden, "only an agent may replace a draft")
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
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, "invalid JSON")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var in draftWrite
	if !decode(w, r, &in) {
		return
	}
	if err := cleanKey(&in.IdempotencyKey); err != nil {
		writeResult(w, 0, nil, err)
		return
	}
	var out json.RawMessage
	err = m.intakeTx(r, p, projectID, true, func(tx pgx.Tx, scopes []string) error {
		if err := requireDraftGrant(r.Context(), tx, p, projectID, scopes); err != nil {
			return err
		}
		if err := bindRequester(r.Context(), &in); err != nil {
			return err
		}
		var err error
		out, err = replaceOne(r.Context(), tx, p, projectID, draftID, in, raw)
		return err
	})
	writeResult(w, http.StatusCreated, out, err)
}

func replaceOne(ctx context.Context, tx pgx.Tx, p tenant.Principal, projectID, oldID string, in draftWrite, raw []byte) (json.RawMessage, error) {
	var priorOld, priorPlugin string
	var priorRequester *string
	var priorSession *string
	var sessionID *string
	if c, ok := delegatedClaims(ctx); ok {
		sessionID = &c.SessionID
	}
	var request, result []byte
	err := tx.QueryRow(ctx, `SELECT old_draft_id::text, plugin_principal_id::text,
		requester_principal_id::text, session_id::text, request_bytes, result_bytes FROM intake_draft_replacements
		WHERE project_node_id=$1::uuid AND operation_key=$2`, projectID, in.IdempotencyKey).
		Scan(&priorOld, &priorPlugin, &priorRequester, &priorSession, &request, &result)
	if err == nil {
		if oldID != priorOld || p.ID != priorPlugin || !sameString(priorRequester, in.RequesterPrincipalID) || !sameString(priorSession, sessionID) || !bytes.Equal(request, raw) {
			return nil, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for different content")
		}
		return json.RawMessage(result), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Operation identity precedes payload/lifecycle validation. Even invalid
	// changed content on an existing key is an idempotency conflict.
	if err := validateDraft(&in); err != nil {
		return nil, err
	}
	if _, err := findDraftByID(ctx, tx, projectID, oldID); err != nil {
		return nil, err
	}
	if _, accepted, err := acceptanceOf(ctx, tx, oldID); err != nil {
		return nil, err
	} else if accepted {
		return nil, refusal(http.StatusConflict, "already_accepted", "draft already accepted")
	}
	if superseded, err := draftSuperseded(ctx, tx, projectID, oldID); err != nil {
		return nil, err
	} else if superseded {
		return nil, refusal(http.StatusConflict, "draft_superseded", "draft was superseded")
	}
	// A create operation's key cannot be recycled into a replace operation.
	if _, exists, err := findDraft(ctx, tx, projectID, in.IdempotencyKey); err != nil {
		return nil, err
	} else if exists {
		return nil, refusal(http.StatusConflict, "idempotency_conflict", "idempotency key was used for another operation")
	}
	view, err := insertDraft(ctx, tx, p, projectID, in)
	if err != nil {
		return nil, err
	}
	view.SupersedesDraftID = &oldID
	result, err = json.Marshal(view)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO intake_draft_replacements
		(tenant_id, project_node_id, old_draft_id, new_draft_id, operation_key,
		 plugin_principal_id, requester_principal_id, session_id, request_bytes, result_bytes)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6::uuid,$7::uuid,$8::uuid,$9,$10)`,
		p.TenantID, projectID, oldID, view.ID, in.IdempotencyKey, p.ID, in.RequesterPrincipalID, sessionID, raw, result); err != nil {
		return nil, err
	}
	if _, err := appendMeta(ctx, tx, p, nil, "intake.draft_superseded", map[string]any{
		"draft_id": oldID, "replacement_draft_id": view.ID, "project_node_id": projectID,
		"requester_principal_id": in.RequesterPrincipalID,
	}); err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}

func draftSuperseded(ctx context.Context, tx pgx.Tx, projectID, draftID string) (bool, error) {
	var superseded bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM intake_draft_replacements
		WHERE project_node_id=$1::uuid AND old_draft_id=$2::uuid)`, projectID, draftID).Scan(&superseded)
	return superseded, err
}
