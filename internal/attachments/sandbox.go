// SPDX-License-Identifier: AGPL-3.0-only
package attachments

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/net/publicsuffix"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const (
	maxHTMLPreviewBytes = 2 << 20
	previewTTL          = time.Minute
	maxPreviewGrants    = 4096
	maxPrincipalGrants  = 32
	// No eval, remote scripts/styles, connections, child frames, workers,
	// forms, popups, downloads or same-origin authority. Self-navigation is
	// NOT blocked by sandbox/CSP; do not advertise this as network isolation.
	sandboxCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; sandbox allow-scripts"
)

type sessionLease func(context.Context) (tenant.Principal, error)

type previewGrant struct {
	principal  tenant.Principal
	lease      sessionLease
	attachment Attachment
	project    string
	expires    time.Time
}

// Sandbox is deliberately process-local: restart/rebalancing invalidates every
// capability. There is no shared/public cache, persisted credential or signer.
// Configure once at startup, and route both hosts to this same instance.
type Sandbox struct {
	origin, appOrigin, appHost, host string
	configured                       bool
	lease                            func(*http.Request) (func(context.Context) (tenant.Principal, error), error)
	mu                               sync.Mutex
	grants                           map[[32]byte]previewGrant
	now                              func() time.Time
}

// NewSandbox fails closed for missing/unsafe origins or a missing session
// verifier. A different registrable domain also rules out Domain cookies and
// same-site session requests. No development exception weakens that boundary.
func NewSandbox(appOrigin, origin string, lease func(*http.Request) (func(context.Context) (tenant.Principal, error), error)) *Sandbox {
	s := &Sandbox{grants: make(map[[32]byte]previewGrant), now: time.Now, lease: lease, configured: origin != ""}
	a, aOK := sandboxOrigin(appOrigin)
	b, bOK := sandboxOrigin(origin)
	if aOK {
		s.appOrigin, s.appHost = a.String(), a.Host
	}
	if !aOK || !bOK || lease == nil {
		return s
	}
	as, aerr := publicsuffix.EffectiveTLDPlusOne(a.Hostname())
	bs, berr := publicsuffix.EffectiveTLDPlusOne(b.Hostname())
	if aerr != nil || berr != nil || as == bs {
		return s
	}
	s.origin, s.host = b.String(), b.Host
	return s
}

func sandboxOrigin(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" ||
		u.Host != u.Hostname() || raw != u.String() || u.Host != strings.ToLower(u.Host) || net.ParseIP(u.Hostname()) != nil {
		return nil, false
	}
	host := u.Hostname()
	if len(host) > 253 || !strings.Contains(host, ".") {
		return nil, false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return nil, false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return nil, false
			}
		}
	}
	// Unknown/private host suffixes are not deployable public HTTPS origins.
	_, icann := publicsuffix.PublicSuffix(host)
	if !icann {
		return nil, false
	}
	return u, true
}

func (s *Sandbox) Origin() string {
	if s == nil {
		return ""
	}
	return s.origin
}

// Wrap runs OUTSIDE all app middleware, including auth, SPA and access logs.
// A sandbox request can never reach an auth endpoint or install a service worker.
func (m *Module) WrapSandbox(app http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := m.Sandbox
		if s != nil && s.origin != "" && r.Host == s.host {
			m.sandboxContent(w, r)
			return
		}
		if s != nil && s.configured && s.appHost != "" && r.Host != s.appHost {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		app.ServeHTTP(w, r)
	})
}

// previewAttachment uses current caller visibility, not a service principal or
// tenant-only lookup. Bind project and timestamp too: moving, editing, deleting
// or restoring an attachment invalidates previously minted capabilities.
func (m *Module) previewAttachment(ctx context.Context, p tenant.Principal, id string) (Attachment, string, error) {
	var a Attachment
	var project string
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.Pool, p.TenantID, func(tx pgx.Tx) error {
		var err error
		a, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM attachments WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, p.TenantID, id))
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,'') FROM nodes n WHERE n.tenant_id=$1 AND n.id=$2 AND n.deleted_at IS NULL
			AND (n.project_id IS NULL OR EXISTS(SELECT 1 FROM nodes project WHERE project.tenant_id=n.tenant_id AND project.id=n.project_id AND project.deleted_at IS NULL))`, p.TenantID, a.NodeID).Scan(&project); err != nil {
			return err
		}
		if err = authz.RequireTx(ctx, tx, p, "attachments.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if !isHTML(a.ContentType) {
			return pgx.ErrNoRows
		}
		return nil
	})
	return a, project, err
}

func (m *Module) preview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	p, ok := m.principal(w, r, "attachments.read")
	if !ok {
		return
	}
	if p.Kind != tenant.Person || r.Header.Get("Authorization") != "" {
		httpapi.WriteError(w, 403, "browser session required")
		return
	}
	id := r.PathValue("id")
	if !uuid(id) {
		httpapi.WriteError(w, 400, "invalid attachment id")
		return
	}
	a, project, err := m.previewAttachment(r.Context(), p, id)
	if err != nil {
		httpapi.WriteError(w, 404, "not found")
		return
	}
	s := m.Sandbox
	if s.Origin() == "" || a.Size > maxHTMLPreviewBytes {
		httpapi.WriteJSON(w, 200, map[string]bool{"available": false})
		return
	}
	// Same-origin POST only: an opaque preview must never mint capabilities.
	if r.Header.Get("Origin") != s.appOrigin || r.Host != s.appHost {
		httpapi.WriteError(w, 403, "same-origin browser request required")
		return
	}
	lease, err := s.lease(r)
	if err != nil || lease == nil {
		httpapi.WriteError(w, 403, "browser session required")
		return
	}
	current, err := lease(r.Context())
	if err != nil || current.ID != p.ID || current.TenantID != p.TenantID || current.Kind != tenant.Person {
		httpapi.WriteError(w, 403, "browser session required")
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		httpapi.WriteError(w, 500, "preview unavailable")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	expires := s.now().Add(previewTTL)
	s.mu.Lock()
	count := 0
	for hash, grant := range s.grants {
		if !s.now().Before(grant.expires) {
			delete(s.grants, hash)
			continue
		}
		if grant.principal.TenantID == p.TenantID && grant.principal.ID == p.ID {
			count++
		}
	}
	if len(s.grants) >= maxPreviewGrants || count >= maxPrincipalGrants {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "60")
		httpapi.WriteError(w, 429, "preview limit reached")
		return
	}
	s.grants[sha256.Sum256([]byte(token))] = previewGrant{principal: p, lease: lease, attachment: a, project: project, expires: expires}
	s.mu.Unlock()
	httpapi.WriteJSON(w, 200, struct {
		Available bool      `json:"available"`
		URL       string    `json:"url"`
		Expires   time.Time `json:"expires_at"`
	}{true, s.origin + "/preview/" + token, expires})
}

func sandboxHeaders(w http.ResponseWriter, appOrigin string) {
	w.Header().Set("Cache-Control", "no-store, private")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", sandboxCSP+"; frame-ancestors "+appOrigin)
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=(), fullscreen=(), display-capture=(), clipboard-read=(), clipboard-write=()")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("X-DNS-Prefetch-Control", "off")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
}

func (m *Module) sandboxContent(w http.ResponseWriter, r *http.Request) {
	s := m.Sandbox
	sandboxHeaders(w, s.appOrigin)
	// Do not log request URLs, capability values, headers, content, or panic
	// values. Ingress must likewise disable access/body logs for this host.
	defer func() {
		if recover() != nil {
			http.Error(w, "preview unavailable", 500)
		}
	}()
	deny := func() { http.Error(w, "preview unavailable", 404) }
	if (r.Method != "GET" && r.Method != "HEAD") || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
		r.Header.Get("Service-Worker") != "" || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.RawPath != "" {
		deny()
		return
	}
	token, ok := strings.CutPrefix(r.URL.Path, "/preview/")
	if !ok || len(token) != 43 {
		deny()
		return
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		deny()
		return
	}
	hash := sha256.Sum256([]byte(token))
	s.mu.Lock()
	grant, ok := s.grants[hash]
	if ok && !s.now().Before(grant.expires) {
		delete(s.grants, hash)
		ok = false
	}
	s.mu.Unlock()
	if !ok {
		deny()
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	p, err := grant.lease(ctx)
	if err != nil || p.ID != grant.principal.ID || p.TenantID != grant.principal.TenantID || p.Kind != tenant.Person {
		deny()
		return
	}
	a, project, err := m.previewAttachment(ctx, p, grant.attachment.ID)
	if err != nil || project != grant.project || a.NodeID != grant.attachment.NodeID || a.SHA256 != grant.attachment.SHA256 || !a.UpdatedAt.Equal(grant.attachment.UpdatedAt) || a.Size > maxHTMLPreviewBytes {
		deny()
		return
	}
	f, err := m.Store.Open(p.TenantID, a.SHA256, "original")
	if err != nil {
		deny()
		return
	}
	defer f.Close()
	body, err := io.ReadAll(io.LimitReader(f, maxHTMLPreviewBytes+1))
	if err != nil || len(body) > maxHTMLPreviewBytes || int64(len(body)) != a.Size || ctx.Err() != nil || !s.now().Before(grant.expires) {
		deny()
		return
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != a.SHA256 {
		deny()
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", fmtInt(int64(len(body))))
	w.Header().Set("Content-Disposition", "inline")
	if r.Method == "GET" {
		_, _ = w.Write(body)
	}
}
