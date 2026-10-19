// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type RetentionSettings struct {
	Enabled  bool  `json:"enabled"`
	DryRun   bool  `json:"dry_run"`
	Months   int   `json:"months"`
	Revision int64 `json:"revision"`
}

func systemPrincipal(tenantID, actorID string) tenant.Principal {
	return tenant.Principal{ID: actorID, TenantID: tenantID, Kind: tenant.Agent, Name: "System", Roles: []string{"system"}}
}

func (m *Module) retentionSettingsTx(ctx context.Context, tx pgx.Tx, tenantID string) (RetentionSettings, error) {
	s := RetentionSettings{DryRun: true, Months: 12}
	err := tx.QueryRow(ctx, `SELECT enabled,dry_run,revision FROM import_retention_settings`).Scan(&s.Enabled, &s.DryRun, &s.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT $1::boolean OR EXISTS(SELECT 1 FROM import_project_retention WHERE source='start')`, tenantID == m.tenant).Scan(&s.Enabled)
	}
	return s, err
}

func retentionPerson(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return p, false
	}
	if p.Kind != tenant.Person {
		httpapi.WriteError(w, http.StatusForbidden, "a person is required")
		return p, false
	}
	return p, true
}

func retentionReply(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case err == nil:
		httpapi.WriteJSON(w, http.StatusOK, v)
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, http.StatusForbidden, "permission denied")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, http.StatusNotFound, "START project not found")
	case errors.Is(err, events.ErrConflict):
		httpapi.WriteError(w, http.StatusConflict, "settings changed; reload before saving")
	default:
		httpapi.WriteError(w, http.StatusInternalServerError, "START retention unavailable")
	}
}

func (m *Module) retentionSettings(w http.ResponseWriter, r *http.Request) {
	p, ok := retentionPerson(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled  *bool  `json:"enabled"`
		DryRun   *bool  `json:"dry_run"`
		Expected *int64 `json:"expected_revision"`
	}
	if r.Method == http.MethodPut {
		raw, err := httpapi.ReadRequestBody(w, r, 4096, readDeadline)
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err != nil || dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || in.Enabled == nil || in.DryRun == nil || in.Expected == nil || *in.Expected < 0 {
			httpapi.WriteError(w, http.StatusBadRequest, "enabled, dry_run and expected_revision are required")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), storeBudget)
	defer cancel()
	var out RetentionSettings
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if r.Method == http.MethodPut {
			if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		var err error
		out, err = m.retentionSettingsTx(ctx, tx, p.TenantID)
		if err != nil || r.Method == http.MethodGet {
			return err
		}
		if out.Revision != *in.Expected {
			return events.ErrConflict
		}
		if out.Enabled == *in.Enabled && out.DryRun == *in.DryRun {
			return nil
		}
		before := out
		out.Enabled, out.DryRun, out.Revision = *in.Enabled, *in.DryRun, out.Revision+1
		if _, err = tx.Exec(ctx, `INSERT INTO import_retention_settings(tenant_id,enabled,dry_run,revision) VALUES($1,$2,$3,$4)
		 ON CONFLICT(tenant_id) DO UPDATE SET enabled=excluded.enabled,dry_run=excluded.dry_run,revision=excluded.revision`, p.TenantID, out.Enabled, out.DryRun, out.Revision); err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "import_retention.settings_changed", Before: before, After: out})
		return err
	})
	retentionReply(w, out, err)
}

func (m *Module) retentionCandidates(w http.ResponseWriter, r *http.Request) {
	p, ok := retentionPerson(w, r)
	if !ok {
		return
	}
	after := r.URL.Query().Get("after")
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil {
			limit = 0
		}
	}
	if after != "" && !uuidPattern.MatchString(after) || limit < 1 || limit > 100 {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid cursor or limit")
		return
	}
	out := struct {
		Items []RetentionCandidate `json:"items"`
		Next  *string              `json:"next_cursor"`
	}{Items: []RetentionCandidate{}}
	ctx, cancel := context.WithTimeout(r.Context(), storeBudget)
	defer cancel()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.project_id::text,r.imported_at FROM import_project_retention r WHERE `+retentionEligible+`
		 AND r.project_id>coalesce(nullif($2,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY r.project_id LIMIT $3`, time.Now().UTC(), after, limit+1)
		if err != nil {
			return err
		}
		out.Items, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RetentionCandidate])
		return err
	})
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Next = &out.Items[limit-1].ProjectID
	}
	retentionReply(w, out, err)
}

func (m *Module) engage(w http.ResponseWriter, r *http.Request) {
	p, ok := retentionPerson(w, r)
	if !ok {
		return
	}
	id := r.PathValue("projectId")
	if !uuidPattern.MatchString(id) {
		httpapi.WriteError(w, http.StatusBadRequest, "invalid project id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), storeBudget)
	defer cancel()
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{ProjectID: id}); err != nil {
			return err
		}
		var engaged bool
		if err := tx.QueryRow(ctx, `SELECT r.engaged_at IS NOT NULL FROM import_project_retention r
		 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.project_id
		 WHERE r.project_id=$1 AND r.source='start' AND r.deleted_at IS NULL AND n.deleted_at IS NULL
		 FOR NO KEY UPDATE OF r`, id).Scan(&engaged); err != nil {
			return err
		}
		if engaged {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE import_project_retention SET engaged_at=clock_timestamp(),engaged_by=$2 WHERE project_id=$1`, id, p.ID); err != nil {
			return err
		}
		_, err := events.Append(ctx, tx, p, events.Change{Type: "import_retention.engaged", NodeID: &id, After: map[string]any{"project_id": id, "engaged": true}})
		return err
	})
	retentionReply(w, map[string]bool{"engaged": true}, err)
}
