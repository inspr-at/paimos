// SPDX-License-Identifier: AGPL-3.0-only

// Package statusautopilot runs the workspace's fixed status rules. Human
// decisions, triage judgement and the release approval gates stay with callers.
package statusautopilot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Rule struct {
	Enabled bool `json:"enabled"`
	Days    int  `json:"days,omitempty"`
}

// UnmarshalJSON preserves the contract's required rule toggle and rejects
// unknown attributes; missing enabled must not silently disable a rule.
func (r *Rule) UnmarshalJSON(raw []byte) error {
	var in struct {
		Enabled *bool `json:"enabled"`
		Days    int   `json:"days,omitempty"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return err
	}
	if in.Enabled == nil {
		return fmt.Errorf("rule enabled is required")
	}
	r.Enabled = *in.Enabled
	r.Days = in.Days
	return nil
}

type Settings struct {
	Enabled  bool            `json:"enabled"`
	Rules    map[string]Rule `json:"rules"`
	Revision int64           `json:"revision"`
}
type Override struct {
	Mode      string `json:"mode"`
	Effective bool   `json:"effective_enabled"`
	Revision  int64  `json:"revision"`
}

func Defaults() Settings {
	return Settings{Enabled: true, Rules: map[string]Rule{"new": {true, 7}, "backlog": {true, 90}, "blocked": {true, 14}, "progress": {true, 3}, "done": {true, 14}, "publish": {true, 0}, "accept": {true, 30}}}
}
func (s Settings) Validate() error {
	if len(s.Rules) != 7 {
		return fmt.Errorf("all seven rules required")
	}
	for key := range Defaults().Rules {
		r, ok := s.Rules[key]
		if !ok || key == "publish" && r.Days != 0 || key != "publish" && (r.Days < 1 || r.Days > 365) {
			return fmt.Errorf("invalid rule %s: use 1 to 365 days (no days on publish)", key)
		}
	}
	return nil
}
func Load(ctx context.Context, tx pgx.Tx) (Settings, error) {
	s := Defaults()
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT enabled,rules,revision FROM status_autopilot_settings WHERE tenant_id=current_setting('aeon.tenant_id')::uuid`).Scan(&s.Enabled, &raw, &s.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s.Rules)
	if err == nil {
		err = s.Validate()
	}
	return s, err
}
func Project(ctx context.Context, tx pgx.Tx, project string, workspace bool) (Override, error) {
	out := Override{Mode: "inherit", Effective: workspace}
	err := tx.QueryRow(ctx, `SELECT mode,revision FROM status_autopilot_projects WHERE project_id=$1`, project).Scan(&out.Mode, &out.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if out.Mode != "inherit" {
		out.Effective = out.Mode == "on"
	}
	return out, err
}
func lock(ctx context.Context, tx pgx.Tx) error {
	// Node writers take the tenant tree lock before the event counter. Use the
	// same order for settings, Undo, daily jobs and release hooks.
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id'),0))`)
	return err
}

type Module struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Module { return &Module{pool: pool} }
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings/status-autopilot", m.settings)
	mux.HandleFunc("PUT /api/settings/status-autopilot", m.settings)
	mux.HandleFunc("GET /api/projects/{projectId}/status-autopilot", m.project)
	mux.HandleFunc("PUT /api/projects/{projectId}/status-autopilot", m.project)
	mux.HandleFunc("GET /api/status-autopilot/changes", m.changes)
}

var uuid = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || !uuid.MatchString(p.ID) || !uuid.MatchString(p.TenantID) {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	return p, true
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("one JSON object required")
	}
	return nil
}
func respond(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case err == nil:
		httpapi.WriteJSON(w, 200, v)
	case errors.Is(err, events.ErrConflict):
		httpapi.WriteError(w, 409, "settings or ticket changed; reload")
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, events.ErrForbidden):
		httpapi.WriteError(w, 403, "permission denied")
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "project not found")
	default:
		httpapi.WriteError(w, 500, "status autopilot unavailable")
	}
}
func admin(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return authz.ErrForbidden
	}
	return authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{})
}
func (m *Module) settings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled  *bool           `json:"enabled"`
		Rules    map[string]Rule `json:"rules"`
		Expected *int64          `json:"expected_revision"`
	}
	if r.Method == "PUT" {
		if decode(w, r, &in) != nil || in.Enabled == nil || in.Expected == nil || *in.Expected < 0 {
			httpapi.WriteError(w, 400, "enabled, rules and expected_revision required")
			return
		}
		if err := (Settings{Rules: in.Rules}).Validate(); err != nil {
			httpapi.WriteError(w, 400, err.Error())
			return
		}
	}
	var out Settings
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "PUT" {
			if err := admin(r.Context(), tx, p); err != nil {
				return err
			}
			if err := lock(r.Context(), tx); err != nil {
				return err
			}
		}
		var err error
		out, err = Load(r.Context(), tx)
		if err != nil || r.Method == "GET" {
			return err
		}
		if out.Revision != *in.Expected {
			return events.ErrConflict
		}
		before := out
		out.Enabled = *in.Enabled
		out.Rules = in.Rules
		out.Revision++
		raw, err := json.Marshal(in.Rules)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO status_autopilot_settings(tenant_id,enabled,rules,revision) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id) DO UPDATE SET enabled=excluded.enabled,rules=excluded.rules,revision=excluded.revision`, p.TenantID, out.Enabled, raw, out.Revision)
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{Type: "status_autopilot.settings_changed", Before: before, After: out})
		return err
	})
	respond(w, out, err)
}
func (m *Module) project(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("projectId")
	if !uuid.MatchString(id) {
		httpapi.WriteError(w, 400, "invalid project id")
		return
	}
	var in struct {
		Mode     string `json:"mode"`
		Expected *int64 `json:"expected_revision"`
	}
	if r.Method == "PUT" && (decode(w, r, &in) != nil || in.Expected == nil || *in.Expected < 0 || in.Mode != "inherit" && in.Mode != "on" && in.Mode != "off") {
		httpapi.WriteError(w, 400, "mode and expected_revision required")
		return
	}
	var out Override
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if r.Method == "PUT" {
			if err := admin(r.Context(), tx, p); err != nil {
				return err
			}
			if err := lock(r.Context(), tx); err != nil {
				return err
			}
		}
		var found string
		if err := tx.QueryRow(r.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id AND k.tenant_id=n.tenant_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, id).Scan(&found); err != nil {
			return err
		}
		s, err := Load(r.Context(), tx)
		if err != nil {
			return err
		}
		out, err = Project(r.Context(), tx, id, s.Enabled)
		if err != nil || r.Method == "GET" {
			return err
		}
		if out.Revision != *in.Expected {
			return events.ErrConflict
		}
		before := out
		out.Mode = in.Mode
		out.Revision++
		out.Effective = s.Enabled
		if out.Mode != "inherit" {
			out.Effective = out.Mode == "on"
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO status_autopilot_projects(tenant_id,project_id,mode,revision) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,project_id) DO UPDATE SET mode=excluded.mode,revision=excluded.revision`, p.TenantID, id, out.Mode, out.Revision)
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "status_autopilot.project_changed", Before: before, After: out})
		return err
	})
	respond(w, out, err)
}
