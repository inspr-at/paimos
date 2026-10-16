// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/nodes"
	"github.com/inspr-at/paimos/internal/tenant"
)

// errGone is the one answer for an unknown, expired, used or someone else's
// draft, so nothing about a draft leaks to a person it does not belong to.
var errGone = errors.New("draft gone")

type draftRow struct {
	ID          string
	Payload     []byte
	Language    string
	CreatedAt   time.Time
	ExpiresAt   time.Time
	ConsumedBy  *string
	ProjectID   *string
	TicketCount *int
}

// View is what the assistant shows. Created answers a person whose own
// create already committed, so a retry lands on the project it made.
type View struct {
	Status       string          `json:"status"`
	Language     string          `json:"language"`
	HandedOverAt time.Time       `json:"handed_over_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	Source       *ViewSource     `json:"source,omitempty"`
	Title        string          `json:"title"`
	Summary      string          `json:"summary"`
	Facts        []string        `json:"facts"`
	OpenPoints   []string        `json:"open_points"`
	Messages     int             `json:"messages"`
	Project      json.RawMessage `json:"project,omitempty"`
	Tickets      *int            `json:"tickets,omitempty"`
}

type ViewSource struct {
	Product string `json:"product"`
	Host    string `json:"host"`
}

// person resolves the caller and the capability header. Only people in the
// configured import tenant can hold a draft; everyone else gets errGone.
func (m *Module) person(w http.ResponseWriter, r *http.Request) (tenant.Principal, []byte, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.ID == "" || p.TenantID == "" {
		httpapi.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return p, nil, false
	}
	if p.Kind != tenant.Person {
		httpapi.WriteError(w, http.StatusForbidden, "a signed-in person creates projects from START")
		return p, nil, false
	}
	digest, ok := capabilityDigest(r.Header.Get(DraftHeader))
	if !ok || m.tenant == "" || m.pool == nil || p.TenantID != m.tenant {
		writeGone(w)
		return p, nil, false
	}
	return p, digest, true
}

func writeGone(w http.ResponseWriter) {
	httpapi.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "This handoff has expired.", "code": "draft_gone"})
}

// loadDraft reads a live draft by its capability digest; forUpdate fences a
// create. Rows past expires_at count as gone before the sweep deletes them.
func loadDraft(ctx context.Context, tx pgx.Tx, digest []byte, forUpdate bool) (draftRow, error) {
	q := `SELECT id::text, payload, language, created_at, expires_at, consumed_by::text, project_id::text, ticket_count
	  FROM import_project_drafts WHERE capability_digest=$1 AND expires_at > clock_timestamp()`
	if forUpdate {
		q += ` FOR NO KEY UPDATE`
	}
	var d draftRow
	err := tx.QueryRow(ctx, q, digest).Scan(&d.ID, &d.Payload, &d.Language, &d.CreatedAt, &d.ExpiresAt, &d.ConsumedBy, &d.ProjectID, &d.TicketCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errGone
	}
	return d, err
}

// created answers the person who already created the project: its current
// snapshot, as long as they can still see it.
func created(ctx context.Context, tx pgx.Tx, p tenant.Principal, d draftRow) (View, error) {
	if d.ConsumedBy == nil || *d.ConsumedBy != p.ID || d.ProjectID == nil {
		return View{}, errGone
	}
	project, err := nodes.NodeSnapshotTx(ctx, tx, *d.ProjectID)
	if err != nil {
		var status interface{ HTTPStatus() int }
		if errors.As(err, &status) && status.HTTPStatus() == http.StatusNotFound {
			return View{}, errGone
		}
		return View{}, err
	}
	return View{Status: "created", Language: d.Language, HandedOverAt: d.CreatedAt, ExpiresAt: d.ExpiresAt, Facts: []string{}, OpenPoints: []string{}, Project: project, Tickets: d.TicketCount}, nil
}

// read never consumes: opening, reloading or prefetching the assistant
// leaves the draft for the explicit create.
func (m *Module) read(w http.ResponseWriter, r *http.Request) {
	p, digest, ok := m.person(w, r)
	if !ok {
		return
	}
	var view View
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		d, err := loadDraft(r.Context(), tx, digest, false)
		if err != nil {
			return err
		}
		if d.ConsumedBy != nil {
			view, err = created(r.Context(), tx, p, d)
			return err
		}
		var payload Payload
		if err := json.Unmarshal(d.Payload, &payload); err != nil {
			return err
		}
		view = View{Status: "ready", Language: payload.Language, HandedOverAt: d.CreatedAt, ExpiresAt: d.ExpiresAt,
			Source: &ViewSource{Product: payload.Source.Product, Host: payload.Source.Host},
			Title:  payload.Title, Summary: payload.Summary, Facts: payload.Facts, OpenPoints: payload.OpenPoints, Messages: len(payload.Transcript)}
		return nil
	})
	if errors.Is(err, errGone) {
		writeGone(w)
		return
	}
	if err != nil {
		httpapi.LogFailure(r, "draft_read", err)
		httpapi.WriteError(w, http.StatusInternalServerError, "The handoff could not be read. Try again.")
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, view)
}
