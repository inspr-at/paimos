// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const deletedViewWindow = 30 * 24 * time.Hour

type deletedView struct {
	ID             string    `json:"id"`
	ProjectID      *string   `json:"project_id"`
	Name           string    `json:"name"`
	Mode           string    `json:"mode"`
	DeletedAt      time.Time `json:"deleted_at"`
	DeletionReason string    `json:"deletion_reason"`
}

type deletedViewPage struct {
	Items      []deletedView `json:"items"`
	NextCursor *string       `json:"next_cursor"`
}

type deletedViewCursor struct {
	Tenant    string    `json:"tenant"`
	Principal string    `json:"principal"`
	Until     time.Time `json:"until"`
	At        time.Time `json:"at"`
	ID        string    `json:"id"`
}

func (m *Module) listDeleted(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	q := r.URL.Query()
	limit := 50
	if q.Has("limit") {
		var err error
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			httpapi.WriteError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
	}
	now := m.now().UTC().Truncate(time.Microsecond)
	cursor := deletedViewCursor{Tenant: p.TenantID, Principal: p.ID, Until: now}
	var at, id any
	if q.Has("cursor") {
		raw := q.Get("cursor")
		// Bound the encoded input before decoding or allocating its JSON.
		if len(raw) == 0 || len(raw) > 1024 {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		cursor = deletedViewCursor{}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Tenant != p.TenantID || cursor.Principal != p.ID ||
			cursor.Until.IsZero() || cursor.Until.After(now) || cursor.Until.Before(now.Add(-time.Hour)) ||
			cursor.At.IsZero() || cursor.At.After(cursor.Until) || cursor.At.Before(cursor.Until.Add(-deletedViewWindow)) || !uuidPattern.MatchString(cursor.ID) {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid cursor")
			return
		}
		at, id = cursor.At, cursor.ID
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := deletedViewPage{Items: make([]deletedView, 0, limit)}
	err := m.inTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Materialize the bounded owner-only page before looking up audit evidence.
		// Do not change RLS visibility to obtain a reason: unreadable evidence is unknown.
		rows, err := tx.Query(ctx, `
			WITH page AS MATERIALIZED (
				SELECT id, tenant_id, owner_principal_id, project_id, name, mode, deleted_at
				FROM saved_views
				WHERE owner_principal_id = $1::uuid AND deleted_at >= $2::timestamptz
				  AND deleted_at <= $3::timestamptz
				  AND ($4::timestamptz IS NULL OR (deleted_at, id) < ($4, $5::uuid))
				ORDER BY deleted_at DESC, id DESC LIMIT $6
			)
			SELECT v.id::text, v.project_id::text, v.name, v.mode, v.deleted_at,
			       audit.deleted_at,
			       CASE WHEN audit.before_id = v.id::text AND audit.was_live
			                  AND audit.owner_id = v.owner_principal_id::text
			         THEN CASE
			           WHEN audit.actor_id = v.owner_principal_id THEN 'owner_deleted'
			           WHEN actor.kind = 'agent' AND actor.name = 'System'
			                AND actor.roles @> ARRAY['system']::text[] AND audit.empty_columns
			             THEN 'retired_by_upgrade'
			           ELSE 'unknown' END
			         ELSE 'unknown' END
			FROM page v
			LEFT JOIN LATERAL (
				SELECT e.actor_principal_id AS actor_id, e.before->>'id' AS before_id,
				       e.before->'deleted_at' = 'null'::jsonb AS was_live,
				       e.after->>'owner_principal_id' AS owner_id,
				       left(e.after->>'deleted_at', 65) AS deleted_at,
				       e.before->'columns' = '[]'::jsonb AND e.after->'columns' = '[]'::jsonb AS empty_columns
				FROM events e
				WHERE e.tenant_id = v.tenant_id AND e.type = 'view.deleted' AND e.after->>'id' = v.id::text
				ORDER BY e.id DESC LIMIT 1
			) audit ON true
			LEFT JOIN principals actor ON actor.tenant_id = v.tenant_id AND actor.id = audit.actor_id
			ORDER BY v.deleted_at DESC, v.id DESC`, p.ID, cursor.Until.Add(-deletedViewWindow), cursor.Until, at, id, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v deletedView
			var evidenceAt *string
			if err := rows.Scan(&v.ID, &v.ProjectID, &v.Name, &v.Mode, &v.DeletedAt, &evidenceAt, &v.DeletionReason); err != nil {
				return err
			}
			// Matching timestamps identify the deletion generation; they never
			// determine its reason. PostgreSQL and API snapshots use different
			// RFC3339 timezone spellings, so compare parsed instants.
			if evidenceAt == nil || len(*evidenceAt) > 64 {
				v.DeletionReason = "unknown"
			} else if evidence, err := time.Parse(time.RFC3339Nano, *evidenceAt); err != nil || !evidence.Equal(v.DeletedAt) {
				v.DeletionReason = "unknown"
			}
			out.Items = append(out.Items, v)
		}
		return rows.Err()
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		cursor.At, cursor.ID = last.DeletedAt, last.ID
		data, err := json.Marshal(cursor)
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "could not encode cursor")
			return
		}
		next := base64.RawURLEncoding.EncodeToString(data)
		out.NextCursor = &next
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}
