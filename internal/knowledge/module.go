// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	evCreated           = "knowledge.created"
	evUpdated           = "knowledge.updated"
	evDeleted           = "knowledge.deleted"
	evLearningAccepted  = "knowledge.learning_accepted"
	evLearningDismissed = "knowledge.learning_dismissed"
	evLearningDrafted   = "knowledge.learning_drafted"
)

type module struct{ pool *pgxpool.Pool }

// New returns the /api/knowledge module. See the package doc for wiring.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool: pool} }

func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/knowledge", m.handleList)
	mux.HandleFunc("POST /api/knowledge", m.handleCreate)
	mux.HandleFunc("GET /api/knowledge/resolve", m.handleResolve)
	mux.HandleFunc("GET /api/knowledge/graph", m.handleGraph)
	mux.HandleFunc("GET /api/knowledge/learnings", m.handleListLearnings)
	mux.HandleFunc("POST /api/knowledge/learnings/{learningId}/accept", m.handleAcceptLearning)
	mux.HandleFunc("POST /api/knowledge/learnings/{learningId}/dismiss", m.handleDismissLearning)
	mux.HandleFunc("POST /api/knowledge/learnings/{learningId}/draft", m.handleDraftLearning)
	mux.HandleFunc("GET /api/knowledge/{id}", m.handleGet)
	mux.HandleFunc("PATCH /api/knowledge/{id}", m.handleUpdate)
	mux.HandleFunc("DELETE /api/knowledge/{id}", m.handleDelete)
}

// ---------- Errors ----------

type apiError struct {
	status   int
	code     string
	msg      string
	entry    *Entry
	conflict *Item
	ranges   []SensitiveRange
}

func (e *apiError) Error() string { return e.msg }

func fail(status int, code, msg string) *apiError {
	return &apiError{status: status, code: code, msg: msg}
}

func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	var pe *pgconn.PgError
	switch {
	case errors.As(err, &ae):
		body := map[string]any{"error": ae.msg, "code": ae.code}
		if ae.entry != nil {
			body["entry"] = ae.entry
		}
		if ae.conflict != nil {
			body["conflict"] = ae.conflict
		}
		if ae.ranges != nil {
			body["ranges"] = ae.ranges
		}
		writeJSON(w, ae.status, body)
	case errors.Is(err, authz.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "permission denied", "code": "forbidden"})
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || db.IsStatementTimeout(err):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "try again", "code": "read_timeout"})
	case errors.Is(err, errNotFound), errors.Is(err, pgx.ErrNoRows):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "knowledge entry not found", "code": "not_found"})
	case errors.As(err, &pe) && pe.Code == "P0001" && strings.Contains(pe.Message, "child kind is not allowed"):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this project does not allow this kind of entry", "code": "kind_not_allowed"})
	case errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01" || strings.HasPrefix(pe.Code, "23")):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "the entry changed at the same time; try again", "code": "conflict"})
	default:
		slog.Error("knowledge", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal", "code": "internal"})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, status, v)
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "code": "unauthorized"})
		return p, false
	}
	return p, true
}

func canWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string) bool {
	return authz.RequireTx(ctx, tx, p, permission, authz.RouteScope(ctx)) == nil
}

func readObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	b, err := io.ReadAll(r.Body)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return nil, fail(http.StatusBadRequest, "invalid_request", "a JSON object is required")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil || raw == nil {
		return nil, fail(http.StatusBadRequest, "invalid_request", "a JSON object is required")
	}
	return raw, nil
}

func stringField(raw map[string]json.RawMessage, key string) (string, bool, error) {
	v, ok := raw[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", true, fail(http.StatusBadRequest, "invalid_request", key+" must be text")
	}
	return s, true, nil
}

// confirmField reads confirm_not_sensitive; absent is false.
func confirmField(raw map[string]json.RawMessage) (bool, error) {
	v, ok := raw["confirm_not_sensitive"]
	if !ok {
		return false, nil
	}
	var b bool
	if err := json.Unmarshal(v, &b); err != nil {
		return false, fail(http.StatusBadRequest, "invalid_request", "confirm_not_sensitive must be true or false")
	}
	return b, nil
}

func metadataField(raw map[string]json.RawMessage) (map[string]any, bool, error) {
	v, ok := raw["metadata"]
	if !ok {
		return nil, false, nil
	}
	var meta map[string]any
	if err := json.Unmarshal(v, &meta); err != nil || meta == nil {
		return nil, true, fail(http.StatusBadRequest, "invalid_request", "metadata must be a JSON object")
	}
	if problem := metadataProblem(meta); problem != "" {
		return nil, true, fail(http.StatusUnprocessableEntity, "invalid_metadata", problem)
	}
	return meta, true, nil
}

func unmodifiedSince(r *http.Request) (*time.Time, error) {
	values, present := r.Header["If-Unmodified-Since"]
	if !present {
		return nil, nil
	}
	if len(values) != 1 {
		return nil, fail(http.StatusBadRequest, "invalid_request", "invalid If-Unmodified-Since")
	}
	at, err := time.Parse(time.RFC3339Nano, values[0])
	if err != nil {
		at, err = http.ParseTime(values[0])
	}
	if err != nil {
		return nil, fail(http.StatusBadRequest, "invalid_request", "invalid If-Unmodified-Since")
	}
	return &at, nil
}

// ---------- Reads ----------

func (m *module) handleList(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	for _, key := range []string{"type", "status"} {
		size, count := 0, 0
		for _, raw := range v[key] {
			size += len(raw)
			count += strings.Count(raw, ",") + 1
			if size > 6400 || count > 100 {
				writeErr(w, fail(400, "invalid_request", "filter exceeds bounds"))
				return
			}
		}
	}
	q := listQuery{Q: strings.TrimSpace(v.Get("q")), Sort: v.Get("sort"), Limit: 500}
	if len(v.Get("q")) > 200 {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "q exceeds 200 UTF-8 bytes"))
		return
	}
	if id := v.Get("project_id"); id != "" {
		if !validUUID(id) {
			writeErr(w, fail(http.StatusBadRequest, "invalid_request", "invalid project_id"))
			return
		}
		q.ProjectID = id
	}
	for _, t := range commaValues(v["type"]) {
		s, ok := specFor(t)
		if !ok {
			writeErr(w, fail(http.StatusBadRequest, "invalid_type", "unknown knowledge type "+t))
			return
		}
		q.Types = append(q.Types, s.Kind)
	}
	for _, s := range commaValues(v["status"]) {
		if _, ok := stateFor(s); !ok {
			writeErr(w, fail(http.StatusBadRequest, "invalid_status", "status is active, proposed or archived"))
			return
		}
		q.Statuses = append(q.Statuses, s)
	}
	switch q.Sort {
	case "", "relevance", "updated", "created", "title", "slug", "type":
	default:
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "sort is relevance, updated, created, title, slug or type"))
		return
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 1000 {
			writeErr(w, fail(http.StatusBadRequest, "invalid_request", "limit is 1 to 1000"))
			return
		}
		q.Limit = n
	}
	if v.Has("ships_in") {
		for _, key := range []string{"project_id", "cursor", "sort", "q", "limit"} {
			if len(v[key]) > 1 {
				writeErr(w, fail(400, "invalid_request", "repeated "+key))
				return
			}
		}
		values := v["ships_in"]
		if len(values) != 1 || (values[0] != "none" && !validUUID(values[0])) || q.ProjectID == "" {
			writeErr(w, fail(400, "invalid_request", "ships_in requires project_id and one UUID or none"))
			return
		}
		q.ShipsIn = values[0]
		q.ProjectID = strings.ToLower(q.ProjectID)
		q.ShipsIn = strings.ToLower(q.ShipsIn)
		slices.Sort(q.Types)
		q.Types = slices.Compact(q.Types)
		slices.Sort(q.Statuses)
		q.Statuses = slices.Compact(q.Statuses)
		q.Cursor = v.Get("cursor")
		if len(q.Cursor) > 2048 {
			writeErr(w, fail(400, "invalid_request", "invalid cursor"))
			return
		}
		if !v.Has("limit") {
			q.Limit = 200
		}
		if q.Limit > 200 {
			writeErr(w, fail(400, "invalid_request", "scoped limit is 1 to 200"))
			return
		}
		page, err := m.readScopedKnowledge(r.Context(), p, q)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, page)
		return
	}
	if v.Has("cursor") {
		writeErr(w, fail(400, "invalid_request", "cursor requires a release scope"))
		return
	}
	var page ListPage
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		page, err = list(r.Context(), tx, p.TenantID, q)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func commaValues(values []string) []string {
	var out []string
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func (m *module) handleGet(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "invalid id"))
		return
	}
	var entry Entry
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		entry, err = loadEntry(r.Context(), tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (m *module) handleResolve(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	project, slug := v.Get("project_id"), strings.TrimSpace(v.Get("slug"))
	s, known := specFor(v.Get("type"))
	if !validUUID(project) || !known || slug == "" {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "project_id, type and slug are required"))
		return
	}
	var entry Entry
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		entry, err = resolve(r.Context(), tx, p.TenantID, project, s, slug)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// ---------- Create ----------

type createInput struct {
	ProjectID string
	Spec      spec
	Slug      string
	Title     string
	Body      string
	Status    string
	Metadata  map[string]any
	KeyPrefix string
}

func (m *module) handleCreate(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	raw, err := readObject(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in, err := parseCreate(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	var entry Entry
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		entry, err = create(r.Context(), tx, p, in)
		return err
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, entry)
}

func parseCreate(raw map[string]json.RawMessage) (createInput, error) {
	for key := range raw {
		switch key {
		case "project_id", "type", "slug", "title", "body", "status", "metadata", "key_prefix":
		default:
			return createInput{}, fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	var in createInput
	var err error
	if in.ProjectID, _, err = stringField(raw, "project_id"); err != nil {
		return in, err
	}
	if !validUUID(in.ProjectID) {
		return in, fail(http.StatusBadRequest, "invalid_request", "project_id is required")
	}
	typ, _, err := stringField(raw, "type")
	if err != nil {
		return in, err
	}
	s, ok := specFor(typ)
	if !ok {
		return in, fail(http.StatusBadRequest, "invalid_type", "type is runbook, guideline, memory, external-system or related-project")
	}
	in.Spec = s
	if in.Slug, _, err = stringField(raw, "slug"); err != nil {
		return in, err
	}
	in.Slug = strings.TrimSpace(in.Slug)
	if problem := slugProblem(s, in.Slug); problem != "" {
		return in, fail(http.StatusUnprocessableEntity, "invalid_slug", problem)
	}
	if in.Title, _, err = stringField(raw, "title"); err != nil {
		return in, err
	}
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return in, fail(http.StatusUnprocessableEntity, "invalid_title", "title is required")
	}
	if len(in.Title) > 500 {
		return in, fail(http.StatusUnprocessableEntity, "invalid_title", "title is longer than 500 characters")
	}
	if in.Body, _, err = stringField(raw, "body"); err != nil {
		return in, err
	}
	status, has, err := stringField(raw, "status")
	if err != nil {
		return in, err
	}
	if !has || status == "" {
		status = statusActive
	}
	if _, ok := stateFor(status); !ok {
		return in, fail(http.StatusBadRequest, "invalid_status", "status is active, proposed or archived")
	}
	in.Status = status
	if in.Metadata, _, err = metadataField(raw); err != nil {
		return in, err
	}
	if in.KeyPrefix, _, err = stringField(raw, "key_prefix"); err != nil {
		return in, err
	}
	if in.KeyPrefix != "" && !keyPrefix.MatchString(in.KeyPrefix) {
		return in, fail(http.StatusBadRequest, "invalid_request", "invalid key_prefix")
	}
	return in, nil
}

func create(ctx context.Context, tx pgx.Tx, p tenant.Principal, in createInput) (Entry, error) {
	if err := lockKnowledgeTree(ctx, tx); err != nil {
		return Entry{}, err
	}
	// A new entry is decided in the project it joins (ADR-003 P2).
	if !validUUID(in.ProjectID) || authz.RequireTx(ctx, tx, p, "knowledge.write", authz.Scope{ProjectID: in.ProjectID}) != nil {
		return Entry{}, fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
	}
	var projectOK bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
	  WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL AND k.slug='project')`, p.TenantID, in.ProjectID).Scan(&projectOK); err != nil {
		return Entry{}, err
	}
	if !projectOK {
		return Entry{}, fail(http.StatusNotFound, "project_not_found", "project not found")
	}
	kindID, prefix, kindEvent, err := ensureKind(ctx, tx, p, in.Spec)
	if err != nil {
		return Entry{}, err
	}
	if err := lockSlugs(ctx, tx, p.TenantID, in.ProjectID, kindID); err != nil {
		return Entry{}, err
	}
	taken, err := slugTaken(ctx, tx, p.TenantID, in.ProjectID, kindID, in.Slug, "")
	if err != nil {
		return Entry{}, err
	}
	if taken != nil {
		return Entry{}, &apiError{status: http.StatusConflict, code: "slug_taken", msg: "another " + strings.ToLower(in.Spec.Label) + " in this project already uses the slug " + in.Slug, conflict: taken}
	}
	if in.KeyPrefix != "" {
		prefix = in.KeyPrefix
	}
	var key string
	if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key($1::uuid, $2)`, p.TenantID, prefix).Scan(&key); err != nil {
		return Entry{}, err
	}
	fields := map[string]any{"slug": in.Slug}
	if len(in.Metadata) > 0 {
		fields["metadata"] = in.Metadata
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return Entry{}, err
	}
	state, _ := stateFor(in.Status)
	node, err := scanSnap(tx.QueryRow(ctx, `
		INSERT INTO nodes (tenant_id, key, kind_id, title, body, fields, state, parent_id, position)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5, $6::jsonb, $7, $8::uuid,
		  (SELECT coalesce(max(position), 0) + 1 FROM nodes WHERE tenant_id=$1::uuid AND parent_id=$8::uuid AND deleted_at IS NULL))
		RETURNING `+nodeReturning, p.TenantID, key, kindID, in.Title, in.Body, string(encoded), state, in.ProjectID))
	if err != nil {
		return Entry{}, err
	}
	if kindEvent != nil {
		if _, err := events.Append(ctx, tx, p, *kindEvent); err != nil {
			return Entry{}, err
		}
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &node.ID, Type: evCreated, After: node})
	if err != nil {
		return Entry{}, err
	}
	entry, err := loadEntry(ctx, tx, p.TenantID, node.ID, false)
	if err != nil {
		return Entry{}, err
	}
	entry.EventID = &ev.ID
	return entry, nil
}

// ensureKind returns the tenant's kind for s and its pending creation event.
// The caller appends after taking the slug and node-key locks, counter last.
func ensureKind(ctx context.Context, tx pgx.Tx, p tenant.Principal, s spec) (string, string, *events.Change, error) {
	var id, prefix string
	err := tx.QueryRow(ctx, `SELECT id::text, short_prefix FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, p.TenantID, s.Kind).Scan(&id, &prefix)
	if err == nil {
		return id, prefix, nil, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil, err
	}
	type kindSnap struct {
		ID                string          `json:"id"`
		Slug              string          `json:"slug"`
		Label             string          `json:"label"`
		ShortPrefix       string          `json:"short_prefix"`
		Icon              string          `json:"icon"`
		AllowedChildKinds []string        `json:"allowed_child_kinds"`
		FieldSchema       json.RawMessage `json:"field_schema"`
	}
	k := kindSnap{Slug: s.Kind, Label: s.Label, ShortPrefix: s.Prefix, Icon: s.Kind, FieldSchema: json.RawMessage(`{}`)}
	err = tx.QueryRow(ctx, `INSERT INTO node_kinds (tenant_id, slug, label, short_prefix, icon, field_schema)
	  VALUES ($1::uuid, $2, $3, $4, $5, '{}'::jsonb)
	  ON CONFLICT (tenant_id, slug) DO NOTHING RETURNING id::text`, p.TenantID, k.Slug, k.Label, k.ShortPrefix, k.Icon).Scan(&k.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Created concurrently: use theirs.
		err = tx.QueryRow(ctx, `SELECT id::text, short_prefix FROM node_kinds WHERE tenant_id=$1 AND slug=$2`, p.TenantID, s.Kind).Scan(&id, &prefix)
		return id, prefix, nil, err
	}
	if err != nil {
		return "", "", nil, err
	}
	return k.ID, k.ShortPrefix, &events.Change{Type: "kind.created", After: k}, nil
}

// ---------- Update ----------

type patchInput struct {
	Title    *string
	Body     *string
	Status   *string
	Slug     *string
	Metadata map[string]any
	HasMeta  bool
}

func parsePatch(raw map[string]json.RawMessage) (patchInput, error) {
	var in patchInput
	if len(raw) == 0 {
		return in, fail(http.StatusBadRequest, "invalid_request", "patch is empty")
	}
	for key := range raw {
		switch key {
		case "title", "body", "status", "slug", "metadata":
		case "type", "kind", "project_id", "key":
			return in, fail(http.StatusBadRequest, "invalid_request", key+" cannot change")
		default:
			return in, fail(http.StatusBadRequest, "invalid_request", "unknown field "+key)
		}
	}
	for _, f := range []struct {
		key string
		dst **string
	}{{"title", &in.Title}, {"body", &in.Body}, {"status", &in.Status}, {"slug", &in.Slug}} {
		s, has, err := stringField(raw, f.key)
		if err != nil {
			return in, err
		}
		if has {
			value := s
			*f.dst = &value
		}
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return in, fail(http.StatusUnprocessableEntity, "invalid_title", "title is required")
		}
		if len(t) > 500 {
			return in, fail(http.StatusUnprocessableEntity, "invalid_title", "title is longer than 500 characters")
		}
		in.Title = &t
	}
	if in.Status != nil {
		if _, ok := stateFor(*in.Status); !ok {
			return in, fail(http.StatusBadRequest, "invalid_status", "status is active, proposed or archived")
		}
	}
	if in.Slug != nil {
		s := strings.TrimSpace(*in.Slug)
		in.Slug = &s
	}
	var err error
	in.Metadata, in.HasMeta, err = metadataField(raw)
	return in, err
}

func (m *module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "invalid id"))
		return
	}
	raw, err := readObject(w, r)
	if err != nil {
		writeErr(w, err)
		return
	}
	in, err := parsePatch(raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	expected, err := unmodifiedSince(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	entry, err := m.update(r.Context(), p, id, in, expected)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// stale reads the current entry for a 412 in a fresh transaction, after the
// write transaction rolled back.
func (m *module) stale(ctx context.Context, p tenant.Principal, id string) error {
	var current Entry
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		current, err = loadEntry(ctx, tx, p.TenantID, id, false)
		return err
	})
	if err != nil {
		if errors.Is(err, errNotFound) {
			return fail(http.StatusNotFound, "not_found", "knowledge entry not found")
		}
		return err
	}
	return &apiError{status: http.StatusPreconditionFailed, code: "stale", msg: "the entry changed since you opened it", entry: &current}
}

var errStale = errors.New("stale")

func (m *module) update(ctx context.Context, p tenant.Principal, id string, in patchInput, expected *time.Time) (Entry, error) {
	var entry Entry
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockKnowledgeTree(ctx, tx); err != nil {
			return err
		}
		if !canWrite(ctx, tx, p, "knowledge.write") {
			return fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
		}
		current, kind, err := lockNode(ctx, tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if expected != nil && !current.UpdatedAt.Equal(*expected) {
			return errStale
		}
		s, _ := specFor(kind)
		fields := current.fieldMap()
		title, body, state := current.Title, current.Body, current.State
		if in.Title != nil {
			title = *in.Title
		}
		if in.Body != nil {
			body = *in.Body
		}
		if in.Status != nil && statusOf(state) != *in.Status {
			state, _ = stateFor(*in.Status)
		}
		oldSlug, _ := fields["slug"].(string)
		if in.Slug != nil && *in.Slug != oldSlug {
			if problem := slugProblem(s, *in.Slug); problem != "" {
				return fail(http.StatusUnprocessableEntity, "invalid_slug", problem)
			}
			project, err := projectOf(ctx, tx, p.TenantID, id)
			if err != nil {
				return err
			}
			if err := lockSlugs(ctx, tx, p.TenantID, project, current.KindID); err != nil {
				return err
			}
			taken, err := slugTaken(ctx, tx, p.TenantID, project, current.KindID, *in.Slug, id)
			if err != nil {
				return err
			}
			if taken != nil {
				return &apiError{status: http.StatusConflict, code: "slug_taken", msg: "another " + strings.ToLower(s.Label) + " in this project already uses the slug " + *in.Slug, conflict: taken}
			}
			fields["slug"] = *in.Slug
		}
		if in.HasMeta {
			if len(in.Metadata) == 0 {
				delete(fields, "metadata")
			} else {
				fields["metadata"] = in.Metadata
			}
		}
		encoded, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if title == current.Title && body == current.Body && state == current.State && jsonEqual(encoded, current.Fields) {
			entry, err = loadEntry(ctx, tx, p.TenantID, id, false)
			return err
		}
		updated, err := scanSnap(tx.QueryRow(ctx, `UPDATE nodes SET title=$3, body=$4, state=$5, fields=$6::jsonb, updated_at=`+bumpUpdated+`
		  WHERE tenant_id=$1 AND id=$2::uuid RETURNING `+nodeReturning, p.TenantID, id, title, body, state, string(encoded)))
		if err != nil {
			return err
		}
		ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &updated.ID, Type: evUpdated, Before: current, After: updated})
		if err != nil {
			return err
		}
		if entry, err = loadEntry(ctx, tx, p.TenantID, id, false); err != nil {
			return err
		}
		entry.EventID = &ev.ID
		return nil
	})
	if errors.Is(err, errStale) {
		return Entry{}, m.stale(ctx, p, id)
	}
	return entry, err
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ax, _ := json.Marshal(x)
	by, _ := json.Marshal(y)
	return bytes.Equal(ax, by)
}

// ---------- Delete ----------

func (m *module) handleDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		writeErr(w, fail(http.StatusBadRequest, "invalid_request", "invalid id"))
		return
	}
	expected, err := unmodifiedSince(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	var eventID int64
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockKnowledgeTree(r.Context(), tx); err != nil {
			return err
		}
		if !canWrite(r.Context(), tx, p, "knowledge.delete") {
			return fail(http.StatusForbidden, "forbidden", "you can read knowledge but not change it")
		}
		current, _, err := lockNode(r.Context(), tx, p.TenantID, id, false)
		if err != nil {
			return err
		}
		if expected != nil && !current.UpdatedAt.Equal(*expected) {
			return errStale
		}
		var children bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND parent_id=$2::uuid AND deleted_at IS NULL)`, p.TenantID, id).Scan(&children); err != nil {
			return err
		}
		if children {
			return fail(http.StatusConflict, "has_children", "move or delete what sits under this entry first")
		}
		deleted, err := scanSnap(tx.QueryRow(r.Context(), `UPDATE nodes SET deleted_at=clock_timestamp(), updated_at=`+bumpUpdated+`
		  WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL RETURNING `+nodeReturning, p.TenantID, id))
		if err != nil {
			return err
		}
		ev, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &deleted.ID, Type: evDeleted, Before: current, After: deleted})
		eventID = ev.ID
		return err
	})
	if errors.Is(err, errStale) {
		err = m.stale(r.Context(), p, id)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "event_id": eventID})
}
