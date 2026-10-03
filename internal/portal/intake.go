// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
)

const wishIntakeLimit = 5

type wishIntake struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Website string `json:"website"`
}

type wishAccepted struct {
	Accepted bool `json:"accepted"`
}

func (m *Module) submitWish(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !m.limit(w, r, "portal-wish", wishIntakeLimit) {
		return
	}
	if !sameSite(r) {
		fail(w, http.StatusForbidden, "cross-site wish denied")
		return
	}
	var in wishIntake
	if !decodePortalInput(w, r, func() error {
		var err error
		in, err = decodeWish(r)
		if err != nil {
			return statusError{http.StatusBadRequest, "invalid wish"}
		}
		return nil
	}) {
		return
	}
	title, summary, err := wishText(in.Title, in.Summary)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid wish")
		return
	}
	tenantID, err := m.resolveTenant(r.Context(), r.PathValue("tenantSlug"))
	if err != nil {
		slog.Error("portal tenant", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	discarded := strings.TrimSpace(in.Website) != ""
	err = db.InTenant(db.AllProjects(r.Context(), "public portal wish"), m.pool, tenantID, func(tx pgx.Tx) error {
		product, err := publicWriteProduct(r.Context(), tx, r.PathValue("productSlug"))
		if err != nil {
			return err
		}
		if err := checkProductBinding(r, product); err != nil {
			return err
		}
		if err := allowParticipation(product, false); err != nil {
			return err
		}
		productID := product.ProductID
		var allowed []string
		if err := tx.QueryRow(r.Context(), `SELECT k.allowed_child_kinds FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid`, productID).Scan(&allowed); err != nil {
			return err
		}
		if !wishChildAllowed(allowed) {
			return errClosed
		}
		if discarded {
			return nil
		}
		return insertPendingWish(r.Context(), tx, tenantID, productID, title, summary)
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
		slog.Error("portal wish", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	write(w, http.StatusCreated, wishAccepted{Accepted: true})
}

func decodeWish(r *http.Request) (wishIntake, error) {
	var in wishIntake
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return in, errors.New("invalid wish")
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, 24*1024+1))
	if err != nil || len(buf) > 24*1024 {
		return in, errors.New("invalid wish")
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return in, errors.New("invalid wish")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return in, errors.New("invalid wish")
	}
	if utf8.RuneCountInString(in.Website) > 500 {
		return in, errors.New("invalid wish")
	}
	return in, nil
}

func wishText(title, summary string) (string, string, error) {
	title, err := cleanWish(title, 300, false)
	if err != nil {
		return "", "", err
	}
	summary, err = cleanWish(summary, 4000, true)
	if err != nil {
		return "", "", err
	}
	return title, summary, nil
}

func cleanWish(raw string, max int, multiline bool) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\x00", ""))
	if raw == "" || utf8.RuneCountInString(raw) > max {
		return "", errors.New("invalid wish")
	}
	for _, r := range raw {
		switch r {
		case '\n', '\t':
			if !multiline {
				return "", errors.New("invalid wish")
			}
		case '\r':
			return "", errors.New("invalid wish")
		default:
			if unicode.IsControl(r) {
				return "", errors.New("invalid wish")
			}
		}
	}
	return raw, nil
}

func wishChildAllowed(slugs []string) bool {
	if slugs == nil {
		return true
	}
	for _, slug := range slugs {
		if slug == "portal_wish" {
			return true
		}
	}
	return false
}

func insertPendingWish(ctx context.Context, tx pgx.Tx, tenantID, productID, title, summary string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id', true), 0))`); err != nil {
		return err
	}
	var kindID, prefix string
	err := tx.QueryRow(ctx, `SELECT id::text, short_prefix FROM node_kinds WHERE slug = 'portal_wish'`).Scan(&kindID, &prefix)
	if errors.Is(err, pgx.ErrNoRows) || !uuidPattern.MatchString(kindID) {
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return errors.New("portal wish kind missing")
	}
	if err != nil {
		return err
	}
	if !prefixPattern.MatchString(prefix) {
		prefix = "PWS"
	}
	var key, position string
	if err := tx.QueryRow(ctx, `SELECT aeon_next_node_key(NULLIF(current_setting('aeon.tenant_id', true), '')::uuid, $1)`, prefix).Scan(&key); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		SELECT (coalesce(max(position), 0) + 1)::text
		FROM nodes WHERE deleted_at IS NULL AND parent_id = $1::uuid`, productID).Scan(&position); err != nil {
		return err
	}
	var wishID string
	err = tx.QueryRow(ctx, `
		INSERT INTO nodes (tenant_id, key, kind_id, title, body, fields, state, parent_id, position)
		VALUES (
			NULLIF(current_setting('aeon.tenant_id', true), '')::uuid,
			$1, $2::uuid, $3, $4, '{}'::jsonb, 'pending', $5::uuid, $6::numeric
		)
		RETURNING id::text`, key, kindID, title, summary, productID, position).Scan(&wishID)
	if err != nil {
		return err
	}
	actor, err := serviceActor(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	_, err = events.Append(ctx, tx, actor, events.Change{
		NodeID: &wishID,
		Type:   "portal.wish_submitted",
		After:  map[string]any{"wish_key": key, "state": "pending"},
	})
	return err
}
