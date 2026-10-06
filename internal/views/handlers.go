// SPDX-License-Identifier: AGPL-3.0-only

package views

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// A list sort key as the node list API spells it ("state", "-updated_at"), a
// grouping and a column id are short lower-case words.
var (
	sortKeyPattern = regexp.MustCompile(`^-?[a-z][a-z_]{0,23}$`)
	wordPattern    = regexp.MustCompile(`^[a-z][a-z_]{0,23}$`)
)

const (
	maxNameRunes   = 80
	maxFilterBytes = 8 << 10
	maxSortKeys    = 8
	maxColumns     = 32
)

// errNotFound hides deleted and foreign views alike.
var errNotFound = errors.New("view not found")

type viewSort struct {
	Field     string `json:"field"`
	Direction string `json:"direction"`
}

// savedView is the API shape and the event snapshot. project_id scopes a view
// to one project's lists (null: workspace-wide); sort_keys is the full list
// sort, while sort keeps the earlier single-key form for older clients.
type savedView struct {
	ID             string          `json:"id"`
	OwnerPrincipal string          `json:"owner_principal_id"`
	ProjectID      *string         `json:"project_id"`
	Name           string          `json:"name"`
	Filters        json.RawMessage `json:"filters"`
	Sort           viewSort        `json:"sort"`
	SortKeys       []string        `json:"sort_keys"`
	GroupBy        string          `json:"group_by"`
	Mode           string          `json:"mode"`
	Columns        []string        `json:"columns"`
	Shared         bool            `json:"shared"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at"`
}

type viewWrite struct {
	Name      string          `json:"name"`
	ProjectID *string         `json:"project_id"`
	Filters   json.RawMessage `json:"filters"`
	Sort      *viewSort       `json:"sort"`
	SortKeys  []string        `json:"sort_keys"`
	GroupBy   string          `json:"group_by"`
	Mode      string          `json:"mode"`
	Columns   []string        `json:"columns"`
	Shared    bool            `json:"shared"`
}

type viewPatch struct {
	Name     *string          `json:"name"`
	Filters  *json.RawMessage `json:"filters"`
	Sort     *viewSort        `json:"sort"`
	SortKeys *[]string        `json:"sort_keys"`
	GroupBy  *string          `json:"group_by"`
	Mode     *string          `json:"mode"`
	Columns  *[]string        `json:"columns"`
	Shared   *bool            `json:"shared"`
}

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var project any
	if r.URL.Query().Has("project_id") {
		id := r.URL.Query().Get("project_id")
		if !uuidPattern.MatchString(id) {
			httpapi.WriteError(w, http.StatusBadRequest, "invalid project_id")
			return
		}
		project = strings.ToLower(id)
	}
	var items []savedView
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// A project's views read in the order they were made (the order of its view
		// bar); the workspace-wide list keeps the most recently changed first.
		order := `updated_at DESC, id`
		if project != nil {
			order = `created_at, id`
		}
		rows, err := tx.Query(r.Context(), `
			SELECT `+viewColumns+`
			FROM saved_views
			WHERE deleted_at IS NULL AND (owner_principal_id = $1::uuid OR shared)
			  AND ($2::uuid IS NULL OR project_id = $2::uuid)
			ORDER BY `+order, p.ID, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		items = make([]savedView, 0)
		for rows.Next() {
			v, err := scanView(rows)
			if err != nil {
				return err
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, struct {
		Items []savedView `json:"items"`
	}{items})
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var in viewWrite
	if err := decodeJSON(w, r, &in); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := normaliseWrite(&in); err != nil {
		httpapi.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	var result savedView
	var badProject bool
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if in.ProjectID != nil {
			ok, err := isProject(r.Context(), tx, *in.ProjectID)
			if err != nil {
				return err
			}
			if !ok {
				badProject = true
				return nil
			}
		}
		var err error
		result, err = insertView(r.Context(), tx, p.TenantID, p.ID, in)
		if err != nil {
			return err
		}
		return m.eventSink.Append(r.Context(), tx, p.ID, "view.created", nil, result)
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	if badProject {
		httpapi.WriteError(w, http.StatusBadRequest, "project_id is not a project")
		return
	}
	httpapi.WriteJSON(w, http.StatusCreated, result)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id := r.PathValue("viewId")
	if !uuidPattern.MatchString(id) {
		httpapi.WriteError(w, http.StatusNotFound, "view not found")
		return
	}
	var result savedView
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		result, err = selectView(r.Context(), tx, `id = $1::uuid AND deleted_at IS NULL AND (owner_principal_id = $2::uuid OR shared)`, id, p.ID)
		return err
	})
	if err != nil {
		writeDBError(w, err)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, result)
}

func (m *Module) patch(w http.ResponseWriter, r *http.Request) {
	var in viewPatch
	m.ownerChange(w, r, "view.updated", func(w http.ResponseWriter, r *http.Request) bool {
		if err := decodeJSON(w, r, &in); err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, err.Error())
			return false
		}
		if err := normalisePatch(&in); err != nil {
			httpapi.WriteError(w, http.StatusBadRequest, err.Error())
			return false
		}
		return true
	}, func(ctx context.Context, tx pgx.Tx, before savedView) (savedView, error) {
		if before.DeletedAt != nil {
			return savedView{}, errNotFound
		}
		return updateView(ctx, tx, before, in)
	}, http.StatusOK)
}

func (m *Module) delete(w http.ResponseWriter, r *http.Request) {
	m.ownerChange(w, r, "view.deleted", nil, func(ctx context.Context, tx pgx.Tx, before savedView) (savedView, error) {
		if before.DeletedAt != nil {
			return savedView{}, errNotFound
		}
		return setDeleted(ctx, tx, before.ID, true)
	}, http.StatusNoContent)
}

// restore brings back a deleted view with its id, so the toast's Undo and links
// to the view keep working. Owner only, like every other change.
func (m *Module) restore(w http.ResponseWriter, r *http.Request) {
	m.ownerChange(w, r, "view.restored", nil, func(ctx context.Context, tx pgx.Tx, before savedView) (savedView, error) {
		if before.DeletedAt == nil {
			return savedView{}, errNotDeleted
		}
		return setDeleted(ctx, tx, before.ID, false)
	}, http.StatusOK)
}

var errNotDeleted = errors.New("view is not deleted")

// ownerChange runs one owner-only change on a view in a tenant transaction and
// appends its event there. read decodes the body first (nil: no body).
func (m *Module) ownerChange(w http.ResponseWriter, r *http.Request, eventType string,
	read func(http.ResponseWriter, *http.Request) bool,
	change func(context.Context, pgx.Tx, savedView) (savedView, error), status int) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	id := r.PathValue("viewId")
	if !uuidPattern.MatchString(id) {
		httpapi.WriteError(w, http.StatusNotFound, "view not found")
		return
	}
	if read != nil && !read(w, r) {
		return
	}
	var result savedView
	var forbidden bool
	err := m.inTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		before, err := selectView(r.Context(), tx, `id = $1::uuid FOR UPDATE`, id)
		if err != nil {
			return err
		}
		if before.OwnerPrincipal != p.ID {
			// Someone else's deleted view does not exist for this caller.
			if before.DeletedAt != nil || !before.Shared {
				return errNotFound
			}
			forbidden = true
			return nil
		}
		result, err = change(r.Context(), tx, before)
		if err != nil {
			return err
		}
		return m.eventSink.Append(r.Context(), tx, p.ID, eventType, before, result)
	})
	switch {
	case errors.Is(err, errNotDeleted):
		httpapi.WriteError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeDBError(w, err)
	case forbidden:
		httpapi.WriteError(w, http.StatusForbidden, "only the owner can change this view")
	case status == http.StatusNoContent:
		w.WriteHeader(http.StatusNoContent)
	default:
		httpapi.WriteJSON(w, status, result)
	}
}

type rowScanner interface {
	Scan(...any) error
}

func scanView(row rowScanner) (savedView, error) {
	var v savedView
	var filters []byte
	err := row.Scan(&v.ID, &v.OwnerPrincipal, &v.ProjectID, &v.Name, &filters, &v.Sort.Field,
		&v.Sort.Direction, &v.SortKeys, &v.GroupBy, &v.Mode, &v.Columns, &v.Shared, &v.CreatedAt, &v.UpdatedAt, &v.DeletedAt)
	if err != nil {
		return savedView{}, err
	}
	v.Filters = json.RawMessage(filters)
	if v.SortKeys == nil {
		v.SortKeys = []string{}
	}
	if v.Columns == nil {
		v.Columns = []string{}
	}
	return v, nil
}

const viewColumns = `id::text, owner_principal_id::text, project_id::text, name, filters, sort_field, sort_direction,
                     sort_keys, group_by, mode, columns, shared, created_at, updated_at, deleted_at`

func selectView(ctx context.Context, tx pgx.Tx, where string, args ...any) (savedView, error) {
	v, err := scanView(tx.QueryRow(ctx, `SELECT `+viewColumns+` FROM saved_views WHERE `+where, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return savedView{}, errNotFound
	}
	return v, err
}

func isProject(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		               WHERE n.id = $1::uuid AND n.deleted_at IS NULL AND k.slug = 'project')`, id).Scan(&ok)
	return ok, err
}

func insertView(ctx context.Context, tx pgx.Tx, tenantID, ownerID string, in viewWrite) (savedView, error) {
	return scanView(tx.QueryRow(ctx, `
		INSERT INTO saved_views (tenant_id, owner_principal_id, project_id, name, filters, sort_field, sort_direction,
		                         sort_keys, group_by, mode, columns, shared)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+viewColumns,
		tenantID, ownerID, in.ProjectID, in.Name, []byte(in.Filters), in.Sort.Field, in.Sort.Direction,
		in.SortKeys, in.GroupBy, in.Mode, in.Columns, in.Shared))
}

func updateView(ctx context.Context, tx pgx.Tx, before savedView, in viewPatch) (savedView, error) {
	after := before
	if in.Name != nil {
		after.Name = *in.Name
	}
	if in.Filters != nil {
		after.Filters = *in.Filters
	}
	if in.Sort != nil {
		after.Sort = *in.Sort
	}
	if in.SortKeys != nil {
		after.SortKeys = *in.SortKeys
	}
	if in.GroupBy != nil {
		after.GroupBy = *in.GroupBy
	}
	if in.Mode != nil {
		after.Mode = *in.Mode
	}
	if in.Columns != nil {
		after.Columns = *in.Columns
	}
	if in.Shared != nil {
		after.Shared = *in.Shared
	}
	return writeView(ctx, tx, after)
}

// writeView stores every editable property of v (also used by undo).
func writeView(ctx context.Context, tx pgx.Tx, v savedView) (savedView, error) {
	return scanView(tx.QueryRow(ctx, `
		UPDATE saved_views SET name = $2, filters = $3::jsonb, sort_field = $4, sort_direction = $5,
		       sort_keys = $6, group_by = $7, columns = $8, shared = $9, mode = $10,
		       updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE id = $1::uuid
		RETURNING `+viewColumns,
		v.ID, v.Name, []byte(v.Filters), v.Sort.Field, v.Sort.Direction, v.SortKeys, v.GroupBy, v.Columns, v.Shared, v.Mode))
}

func setDeleted(ctx context.Context, tx pgx.Tx, id string, deleted bool) (savedView, error) {
	return scanView(tx.QueryRow(ctx, `
		UPDATE saved_views SET deleted_at = CASE WHEN $2::bool THEN clock_timestamp() END,
		       updated_at = greatest(clock_timestamp(), updated_at + interval '1 microsecond')
		WHERE id = $1::uuid
		RETURNING `+viewColumns, id, deleted))
}

func normaliseWrite(in *viewWrite) error {
	in.Name = strings.TrimSpace(in.Name)
	if err := validateName(in.Name); err != nil {
		return err
	}
	if in.ProjectID != nil {
		if !uuidPattern.MatchString(*in.ProjectID) {
			return errors.New("project_id is invalid")
		}
		lower := strings.ToLower(*in.ProjectID)
		in.ProjectID = &lower
	}
	if err := validateFilters(in.Filters); err != nil {
		return err
	}
	if in.Sort == nil {
		in.Sort = &viewSort{Field: "position", Direction: "asc"}
	}
	if err := validateSort(*in.Sort); err != nil {
		return err
	}
	if in.SortKeys == nil {
		in.SortKeys = []string{}
	}
	if err := validateSortKeys(in.SortKeys); err != nil {
		return err
	}
	if in.GroupBy == "" {
		in.GroupBy = "none"
	}
	if !wordPattern.MatchString(in.GroupBy) {
		return errors.New("group_by is invalid")
	}
	if in.Mode == "" {
		in.Mode = "list"
	}
	if err := validateMode(in.Mode); err != nil {
		return err
	}
	if in.Columns == nil {
		return errors.New("columns is required")
	}
	return validateColumns(in.Columns)
}

func normalisePatch(in *viewPatch) error {
	if in.Name == nil && in.Filters == nil && in.Sort == nil && in.SortKeys == nil && in.GroupBy == nil && in.Columns == nil && in.Mode == nil && in.Shared == nil {
		return errors.New("at least one property is required")
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name == "" {
			return errors.New("name must not be empty")
		}
		if err := validateName(name); err != nil {
			return err
		}
		in.Name = &name
	}
	if in.Filters != nil {
		if err := validateFilters(*in.Filters); err != nil {
			return err
		}
	}
	if in.Sort != nil {
		if err := validateSort(*in.Sort); err != nil {
			return err
		}
	}
	if in.SortKeys != nil {
		if *in.SortKeys == nil {
			*in.SortKeys = []string{}
		}
		if err := validateSortKeys(*in.SortKeys); err != nil {
			return err
		}
	}
	if in.GroupBy != nil && !wordPattern.MatchString(*in.GroupBy) {
		return errors.New("group_by is invalid")
	}
	if in.Mode != nil {
		if err := validateMode(*in.Mode); err != nil {
			return err
		}
	}
	if in.Columns != nil {
		if *in.Columns == nil {
			*in.Columns = []string{}
		}
		if err := validateColumns(*in.Columns); err != nil {
			return err
		}
	}
	return nil
}

func validateName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if utf8.RuneCountInString(name) > maxNameRunes {
		return errors.New("name is too long")
	}
	return nil
}

func validateFilters(filters json.RawMessage) error {
	var value map[string]json.RawMessage
	if len(filters) == 0 || json.Unmarshal(filters, &value) != nil || value == nil {
		return errors.New("filters must be an object")
	}
	if len(filters) > maxFilterBytes {
		return errors.New("filters are too large")
	}
	return nil
}

func validateSort(s viewSort) error {
	switch s.Field {
	case "position", "updated_at", "created_at", "key", "title":
	default:
		return errors.New("sort.field is invalid")
	}
	if s.Direction != "asc" && s.Direction != "desc" {
		return errors.New("sort.direction is invalid")
	}
	return nil
}

func validateSortKeys(keys []string) error {
	if len(keys) > maxSortKeys {
		return errors.New("sort_keys has too many keys")
	}
	seen := map[string]bool{}
	for _, key := range keys {
		field := strings.TrimPrefix(key, "-")
		if !sortKeyPattern.MatchString(key) || seen[field] {
			return errors.New("sort_keys is invalid")
		}
		seen[field] = true
	}
	return nil
}

func validateMode(mode string) error {
	if mode != "list" && mode != "outline" && mode != "graph" {
		return errors.New("mode is invalid")
	}
	return nil
}

func validateColumns(columns []string) error {
	if len(columns) > maxColumns {
		return errors.New("columns has too many entries")
	}
	for _, column := range columns {
		if !wordPattern.MatchString(column) {
			return errors.New("columns is invalid")
		}
	}
	return nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("invalid JSON request body")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeDBError(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, http.StatusNotFound, "view not found")
		return
	}
	httpapi.WriteError(w, http.StatusInternalServerError, "database operation failed")
}
