// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type AlertPage struct {
	Items []Alert `json:"items"`
	Next  *string `json:"next_cursor"`
}

func alertCursor(a Alert) string {
	return base64.RawURLEncoding.EncodeToString([]byte(a.ItemID + "|" + string(a.State) + "|" + a.Since.UTC().Format(time.RFC3339Nano)))
}

func parseAlertCursor(raw string) (Alert, error) {
	if raw == "" {
		return Alert{}, nil
	}
	if len(raw) > 256 {
		return Alert{}, fail(400, "invalid alert cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	parts := strings.Split(string(decoded), "|")
	if err != nil || len(parts) != 3 || !workorders.UUID(parts[0]) || !validState(State(parts[1])) {
		return Alert{}, fail(400, "invalid alert cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[2])
	if err != nil {
		return Alert{}, fail(400, "invalid alert cursor")
	}
	return Alert{ItemID: parts[0], State: State(parts[1]), Since: at}, nil
}

func (m *Module) listAlerts(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	key := q.Get("project")
	after, err := parseAlertCursor(q.Get("after"))
	if err != nil {
		respondError(w, err)
		return
	}
	limit := 50
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
	}
	if err != nil || len(key) > 64 || limit < 1 || limit > 100 {
		respondError(w, fail(400, "invalid alert filter"))
		return
	}
	var open *bool
	if raw, present := q["open"]; present {
		if len(raw) != 1 || raw[0] != "true" && raw[0] != "false" {
			respondError(w, fail(400, "invalid open filter"))
			return
		}
		value := raw[0] == "true"
		open = &value
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := AlertPage{Items: []Alert{}}
	err = db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var project *string
		if key != "" {
			rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id
 WHERE k.slug='project' AND n.deleted_at IS NULL AND
 (n.key=$1 OR coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1))=$1)
 ORDER BY n.id LIMIT 2`, key)
			if err != nil {
				return err
			}
			ids := []string{}
			for rows.Next() {
				var id string
				if err = rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				ids = append(ids, id)
			}
			rows.Close()
			if err = rows.Err(); err != nil {
				return err
			}
			if len(ids) == 0 {
				return pgx.ErrNoRows
			}
			if len(ids) != 1 {
				return fail(400, "ambiguous project key")
			}
			project = &ids[0]
		}
		permissionScope := scope(project)
		permissionScope.AnyProject = project == nil
		if err := authz.RequireTx(ctx, tx, p, "delivery.read", permissionScope); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT a.item_id::text,a.state,a.state_since,a.alerted_at,
 a.recipient_principal_id::text,a.inbox_message_id::text,a.cleared_at FROM delivery_alerts a
 JOIN delivery_items i ON i.tenant_id=a.tenant_id AND i.id=a.item_id
 WHERE ($1::uuid IS NULL OR i.project_id=$1) AND ($2::boolean IS NULL OR (a.cleared_at IS NULL)=$2)
 AND ($3::uuid IS NULL OR (a.item_id,a.state,a.state_since)>($3,$4::text,$5::timestamptz))
 ORDER BY a.item_id,a.state,a.state_since LIMIT $6`, project, open, nullable(after.ItemID), after.State, after.Since, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a Alert
			if err = rows.Scan(&a.ItemID, &a.State, &a.Since, &a.AlertedAt, &a.Recipient, &a.MessageID, &a.ClearedAt); err != nil {
				return err
			}
			out.Items = append(out.Items, a)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			cursor := alertCursor(out.Items[limit-1])
			out.Next = &cursor
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
