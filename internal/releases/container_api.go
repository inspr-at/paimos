// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5/pgconn"
)

type Option func(*module)

func WithAdoptionReporting(reporter delivery.AdoptionReporting) Option {
	return func(m *module) { m.adoption = reporter }
}
func WithReleaseHistory(load func() (releasehistory.History, error)) Option {
	return func(m *module) { m.history = load }
}

func containerResponse(w http.ResponseWriter, out any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err == nil {
		httpapi.WriteJSON(w, 200, out)
		return
	}
	var conflict *delivery.Conflict
	var sqlErr *pgconn.PgError
	switch {
	case errors.As(err, &conflict):
		httpapi.WriteJSON(w, 409, map[string]any{"error": conflict.Message, "code": conflict.Code})
	case errors.Is(err, delivery.ErrInvalidInput):
		httpapi.WriteError(w, 400, err.Error())
	case errors.Is(err, delivery.ErrNotFound):
		httpapi.WriteError(w, 404, "project or release not found")
	case errors.Is(err, authz.ErrForbidden):
		httpapi.WriteError(w, 403, "permission denied")
	case errors.Is(err, delivery.ErrAdoptionUnavailable):
		httpapi.WriteError(w, 503, err.Error())
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || db.IsStatementTimeout(err):
		httpapi.WriteError(w, 503, "try again")
	case errors.As(err, &sqlErr):
		if sqlErr.Code == "55P03" || sqlErr.Code == "25P03" || strings.HasPrefix(sqlErr.Code, "08") {
			httpapi.WriteError(w, 503, "try again")
		} else {
			respond(w, nil, err)
		}
	default:
		respond(w, nil, err)
	}
}
func decodeContainer(w http.ResponseWriter, r *http.Request, out any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		httpapi.WriteError(w, 400, "invalid request body")
		return false
	}
	return true
}
func containerOptions(w http.ResponseWriter, r *http.Request) (delivery.ReadOptions, bool) {
	q := r.URL.Query()
	out := delivery.ReadOptions{Cursor: q.Get("cursor"), State: q.Get("state"), Part: q.Get("part"), Through: q.Get("through"), CompletedLater: q.Get("completed_later") == "1" || q.Get("completed_later") == "true", CompletedUnplaced: q.Get("completed_unplaced") == "1" || q.Get("completed_unplaced") == "true"}
	if len(out.Cursor) > 2048 || len(out.State) > 32 || len(q.Get("released_cursor")) > 2048 {
		httpapi.WriteError(w, 400, "invalid page")
		return out, false
	}
	for _, key := range []string{"completed_later", "completed_unplaced"} {
		v := q.Get(key)
		if v != "" && v != "0" && v != "1" && v != "false" && v != "true" {
			httpapi.WriteError(w, 400, "invalid recovery filter")
			return out, false
		}
	}
	if v := q.Get("limit"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 || n > 200 {
			httpapi.WriteError(w, 400, "invalid limit")
			return out, false
		}
		out.Limit = n
	}
	return out, true
}
func (m *module) containerMount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/delivery", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/delivery/overview", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/delivery/adoption-report", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/delivery/verify", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/releases", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}/items", m.containerRead)
	mux.HandleFunc("GET /api/projects/{projectId}/backlog", m.containerRead)
	mux.HandleFunc("GET /api/delivery/adoptions", m.containerRead)

	mux.HandleFunc("PATCH /api/projects/{projectId}/delivery", m.containerDefaults)
	mux.HandleFunc("POST /api/projects/{projectId}/delivery/adopt", m.containerAdopt)
	mux.HandleFunc("POST /api/projects/{projectId}/releases", m.containerPlan)
	mux.HandleFunc("PATCH /api/projects/{projectId}/releases/{releaseId}", m.containerEdit)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/state", m.containerAction)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/cut", m.containerAction)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/publish", m.containerAction)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/close", m.containerAction)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/rank", m.containerAction)

	mux.HandleFunc("PUT /api/nodes/{nodeId}/ships-in", m.containerPlace)
	mux.HandleFunc("POST /api/projects/{projectId}/ships-in/batch", m.containerBatch)
}
func (m *module) containerRead(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
		return
	}
	if r.PathValue("projectId") != "" && !uuid.MatchString(r.PathValue("projectId")) {
		httpapi.WriteError(w, 400, "invalid project id")
		return
	}
	opt, ok := containerOptions(w, r)
	if !ok {
		return
	}
	var out any
	var err error
	project, release := r.PathValue("projectId"), r.PathValue("releaseId")
	switch {
	case r.URL.Path == "/api/delivery/adoptions":
		if opt.Limit > 50 {
			httpapi.WriteError(w, 400, "limit must be at most 50")
			return
		}
		out, err = m.store.Adoptions(r.Context(), p, opt)
	case strings.HasSuffix(r.URL.Path, "/delivery/overview"):
		out, err = m.store.Overview(r.Context(), p, project, r.URL.Query().Get("released_cursor"))
	case strings.HasSuffix(r.URL.Path, "/delivery/verify"):
		out, err = m.store.VerifyAdoption(r.Context(), p, project, m.adoption)
	case strings.HasSuffix(r.URL.Path, "/adoption-report"):
		out, err = m.store.AdoptionReport(r.Context(), p, project, opt.Cursor, opt.Limit, m.adoption)
	case strings.HasSuffix(r.URL.Path, "/delivery"):
		out, err = m.store.Status(r.Context(), p, project)
	case strings.HasSuffix(r.URL.Path, "/backlog"), strings.HasSuffix(r.URL.Path, "/items"):
		out, err = m.store.Items(r.Context(), p, project, release, opt)
	case release != "":
		if !uuid.MatchString(release) {
			httpapi.WriteError(w, 400, "invalid release id")
			return
		}
		out, err = m.store.GetRelease(r.Context(), p, project, release)
	default:
		if opt.Limit > 50 {
			httpapi.WriteError(w, 400, "limit must be at most 50")
			return
		}
		out, err = m.store.ListReleases(r.Context(), p, project, opt)
	}
	containerResponse(w, out, err)
}
func parseDeadline(raw json.RawMessage) (*time.Time, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil, errors.New("entry deadline must be RFC3339 or null")
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil, err
	}
	return &at, nil
}
func (m *module) containerPlan(w http.ResponseWriter, r *http.Request) {
	p, ok := projectPrincipal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Title       string          `json:"title"`
		Visibility  string          `json:"visibility"`
		CreationKey string          `json:"creation_key"`
		After       string          `json:"after_release_id"`
		Deadline    json.RawMessage `json:"entry_closes_at"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	var at *time.Time
	var err error
	if len(in.Deadline) > 0 {
		at, err = parseDeadline(in.Deadline)
	}
	if err != nil || len(in.Title) > 512 || in.Title != "" && strings.TrimSpace(in.Title) == "" || len(in.CreationKey) > 128 || in.Visibility != "internal" && in.Visibility != "published" || in.After != "" && !uuid.MatchString(in.After) {
		httpapi.WriteError(w, 400, "invalid release plan")
		return
	}
	out, err := m.store.Plan(r.Context(), p, delivery.PlanRequest{ProjectID: r.PathValue("projectId"), Title: in.Title, Visibility: in.Visibility, CreationKey: in.CreationKey, AfterReleaseID: in.After, EntryClosesAt: at})
	containerResponse(w, out, err)
}
func (m *module) containerEdit(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Title      *string         `json:"title"`
		Body       *string         `json:"body"`
		Visibility *string         `json:"visibility"`
		Revision   int64           `json:"expected_revision"`
		Deadline   json.RawMessage `json:"entry_closes_at"`
		Settings   json.RawMessage `json:"build_settings"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	if in.Revision < 1 || in.Title != nil && (strings.TrimSpace(*in.Title) == "" || len(*in.Title) > 512) || in.Body != nil && len(*in.Body) > 65536 || in.Visibility != nil && *in.Visibility != "internal" && *in.Visibility != "published" {
		httpapi.WriteError(w, 400, "invalid release edit")
		return
	}
	var at *time.Time
	var err error
	if len(in.Deadline) > 0 {
		at, err = parseDeadline(in.Deadline)
	}
	if err == nil && len(in.Settings) > 0 {
		_, err = delivery.ParseBuildSettings(in.Settings)
	}
	if err != nil {
		httpapi.WriteError(w, 400, "invalid deadline or settings")
		return
	}
	out, err := m.store.Update(r.Context(), p, delivery.UpdateRequest{ProjectID: r.PathValue("projectId"), ReleaseID: r.PathValue("releaseId"), ExpectedRevision: in.Revision, Title: in.Title, Body: in.Body, Visibility: in.Visibility, SetEntryDeadline: len(in.Deadline) > 0, EntryClosesAt: at, BuildSettings: in.Settings})
	containerResponse(w, out, err)
}
func (m *module) containerDefaults(w http.ResponseWriter, r *http.Request) {
	p, ok := projectPrincipal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Revision int64           `json:"expected_revision"`
		Defaults json.RawMessage `json:"build_defaults"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	if _, err := delivery.ParseBuildSettings(in.Defaults); err != nil || in.Revision < 1 {
		httpapi.WriteError(w, 400, "invalid defaults")
		return
	}
	err := m.store.SetDefaults(r.Context(), p, r.PathValue("projectId"), in.Revision, in.Defaults)
	containerResponse(w, map[string]any{"project_id": r.PathValue("projectId"), "revision": in.Revision + 1, "build_defaults": in.Defaults, "mode": "releases"}, err)
}
func (m *module) containerAction(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Revision int64  `json:"expected_revision"`
		To       string `json:"to"`
		Scheme   string `json:"version_scheme"`
		Version  string `json:"version"`
		Ref      string `json:"reservation_ref"`
		Before   string `json:"before_id"`
		After    string `json:"after_id"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	if in.Revision < 1 {
		httpapi.WriteError(w, 400, "expected_revision required")
		return
	}
	action := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if (action != "state" && in.To != "") || (action != "cut" && (in.Scheme != "" || in.Version != "")) || (action != "publish" && in.Ref != "") || (action != "rank" && (in.Before != "" || in.After != "")) || (in.Before != "" && in.After != "") {
		httpapi.WriteError(w, 400, "fields do not match this release action")
		return
	}
	edit := delivery.ReleaseEdit{ProjectID: r.PathValue("projectId"), ReleaseID: r.PathValue("releaseId"), ExpectedRevision: in.Revision, Slot: delivery.Slot{BeforeID: in.Before, AfterID: in.After}}
	var out delivery.Release
	var err error
	switch action {
	case "rank":
		if in.Before != "" && !uuid.MatchString(in.Before) || in.After != "" && !uuid.MatchString(in.After) {
			httpapi.WriteError(w, 400, "invalid rank neighbours")
			return
		}
		out, err = m.store.Rerank(r.Context(), p, edit)
	case "publish":
		if len(in.Ref) > 512 {
			httpapi.WriteError(w, 400, "reservation reference too long")
			return
		}
		history, e := m.history()
		if e != nil {
			containerResponse(w, nil, e)
			return
		}
		out, err = m.store.PublishNotes(r.Context(), p, delivery.PublishRequest{ReleaseEdit: edit, ReservationRef: in.Ref}, history, statusautopilot.PublishTx)
	default:
		if action == "cut" && !delivery.ValidProjectVersion(in.Scheme, in.Version) {
			httpapi.WriteError(w, 400, "invalid scheme/version pair")
			return
		}
		if action == "state" {
			action = in.To
			switch action {
			case "frozen":
				action = "freeze"
			case "abandoned":
				action = "abandon"
			case "planned", "building":
			default:
				httpapi.WriteError(w, 400, "invalid lifecycle target")
				return
			}
			if action == "building" {
				action = "state_building"
			}
		}
		out, err = m.store.Transition(r.Context(), p, delivery.TransitionRequest{ProjectID: edit.ProjectID, ReleaseID: edit.ReleaseID, ExpectedRevision: in.Revision, Action: action, VersionScheme: in.Scheme, Version: in.Version})
	}
	containerResponse(w, out, err)
}
func (m *module) containerAdopt(w http.ResponseWriter, r *http.Request) {
	p, ok := projectPrincipal(w, r, true)
	if !ok {
		return
	}
	var in struct {
		Action   string `json:"action"`
		Revision *int64 `json:"expected_revision"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	if in.Revision == nil || *in.Revision < 0 || in.Action != "preview" && in.Action != "retry" {
		httpapi.WriteError(w, 400, "invalid adoption action")
		return
	}
	out, err := m.store.RequestAdoption(r.Context(), p, r.PathValue("projectId"), in.Action, *in.Revision, m.adoption)
	if err != nil {
		containerResponse(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, 202, map[string]any{"project_id": r.PathValue("projectId"), "adoption": out, "status_ref": "/api/projects/" + r.PathValue("projectId") + "/delivery"})
}

type placementInput struct {
	Release         json.RawMessage `json:"release_id"`
	Revision        *int64          `json:"expected_revision"`
	Project         string          `json:"expected_project_id"`
	ReleaseRevision int64           `json:"expected_release_revision"`
	Before          string          `json:"before_id"`
	After           string          `json:"after_id"`
	Expedite        *bool           `json:"expedite"`
	Due             json.RawMessage `json:"due_on"`
	Reason          string          `json:"reason"`
}

func parseReleaseID(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	var id string
	if json.Unmarshal(raw, &id) != nil || !uuid.MatchString(id) {
		return "", errors.New("release_id must be UUID or null")
	}
	return id, nil
}
func (m *module) containerPlace(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
		return
	}
	var in placementInput
	if !decodeContainer(w, r, &in) {
		return
	}
	release, err := parseReleaseID(in.Release)
	if err != nil || !uuid.MatchString(r.PathValue("nodeId")) || !uuid.MatchString(in.Project) || in.Revision == nil || *in.Revision < 0 || release != "" && in.ReleaseRevision < 1 || len(in.Reason) > 2048 || in.Before != "" && !uuid.MatchString(in.Before) || in.After != "" && !uuid.MatchString(in.After) {
		httpapi.WriteError(w, 400, "invalid placement")
		return
	}
	var due *string
	if len(in.Due) > 0 && string(in.Due) != "null" {
		var text string
		if json.Unmarshal(in.Due, &text) != nil {
			httpapi.WriteError(w, 400, "invalid due date")
			return
		}
		if _, err = time.Parse("2006-01-02", text); err != nil {
			httpapi.WriteError(w, 400, "invalid due date")
			return
		}
		due = &text
	}
	expedite := in.Expedite != nil && *in.Expedite
	out, err := m.store.PlaceWithRevision(r.Context(), p, in.Project, []delivery.PlacementRequest{{ItemID: r.PathValue("nodeId"), ExpectedProjectID: in.Project, ExpectedRevision: *in.Revision, ReleaseID: release, ExpectedReleaseRevision: in.ReleaseRevision, Slot: delivery.Slot{BeforeID: in.Before, AfterID: in.After}, Expedite: expedite, DueOn: due, PreserveExpedite: in.Expedite == nil, PreserveDueOn: len(in.Due) == 0}})
	containerResponse(w, out, err)
}
func (m *module) containerBatch(w http.ResponseWriter, r *http.Request) {
	p, ok := projectPrincipal(w, r, false)
	if !ok {
		return
	}
	var in struct {
		Items []struct {
			ID       string `json:"id"`
			Revision *int64 `json:"expected_revision"`
		} `json:"items"`
		Release         json.RawMessage `json:"release_id"`
		ReleaseRevision int64           `json:"expected_release_revision"`
		Position        string          `json:"position"`
		Before          string          `json:"before_id"`
		After           string          `json:"after_id"`
	}
	if !decodeContainer(w, r, &in) {
		return
	}
	release, err := parseReleaseID(in.Release)
	if err != nil || len(in.Items) < 1 || len(in.Items) > 100 || release != "" && in.ReleaseRevision < 1 || in.Position != "" && in.Position != "append" && in.Position != "top" || in.Before != "" && !uuid.MatchString(in.Before) || in.After != "" && !uuid.MatchString(in.After) || in.Before != "" && in.After != "" || in.Position == "top" && (in.Before != "" || in.After != "") {
		httpapi.WriteError(w, 400, "invalid placement batch")
		return
	}
	requests := []delivery.PlacementRequest{}
	seen := map[string]bool{}
	for _, item := range in.Items {
		if !uuid.MatchString(item.ID) || seen[item.ID] || item.Revision == nil || *item.Revision < 0 {
			httpapi.WriteError(w, 400, "invalid or duplicate item")
			return
		}
		seen[item.ID] = true
		requests = append(requests, delivery.PlacementRequest{ItemID: item.ID, ExpectedProjectID: r.PathValue("projectId"), ExpectedRevision: *item.Revision, ReleaseID: release, ExpectedReleaseRevision: in.ReleaseRevision, Slot: delivery.Slot{BeforeID: in.Before, AfterID: in.After}, Top: in.Position == "top" && len(requests) == 0, PreserveExpedite: true, PreserveDueOn: true})
	}
	for i := 1; i < len(requests); i++ {
		if in.Position == "top" || in.Before != "" || in.After != "" {
			requests[i].Slot = delivery.Slot{AfterID: requests[i-1].ItemID}
		}
	}
	out, err := m.store.PlaceWithRevision(r.Context(), p, r.PathValue("projectId"), requests)
	containerResponse(w, out, err)
}
