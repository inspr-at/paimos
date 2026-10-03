// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	maxCompetitors        = 24
	maxAspects            = 40
	maxPendingCorrections = 50
	staleAfterDays        = 180
	recheckAfterDays      = 90
	correctionLimit       = 5
)

var errNoProduct = errors.New("no portal product")

type portalComparisonRow struct {
	Aspect string                 `json:"aspect"`
	Cells  []portalComparisonCell `json:"cells"`
}

type portalComparisonCell struct {
	Competitor  string `json:"competitor"`
	Stance      string `json:"stance"`
	Quote       string `json:"quote,omitempty"`
	SourceURL   string `json:"source_url,omitempty"`
	RetrievedOn string `json:"retrieved_on,omitempty"`
	Stale       bool   `json:"stale,omitempty"`
}

type competitorItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Published bool   `json:"published"`
}

type aspectItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type cellAdmin struct {
	ID           string `json:"id"`
	AspectID     string `json:"aspect_id"`
	CompetitorID string `json:"competitor_id"`
	Stance       string `json:"stance"`
	Quote        string `json:"quote,omitempty"`
	SourceURL    string `json:"source_url,omitempty"`
	RetrievedOn  string `json:"retrieved_on,omitempty"`
	Approved     bool   `json:"approved"`
	Stale        bool   `json:"stale"`
	Recheck      bool   `json:"recheck"`
}

type cellRevision struct {
	CellID      string    `json:"cell_id"`
	Stance      string    `json:"stance"`
	Quote       string    `json:"quote,omitempty"`
	SourceURL   string    `json:"source_url,omitempty"`
	RetrievedOn string    `json:"retrieved_on,omitempty"`
	Approved    bool      `json:"approved"`
	At          time.Time `json:"at"`
}

type correctionItem struct {
	ID         string    `json:"id"`
	Competitor string    `json:"competitor"`
	Aspect     string    `json:"aspect"`
	Statement  string    `json:"statement"`
	SourceURL  string    `json:"source_url,omitempty"`
	At         time.Time `json:"at"`
}

type marketAdmin struct {
	Competitors []competitorItem `json:"competitors"`
	Aspects     []aspectItem     `json:"aspects"`
	Cells       []cellAdmin      `json:"cells"`
	History     []cellRevision   `json:"history"`
	Corrections []correctionItem `json:"corrections"`
}

type cellWrite struct {
	AspectID     string `json:"aspect_id"`
	CompetitorID string `json:"competitor_id"`
	Stance       string `json:"stance"`
	Quote        string `json:"quote"`
	SourceURL    string `json:"source_url"`
	RetrievedOn  string `json:"retrieved_on"`
}

type correctionIntake struct {
	Competitor string `json:"competitor"`
	Aspect     string `json:"aspect"`
	Statement  string `json:"statement"`
	SourceURL  string `json:"source_url"`
	Website    string `json:"website"`
}

func emptyMarket() marketAdmin {
	return marketAdmin{
		Competitors: []competitorItem{},
		Aspects:     []aspectItem{},
		Cells:       []cellAdmin{},
		History:     []cellRevision{},
		Corrections: []correctionItem{},
	}
}

func (m *Module) manage(w http.ResponseWriter, r *http.Request, fn func(context.Context, pgx.Tx, tenant.Principal) (any, error)) {
	publicHeaders(w)
	p, ok := m.person(w, r)
	if !ok {
		return
	}
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	var out any
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockPortalTenant(r.Context(), tx); err != nil {
			return err
		}
		if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
		if err := lockPortalTree(r.Context(), tx); err != nil {
			return err
		}
		// The market row guard allows publication only after this person check.
		if _, err := tx.Exec(r.Context(), `SELECT set_config('aeon.portal_moderation','on',true)`); err != nil {
			return err
		}
		var applyErr error
		ctx := r.Context()
		if id := r.PathValue("productId"); id != "" {
			if !uuidPattern.MatchString(id) {
				return errClosed
			}
			ctx = context.WithValue(ctx, productContextKey{}, id)
		}
		out, applyErr = fn(ctx, tx, p)
		return applyErr
	})
	if errors.Is(err, authz.ErrForbidden) {
		fail(w, http.StatusForbidden, "permission denied")
		return
	}
	var se statusError
	if errors.As(err, &se) {
		fail(w, se.status, se.msg)
		return
	}
	if errors.Is(err, errClosed) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, errNoProduct) {
		fail(w, http.StatusNotFound, "Publish a product first.")
		return
	}
	if err != nil {
		slog.Error("portal market", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusOK, out)
}

func (m *Module) readMarket(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, _ tenant.Principal) (any, error) {
		return loadMarket(ctx, tx)
	})
}

func (m *Module) createCompetitor(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		var in struct {
			Name string `json:"name"`
		}
		if err := decodeEdit(r, &in); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid competitor"}
		}
		name, ok := plainLabel(in.Name, 80)
		if !ok {
			return nil, statusError{status: http.StatusBadRequest, msg: "Name that competitor."}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		item, err := insertCompetitor(ctx, tx, productID, name)
		if err != nil {
			return nil, err
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.competitor_saved", map[string]any{"published": false}); err != nil {
			return nil, err
		}
		return item, nil
	})
}

func (m *Module) patchCompetitor(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		id := r.PathValue("competitorId")
		if !uuidPattern.MatchString(id) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid competitor"}
		}
		var in struct {
			Name      *string `json:"name"`
			Published *bool   `json:"published"`
		}
		if err := decodeEdit(r, &in); err != nil || (in.Name == nil && in.Published == nil) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid competitor"}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		var name string
		var published bool
		err = tx.QueryRow(ctx, `SELECT name, published FROM portal_competitors WHERE id = $1::uuid AND product_id = $2::uuid FOR UPDATE`, id, productID).Scan(&name, &published)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, statusError{status: http.StatusNotFound, msg: "not found"}
		}
		if err != nil {
			return nil, err
		}
		if in.Name != nil {
			next, ok := plainLabel(*in.Name, 80)
			if !ok {
				return nil, statusError{status: http.StatusBadRequest, msg: "Name that competitor."}
			}
			name = next
		}
		if in.Published != nil {
			published = *in.Published
		}
		if _, err := tx.Exec(ctx, `UPDATE portal_competitors SET name = $3, published = $4 WHERE id = $1::uuid AND product_id = $2::uuid`, id, productID, name, published); err != nil {
			if uniqueViolation(err) {
				return nil, statusError{status: http.StatusConflict, msg: "That competitor is already listed."}
			}
			return nil, err
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.competitor_saved", map[string]any{"published": published}); err != nil {
			return nil, err
		}
		return competitorItem{ID: id, Name: name, Published: published}, nil
	})
}

func (m *Module) deleteCompetitor(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		return deleteNamed(ctx, tx, p, r.PathValue("competitorId"), "portal_competitors", "portal.competitor_saved")
	})
}

func (m *Module) createAspect(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		var in struct {
			Label string `json:"label"`
		}
		if err := decodeEdit(r, &in); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid aspect"}
		}
		label, ok := plainLabel(in.Label, 120)
		if !ok {
			return nil, statusError{status: http.StatusBadRequest, msg: "Name what you compare."}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		var n, pos int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM portal_aspects WHERE product_id = $1::uuid`, productID).Scan(&n); err != nil {
			return nil, err
		}
		if n >= maxAspects {
			return nil, statusError{status: http.StatusBadRequest, msg: "The comparison already has 40 rows."}
		}
		if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), 0) + 1 FROM portal_aspects WHERE product_id = $1::uuid`, productID).Scan(&pos); err != nil {
			return nil, err
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO portal_aspects(tenant_id, product_id, label, position)
			VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2, $3)
			RETURNING id::text`, productID, label, pos).Scan(&id)
		if uniqueViolation(err) {
			return nil, statusError{status: http.StatusConflict, msg: "That row is already listed."}
		}
		if err != nil {
			return nil, err
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.aspect_saved", map[string]any{"saved": true}); err != nil {
			return nil, err
		}
		return aspectItem{ID: id, Label: label}, nil
	})
}

func (m *Module) patchAspect(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		id := r.PathValue("aspectId")
		if !uuidPattern.MatchString(id) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid aspect"}
		}
		var in struct {
			Label string `json:"label"`
		}
		if err := decodeEdit(r, &in); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid aspect"}
		}
		label, ok := plainLabel(in.Label, 120)
		if !ok {
			return nil, statusError{status: http.StatusBadRequest, msg: "Name what you compare."}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE portal_aspects SET label = $3 WHERE id = $1::uuid AND product_id = $2::uuid`, id, productID, label)
		if uniqueViolation(err) {
			return nil, statusError{status: http.StatusConflict, msg: "That row is already listed."}
		}
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, statusError{status: http.StatusNotFound, msg: "not found"}
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.aspect_saved", map[string]any{"saved": true}); err != nil {
			return nil, err
		}
		return aspectItem{ID: id, Label: label}, nil
	})
}

func (m *Module) deleteAspect(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		return deleteNamed(ctx, tx, p, r.PathValue("aspectId"), "portal_aspects", "portal.aspect_saved")
	})
}

func (m *Module) putCell(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		var in cellWrite
		if err := decodeEdit(r, &in); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid cell"}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		return saveCell(ctx, tx, p, productID, in)
	})
}

func (m *Module) approveCell(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		if err := emptyWishBody(r); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid cell"}
		}
		id := r.PathValue("cellId")
		if !uuidPattern.MatchString(id) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid cell"}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		row, err := lockCell(ctx, tx, productID, id)
		if err != nil {
			return nil, err
		}
		today, err := portalToday(ctx, tx)
		if err != nil {
			return nil, err
		}
		if !canApprove(row.Stance, row.Quote, row.SourceURL, row.RetrievedOn, today) {
			return nil, statusError{status: http.StatusBadRequest, msg: "A cell needs a public https page, a short quote and the day it was read before it can be approved."}
		}
		if !row.Approved {
			if _, err := tx.Exec(ctx, `UPDATE portal_cells SET approved = true, updated_at = clock_timestamp() WHERE id = $1::uuid`, id); err != nil {
				return nil, err
			}
			row.Approved = true
			if err := insertRevision(ctx, tx, row); err != nil {
				return nil, err
			}
			if err := appendProductEvent(ctx, tx, p, productID, "portal.cell_revised", map[string]any{"stance": row.Stance, "approved": true}); err != nil {
				return nil, err
			}
		}
		return presentAdminCell(row, today), nil
	})
}

func (m *Module) closeCorrection(w http.ResponseWriter, r *http.Request) {
	m.manage(w, r, func(ctx context.Context, tx pgx.Tx, p tenant.Principal) (any, error) {
		if err := emptyWishBody(r); err != nil {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid correction"}
		}
		id := r.PathValue("correctionId")
		if !uuidPattern.MatchString(id) {
			return nil, statusError{status: http.StatusBadRequest, msg: "invalid correction"}
		}
		productID, err := portalProductID(ctx, tx)
		if err != nil {
			return nil, err
		}
		tag, err := tx.Exec(ctx, `UPDATE portal_corrections SET state = 'closed' WHERE id = $1::uuid AND product_id = $2::uuid AND state = 'pending'`, id, productID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() == 0 {
			return nil, statusError{status: http.StatusNotFound, msg: "not found"}
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.correction_closed", map[string]any{"closed": true}); err != nil {
			return nil, err
		}
		return map[string]bool{"closed": true}, nil
	})
}

func (m *Module) submitCorrection(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !m.limit(w, r, "portal-correction", correctionLimit) {
		return
	}
	if !sameSite(r) {
		fail(w, http.StatusForbidden, "cross-site correction denied")
		return
	}
	in, err := decodeCorrection(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid correction")
		return
	}
	competitor, aspect, statement, source, err := correctionText(in)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid correction")
		return
	}
	tenantID, err := m.resolveTenant(r.Context(), r.PathValue("tenantSlug"))
	if err != nil {
		slog.Error("portal tenant", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	discarded := strings.TrimSpace(in.Website) != ""
	err = db.InTenant(db.AllProjects(r.Context(), "public portal correction"), m.pool, tenantID, func(tx pgx.Tx) error {
		product, err := publicWriteProduct(r.Context(), tx, r.PathValue("productSlug"))
		if err != nil {
			return err
		}
		if err := allowParticipation(product, false); err != nil {
			return err
		}
		productID := product.ProductID
		if discarded {
			return nil
		}
		var canonical string
		err = tx.QueryRow(r.Context(), `
			SELECT name FROM portal_competitors
			WHERE product_id = $1::uuid AND published AND lower(name) = lower($2)
			LIMIT 1`, productID, competitor).Scan(&canonical)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM portal_corrections WHERE product_id = $1::uuid AND state = 'pending'`, productID).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingCorrections {
			return nil
		}
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO portal_corrections(tenant_id, product_id, competitor_name, aspect_label, statement, source_url)
			VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2, $3, $4, $5)`,
			productID, canonical, aspect, statement, source); err != nil {
			return err
		}
		actor, err := serviceActor(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		_, err = events.Append(r.Context(), tx, actor, events.Change{
			Type:  "portal.correction_submitted",
			After: map[string]any{"state": "pending"},
		})
		return err
	})
	if errors.Is(err, errClosed) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	var se statusError
	if errors.As(err, &se) {
		fail(w, se.status, se.msg)
		return
	}
	if err != nil {
		slog.Error("portal correction", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusCreated, wishAccepted{Accepted: true})
}

func attachMarket(ctx context.Context, tx pgx.Tx, productID string, doc *portalDocument) error {
	rows, err := loadPublicComparison(ctx, tx, productID)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		doc.Comparison = rows
	}
	return nil
}

func loadPublicComparison(ctx context.Context, tx pgx.Tx, productID string) ([]portalComparisonRow, error) {
	today, err := portalToday(ctx, tx)
	if err != nil {
		return nil, err
	}
	competitors, err := listCompetitors(ctx, tx, productID, true)
	if err != nil || len(competitors) == 0 {
		return nil, err
	}
	aspects, err := listAspects(ctx, tx, productID)
	if err != nil || len(aspects) == 0 {
		return nil, err
	}
	cells, err := listCells(ctx, tx, productID, today)
	if err != nil {
		return nil, err
	}
	byPair := map[string]cellAdmin{}
	for _, cell := range cells {
		byPair[cell.AspectID+"/"+cell.CompetitorID] = cell
	}
	var out []portalComparisonRow
	for _, aspect := range aspects {
		label := publicLine(aspect.Label, 120)
		if label == "" {
			continue
		}
		row := portalComparisonRow{Aspect: label, Cells: []portalComparisonCell{}}
		for _, competitor := range competitors {
			if !competitor.Published {
				continue
			}
			name := publicLine(competitor.Name, 80)
			if name == "" {
				continue
			}
			cell := portalComparisonCell{Competitor: name, Stance: "unknown"}
			if stored, ok := byPair[aspect.ID+"/"+competitor.ID]; ok {
				cell = presentPublicCell(name, stored.Stance, stored.Quote, stored.SourceURL, stored.RetrievedOn, stored.Approved, today)
			}
			row.Cells = append(row.Cells, cell)
		}
		if len(row.Cells) > 0 {
			out = append(out, row)
		}
	}
	return out, nil
}

func loadMarket(ctx context.Context, tx pgx.Tx) (marketAdmin, error) {
	out := emptyMarket()
	productID, err := portalProductID(ctx, tx)
	if errors.Is(err, errNoProduct) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	today, err := portalToday(ctx, tx)
	if err != nil {
		return out, err
	}
	out.Competitors, err = listCompetitors(ctx, tx, productID, false)
	if err != nil {
		return out, err
	}
	out.Aspects, err = listAspects(ctx, tx, productID)
	if err != nil {
		return out, err
	}
	out.Cells, err = listCells(ctx, tx, productID, today)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `
		SELECT cell_id::text, stance, quote, source_url, coalesce(retrieved_on::text, ''), approved, at
		FROM (
			SELECT r.cell_id, r.stance, r.quote, r.source_url, r.retrieved_on, r.approved, r.at,
			       row_number() OVER (PARTITION BY r.cell_id ORDER BY r.at DESC, r.id DESC) AS n
			FROM portal_cell_revisions r
			JOIN portal_cells c ON c.tenant_id = r.tenant_id AND c.id = r.cell_id
			JOIN portal_aspects a ON a.tenant_id = c.tenant_id AND a.id = c.aspect_id
			WHERE a.product_id = $1::uuid
		) s
		WHERE n <= 8
		ORDER BY at DESC`, productID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var item cellRevision
		if err := rows.Scan(&item.CellID, &item.Stance, &item.Quote, &item.SourceURL, &item.RetrievedOn, &item.Approved, &item.At); err != nil {
			return out, err
		}
		out.History = append(out.History, item)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	corr, err := tx.Query(ctx, `
		SELECT id::text, competitor_name, aspect_label, statement, source_url, created_at
		FROM portal_corrections
		WHERE product_id = $1::uuid AND state = 'pending'
		ORDER BY created_at, id
		LIMIT $2`, productID, maxPendingCorrections)
	if err != nil {
		return out, err
	}
	defer corr.Close()
	for corr.Next() {
		var item correctionItem
		if err := corr.Scan(&item.ID, &item.Competitor, &item.Aspect, &item.Statement, &item.SourceURL, &item.At); err != nil {
			return out, err
		}
		out.Corrections = append(out.Corrections, item)
	}
	return out, corr.Err()
}

func listCompetitors(ctx context.Context, tx pgx.Tx, productID string, publishedOnly bool) ([]competitorItem, error) {
	query := `
		SELECT id::text, name, published
		FROM portal_competitors
		WHERE product_id = $1::uuid`
	if publishedOnly {
		query += ` AND published`
	}
	query += ` ORDER BY position, lower(name), id LIMIT $2`
	rows, err := tx.Query(ctx, query, productID, maxCompetitors)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []competitorItem{}
	for rows.Next() {
		var item competitorItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Published); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listAspects(ctx context.Context, tx pgx.Tx, productID string) ([]aspectItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, label FROM portal_aspects
		WHERE product_id = $1::uuid
		ORDER BY position, lower(label), id
		LIMIT $2`, productID, maxAspects)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []aspectItem{}
	for rows.Next() {
		var item aspectItem
		if err := rows.Scan(&item.ID, &item.Label); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func listCells(ctx context.Context, tx pgx.Tx, productID string, today time.Time) ([]cellAdmin, error) {
	rows, err := tx.Query(ctx, `
		SELECT c.id::text, c.aspect_id::text, c.competitor_id::text, c.stance, c.quote, c.source_url,
		       coalesce(c.retrieved_on::text, ''), c.approved
		FROM portal_cells c
		JOIN portal_aspects a ON a.tenant_id = c.tenant_id AND a.id = c.aspect_id
		WHERE a.product_id = $1::uuid
		ORDER BY c.aspect_id, c.competitor_id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []cellAdmin{}
	for rows.Next() {
		var row cellRow
		if err := rows.Scan(&row.ID, &row.AspectID, &row.CompetitorID, &row.Stance, &row.Quote, &row.SourceURL, &row.RetrievedOn, &row.Approved); err != nil {
			return nil, err
		}
		out = append(out, presentAdminCell(row, today))
	}
	return out, rows.Err()
}

type cellRow struct {
	ID           string
	AspectID     string
	CompetitorID string
	Stance       string
	Quote        string
	SourceURL    string
	RetrievedOn  string
	Approved     bool
}

func saveCell(ctx context.Context, tx pgx.Tx, p tenant.Principal, productID string, in cellWrite) (cellAdmin, error) {
	if !uuidPattern.MatchString(in.AspectID) || !uuidPattern.MatchString(in.CompetitorID) {
		return cellAdmin{}, statusError{status: http.StatusBadRequest, msg: "invalid cell"}
	}
	switch in.Stance {
	case "unknown", "yes", "no", "partial":
	default:
		return cellAdmin{}, statusError{status: http.StatusBadRequest, msg: "invalid cell"}
	}
	quote, ok := storedQuote(in.Quote)
	if !ok {
		return cellAdmin{}, statusError{status: http.StatusBadRequest, msg: "Quote the source in a short sentence."}
	}
	source, ok := storedURL(in.SourceURL)
	if !ok {
		return cellAdmin{}, statusError{status: http.StatusBadRequest, msg: "The source has to be an https page."}
	}
	today, err := portalToday(ctx, tx)
	if err != nil {
		return cellAdmin{}, err
	}
	retrieved, ok := storedDay(in.RetrievedOn, today)
	if !ok {
		return cellAdmin{}, statusError{status: http.StatusBadRequest, msg: "The source date cannot be in the future."}
	}
	var aspectOK, competitorOK bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM portal_aspects WHERE id = $1::uuid AND product_id = $2::uuid), EXISTS(SELECT 1 FROM portal_competitors WHERE id = $3::uuid AND product_id = $2::uuid)`, in.AspectID, productID, in.CompetitorID).Scan(&aspectOK, &competitorOK); err != nil {
		return cellAdmin{}, err
	}
	if !aspectOK || !competitorOK {
		return cellAdmin{}, statusError{status: http.StatusNotFound, msg: "not found"}
	}
	retrievedText := ""
	if retrieved != nil {
		retrievedText = *retrieved
	}
	row, err := findCell(ctx, tx, in.AspectID, in.CompetitorID)
	if err != nil {
		return cellAdmin{}, err
	}
	if row == nil {
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO portal_cells(tenant_id, aspect_id, competitor_id, stance, quote, source_url, retrieved_on, approved)
			VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2::uuid, $3, $4, $5, $6::date, false)
			RETURNING id::text`, in.AspectID, in.CompetitorID, in.Stance, quote, source, retrieved).Scan(&id)
		if uniqueViolation(err) {
			return cellAdmin{}, statusError{status: http.StatusConflict, msg: "Save that cell again."}
		}
		if err != nil {
			return cellAdmin{}, err
		}
		row = &cellRow{ID: id, AspectID: in.AspectID, CompetitorID: in.CompetitorID, Stance: in.Stance, Quote: quote, SourceURL: source, RetrievedOn: retrievedText}
		if err := insertRevision(ctx, tx, *row); err != nil {
			return cellAdmin{}, err
		}
		if err := appendProductEvent(ctx, tx, p, productID, "portal.cell_revised", map[string]any{"stance": row.Stance, "approved": false}); err != nil {
			return cellAdmin{}, err
		}
		return presentAdminCell(*row, today), nil
	}
	if row.Stance == in.Stance && row.Quote == quote && row.SourceURL == source && row.RetrievedOn == retrievedText {
		return presentAdminCell(*row, today), nil
	}
	row.Stance = in.Stance
	row.Quote = quote
	row.SourceURL = source
	row.RetrievedOn = retrievedText
	row.Approved = false
	if _, err := tx.Exec(ctx, `
		UPDATE portal_cells
		SET stance = $2, quote = $3, source_url = $4, retrieved_on = $5::date, approved = false, updated_at = clock_timestamp()
		WHERE id = $1::uuid`, row.ID, row.Stance, row.Quote, row.SourceURL, retrieved); err != nil {
		return cellAdmin{}, err
	}
	if err := insertRevision(ctx, tx, *row); err != nil {
		return cellAdmin{}, err
	}
	if err := appendProductEvent(ctx, tx, p, productID, "portal.cell_revised", map[string]any{"stance": row.Stance, "approved": false}); err != nil {
		return cellAdmin{}, err
	}
	return presentAdminCell(*row, today), nil
}

func findCell(ctx context.Context, tx pgx.Tx, aspectID, competitorID string) (*cellRow, error) {
	var row cellRow
	err := tx.QueryRow(ctx, `
		SELECT id::text, aspect_id::text, competitor_id::text, stance, quote, source_url, coalesce(retrieved_on::text, ''), approved
		FROM portal_cells
		WHERE aspect_id = $1::uuid AND competitor_id = $2::uuid
		FOR UPDATE`, aspectID, competitorID).Scan(&row.ID, &row.AspectID, &row.CompetitorID, &row.Stance, &row.Quote, &row.SourceURL, &row.RetrievedOn, &row.Approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func lockCell(ctx context.Context, tx pgx.Tx, productID, id string) (cellRow, error) {
	var row cellRow
	err := tx.QueryRow(ctx, `
		SELECT c.id::text, c.aspect_id::text, c.competitor_id::text, c.stance, c.quote, c.source_url,
		       coalesce(c.retrieved_on::text, ''), c.approved
		FROM portal_cells c
		JOIN portal_aspects a ON a.tenant_id = c.tenant_id AND a.id = c.aspect_id
		WHERE c.id = $1::uuid AND a.product_id = $2::uuid
		FOR UPDATE OF c`, id, productID).Scan(&row.ID, &row.AspectID, &row.CompetitorID, &row.Stance, &row.Quote, &row.SourceURL, &row.RetrievedOn, &row.Approved)
	if errors.Is(err, pgx.ErrNoRows) {
		return cellRow{}, statusError{status: http.StatusNotFound, msg: "not found"}
	}
	return row, err
}

func insertRevision(ctx context.Context, tx pgx.Tx, row cellRow) error {
	var retrieved any
	if row.RetrievedOn != "" {
		retrieved = row.RetrievedOn
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO portal_cell_revisions(tenant_id, cell_id, stance, quote, source_url, retrieved_on, approved)
		VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2, $3, $4, $5::date, $6)`,
		row.ID, row.Stance, row.Quote, row.SourceURL, retrieved, row.Approved)
	return err
}

func insertCompetitor(ctx context.Context, tx pgx.Tx, productID, name string) (competitorItem, error) {
	var n, pos int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM portal_competitors WHERE product_id = $1::uuid`, productID).Scan(&n); err != nil {
		return competitorItem{}, err
	}
	if n >= maxCompetitors {
		return competitorItem{}, statusError{status: http.StatusBadRequest, msg: "The comparison already has 24 competitors."}
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(position), 0) + 1 FROM portal_competitors WHERE product_id = $1::uuid`, productID).Scan(&pos); err != nil {
		return competitorItem{}, err
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO portal_competitors(tenant_id, product_id, name, position)
		VALUES (NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1::uuid, $2, $3)
		RETURNING id::text`, productID, name, pos).Scan(&id)
	if uniqueViolation(err) {
		return competitorItem{}, statusError{status: http.StatusConflict, msg: "That competitor is already listed."}
	}
	if err != nil {
		return competitorItem{}, err
	}
	return competitorItem{ID: id, Name: name, Published: false}, nil
}

func deleteNamed(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, table, eventType string) (any, error) {
	if !uuidPattern.MatchString(id) || (table != "portal_competitors" && table != "portal_aspects") {
		return nil, statusError{status: http.StatusBadRequest, msg: "not found"}
	}
	productID, err := portalProductID(ctx, tx)
	if err != nil {
		return nil, err
	}
	query := `DELETE FROM portal_competitors WHERE id = $1::uuid AND product_id = $2::uuid`
	if table == "portal_aspects" {
		query = `DELETE FROM portal_aspects WHERE id = $1::uuid AND product_id = $2::uuid`
	}
	tag, err := tx.Exec(ctx, query, id, productID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, statusError{status: http.StatusNotFound, msg: "not found"}
	}
	if err := appendProductEvent(ctx, tx, p, productID, eventType, map[string]any{"deleted": true}); err != nil {
		return nil, err
	}
	return map[string]bool{"deleted": true}, nil
}

func appendProductEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, productID, eventType string, after map[string]any) error {
	_, err := events.Append(ctx, tx, p, events.Change{
		NodeID: &productID,
		Type:   eventType,
		After:  after,
	})
	return err
}

func portalProductID(ctx context.Context, tx pgx.Tx) (string, error) {
	if id := productForContext(ctx); id != "" {
		p, err := loadProductSettings(ctx, tx, id, false)
		if err != nil {
			return "", err
		}
		return p.ProductID, nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT p.product_id::text FROM portal_products p
        JOIN nodes n ON n.tenant_id=p.tenant_id AND n.id=p.product_id
        JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
        WHERE p.is_default AND n.parent_id IS NULL AND n.deleted_at IS NULL AND k.slug='portal_product'`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNoProduct
	}
	return id, err
}

func portalToday(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var day string
	if err := tx.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE 'UTC')::date::text`).Scan(&day); err != nil {
		return time.Time{}, err
	}
	return time.Parse("2006-01-02", day)
}

func presentAdminCell(row cellRow, today time.Time) cellAdmin {
	stale, recheck := ageFlags(row.RetrievedOn, today)
	return cellAdmin{
		ID: row.ID, AspectID: row.AspectID, CompetitorID: row.CompetitorID,
		Stance: row.Stance, Quote: row.Quote, SourceURL: row.SourceURL, RetrievedOn: row.RetrievedOn,
		Approved: row.Approved, Stale: stale, Recheck: recheck,
	}
}

func presentPublicCell(name, stance, quote, source, retrieved string, approved bool, today time.Time) portalComparisonCell {
	cell := portalComparisonCell{Competitor: name, Stance: "unknown"}
	if !approved || stance == "unknown" || !canApprove(stance, quote, source, retrieved, today) {
		return cell
	}
	q, _ := storedQuote(quote)
	u, _ := validSourceURL(source)
	cell.Stance = stance
	cell.Quote = q
	cell.SourceURL = u
	cell.RetrievedOn = retrieved
	if stale, _ := ageFlags(retrieved, today); stale {
		cell.Stale = true
	}
	return cell
}

func canApprove(stance, quote, source, retrieved string, today time.Time) bool {
	switch stance {
	case "unknown":
		return true
	case "yes", "no", "partial":
	default:
		return false
	}
	q, ok := storedQuote(quote)
	if !ok || q == "" {
		return false
	}
	if _, ok := validSourceURL(source); !ok {
		return false
	}
	day, err := time.Parse("2006-01-02", retrieved)
	if err != nil || day.After(utcDate(today)) {
		return false
	}
	return true
}

func ageFlags(retrieved string, today time.Time) (stale, recheck bool) {
	day, err := time.Parse("2006-01-02", retrieved)
	if err != nil {
		return false, false
	}
	days := int(utcDate(today).Sub(utcDate(day)).Hours() / 24)
	if days < 0 {
		return false, false
	}
	return days > staleAfterDays, days >= recheckAfterDays
}

func plainLabel(raw string, max int) (string, bool) {
	text, ok := cleanText(raw, max, false)
	if !ok || text == "" || strings.ContainsAny(text, "<>") {
		return "", false
	}
	return text, true
}

func storedQuote(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	text, ok := cleanText(raw, 400, false)
	if !ok || strings.ContainsAny(text, "<>") || utf8.RuneCountInString(text) < 8 {
		return "", false
	}
	return text, true
}

func storedURL(raw string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	return validSourceURL(raw)
}

func storedDay(raw string, today time.Time) (*string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil || day.After(utcDate(today)) {
		return nil, false
	}
	formatted := day.Format("2006-01-02")
	return &formatted, true
}

func validSourceURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 500 || strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		return "", false
	}
	return parsed.String(), true
}

func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func decodeCorrection(r *http.Request) (correctionIntake, error) {
	var in correctionIntake
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return in, errors.New("invalid correction")
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 8*1024+1))
	if err != nil || len(buf) == 0 || len(buf) > 8*1024 {
		return in, errors.New("invalid correction")
	}
	dec := json.NewDecoder(strings.NewReader(string(buf)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, errors.New("invalid correction")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return in, errors.New("invalid correction")
	}
	if utf8.RuneCountInString(in.Website) > 500 {
		return in, errors.New("invalid correction")
	}
	return in, nil
}

func correctionText(in correctionIntake) (competitor, aspect, statement, source string, err error) {
	var ok bool
	competitor, ok = plainLabel(in.Competitor, 80)
	if !ok || hidesContact(in.Competitor) || hidesContact(competitor) {
		return "", "", "", "", errors.New("invalid correction")
	}
	aspect, ok = plainLabel(in.Aspect, 120)
	if !ok || hidesContact(in.Aspect) || hidesContact(aspect) {
		return "", "", "", "", errors.New("invalid correction")
	}
	statement, err = cleanWish(in.Statement, 2000, true)
	if err != nil || utf8.RuneCountInString(statement) < 8 || strings.ContainsAny(statement, "<>") || hidesContact(in.Statement) || hidesContact(statement) {
		return "", "", "", "", errors.New("invalid correction")
	}
	source, ok = storedURL(in.SourceURL)
	if !ok || hidesContact(in.SourceURL) || hidesContact(source) {
		return "", "", "", "", errors.New("invalid correction")
	}
	return competitor, aspect, statement, source, nil
}

// obfuscatedEmail matches a local part, an "(at)" stand-in, and a domain.
// A literal @ is rejected separately, including after percent and entity decoding.
var obfuscatedEmail = regexp.MustCompile(`(?i)[a-z0-9._%+\-]{1,64}\s*(?:\(at\)|\[at\]|\{at\})\s*[a-z0-9][a-z0-9.-]*\.[a-z]{2,}`)

// hidesContact reports an address in text that will be stored. Each pass
// decodes HTML entities and every well-formed %XX (a broken % stays literal),
// then looks again. Past the budget, an escape that would still decode fails
// closed so a deeper encoding cannot outlast the loop.
func hidesContact(raw string) bool {
	cur := raw
	for i := 0; i < 4; i++ {
		if contactMark(cur) {
			return true
		}
		next := unfoldContact(cur)
		if next == cur {
			return false
		}
		cur = next
	}
	return contactMark(cur) || unfoldContact(cur) != cur
}

func unfoldContact(raw string) string {
	next := html.UnescapeString(raw)
	next = decodePercentTolerant(next)
	return strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff', '\u2060':
			return -1
		default:
			return r
		}
	}, next)
}

// decodePercentTolerant decodes each %XX and leaves a malformed % in place.
// url.PathUnescape drops every escape once any one of them is broken, which
// let "100% reader%40example.com" through.
func decodePercentTolerant(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	for i := 0; i < len(s); i++ {
		if s[i] != '%' || i+2 >= len(s) || !isHex(s[i+1]) || !isHex(s[i+2]) {
			b.WriteByte(s[i])
			continue
		}
		b.WriteByte(unhex(s[i+1])<<4 | unhex(s[i+2]))
		i += 2
		changed = true
	}
	if !changed {
		return s
	}
	return b.String()
}

func isHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func contactMark(s string) bool {
	if strings.ContainsAny(s, "@\uFF20\uFE6B") {
		return true
	}
	return obfuscatedEmail.MatchString(s)
}
