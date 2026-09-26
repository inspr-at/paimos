// SPDX-License-Identifier: AGPL-3.0-only

// Package auth is the OIDC session and agent-key boundary for PAIMOS AEON.
package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/auth, /api/me and /api/agent-keys, and resolves the caller.
type Module struct {
	cfg      Config
	pool     *pgxpool.Pool
	inTenant func(context.Context, *pgxpool.Pool, string, func(pgx.Tx) error) error

	mu       sync.Mutex
	provider *oidc.Provider
	oauth    oauth2.Config
}

type credKind int

const (
	credNone credKind = iota
	credSession
	credAgent
	credStale
)

// New validates cfg and binds the module to pool. Tenant-scoped queries go
// through db.InTenant. The result implements httpapi.Module; register Middleware
// alongside it (or use Attach). Auth is a core boundary, not a plugin.
func New(cfg Config, pool *pgxpool.Pool) (*Module, error) {
	if len(cfg.SessionKey) < minSessionKey {
		return nil, errShortKey
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.BootstrapTenantSlug == "" {
		cfg.BootstrapTenantSlug = defaultTenantSlug
	}
	return &Module{cfg: cfg, pool: pool, inTenant: db.InTenant}, nil
}

// Attach registers the module and its middleware on srv. The server calls Mount.
func Attach(srv *httpapi.Server, cfg Config) (*Module, error) {
	m, err := New(cfg, srv.Pool)
	if err != nil {
		return nil, err
	}
	srv.Modules = append(srv.Modules, m)
	srv.Middleware = append(srv.Middleware, m.Middleware)
	return m, nil
}

// Mount adds the auth and agent-key routes. POST /api/auth/dev-login is
// registered only when AEON_ENV=dev.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/login", m.handleLogin)
	mux.HandleFunc("GET /api/auth/callback", m.handleCallback)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	mux.HandleFunc("GET /api/me", m.handleMe)
	mux.HandleFunc("POST /api/agent-keys", m.handleCreateAgentKey)
	mux.HandleFunc("GET /api/agent-keys", m.handleListAgentKeys)
	mux.HandleFunc("DELETE /api/agent-keys/{id}", m.handleRevokeAgentKey)
	if m.cfg.Dev() {
		mux.HandleFunc("POST /api/auth/dev-login", m.handleDevLogin)
	}
}

// Middleware resolves a session cookie or an agent bearer token onto the
// request context. Unauthenticated /api requests, other than health, version
// and /api/auth/* and /api/public/quotes/*, get 401 JSON.
func (m *Module) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, kind, err := m.authenticate(r)
		if err != nil && !isPublicAPI(r.URL.Path) {
			writeInternal(w)
			return
		}
		if err != nil {
			kind = credNone
		}
		switch kind {
		case credStale:
			if r.URL.Path != "/api/auth/logout" {
				m.clearSessionCookie(w)
			}
		case credSession:
			if r.URL.Path != "/api/auth/logout" {
				if c, cErr := r.Cookie(sessionCookieName); cErr == nil {
					m.setSessionCookie(w, c.Value)
				}
			}
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		case credAgent:
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		if kind != credSession && kind != credAgent && isProtectedAPI(r.URL.Path) {
			if r.URL.Path == "/api/me" {
				m.writeMeUnauthorized(w)
			} else {
				writeUnauthorized(w)
			}
			return
		}
		if kind == credAgent {
			if scope, controlled := coreAgentScope(r); !controlled || scope == "" || r.URL.Path == "/api/me" && !agentHasScope(p.Scopes, scope) {
				if receiptRoute(r) {
					writeReceiptNotFound(w)
				} else {
					httpapi.WriteError(w, http.StatusForbidden, "agent key scope required")
				}
				return
			}
		}
		if (kind == credSession || kind == credAgent) && isProtectedAPI(r.URL.Path) {
			// SEC4's route table remains the outer agent allowlist. The binding
			// and the exact route permission are checked inside the tenant.
			if kind != credAgent || r.Method != http.MethodGet || r.URL.Path != "/api/me" {
				ctx := authz.BindPool(r.Context(), m.pool)
				scope := projectScope(r)
				permissionErr := authz.RequirePattern(ctx, r.Pattern, scope)
				// The workspace binding decides first; it is the whole answer for
				// every workspace member. A caller without it may still act through
				// a project binding, in the project the route targets (ADR-003 P2).
				if errors.Is(permissionErr, authz.ErrForbidden) && scope.ProjectID == "" {
					resolved, ok, err := authz.ResolveRouteScope(ctx, m.pool, r.Pattern, r.URL.Path)
					if err != nil {
						permissionErr = err
					} else if ok {
						scope = resolved
						permissionErr = authz.RequirePattern(ctx, r.Pattern, scope)
					}
				}
				ctx = authz.WithRouteScope(ctx, scope)
				if permissionErr != nil && r.Pattern == "GET /api/people/{principalId}/avatar/{size}" && p.Kind == tenant.Person {
					parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
					if len(parts) == 5 && parts[2] == p.ID {
						permissionErr = authz.Require(ctx, "profile.portal_read", authz.Scope{})
					}
				}
				if err := permissionErr; err != nil {
					if errors.Is(err, authz.ErrForbidden) {
						if receiptRoute(r) {
							writeReceiptNotFound(w)
						} else {
							httpapi.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "permission denied", "code": "forbidden", "reason": "This action needs a permission you do not hold"})
						}
					} else {
						writeInternal(w)
					}
					return
				}
				r = r.WithContext(ctx)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func receiptRoute(r *http.Request) bool {
	return r.Pattern == "GET /api/inbox/messages/{messageId}/receipt"
}

func writeReceiptNotFound(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	httpapi.WriteJSON(w, http.StatusNotFound, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{"not_found", "not found"})
}

func projectScope(r *http.Request) authz.Scope {
	if !strings.Contains(r.Pattern, "{projectId}") {
		return authz.Scope{}
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "api" && parts[1] == "projects" && validRouteUUID(parts[2]) {
		return authz.Scope{ProjectID: parts[2]}
	}
	return authz.Scope{}
}

func validRouteUUID(s string) bool {
	var id pgtype.UUID
	return len(s) == 36 && id.Scan(s) == nil && id.Valid
}

// Customer sessions may access only their own profile and quote handlers that
// independently verify the contact binding and frozen recipient digest.
func customerRouteAllowed(r *http.Request, p tenant.Principal) bool {
	if isPublicAPI(r.URL.Path) || r.URL.Path == "/api/me" && r.Method == http.MethodGet {
		return true
	}
	if r.URL.Path == "/api/me/greeting" && r.Method == http.MethodGet || r.URL.Path == "/api/me/profile" && (r.Method == http.MethodGet || r.Method == http.MethodPatch) || r.URL.Path == "/api/me/avatar" && (r.Method == http.MethodPost || r.Method == http.MethodDelete) {
		return true
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) == 5 && parts[0] == "api" && parts[1] == "people" && parts[2] == p.ID && parts[3] == "avatar" && r.Method == http.MethodGet {
		return true
	}
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "quotes" || !validRouteUUID(parts[2]) {
		return false
	}
	if len(parts) == 3 {
		return r.Method == http.MethodGet
	}
	if len(parts) < 5 || parts[3] != "versions" {
		return false
	}
	version, err := strconv.Atoi(parts[4])
	if err != nil || version < 1 {
		return false
	}
	if len(parts) == 5 {
		return r.Method == http.MethodGet
	}
	return len(parts) == 6 && (parts[5] == "accept" && r.Method == http.MethodPost || parts[5] == "export" && r.Method == http.MethodGet)
}

// The key is an outer ceiling for every agent route. Modules retain their own
// resource, grant and actor checks. Unlisted paths have no agent authority.
func coreAgentScope(r *http.Request) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/"), "/")
	if !strings.HasPrefix(r.URL.Path, "/api/") || len(parts) == 0 {
		return "", false
	}
	read := r.Method == http.MethodGet || r.Method == http.MethodHead
	scope := func(resource string) (string, bool) {
		if read {
			return resource + ".read", true
		}
		return resource + ".write", true
	}
	switch parts[0] {
	case "projects":
		if len(parts) == 1 && read {
			return "nodes.read", true
		}
		if len(parts) < 3 {
			break
		}
		switch parts[2] {
		case "messages", "message-targets", "message-deliveries":
			if read {
				return "inbox.read", true
			}
			return "inbox.send", true
		case "intake":
			return scope("intake")
		case "journey", "requirements", "releases":
			if read {
				return "journey.read", true
			}
		case "harness-sessions":
			return harnessScope(parts[3:], read), true
		case "baseline-batches":
			return "stage.<op>", true
		}
	case "nodes":
		if len(parts) > 2 && parts[2] == "time-totals" && read {
			return "hours.read", true
		}
		if len(parts) > 2 && parts[2] == "activity" && read {
			return "nodes.read", true
		}
		if len(parts) > 2 && parts[2] == "comments" {
			return scope("nodes")
		}
		if len(parts) > 2 && parts[2] == "attachments" {
			return scope("nodes")
		}
		return scope("nodes")
	case "node-keys":
		if read {
			return "nodes.read", true
		}
	case "tags":
		if read {
			return "nodes.read", true
		}
		return "nodes.configure", true
	case "tickets":
		if read && len(parts) == 2 && parts[1] == "graph" {
			return "nodes.read", true
		}
	case "attachments":
		return scope("nodes")
	case "kinds":
		if !read {
			return "nodes.configure", true
		}
		return "nodes.read", true
	case "relations":
		return scope("relations")
	case "events":
		if read {
			return "events.read", true
		}
		return "events.undo", true
	case "search":
		if read {
			return "search.read", true
		}
	case "views", "preferences", "project-groups":
		return scope("views")
	case "knowledge":
		return scope("knowledge")
	case "approvals":
		if len(parts) == 1 {
			if read {
				return "approvals.read", true
			}
			if r.Method == http.MethodPost {
				return "approvals.request", true
			}
		}
	case "inbox":
		// Hand-off proof is narrower than reading the recipient inbox.
		if read && len(parts) >= 4 && parts[1] == "messages" && parts[3] == "receipt" {
			return "inbox.receipt", true
		}
		if read {
			return "inbox.read", true
		}
		return "inbox.send", true
	case "models":
		if read {
			return "models.read", true
		}
	case "plugins":
		if read {
			return "plugins.read", true
		}
	case "work-orders":
		if len(parts) > 2 && parts[2] == "runs" {
			if read {
				return "run.read", true
			}
			return "run.create", true
		}
		return scope("work_orders")
	case "runs":
		if read {
			return "run.read", true
		}
		if len(parts) > 2 {
			switch parts[2] {
			case "claim":
				return "run.claim", true
			case "telemetry":
				return "run.telemetry", true
			}
		}
	case "harness-sessions":
		return harnessScope(parts[1:], read), true
	case "agent-accounts":
		return "account.manage", true
	case "stage-handoffs":
		return "stage.<op>", true
	case "me":
		// Any key may read its own identity; the rest of /api/me is for people.
		if len(parts) == 1 && read {
			return selfScope, true
		}
	case "time-entries", "time-periods":
		return scope("hours")
	}
	return "", false
}

func harnessScope(parts []string, read bool) string {
	if read {
		return "harness.read"
	}
	for _, part := range parts {
		if part == "controls" {
			return "harness.control"
		}
	}
	if len(parts) > 0 {
		switch parts[len(parts)-1] {
		case "heartbeat", "yield", "drain", "complete-delivery", "complete", "stop":
			return "harness.worker"
		}
	}
	return "harness.write"
}

// selfScope marks routes that only reveal the calling agent to itself.
const selfScope = "self"

func agentHasScope(have []string, want string) bool {
	if want == selfScope {
		return true
	}
	if want == "stage.<op>" {
		for _, op := range []string{"prepare", "deploy", "verify", "apply"} {
			if hasScope(have, "stage."+op) {
				return true
			}
		}
		return false
	}
	return hasScope(have, want)
}

func isPublicAPI(path string) bool {
	switch path {
	case "/api/health", "/api/version":
		return true
	default:
		return strings.HasPrefix(path, "/api/auth/") || strings.HasPrefix(path, "/api/public/quotes/")
	}
}

func isProtectedAPI(path string) bool {
	return strings.HasPrefix(path, "/api/") && !isPublicAPI(path)
}

func (m *Module) authenticate(r *http.Request) (tenant.Principal, credKind, error) {
	if h := strings.TrimSpace(r.Header.Get("Authorization")); h != "" {
		prefix, secret, ok := parseBearer(h)
		if !ok {
			return tenant.Principal{}, credNone, nil
		}
		p, ok, err := m.authenticateAgent(r.Context(), prefix, secret)
		if err != nil {
			return tenant.Principal{}, credNone, err
		}
		if !ok {
			return tenant.Principal{}, credNone, nil
		}
		return p, credAgent, nil
	}
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return tenant.Principal{}, credNone, nil
	}
	raw, err := decodeSessionToken(c.Value)
	if err != nil {
		return tenant.Principal{}, credStale, nil
	}
	p, ok, err := m.authenticateSession(r.Context(), raw)
	if err != nil {
		return tenant.Principal{}, credNone, err
	}
	if !ok {
		return tenant.Principal{}, credStale, nil
	}
	return p, credSession, nil
}

func parseBearer(h string) (prefix, secret string, ok bool) {
	scheme, token, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	token = strings.TrimSpace(token)
	rest, ok := strings.CutPrefix(token, "aeon_")
	if !ok {
		return "", "", false
	}
	prefix, secret, ok = strings.Cut(rest, "_")
	if !ok || prefix == "" || secret == "" {
		return "", "", false
	}
	return prefix, secret, true
}

func (m *Module) oidcProvider(ctx context.Context) (*oidc.Provider, oauth2.Config, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.provider != nil {
		return m.provider, m.oauth, nil
	}
	if m.cfg.OIDCIssuer == "" || m.cfg.OIDCClientID == "" || m.cfg.PublicURL == "" {
		return nil, oauth2.Config{}, errOIDCNotConfigured
	}
	p, err := oidc.NewProvider(ctx, m.cfg.OIDCIssuer)
	if err != nil {
		return nil, oauth2.Config{}, err
	}
	ep := p.Endpoint()
	// Public client: client_id in the body, no client secret.
	ep.AuthStyle = oauth2.AuthStyleInParams
	oc := oauth2.Config{
		ClientID:    m.cfg.OIDCClientID,
		RedirectURL: m.cfg.PublicURL + "/api/auth/callback",
		Endpoint:    ep,
		Scopes:      []string{oidc.ScopeOpenID, "profile", "email"},
	}
	m.provider = p
	m.oauth = oc
	return p, oc, nil
}

// hasScope matches a required scope against a key's scopes. Scopes are
// canonical in dot notation (harness.read); SEC1 briefly required colon forms
// (nodes:read), so both separators are accepted.
func hasScope(have []string, want string) bool {
	want = strings.ReplaceAll(want, ":", ".")
	for _, s := range have {
		if strings.ReplaceAll(s, ":", ".") == want {
			return true
		}
	}
	return false
}
