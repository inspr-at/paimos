// SPDX-License-Identifier: AGPL-3.0-only

// Package public exposes quote-scoped capability links. The coordinator mounts
// New as an httpapi.Module and allows only /api/public/quotes/* through auth
// middleware. Link management remains authenticated and admin-only. The module
// never grants a tenant principal to a capability holder.
package public

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/linkvault"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/plugins/fence"
	"github.com/inspr-at/paimos/internal/quotepdf"
	"github.com/inspr-at/paimos/internal/tenant"
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var selectorPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{20,80}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const zeroTenant = "00000000-0000-0000-0000-000000000000"

type Module struct {
	pool          *pgxpool.Pool
	registry      *plugins.Registry
	assets        fs.FS
	store         *attachments.Store
	publicBaseURL string
	linkKey       []byte
	limitCalls    atomic.Uint64
}

var _ httpapi.Module = (*Module)(nil)

// New preserves the existing module constructor until the coordinator wires
// the receipt store. PDF downloads return unavailable without that store.
func New(pool *pgxpool.Pool, registry *plugins.Registry, assets fs.FS, publicBaseURL string) (httpapi.Module, error) {
	return newModule(pool, registry, assets, nil, publicBaseURL)
}

// NewWithStore exposes management and public capability routes. The receipt
// store must be the same tenant-namespaced store used by the confirmation
// worker, so an accepted public PDF resolves to its immutable receipt. The
// coordinator mounts this httpapi.Module. assets is the built web filesystem
// for offered PDF rendering; publicBaseURL is trusted QR configuration.
func NewWithStore(pool *pgxpool.Pool, registry *plugins.Registry, assets fs.FS, store attachments.Store, publicBaseURL string) (httpapi.Module, error) {
	return newModule(pool, registry, assets, &store, publicBaseURL)
}

func newModule(pool *pgxpool.Pool, registry *plugins.Registry, assets fs.FS, store *attachments.Store, publicBaseURL string) (httpapi.Module, error) {
	if pool == nil || registry == nil {
		return nil, errors.New("quote public module requires pool and registry")
	}
	if _, ok := registry.Lookup("business_quotes"); !ok {
		return nil, errors.New("business_quotes manifest is absent")
	}
	if publicBaseURL != "" {
		u, err := url.Parse(publicBaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("invalid public base URL")
		}
	}
	key, err := config.LinkKeyFromEnv()
	if err != nil {
		return nil, err
	}
	return &Module{pool: pool, registry: registry, assets: assets, store: store, publicBaseURL: strings.TrimRight(publicBaseURL, "/"), linkKey: key}, nil
}

func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions/{version}/public-link", m.createLink)
	mux.HandleFunc("GET /api/quotes/{quoteId}/versions/{version}/public-link", m.linkInfo)
	mux.HandleFunc("POST /api/quotes/{quoteId}/versions/{version}/public-link/revoke", m.revokeLink)
	mux.HandleFunc("GET /api/public/quotes/{publicTenant}/{token}", m.read)
	mux.HandleFunc("POST /api/public/quotes/{publicTenant}/{token}/accept", m.accept)
	mux.HandleFunc("GET /api/public/quotes/{publicTenant}/{token}/pdf", m.pdf)
}

func write(w http.ResponseWriter, status int, value any)     { httpapi.WriteJSON(w, status, value) }
func fail(w http.ResponseWriter, status int, message string) { httpapi.WriteError(w, status, message) }
func safeHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
}
func (m *Module) admin(r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Person || !uuidPattern.MatchString(p.TenantID) || !uuidPattern.MatchString(p.ID) {
		return p, false
	}
	return p, authz.Require(authz.BindPool(r.Context(), m.pool), "quotes.manage", authz.Scope{}) == nil
}
func routeQuote(r *http.Request) (string, int, bool) {
	id := r.PathValue("quoteId")
	v, err := strconv.Atoi(r.PathValue("version"))
	return id, v, uuidPattern.MatchString(id) && err == nil && v > 0
}
func randomURL(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func decode(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("JSON required")
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 8193))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("invalid JSON")
	}
	return nil
}
func (m *Module) gate(ctx context.Context, tx pgx.Tx, tenantID, permission string) error {
	for _, id := range []string{"business_quotes", "business_costs", "business_crm"} {
		plugin, ok := m.registry.Lookup(id)
		if !ok {
			return errors.New("plugin disabled")
		}
		var enabled bool
		var digest string
		var permissions []string
		err := tx.QueryRow(ctx, `SELECT enabled,manifest_digest_sha256,permissions FROM plugin_installations WHERE tenant_id=$1::uuid AND plugin_id=$2`, tenantID, id).Scan(&enabled, &digest, &permissions)
		if err != nil || !enabled || digest != plugin.Manifest.DigestSHA256 {
			return errors.New("plugin disabled")
		}
		if id == "business_quotes" {
			found := false
			for _, p := range permissions {
				if p == permission {
					found = true
				}
			}
			if !found {
				return errors.New("plugin permission absent")
			}
		}
	}
	return nil
}

type managedLink struct {
	ID                    string     `json:"id"`
	PublicTenant          string     `json:"public_tenant"`
	QuoteNodeID           string     `json:"quote_node_id"`
	Version               int        `json:"version"`
	TargetContentSHA256   string     `json:"target_content_sha256"`
	ExpiresAt             time.Time  `json:"expires_at"`
	RevokedAt             *time.Time `json:"revoked_at,omitempty"`
	Path                  string     `json:"path,omitempty"`
	Token                 string     `json:"token,omitempty"`
	CopyUnavailableReason string     `json:"copy_unavailable_reason,omitempty"`
}

func (m *Module) createLink(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(r)
	if !ok {
		fail(w, 403, "administrator required")
		return
	}
	id, version, ok := routeQuote(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var input struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &input); err != nil {
			fail(w, 400, "invalid link request")
			return
		}
	}
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = time.Now().Add(30 * 24 * time.Hour)
	}
	if input.ExpiresAt.Before(time.Now().Add(time.Minute)) || input.ExpiresAt.After(time.Now().Add(366*24*time.Hour)) {
		fail(w, 400, "invalid expiry")
		return
	}
	token, err := randomURL(32)
	if err != nil {
		fail(w, 500, "link unavailable")
		return
	}
	selector, err := randomURL(24)
	if err != nil {
		fail(w, 500, "link unavailable")
		return
	}
	var out managedLink
	err = db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermStepsApply); err != nil {
			return err
		}
		var state, digest string
		var current int
		err := tx.QueryRow(r.Context(), `SELECT q.state,q.current_version,v.content_sha256 FROM business_quotes q JOIN quote_versions v ON v.tenant_id=q.tenant_id AND v.quote_node_id=q.quote_node_id AND v.version=$2 JOIN quote_version_snapshots s ON s.tenant_id=v.tenant_id AND s.quote_node_id=v.quote_node_id AND s.version=v.version WHERE q.quote_node_id=$1::uuid FOR UPDATE OF q`, id, version).Scan(&state, &current, &digest)
		if err != nil {
			return err
		}
		if state != "issued" || current != version {
			return errors.New("quote is not currently issued")
		}
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM quote_public_links WHERE quote_node_id=$1::uuid AND version=$2 AND revoked_at IS NULL)`, id, version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return errors.New("active link already exists; revoke before reissue")
		}
		var publicTenant string
		err = tx.QueryRow(r.Context(), `SELECT selector FROM quote_public_tenant_selectors WHERE tenant_id=$1::uuid`, p.TenantID).Scan(&publicTenant)
		if errors.Is(err, pgx.ErrNoRows) {
			publicTenant = selector
			_, err = tx.Exec(r.Context(), `INSERT INTO quote_public_tenant_selectors(tenant_id,selector) VALUES($1::uuid,$2)`, p.TenantID, selector)
		}
		if err != nil {
			return err
		}
		out = managedLink{PublicTenant: publicTenant, QuoteNodeID: id, Version: version, TargetContentSHA256: digest, ExpiresAt: input.ExpiresAt, Token: token}
		if err := tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&out.ID); err != nil {
			return err
		}
		event, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "quote.public_link_created", After: map[string]any{"link_id": out.ID, "version": version, "target_content_sha256": digest}})
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO quote_public_links(tenant_id,id,quote_node_id,version,token_sha256,target_content_sha256,issued_by_principal_id,issued_event_id,expires_at) VALUES($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid,$8,$9)`, p.TenantID, out.ID, id, version, hash(token), digest, p.ID, event.ID, input.ExpiresAt)
		if err != nil {
			return err
		}
		if m.linkKey != nil {
			ciphertext, err := linkvault.Encrypt(m.linkKey, p.TenantID, out.ID, token)
			if err != nil {
				return err
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO quote_public_link_tokens(tenant_id,link_id,ciphertext) VALUES($1::uuid,$2::uuid,$3)`, p.TenantID, out.ID, ciphertext)
		} else {
			out.CopyUnavailableReason = "key_not_configured"
		}
		out.Path = "/offers/" + publicTenant + "/" + token
		return err
	})
	if err != nil {
		fail(w, 409, "link cannot be created")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 201, out)
}
func (m *Module) linkInfo(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(r)
	if !ok {
		fail(w, 403, "administrator required")
		return
	}
	id, version, ok := routeQuote(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var out managedLink
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermViewsProvide); err != nil {
			return err
		}
		var ciphertext []byte
		var verifier string
		err := tx.QueryRow(r.Context(), `SELECT l.id::text,s.selector,l.quote_node_id::text,l.version,l.target_content_sha256,l.expires_at,l.revoked_at,l.token_sha256,t.ciphertext FROM quote_public_links l JOIN quote_public_tenant_selectors s ON s.tenant_id=l.tenant_id LEFT JOIN quote_public_link_tokens t ON t.tenant_id=l.tenant_id AND t.link_id=l.id WHERE l.quote_node_id=$1::uuid AND l.version=$2 ORDER BY l.issued_at DESC LIMIT 1`, id, version).Scan(&out.ID, &out.PublicTenant, &out.QuoteNodeID, &out.Version, &out.TargetContentSHA256, &out.ExpiresAt, &out.RevokedAt, &verifier, &ciphertext)
		if err == nil && out.RevokedAt == nil && m.linkKey == nil {
			out.CopyUnavailableReason = "key_not_configured"
		} else if err == nil && out.RevokedAt == nil && ciphertext != nil {
			token, decryptErr := linkvault.Decrypt(m.linkKey, p.TenantID, out.ID, ciphertext)
			if decryptErr != nil || hash(token) != verifier {
				return errors.New("link cannot be re-copied")
			}
			out.Path = "/offers/" + out.PublicTenant + "/" + token
		}
		return err
	})
	if err != nil {
		fail(w, 404, "link not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, out)
}
func (m *Module) revokeLink(w http.ResponseWriter, r *http.Request) {
	p, ok := m.admin(r)
	if !ok {
		fail(w, 403, "administrator required")
		return
	}
	id, version, ok := routeQuote(r)
	if !ok {
		fail(w, 400, "invalid quote route")
		return
	}
	var out managedLink
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := m.gate(r.Context(), tx, p.TenantID, fence.PermStepsApply); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT 1 FROM business_quotes WHERE quote_node_id=$1::uuid FOR UPDATE`, id).Scan(new(int)); err != nil {
			return err
		}
		var linkID string
		if err := tx.QueryRow(r.Context(), `SELECT id::text FROM quote_public_links WHERE quote_node_id=$1::uuid AND version=$2 AND revoked_at IS NULL FOR UPDATE`, id, version).Scan(&linkID); err != nil {
			return err
		}
		event, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &id, Type: "quote.public_link_revoked", After: map[string]any{"version": version, "link_id": linkID}})
		if err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `UPDATE quote_public_links SET revoked_at=clock_timestamp(),revoked_by_principal_id=$2::uuid,revoked_event_id=$3 WHERE id=$1::uuid RETURNING id::text,quote_node_id::text,version,target_content_sha256,expires_at,revoked_at`, linkID, p.ID, event.ID).Scan(&out.ID, &out.QuoteNodeID, &out.Version, &out.TargetContentSHA256, &out.ExpiresAt, &out.RevokedAt); err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `DELETE FROM quote_public_link_tokens WHERE link_id=$1::uuid`, linkID)
		return err
	})
	if err != nil {
		fail(w, 409, "active link not found")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, out)
}

type publicQuote struct {
	Document      json.RawMessage `json:"document"`
	OfferNo       string          `json:"offer_no"`
	Version       int             `json:"version"`
	ContentSHA256 string          `json:"content_sha256"`
	State         string          `json:"state"`
	ExpiresAt     time.Time       `json:"expires_at"`
	Acceptable    bool            `json:"acceptable"`
	ReceiptReady  bool            `json:"receipt_ready"`
	AcceptedAt    *time.Time      `json:"accepted_at,omitempty"`
	quoteID       string
	linkID        string
	tenantID      string
	receiptHash   *string
}

func (m *Module) resolve(ctx context.Context, selector, token string, lock bool, fn func(pgx.Tx, publicQuote) error) error {
	if !selectorPattern.MatchString(selector) || len(token) != 43 {
		return errors.New("capability not found")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return errors.New("capability not found")
	}
	// The capability link, not a signed-in visitor, decides access here; it
	// reads the quote node, which belongs to no project (ADR-003 P2).
	ctx = db.AllProjects(ctx, "public quote link")
	var tenantID string
	err = db.InTenant(ctx, m.pool, zeroTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.public_quote_selector', $1, true)`, selector); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT aeon_resolve_quote_public_tenant($1)::text`, selector).Scan(&tenantID)
	})
	if err != nil || !uuidPattern.MatchString(tenantID) {
		return errors.New("capability not found")
	}
	return db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		permission := fence.PermViewsProvide
		if lock {
			permission = fence.PermStepsApply
		}
		if err := m.gate(ctx, tx, tenantID, permission); err != nil {
			return err
		}
		var out publicQuote
		var validUntil string
		var timeZone string
		var current int
		var linkRevoked *time.Time
		var receiptHash *string
		query := `SELECT l.id::text,l.quote_node_id::text,l.version,v.content_sha256,s.document,s.offer_no,q.state,q.current_version,l.expires_at,l.revoked_at,s.valid_until::text,s.validity_time_zone,cr.file_sha256 FROM quote_public_links l JOIN quote_versions v ON v.tenant_id=l.tenant_id AND v.quote_node_id=l.quote_node_id AND v.version=l.version JOIN quote_version_snapshots s ON s.tenant_id=v.tenant_id AND s.quote_node_id=v.quote_node_id AND s.version=v.version JOIN business_quotes q ON q.tenant_id=l.tenant_id AND q.quote_node_id=l.quote_node_id LEFT JOIN quote_confirmation_receipts cr ON cr.tenant_id=l.tenant_id AND cr.quote_node_id=l.quote_node_id AND cr.version=l.version WHERE l.token_sha256=$1`
		if lock {
			query += ` FOR UPDATE OF q`
		}
		err := tx.QueryRow(ctx, query, hash(token)).Scan(&out.linkID, &out.quoteID, &out.Version, &out.ContentSHA256, &out.Document, &out.OfferNo, &out.State, &current, &out.ExpiresAt, &linkRevoked, &validUntil, &timeZone, &receiptHash)
		if err != nil || linkRevoked != nil {
			return errors.New("capability not found")
		}
		out.tenantID = tenantID
		out.ReceiptReady = receiptHash != nil
		out.receiptHash = receiptHash
		var decided bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM quote_decisions WHERE quote_node_id=$1::uuid AND version=$2)`, out.quoteID, out.Version).Scan(&decided); err != nil {
			return err
		}
		localNow := time.Now()
		if zone, err := time.LoadLocation(timeZone); err == nil {
			localNow = localNow.In(zone)
		}
		out.Acceptable = out.State == "issued" && current == out.Version && !decided && time.Now().Before(out.ExpiresAt) && localNow.Format("2006-01-02") <= validUntil
		if decided {
			out.State = "accepted"
		}
		if err := tx.QueryRow(ctx, `SELECT accepted_at FROM quote_public_acceptances WHERE quote_node_id=$1::uuid AND version=$2`, out.quoteID, out.Version).Scan(&out.AcceptedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return fn(tx, out)
	})
}
func (m *Module) read(w http.ResponseWriter, r *http.Request) {
	safeHeaders(w)
	if !m.limitPublic(w, r, "read", 120) {
		return
	}
	var out publicQuote
	err := m.resolve(r.Context(), r.PathValue("publicTenant"), r.PathValue("token"), false, func(_ pgx.Tx, q publicQuote) error { out = q; return nil })
	if err != nil {
		fail(w, 404, "quote link not found")
		return
	}
	write(w, 200, out)
}
func (m *Module) pdf(w http.ResponseWriter, r *http.Request) {
	safeHeaders(w)
	if !m.limitPublic(w, r, "pdf", 20) {
		return
	}
	var out publicQuote
	err := m.resolve(r.Context(), r.PathValue("publicTenant"), r.PathValue("token"), false, func(_ pgx.Tx, q publicQuote) error { out = q; return nil })
	if err != nil {
		fail(w, 404, "quote link not found")
		return
	}
	if out.receiptHash != nil {
		if m.store == nil {
			fail(w, 503, "quote PDF unavailable")
			return
		}
		file, err := m.store.Open(out.tenantID, *out.receiptHash, "original")
		if err != nil {
			fail(w, 503, "quote PDF unavailable")
			return
		}
		defer file.Close()
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `inline; filename="quote-receipt.pdf"`)
		_, _ = io.Copy(w, file)
		return
	}
	publicURL := ""
	if m.publicBaseURL != "" {
		publicURL = m.publicBaseURL + "/offers/" + r.PathValue("publicTenant") + "/" + r.PathValue("token")
	}
	store := attachments.Store{}
	if m.store != nil {
		store = *m.store
	}
	profileAssets, err := quotepdf.LoadProfileAssets(r.Context(), m.pool, store, out.tenantID, out.Document)
	if err != nil {
		fail(w, 503, "quote PDF unavailable")
		return
	}
	bytes, err := quotepdf.Render(r.Context(), m.assets, quotepdf.Payload{Document: out.Document, OfferNo: out.OfferNo, PublicURL: publicURL, ProfileAssets: profileAssets})
	if err != nil {
		fail(w, 503, "quote PDF unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="quote.pdf"`)
	_, _ = w.Write(bytes)
}

type acceptanceWrite struct {
	Version               int    `json:"version"`
	ExpectedContentSHA256 string `json:"expected_content_sha256"`
	ClientMutationID      string `json:"client_mutation_id"`
	Name                  string `json:"name"`
	Company               string `json:"company"`
	Note                  string `json:"note"`
	Confirm               bool   `json:"confirm"`
}
type acceptanceResult struct {
	Version           int       `json:"version"`
	ContentSHA256     string    `json:"content_sha256"`
	AcceptedAt        time.Time `json:"accepted_at"`
	ConfirmationState string    `json:"confirmation_state"`
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
func evidence(in acceptanceWrite, linkID string) string {
	value, _ := json.Marshal([]any{linkID, in.Version, in.ExpectedContentSHA256, in.Name, in.Company, in.Note, in.Confirm})
	return hash(string(value))
}
func serviceActor(ctx context.Context, tx pgx.Tx, tenantID string) (tenant.Principal, error) {
	var p tenant.Principal
	p.TenantID = tenantID
	p.Kind = tenant.Agent
	p.Roles = []string{"quote_public_service"}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "quote-public-service:"+tenantID); err != nil {
		return p, err
	}
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE kind='agent' AND 'quote_public_service'=ANY(roles) ORDER BY created_at,id LIMIT 1`).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','Quote public service',ARRAY['quote_public_service']) RETURNING id::text`, tenantID).Scan(&p.ID)
		if err == nil {
			_, err = events.Append(ctx, tx, p, events.Change{Type: "principal.created", After: map[string]any{"id": p.ID, "kind": "agent", "role": "quote_public_service"}})
		}
	}
	return p, err
}
func (m *Module) accept(w http.ResponseWriter, r *http.Request) {
	safeHeaders(w)
	if !m.limitPublic(w, r, "accept", 10) {
		return
	}
	if !sameSite(r) {
		fail(w, 403, "cross-site acceptance denied")
		return
	}
	var in acceptanceWrite
	if err := decode(r, &in); err != nil || !in.Confirm || in.Version < 1 || !digestPattern.MatchString(in.ExpectedContentSHA256) || !uuidPattern.MatchString(in.ClientMutationID) {
		fail(w, 400, "invalid acceptance")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Company = strings.TrimSpace(in.Company)
	in.Note = strings.TrimSpace(in.Note)
	if len(in.Name) < 1 || len(in.Name) > 500 || len(in.Company) > 500 || len(in.Note) > 4000 {
		fail(w, 400, "invalid acceptance identity")
		return
	}
	remote, _, _ := net.SplitHostPort(r.RemoteAddr)
	if remote == "" {
		remote = r.RemoteAddr
	}
	var out acceptanceResult
	replayed := false
	err := m.resolve(r.Context(), r.PathValue("publicTenant"), r.PathValue("token"), true, func(tx pgx.Tx, q publicQuote) error {
		if q.Version != in.Version || q.ContentSHA256 != in.ExpectedContentSHA256 {
			return errors.New("stale acceptance")
		}
		proof := evidence(in, q.linkID)
		var oldHash string
		err := tx.QueryRow(r.Context(), `SELECT evidence_sha256,accepted_at FROM quote_public_acceptances WHERE public_link_id=$1::uuid AND client_mutation_id=$2::uuid`, q.linkID, in.ClientMutationID).Scan(&oldHash, &out.AcceptedAt)
		if err == nil {
			if oldHash != proof {
				return errors.New("mutation replay differs")
			}
			replayed = true
			out.Version = q.Version
			out.ContentSHA256 = q.ContentSHA256
			out.ConfirmationState = "pending"
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !q.Acceptable {
			return errors.New("quote cannot be accepted")
		}
		p, err := serviceActor(r.Context(), tx, q.tenantID)
		if err != nil {
			return err
		}
		event, err := events.Append(r.Context(), tx, p, events.Change{NodeID: &q.quoteID, Type: "quote.accepted_public", After: map[string]any{"version": q.Version, "content_sha256": q.ContentSHA256, "channel": "public"}})
		if err != nil {
			return err
		}
		audit := map[string]any{"remote_address": remote, "user_agent": r.UserAgent()}
		if len(r.UserAgent()) > 256 {
			audit["user_agent"] = r.UserAgent()[:256]
		}
		auditJSON, err := json.Marshal(audit)
		if err != nil {
			return err
		}
		err = tx.QueryRow(r.Context(), `INSERT INTO quote_public_acceptances(tenant_id,quote_node_id,version,public_link_id,accepted_content_sha256,accepted_name,accepted_company,accepted_note,evidence_sha256,restricted_audit,event_id,client_mutation_id) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8,$9,$10::jsonb,$11,$12::uuid) RETURNING accepted_at`, q.tenantID, q.quoteID, q.Version, q.linkID, q.ContentSHA256, in.Name, in.Company, in.Note, proof, string(auditJSON), event.ID, in.ClientMutationID).Scan(&out.AcceptedAt)
		if err != nil {
			return err
		}
		_, err = tx.Exec(r.Context(), `UPDATE business_quotes SET state='accepted',revision=revision+1 WHERE quote_node_id=$1::uuid`, q.quoteID)
		out.Version = q.Version
		out.ContentSHA256 = q.ContentSHA256
		out.ConfirmationState = "pending"
		return err
	})
	if err != nil {
		fail(w, 409, "quote cannot be accepted")
		return
	}
	status := 201
	if replayed {
		status = 200
	}
	write(w, status, out)
}
