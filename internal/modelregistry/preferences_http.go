// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxPreferenceKinds = 256

type preferenceError struct {
	status int
	code   string
}

func (e *preferenceError) Error() string     { return e.code }
func prefFail(status int, code string) error { return &preferenceError{status, code} }
func writePreferenceError(w http.ResponseWriter, err error) {
	var bounded interface {
		error
		HTTPStatus() int
		ErrorCode() string
	}
	if errors.As(err, &bounded) {
		httpapi.WriteJSON(w, bounded.HTTPStatus(), map[string]string{"error": bounded.ErrorCode(), "code": bounded.ErrorCode()})
		return
	}
	var e *preferenceError
	if errors.As(err, &e) {
		httpapi.WriteJSON(w, e.status, map[string]string{"error": e.code, "code": e.code})
		return
	}
	if errors.Is(err, authz.ErrForbidden) {
		authz.WriteForbidden(w, err)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, 404, "not found")
		return
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		httpapi.WriteJSON(w, 409, map[string]string{"error": "slug_taken", "code": "slug_taken"})
		return
	}
	writeErr(w, err)
}

// The same tenant row serializes role changes, links and editor mutations.
// A no-key-update fence avoids blocking child-table FK share locks.
func preferenceFence(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID).Scan(&id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-prefs:'||current_setting('aeon.tenant_id'),0))`)
	return err
}
func projectInput(r *http.Request) (string, error) {
	project := r.URL.Query().Get("project_id")
	if project != "" && !uuidRE.MatchString(project) {
		return "", prefFail(400, "invalid_project_id")
	}
	return project, nil
}
func readableProject(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) error {
	if project == "" {
		return nil
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	var id string
	return tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND k.slug='project' AND n.deleted_at IS NULL`, project).Scan(&id)
}
func authorizePreference(ctx context.Context, tx pgx.Tx, p tenant.Principal, level, project string, person *string) error {
	if p.Kind != tenant.Person {
		return prefFail(403, "person_required")
	}
	switch level {
	case "person":
		if person == nil {
			return prefFail(403, "person_required")
		}
		return authz.RequireTx(ctx, tx, p, "models.read", authz.Scope{})
	case "default":
		return authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{})
	case "project":
		if project == "" {
			return prefFail(400, "project_required")
		}
		if err := readableProject(ctx, tx, p, project); err != nil {
			return err
		}
		return authz.RequireTx(ctx, tx, p, "model_prefs.manage", authz.Scope{ProjectID: project})
	default:
		return prefFail(400, "invalid_level")
	}
}
func levelIndex(level string) int {
	switch level {
	case "default":
		return 0
	case "person":
		return 1
	case "project":
		return 2
	}
	return -1
}

type preferenceRow struct {
	KindID  string          `json:"kind_id"`
	Locked  bool            `json:"locked"`
	Normal  modelprefs.Cell `json:"normal"`
	Complex modelprefs.Cell `json:"complex"`
}
type preferenceLevel struct {
	Residency       *string         `json:"residency"`
	ResidencyLocked bool            `json:"residency_locked"`
	PrefsLocked     bool            `json:"prefs_locked"`
	Revision        int64           `json:"revision"`
	Rows            []preferenceRow `json:"rows"`
	UpdatedBy       *string         `json:"updated_by"`
	UpdatedAt       *time.Time      `json:"updated_at"`
}

func levelResponse(ctx context.Context, tx pgx.Tx, s modelprefs.Scope, kinds []modelprefs.Kind) (preferenceLevel, error) {
	out := preferenceLevel{Residency: s.Residency, ResidencyLocked: s.ResidencyLocked, PrefsLocked: s.PrefsLocked, Revision: s.Revision, Rows: []preferenceRow{}}
	for _, k := range kinds {
		if row, ok := s.Rows[k.Slug]; ok {
			out.Rows = append(out.Rows, preferenceRow{k.ID, row.Locked, cellOrAuto(row.Cells["normal"]), cellOrAuto(row.Cells["complex"])})
		}
	}
	if s.ID != "" {
		if err := tx.QueryRow(ctx, `SELECT updated_by::text,updated_at FROM model_pref_scopes WHERE id=$1`, s.ID).Scan(&out.UpdatedBy, &out.UpdatedAt); err != nil {
			return out, err
		}
	}
	return out, nil
}
func cellOrAuto(c modelprefs.Cell) modelprefs.Cell {
	if c.Mode == "" {
		return modelprefs.Cell{Mode: "auto"}
	}
	return c
}
func visibleKinds(ctx context.Context, tx pgx.Tx, project string) ([]modelprefs.Kind, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,slug,label,hint,project_id::text,system,position FROM work_kinds WHERE archived_at IS NULL AND (project_id IS NULL OR project_id=$1::uuid) ORDER BY position,slug,id LIMIT 257`, optionalUUID(project))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []modelprefs.Kind{}
	for rows.Next() {
		var k modelprefs.Kind
		if err := rows.Scan(&k.ID, &k.Slug, &k.Label, &k.Hint, &k.ProjectID, &k.System, &k.Position); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) > maxPreferenceKinds {
		return nil, prefFail(413, "too_many_kinds")
	}
	return out, nil
}
func optionalUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}
func requestedRevision(r *http.Request) (int64, error) {
	v, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || v < 0 {
		return 0, prefFail(400, "revision_required")
	}
	return v, nil
}

func (m *Module) preferences(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	project, err := projectInput(r)
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	var out preferenceDocument
	err = m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(r.Context(), tx, p, "models.read", authz.Scope{}); err != nil {
			return err
		}
		if err := readableProject(r.Context(), tx, p, project); err != nil {
			return err
		}
		if err := ensureCatalog(r.Context(), tx, p); err != nil {
			return err
		}
		var err error
		out, err = m.preferenceDocument(r.Context(), tx, p, project)
		return err
	})
	if err != nil {
		writePreferenceError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
