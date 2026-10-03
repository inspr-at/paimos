// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/principallink"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evNodeCreated      = "node.created"
	evNodeUpdated      = "node.updated"
	evNodeMoved        = "node.moved"
	evNodeKindChanged  = "node.kind_changed"
	evNodeProjectMoved = "node.project_moved"
	evNodeDeleted      = "node.deleted"
	evKindCreated      = "kind.created"
	evKindUpdated      = "kind.updated"
	evKindDeleted      = "kind.deleted"
)

// Event is one append-only change. Before and After are complete resource
// snapshots; a nil snapshot is SQL NULL. The events trigger allocates id.
type Event struct {
	ActorPrincipalID string
	NodeID           *string
	Type             string
	Before           json.RawMessage
	After            json.RawMessage
	Metadata         json.RawMessage
}

// Writer records one event inside the caller's tenant transaction.
// SQLWriter is the stand-in until internal/events exposes its writer.
type Writer interface {
	WriteEvent(ctx context.Context, tx pgx.Tx, e Event) error
}

// SQLWriter inserts into events. The row is in the same transaction as the
// mutation, so a rollback drops both.
type SQLWriter struct{}

func (SQLWriter) WriteEvent(ctx context.Context, tx pgx.Tx, e Event) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO events (tenant_id, actor_principal_id, node_id, type, before, after, metadata)
		VALUES (
			NULLIF(current_setting('aeon.tenant_id', true), '')::uuid,
			$1::uuid, $2::uuid, $3, $4::jsonb, $5::jsonb, $6::jsonb
		)`, e.ActorPrincipalID, e.NodeID, e.Type, jsonbArg(e.Before), jsonbArg(e.After), jsonbArg(e.Metadata))
	return err
}

func jsonbArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func snapshot(v any) (json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func (m *Module) writeEvent(ctx context.Context, tx pgx.Tx, e Event) error {
	var tenantID string
	if err := tx.QueryRow(ctx, `SELECT current_setting('aeon.tenant_id')`).Scan(&tenantID); err != nil {
		return err
	}
	canonical, _, err := principallink.Resolve(ctx, tx, tenantID, e.ActorPrincipalID)
	if err != nil {
		return err
	}
	e.ActorPrincipalID = canonical
	if err := authorizePortalChange(ctx, tx, tenant.Principal{ID: canonical, TenantID: tenantID}, e.NodeID, e.Type, e.Before, e.After); err != nil {
		if errors.Is(err, errPortalDenied) {
			return &httpError{status: http.StatusForbidden, msg: "permission denied"}
		}
		return err
	}
	if err := m.events.WriteEvent(ctx, tx, e); err != nil {
		return err
	}
	return snapshotNodeChange(ctx, tx, e)
}
