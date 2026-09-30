// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

var liveSincePattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$`)

var catalogStates = []string{"idea", "reviewed", "planned", "in_progress", "live", "declined"}

type portalProduct struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type portalFeature struct {
	Key           string `json:"key"`
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	Status        string `json:"status"`
	LiveSince     string `json:"live_since,omitempty"`
	LegalBasis    string `json:"legal_basis,omitempty"`
	DeclineReason string `json:"decline_reason,omitempty"`
}

type portalWish struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Votes   int    `json:"votes"`
}

type portalDocument struct {
	Product        *portalProduct        `json:"product"`
	Catalog        []portalFeature       `json:"catalog"`
	Wishes         []portalWish          `json:"wishes"`
	Comparison     []portalComparisonRow `json:"comparison,omitempty"`
	Pace           *portalPace           `json:"pace,omitempty"`
	ReleaseHistory bool                  `json:"release_history,omitempty"`
	Roadmap        bool                  `json:"roadmap,omitempty"`
}

type voteResult struct {
	Votes int `json:"votes"`
}

func (m *Module) resolveTenant(ctx context.Context, slug string) (string, error) {
	if m.pool == nil || !slugPattern.MatchString(slug) {
		return zeroTenant, nil
	}
	var id string
	err := db.InTenant(db.NoProjects(ctx, "public portal tenant"), m.pool, zeroTenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text FROM tenants WHERE slug=$1`, slug).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return zeroTenant, nil
	}
	if err != nil || !uuidPattern.MatchString(id) {
		if err == nil {
			err = errors.New("portal tenant id")
		}
		return "", err
	}
	return id, nil
}

const (
	publicCatalog      = "catalog"
	publicReleasesKind = "releases"
	publicLlms         = "llms"
	publicRoadmapKind  = "roadmap"
)

func (m *Module) read(w http.ResponseWriter, r *http.Request) {
	m.servePublic(w, r, publicCatalog)
}

func (m *Module) catalogFile(w http.ResponseWriter, r *http.Request) {
	m.servePublic(w, r, publicCatalog)
}

func (m *Module) releases(w http.ResponseWriter, r *http.Request) {
	m.servePublic(w, r, publicReleasesKind)
}

func (m *Module) roadmap(w http.ResponseWriter, r *http.Request) {
	m.servePublic(w, r, publicRoadmapKind)
}

func (m *Module) llms(w http.ResponseWriter, r *http.Request) {
	m.servePublic(w, r, publicLlms)
}

func (m *Module) servePublic(w http.ResponseWriter, r *http.Request, kind string) {
	publicHeaders(w)
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !m.limit(w, r, "portal-read", 120) {
		return
	}
	tenantID, err := m.resolveTenant(r.Context(), r.PathValue("tenantSlug"))
	if err != nil {
		slog.Error("portal tenant", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	// Unknown slugs use the zero tenant and still run this read, so a closed
	// portal and a missing slug do the same work and return the same 404.
	var doc portalDocument
	var releases []publicRelease
	var roadmapItems []publicRoadmapItem
	err = db.InTenant(db.AllProjects(r.Context(), "public portal read"), m.pool, tenantID, func(tx pgx.Tx) error {
		loaded, loadErr := loadPortal(r.Context(), tx)
		if loadErr != nil {
			return loadErr
		}
		doc = loaded
		if doc.Product != nil && kind != publicReleasesKind {
			roadmapItems, loadErr = loadPublicRoadmap(r.Context(), tx)
			if loadErr != nil {
				return loadErr
			}
			doc.Roadmap = len(roadmapItems) > 0
		}
		if kind == publicCatalog || kind == publicRoadmapKind || doc.Product == nil || !doc.ReleaseHistory {
			return nil
		}
		releases, loadErr = loadPublicReleases(r.Context(), tx)
		return loadErr
	})
	if errors.Is(err, errClosed) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("portal read", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if releases == nil {
		releases = []publicRelease{}
	}
	switch kind {
	case publicRoadmapKind:
		if roadmapItems == nil {
			roadmapItems = []publicRoadmapItem{}
		}
		w.Header().Set("Cache-Control", roadmapCache)
		write(w, http.StatusOK, publicRoadmapDocument{Schema: roadmapSchema, Product: doc.Product, Items: roadmapItems})
	case publicReleasesKind:
		write(w, http.StatusOK, publicReleasesDocument{Product: doc.Product, Releases: releases})
	case publicLlms:
		slug := r.PathValue("tenantSlug")
		if !slugPattern.MatchString(slug) {
			fail(w, http.StatusNotFound, "not found")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, renderLlms(slug, doc, releases))
	default:
		write(w, http.StatusOK, doc)
	}
}

func loadPortal(ctx context.Context, tx pgx.Tx) (portalDocument, error) {
	doc := portalDocument{Catalog: []portalFeature{}, Wishes: []portalWish{}}
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM portal_settings`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
		return doc, errClosed
	}
	if err != nil {
		return doc, err
	}
	var id, key, title, body string
	err = tx.QueryRow(ctx, `
		SELECT n.id::text, n.key, left(n.title, 300), left(n.body, 4000)
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE k.slug = 'portal_product' AND n.parent_id IS NULL AND n.deleted_at IS NULL AND n.state = 'published'
		ORDER BY n.position, n.key
		LIMIT 1`).Scan(&id, &key, &title, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return doc, nil
	}
	if err != nil {
		return doc, err
	}
	title = clip(title, 300)
	if title == "" {
		return doc, nil
	}
	doc.Product = &portalProduct{Key: key, Title: title, Summary: clip(body, 4000)}

	rows, err := tx.Query(ctx, `
		SELECT n.key, left(n.title, 300), left(n.body, 4000), n.state,
		       n.fields->>'live_since', n.fields->>'legal_basis', n.fields->>'decline_reason'
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE k.slug = 'portal_feature' AND n.parent_id = $1::uuid AND n.deleted_at IS NULL
		  AND n.state = ANY($2::text[])
		ORDER BY n.position, n.key
		LIMIT 500`, id, catalogStates)
	if err != nil {
		return doc, err
	}
	for rows.Next() {
		var item portalFeature
		var live, legal, decline *string
		if err := rows.Scan(&item.Key, &item.Title, &item.Summary, &item.Status, &live, &legal, &decline); err != nil {
			rows.Close()
			return doc, err
		}
		item.Title = clip(item.Title, 300)
		item.Summary = clip(item.Summary, 4000)
		if item.Title == "" || !catalogState(item.Status) {
			continue
		}
		if item.Status == "live" && live != nil && liveSincePattern.MatchString(*live) {
			item.LiveSince = *live
		}
		if legal != nil {
			item.LegalBasis = publicLine(*legal, 240)
		}
		if item.Status == "declined" && decline != nil {
			item.DeclineReason = publicLine(*decline, 500)
		}
		doc.Catalog = append(doc.Catalog, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return doc, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `
		SELECT n.key, left(n.title, 300), left(n.body, 4000),
		       coalesce((SELECT sum(v.weight)::bigint FROM portal_votes v WHERE v.tenant_id = n.tenant_id AND v.wish_id = n.id), 0)
		FROM nodes n
		JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
		WHERE k.slug = 'portal_wish' AND n.parent_id = $1::uuid AND n.deleted_at IS NULL AND n.state = 'published'
		ORDER BY n.position, n.key
		LIMIT 500`, id)
	if err != nil {
		return doc, err
	}
	defer rows.Close()
	for rows.Next() {
		var item portalWish
		var votes int64
		if err := rows.Scan(&item.Key, &item.Title, &item.Summary, &votes); err != nil {
			return doc, err
		}
		item.Title = clip(item.Title, 300)
		item.Summary = clip(item.Summary, 4000)
		if item.Title == "" || votes < 0 || votes > math.MaxInt32 {
			continue
		}
		item.Votes = int(votes)
		doc.Wishes = append(doc.Wishes, item)
	}
	if err := rows.Err(); err != nil {
		return doc, err
	}
	if err := attachMarket(ctx, tx, id, &doc); err != nil {
		return doc, err
	}
	if err := attachPace(ctx, tx, id, &doc); err != nil {
		return doc, err
	}
	on, err := portalPublishesReleases(ctx, tx)
	if err != nil {
		return doc, err
	}
	doc.ReleaseHistory = on
	return doc, nil
}

func catalogState(state string) bool {
	for _, allowed := range catalogStates {
		if state == allowed {
			return true
		}
	}
	return false
}

func clip(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
	runes := []rune(s)
	if len(runes) > max {
		runes = runes[:max]
	}
	return string(runes)
}

func publicLine(raw string, max int) string {
	if strings.ContainsAny(raw, "<>") {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
	return clip(cleaned, max)
}

func (m *Module) vote(w http.ResponseWriter, r *http.Request) {
	publicHeaders(w)
	if m.pool == nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !m.limit(w, r, "portal-vote", 10) {
		return
	}
	if !sameSite(r) {
		fail(w, http.StatusForbidden, "cross-site vote denied")
		return
	}
	if err := decodeVote(r); err != nil {
		fail(w, http.StatusBadRequest, "invalid vote")
		return
	}
	wishKey := r.PathValue("wishKey")
	tenantID, err := m.resolveTenant(r.Context(), r.PathValue("tenantSlug"))
	if err != nil {
		slog.Error("portal tenant", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if !validKey(wishKey) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	ballotValue, minted, err := ballotValue(r)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	var votes int64
	var created bool
	err = db.InTenant(db.AllProjects(r.Context(), "public portal vote"), m.pool, tenantID, func(tx pgx.Tx) error {
		open, err := portalOpen(r.Context(), tx)
		if err != nil || !open {
			if err != nil {
				return err
			}
			return errClosed
		}
		var wishID string
		err = tx.QueryRow(r.Context(), `
			SELECT n.id::text
			FROM nodes n
			JOIN node_kinds k ON k.tenant_id = n.tenant_id AND k.id = n.kind_id
			JOIN nodes parent ON parent.tenant_id = n.tenant_id AND parent.id = n.parent_id
			JOIN node_kinds pk ON pk.tenant_id = parent.tenant_id AND pk.id = parent.kind_id
			WHERE k.slug = 'portal_wish' AND pk.slug = 'portal_product'
			  AND parent.parent_id IS NULL AND parent.deleted_at IS NULL AND parent.state = 'published'
			  AND n.deleted_at IS NULL AND n.state = 'published' AND n.key = $1`, wishKey).Scan(&wishID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errClosed
		}
		if err != nil {
			return err
		}
		tag, err := tx.Exec(r.Context(), `
			INSERT INTO portal_votes(tenant_id, wish_id, voter_hash, weight)
			VALUES ($1::uuid, $2::uuid, $3, 1)
			ON CONFLICT (tenant_id, wish_id, voter_hash) DO NOTHING`, tenantID, wishID, hash(ballotValue+":"+wishID))
		if err != nil {
			return err
		}
		created = tag.RowsAffected() == 1
		if created {
			actor, err := serviceActor(r.Context(), tx, tenantID)
			if err != nil {
				return err
			}
			if _, err := events.Append(r.Context(), tx, actor, events.Change{
				NodeID: &wishID,
				Type:   "portal.vote_cast",
				After:  map[string]any{"wish_key": wishKey, "weight": 1},
			}); err != nil {
				return err
			}
		}
		return tx.QueryRow(r.Context(), `SELECT coalesce(sum(weight),0)::bigint FROM portal_votes WHERE wish_id=$1::uuid`, wishID).Scan(&votes)
	})
	if errors.Is(err, errClosed) {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.Error("portal vote", "err", err)
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if votes < 0 || votes > math.MaxInt32 {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return
	}
	if minted {
		m.setBallot(w, ballotValue)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	write(w, status, voteResult{Votes: int(votes)})
}

func portalOpen(ctx context.Context, tx pgx.Tx) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT enabled FROM portal_settings`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}

func decodeVote(r *http.Request) error {
	buf, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	if err != nil || len(buf) > 1024 {
		return errors.New("invalid vote")
	}
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 {
		return nil
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || trimmed[0] != '{' {
		return errors.New("invalid vote")
	}
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	var body struct{}
	if err := dec.Decode(&body); err != nil {
		return errors.New("invalid vote")
	}
	var extra struct{}
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid vote")
	}
	return nil
}

func ballotValue(r *http.Request) (string, bool, error) {
	if c, err := r.Cookie(ballotCookie); err == nil && ballotPattern.MatchString(c.Value) {
		return c.Value, false, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", false, err
	}
	return base64.RawURLEncoding.EncodeToString(buf), true, nil
}

func (m *Module) setBallot(w http.ResponseWriter, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     ballotCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   400 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   m.secureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func serviceActor(ctx context.Context, tx pgx.Tx, tenantID string) (tenant.Principal, error) {
	p := tenant.Principal{TenantID: tenantID, Kind: tenant.Agent, Roles: []string{portalServiceRole}}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "portal-public-service:"+tenantID); err != nil {
		return p, err
	}
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND $1=ANY(roles) ORDER BY created_at,id LIMIT 1`, portalServiceRole).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','Portal public service',ARRAY[$2]::text[]) RETURNING id::text`, tenantID, portalServiceRole).Scan(&p.ID)
		if err == nil {
			_, err = events.Append(ctx, tx, p, events.Change{Type: "principal.created", After: map[string]any{"id": p.ID, "kind": "agent", "role": portalServiceRole}})
		}
	}
	return p, err
}

func sameSite(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	for _, raw := range []string{r.Header.Get("Origin"), r.Header.Get("Referer")} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Host, r.Host) || (u.Scheme != "https" && u.Scheme != "http") {
			return false
		}
	}
	return true
}
