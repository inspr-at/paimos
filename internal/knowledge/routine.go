// SPDX-License-Identifier: AGPL-3.0-only
package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

func appendKnowledgeChange(ctx context.Context, tx pgx.Tx, p tenant.Principal, c events.Change, pending *[]events.Change) (events.Event, error) {
	if pending == nil {
		return events.Append(ctx, tx, p, c)
	}
	*pending = append(*pending, c)
	return events.Event{}, nil
}

// WriteRoutineTx reuses ordinary knowledge parsing, slug and Decision Desk
// guards. The broker holds tenant/tree fences and appends returned audit only
// after persisting its receipt. Type and slug cannot change on an update.
func WriteRoutineTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, target string, revision int64, typ, slug, title, body string) (string, int64, []events.Change, error) {
	if err := authz.RequireTx(ctx, tx, p, "knowledge.write", authz.Scope{ProjectID: project}); err != nil {
		return "", 0, nil, err
	}
	ctx = authz.WithRouteScope(ctx, authz.Scope{ProjectID: project})
	var entry Entry
	pending := []events.Change{}
	var err error
	if target == project {
		raw := map[string]json.RawMessage{}
		for k, v := range map[string]string{"project_id": project, "type": typ, "slug": slug, "title": title, "body": body, "status": "proposed"} {
			raw[k], _ = json.Marshal(v)
		}
		var in createInput
		in, err = parseCreate(raw)
		if err == nil {
			entry, err = createTx(ctx, tx, p, in, &pending)
		}
	} else {
		var links int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM node_relations WHERE source_node_id=$1 OR target_node_id=$1 LIMIT 257) bounded`, target).Scan(&links); err != nil {
			return "", 0, nil, err
		}
		if links > 256 {
			return "", 0, nil, workorders.Fail(409, "knowledge_link_limit_exceeded")
		}
		var current Entry
		current, err = loadEntry(ctx, tx, p.TenantID, target, false)
		if err == nil && (current.Type != typ || current.Slug != slug) {
			err = workorders.Fail(409, "knowledge_identity_changed")
		}
		if err == nil {
			expected := time.UnixMicro(revision)
			entry, err = updateTx(ctx, tx, p, target, patchInput{Title: &title, Body: &body}, &expected, &pending)
		}
	}
	var e *apiError
	if errors.As(err, &e) {
		err = workorders.Fail(e.status, e.code)
	}
	if errors.Is(err, errStale) {
		err = workorders.Fail(409, "target_revision_changed")
	}
	if errors.Is(err, errNotFound) {
		err = workorders.Fail(404, "knowledge_not_found")
	}
	return entry.ID, entry.UpdatedAt.UnixMicro(), pending, err
}
