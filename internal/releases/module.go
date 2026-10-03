// SPDX-License-Identifier: AGPL-3.0-only

// Package releases implements the R3 release walker and planning API.
// The coordinator mounts New(pool); release creation and stage transitions
// belong to the journey module. No version or completion state is invented.
package releases

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/delivery"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

type module struct{ pool *pgxpool.Pool }

// New returns an httpapi.Module serving walker GET and revision-fenced plan
// PUT and ticket creation POST. No additional plugin registration is needed.
// Clients retain their own remembered partial feature selections; PUT
// persists only the complete eligible ticket order and selected ticket set.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool} }
func (m *module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}/note-snapshot", m.noteSnapshot)
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}/walker", m.get)
	mux.HandleFunc("GET /api/projects/{projectId}/release-memberships", m.readMemberships)
	mux.HandleFunc("PUT /api/projects/{projectId}/releases/{releaseId}/plan", m.put)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/tickets", m.createTicket)
	mux.HandleFunc("GET /api/projects/{projectId}/releases/{releaseId}/ticket-options", m.ticketOptions)
	mux.HandleFunc("POST /api/projects/{projectId}/releases/{releaseId}/membership", m.addMembership)
}

type Walker struct {
	Warnings  []string  `json:"warnings,omitempty"`
	ReleaseID string    `json:"release_node_id"`
	ProjectID string    `json:"project_node_id"`
	State     string    `json:"state"`
	Revision  int64     `json:"revision"`
	Features  []Feature `json:"features"`
	Tickets   []Ticket  `json:"tickets"`
}
type Feature struct {
	NodeID        string `json:"feature_node_id"`
	Key           string `json:"epic_key"`
	Title         string `json:"title"`
	Selection     string `json:"selection"`
	IncludedCount int    `json:"included_count"`
	OpenCount     int    `json:"open_count"`
}
type Ticket struct {
	NodeID    string   `json:"ticket_node_id"`
	Key       string   `json:"key"`
	Title     string   `json:"title"`
	FeatureID *string  `json:"feature_node_id"`
	Included  bool     `json:"included"`
	Position  int      `json:"position"`
	Hours     *float64 `json:"estimated_hours"`
	ScreenIDs []string `json:"screen_node_ids"`
}
type planInput struct {
	Revision int64    `json:"expected_revision"`
	Order    []string `json:"ordered_ticket_ids"`
	Included []string `json:"included_ticket_ids"`
}

var uuid = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type failure struct {
	code    int
	message string
}

func (e *failure) Error() string      { return e.message }
func fail(code int, msg string) error { return &failure{code, msg} }
func respond(w http.ResponseWriter, out any, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if err == nil {
		httpapi.WriteJSON(w, 200, out)
		return
	}
	var f *failure
	if errors.As(err, &f) {
		httpapi.WriteError(w, f.code, f.message)
		return
	}
	if errors.Is(err, pgx.ErrNoRows) {
		httpapi.WriteError(w, 404, "project or release not found")
		return
	}
	slog.Error("releases", "err", err)
	httpapi.WriteError(w, 500, "internal")
}
func projectPrincipal(w http.ResponseWriter, r *http.Request, write bool) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok {
		httpapi.WriteError(w, 401, "authentication required")
		return p, false
	}
	if (p.Kind != tenant.Person && p.Kind != tenant.Agent) || (write && p.Kind != tenant.Person) {
		httpapi.WriteError(w, 403, "person required")
		return p, false
	}
	if !uuid.MatchString(r.PathValue("projectId")) {
		httpapi.WriteError(w, 400, "invalid project id")
		return p, false
	}
	return p, true
}
func principal(w http.ResponseWriter, r *http.Request, write bool) (tenant.Principal, bool) {
	p, ok := projectPrincipal(w, r, write)
	if !ok {
		return p, false
	}
	if !uuid.MatchString(r.PathValue("releaseId")) {
		httpapi.WriteError(w, 400, "invalid release id")
		return p, false
	}
	return p, true
}
func (m *module) get(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, false)
	if !ok {
		return
	}
	var out Walker
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		// Keep membership and order stable while R1 structural edits and R3 plan
		// mutations use the corresponding exclusive tree lock.
		if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock_shared(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}
		if err := requireJourneyMode(r.Context(), tx, r.PathValue("projectId")); err != nil {
			return err
		}
		var locked string
		if err := tx.QueryRow(r.Context(), `SELECT project_node_id::text FROM journey_projects WHERE project_node_id=$1 FOR SHARE`, r.PathValue("projectId")).Scan(&locked); err != nil {
			return err
		}
		var err error
		out, err = load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("releaseId"))
		return err
	})
	respond(w, out, err)
}
func (m *module) put(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r, true)
	if !ok {
		return
	}
	var in planInput
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		respond(w, nil, fail(400, "invalid plan"))
		return
	}
	if d.Decode(new(any)) != io.EOF {
		respond(w, nil, fail(400, "invalid plan"))
		return
	}
	if in.Revision < 1 || in.Order == nil || in.Included == nil {
		respond(w, nil, fail(400, "revision and ticket arrays required"))
		return
	}
	for _, ids := range [][]string{in.Order, in.Included} {
		seen := map[string]bool{}
		for i, id := range ids {
			id = strings.ToLower(id)
			if !uuid.MatchString(id) || seen[id] {
				respond(w, nil, fail(400, "ticket ids must be unique UUIDs"))
				return
			}
			ids[i] = id
			seen[id] = true
		}
	}
	var out Walker
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		out, err = replace(r.Context(), tx, p, strings.ToLower(r.PathValue("projectId")), strings.ToLower(r.PathValue("releaseId")), in)
		return err
	})
	respond(w, out, err)
}

func load(ctx context.Context, tx pgx.Tx, project, release string) (Walker, error) {
	out := Walker{Features: []Feature{}, Tickets: []Ticket{}}
	err := tx.QueryRow(ctx, `SELECT r.release_node_id::text,r.project_node_id::text,r.state,r.revision FROM journey_releases r
 JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id
 JOIN nodes pn ON pn.tenant_id=r.tenant_id AND pn.id=r.project_node_id
 WHERE r.project_node_id=$1 AND r.release_node_id=$2 AND rn.deleted_at IS NULL AND pn.deleted_at IS NULL FOR SHARE OF r`, project, release).Scan(&out.ReleaseID, &out.ProjectID, &out.State, &out.Revision)
	if err != nil {
		return out, err
	}
	// Historical release members cannot be selected into a different release.
	// Backlog is available in planning only; active/historical walkers are sealed
	// to their own members so subsequent backlog changes do not rewrite history.
	rows, err := tx.Query(ctx, `SELECT t.ticket_node_id::text,n.key,n.title,t.feature_node_id::text,coalesce(t.release_node_id=$2,false),t.walker_position,t.estimated_hours::float8,n.state,
 ARRAY(SELECT DISTINCT screen.id::text FROM node_relations rel
 JOIN nodes screen ON screen.tenant_id=rel.tenant_id AND screen.id=CASE WHEN rel.source_node_id=n.id THEN rel.target_node_id ELSE rel.source_node_id END
 JOIN node_kinds sk ON sk.tenant_id=screen.tenant_id AND sk.id=screen.kind_id
 WHERE (rel.source_node_id=n.id OR rel.target_node_id=n.id) AND screen.deleted_at IS NULL AND sk.slug='screen' ORDER BY screen.id::text)
 FROM journey_tickets t JOIN nodes n ON n.tenant_id=t.tenant_id AND n.id=t.ticket_node_id
 WHERE t.project_node_id=$1 AND (t.release_node_id=$2 OR ($3='planning' AND t.release_node_id IS NULL)) AND n.deleted_at IS NULL
 ORDER BY t.walker_position,t.ticket_node_id`, project, release, out.State)
	if err != nil {
		return out, err
	}
	counts := map[string][2]int{}
	for rows.Next() {
		var t Ticket
		var state string
		if err = rows.Scan(&t.NodeID, &t.Key, &t.Title, &t.FeatureID, &t.Included, &t.Position, &t.Hours, &state, &t.ScreenIDs); err != nil {
			rows.Close()
			return out, err
		}
		out.Tickets = append(out.Tickets, t)
		// R1 uses 'done' for completed nodes. Other tenant-defined states are not
		// guessed to be completion evidence.
		if t.FeatureID != nil && state != "done" {
			c := counts[*t.FeatureID]
			c[0]++
			if t.Included {
				c[1]++
			}
			counts[*t.FeatureID] = c
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	rows, err = tx.Query(ctx, `SELECT f.feature_node_id::text,n.key,n.title FROM journey_features f JOIN nodes n ON n.tenant_id=f.tenant_id AND n.id=f.feature_node_id
 WHERE f.project_node_id=$1 AND n.deleted_at IS NULL
 AND ($3='planning' OR EXISTS(SELECT 1 FROM journey_tickets t WHERE t.feature_node_id=f.feature_node_id AND t.release_node_id=$2))
 ORDER BY coalesce((SELECT min(t.walker_position) FROM journey_tickets t WHERE t.feature_node_id=f.feature_node_id AND (t.release_node_id=$2 OR ($3='planning' AND t.release_node_id IS NULL))),2147483647),n.position,n.id`, project, release, out.State)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Feature
		if err = rows.Scan(&f.NodeID, &f.Key, &f.Title); err != nil {
			return out, err
		}
		c := counts[f.NodeID]
		f.OpenCount = c[0]
		f.IncludedCount = c[1]
		switch {
		case c[0] == 0:
			f.Selection = "empty"
		case c[1] == 0:
			f.Selection = "none"
		case c[0] == c[1]:
			f.Selection = "all"
		default:
			f.Selection = "some"
		}
		out.Features = append(out.Features, f)
	}
	return out, rows.Err()
}

func requireJourneyMode(ctx context.Context, tx pgx.Tx, project string) error {
	if err := delivery.RequireJourney(ctx, tx, project); err != nil {
		if errors.Is(err, delivery.ErrReleasesMode) {
			return fail(409, err.Error())
		}
		return err
	}
	return nil
}
