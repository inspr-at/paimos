// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string     { return e.message }
func fail(status int, s string) error { return &apiError{status, s} }
func respondError(w http.ResponseWriter, err error) {
	var e *apiError
	switch {
	case errors.As(err, &e):
		httpapi.WriteError(w, e.status, e.message)
	case errors.Is(err, authz.ErrForbidden):
		authz.WriteForbidden(w, err)
	case errors.Is(err, pgx.ErrNoRows):
		httpapi.WriteError(w, 404, "delivery target not found")
	default:
		httpapi.WriteError(w, 500, "delivery operation failed")
	}
}
func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
	}
	return p, ok
}
func decode(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fail(400, "invalid delivery JSON")
	}
	if dec.Decode(new(any)) != io.EOF {
		return fail(400, "trailing delivery JSON")
	}
	return nil
}
func scope(project *string) authz.Scope {
	if project != nil {
		return authz.Scope{ProjectID: *project}
	}
	return authz.Scope{}
}

type Page struct {
	Items []Item  `json:"items"`
	Next  *string `json:"next_cursor"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	after := query.Get("after")
	key := query.Get("project")
	s := State(query.Get("state"))
	ticket := r.PathValue("id")
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			respondError(w, fail(400, "invalid delivery limit"))
			return
		}
		limit = n
	}
	if after != "" && !workorders.UUID(after) || len(key) > 64 || s != "" && !validState(s) || ticket != "" && !workorders.UUID(ticket) {
		respondError(w, fail(400, "invalid delivery filter"))
		return
	}
	out := Page{Items: []Item{}}
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		project := (*string)(nil)
		if key != "" {
			rows, err := tx.Query(r.Context(), `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id
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
			if len(ids) > 1 {
				return fail(400, "ambiguous project key")
			}
			project = &ids[0]
		}
		if ticket != "" {
			var id string
			if err := tx.QueryRow(r.Context(), `SELECT id::text,project_id::text FROM nodes WHERE id=$1 AND deleted_at IS NULL`, ticket).Scan(&id, &project); err != nil {
				return err
			}
		}
		permissionScope := scope(project)
		if project == nil && ticket == "" {
			permissionScope.AnyProject = true
		}
		if err := authz.RequireTx(r.Context(), tx, p, "delivery.read", permissionScope); err != nil {
			return err
		}
		rows, err := tx.Query(r.Context(), `SELECT `+columns+` FROM delivery_items WHERE ($1::uuid IS NULL OR project_id=$1) AND ($2::uuid IS NULL OR ticket_node_id=$2) AND ($3='' OR state=$3) AND ($4::uuid IS NULL OR id>$4) ORDER BY id LIMIT $5`, project, nullable(ticket), s, nullable(after), limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			i, err := scan(rows)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, i)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			id := out.Items[limit-1].ID
			out.Next = &id
		}
		return nil
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func validateSettings(s Settings) error {
	if s.RequiredChecks != nil {
		if len(*s.RequiredChecks) > 64 {
			return fail(400, "too many required checks")
		}
		seen := map[string]bool{}
		for _, name := range *s.RequiredChecks {
			if strings.TrimSpace(name) != name || name == "" || len(name) > 200 || seen[name] {
				return fail(400, "invalid required checks")
			}
			seen[name] = true
		}
	}
	for k, n := range s.Deadlines {
		if k != Reviewed && k != Pushed && k != CIGreen && k != InQueue && k != QueueFailed || n < 0 || n > 10080 {
			return fail(400, "invalid delivery deadline")
		}
	}
	return nil
}
func projectSettings(ctx context.Context, tx pgx.Tx, id string) (*string, error) {
	if id == "" {
		return nil, nil
	}
	var project string
	err := tx.QueryRow(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND k.slug='project' AND n.deleted_at IS NULL`, id).Scan(&project)
	return &project, err
}
func (m *Module) settings(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	projectID := r.PathValue("projectId")
	if projectID != "" && !workorders.UUID(projectID) {
		respondError(w, fail(400, "invalid project"))
		return
	}
	input := Settings{}
	write := r.Method == http.MethodPut
	if write {
		if err := decode(w, r, &input); err != nil {
			respondError(w, err)
			return
		}
		if err := validateSettings(input); err != nil {
			respondError(w, err)
			return
		}
	}
	var out Settings
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if write {
			if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		project, err := projectSettings(ctx, tx, projectID)
		if err != nil {
			return err
		}
		permission := "delivery.read"
		if write {
			permission = "delivery.manage"
		}
		if err = authz.RequireTx(ctx, tx, p, permission, scope(project)); err != nil {
			return err
		}
		if write {
			var checks any
			if input.RequiredChecks != nil {
				checks = *input.RequiredChecks
			}
			if input.Deadlines == nil {
				input.Deadlines = map[State]int{}
			}
			raw, _ := json.Marshal(input.Deadlines)
			_, err = tx.Exec(ctx, `INSERT INTO delivery_settings(tenant_id,project_id,required_checks,deadlines) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE SET required_checks=EXCLUDED.required_checks,deadlines=EXCLUDED.deadlines,updated_at=clock_timestamp()`, p.TenantID, project, checks, raw)
			if err != nil {
				return err
			}
		}
		out, err = settingsTx(ctx, tx, project)
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
func (m *Module) hold(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("itemId")
	if !workorders.UUID(id) {
		respondError(w, fail(400, "invalid delivery item"))
		return
	}
	var reason *string
	if r.Method == http.MethodPost {
		var in struct {
			Reason string `json:"reason"`
		}
		if err := decode(w, r, &in); err != nil {
			respondError(w, err)
			return
		}
		in.Reason = strings.TrimSpace(in.Reason)
		if in.Reason == "" || len(in.Reason) > 1000 {
			respondError(w, fail(400, "hold reason required"))
			return
		}
		reason = &in.Reason
	}
	var out Item
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		current, err := load(ctx, tx, id)
		if err != nil {
			return err
		}
		if current == nil {
			return pgx.ErrNoRows
		}
		if err = authz.RequireTx(ctx, tx, p, "delivery.manage", scope(current.Project)); err != nil {
			return err
		}
		if current.State == Merged {
			return fail(409, "merged delivery is terminal")
		}
		o := current.Observation
		o.HoldReason = reason
		o.At = m.now()
		if err = platformTx(ctx, tx, &o); err != nil {
			return err
		}
		o.Settings, err = settingsTx(ctx, tx, o.Project)
		if err != nil {
			return err
		}
		kind := "hold"
		if reason == nil {
			kind = "release_hold"
		}
		// The ledger contains no cross-project reader surface. Hold authority is
		// rechecked above, inside this tenant-fenced final write transaction.
		_, err = recordTx(ctx, tx, p.TenantID, internalRecord(kind, o.At, []Observation{o}))
		if err != nil {
			return err
		}
		result, err := load(ctx, tx, id)
		if err == nil && result != nil {
			out = *result
		}
		return err
	})
	if err != nil {
		respondError(w, err)
		return
	}
	httpapi.WriteJSON(w, 200, out)
}
