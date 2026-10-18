// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Target is a pull or webhook delivery registration for one principal.
type Target struct {
	ID          string    `json:"id"`
	PrincipalID string    `json:"principal_id"`
	Kind        string    `json:"kind"`
	WebhookURL  *string   `json:"webhook_url,omitempty"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
}

// targetMeta is the tenant event. The webhook URL stays on the target row
// because the event stream is readable by every principal in the tenant.
type targetMeta struct {
	ID          string `json:"id"`
	PrincipalID string `json:"principal_id"`
	Kind        string `json:"kind"`
	Enabled     bool   `json:"enabled"`
}

func metaTarget(t Target, enabled bool) targetMeta {
	return targetMeta{ID: t.ID, PrincipalID: t.PrincipalID, Kind: t.Kind, Enabled: enabled}
}

func scanTarget(row pgx.Row) (Target, error) {
	var t Target
	err := row.Scan(&t.ID, &t.PrincipalID, &t.Kind, &t.WebhookURL, &t.Enabled, &t.CreatedAt)
	return t, err
}

func (m *module) handleListTargets(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	items, err := m.listTargets(r.Context(), p)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (m *module) listTargets(ctx context.Context, p tenant.Principal) ([]Target, error) {
	items := []Target{}
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, principal_id::text, kind, webhook_url, enabled, created_at
			FROM inbox_delivery_targets
			WHERE principal_id = $1::uuid
			ORDER BY created_at, id`, p.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanTarget(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (m *module) handleCreateTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var body struct {
		PrincipalID string  `json:"principal_id"`
		Kind        string  `json:"kind"`
		WebhookURL  *string `json:"webhook_url"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	principalID, ok := parseUUID(body.PrincipalID)
	if !ok {
		writeError(w, 400, "invalid_request", "invalid principal_id")
		return
	}
	if !strings.EqualFold(principalID, p.ID) && authz.Require(authz.BindPool(r.Context(), m.pool), "inbox.manage", authz.Scope{}) != nil {
		writeError(w, 403, "forbidden", "forbidden")
		return
	}
	var webhook *string
	switch body.Kind {
	case "pull":
		if body.WebhookURL != nil && *body.WebhookURL != "" {
			writeError(w, 400, "invalid_request", "pull targets have no webhook URL")
			return
		}
	case "webhook":
		if body.WebhookURL == nil || *body.WebhookURL == "" {
			writeError(w, 400, "invalid_request", "webhook URL is required")
			return
		}
		if err := validateWebhookURL(r.Context(), *body.WebhookURL); err != nil {
			writeError(w, 400, "invalid_request", "webhook URL is not a public https address")
			return
		}
		webhook = body.WebhookURL
	default:
		writeError(w, 400, "invalid_request", "invalid target kind")
		return
	}
	item, err := m.createTarget(r.Context(), p, principalID, body.Kind, webhook)
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (m *module) createTarget(ctx context.Context, p tenant.Principal, principalID, kind string, webhook *string) (Target, error) {
	var out Target
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var present bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM principals WHERE id = $1::uuid)`, principalID).Scan(&present); err != nil {
			return err
		}
		if !present {
			return errNotFound
		}
		if kind == "pull" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inbox_delivery_targets
				WHERE principal_id = $1::uuid AND kind = 'pull')`, principalID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return &httpError{409, "conflict", "principal already has a pull target"}
			}
		}
		item, err := scanTarget(tx.QueryRow(ctx, `INSERT INTO inbox_delivery_targets
			(tenant_id, principal_id, kind, webhook_url)
			VALUES ($1::uuid, $2::uuid, $3, $4)
			RETURNING id::text, principal_id::text, kind, webhook_url, enabled, created_at`,
			p.TenantID, principalID, kind, webhook))
		if err != nil {
			return mapWrite(err)
		}
		if _, err := events.Append(ctx, tx, p, events.Change{Type: "inbox.target_created", After: metaTarget(item, true)}); err != nil {
			return err
		}
		out = item
		return nil
	})
	return out, err
}

func (m *module) handleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id, ok := parseUUID(r.PathValue("targetId"))
	if !ok {
		writeError(w, 404, "not_found", "not found")
		return
	}
	if err := m.disableTarget(r.Context(), p, id); err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (m *module) disableTarget(ctx context.Context, p tenant.Principal, id string) error {
	return db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		item, err := scanTarget(tx.QueryRow(ctx, `SELECT id::text, principal_id::text, kind, webhook_url, enabled, created_at
			FROM inbox_delivery_targets WHERE id = $1::uuid FOR NO KEY UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if !strings.EqualFold(item.PrincipalID, p.ID) && authz.RequireTx(ctx, tx, p, "inbox.manage", authz.Scope{}) != nil {
			return errForbidden
		}
		if !item.Enabled {
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE inbox_delivery_targets SET enabled = false
			WHERE id = $1::uuid AND enabled`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = events.Append(ctx, tx, p, events.Change{
			Type:   "inbox.target_disabled",
			Before: metaTarget(item, true),
			After:  metaTarget(item, false),
		})
		return err
	})
}
