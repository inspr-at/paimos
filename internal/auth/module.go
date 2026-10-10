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
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reportercontract"
	"github.com/inspr-at/paimos/internal/stepup/server"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Module serves /api/auth, /api/me and /api/agent-keys, and resolves the caller.
type Module struct {
	StepUp   *stepup.Module   // native requests, using the existing OIDC callback
	trimNow  func() time.Time // injected only by deterministic key-trim tests
	cfg      Config
	pool     *pgxpool.Pool
	inTenant func(context.Context, *pgxpool.Pool, string, func(pgx.Tx) error) error
	// Delegated is an optional exact-route authentication handoff. It must
	// verify and authorize its credential before serving next, returning true
	// only when it has handled the request. Ordinary sessions/keys keep their
	// existing path; no protected route becomes public.
	Delegated func(http.ResponseWriter, *http.Request, http.Handler) bool

	mu        sync.Mutex
	provider  *oidc.Provider
	oauth     oauth2.Config
	discovery *oidcDiscovery
}

type oidcDiscovery struct {
	done     chan struct{}
	provider *oidc.Provider
	oauth    oauth2.Config
	err      error
}

const oidcDiscoveryTimeout = 5 * time.Second

// Discovery and JWKS are small documents. Enforce the byte bound before the
// OIDC library's ReadAll, including for chunked responses with no length.
type oidcBoundedTransport struct{ base http.RoundTripper }

func (t oidcBoundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err == nil && response.Body != nil {
		response.Body = http.MaxBytesReader(nil, response.Body, 1<<20)
	}
	return response, err
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
// always registered so the matched pattern stays the public declaration.
// The handler returns 404 unless AEON_ENV=dev, and it never mints a session then.
func (m *Module) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/login", m.handleLogin)
	mux.HandleFunc("GET /api/auth/callback", m.handleCallback)
	mux.HandleFunc("POST /api/auth/logout", m.handleLogout)
	mux.HandleFunc("POST /api/auth/dev-login", m.handleDevLogin)
	mux.HandleFunc("GET /api/me", reportercontract.WithHeader(reportercontract.Me, m.handleMe))
	mux.HandleFunc("POST /api/agent-keys", m.handleCreateAgentKey)
	mux.HandleFunc("GET /api/agent-keys", m.handleListAgentKeys)
	mux.HandleFunc("POST /api/agent-keys/{id}/adopt", m.handleAdoptAgentKey)
	mux.HandleFunc("DELETE /api/agent-keys/{id}", m.handleRevokeAgentKey)
	mux.HandleFunc("GET /api/agent-keys/{id}/scopes", m.handleAgentKeyScopes)
	mux.HandleFunc("PATCH /api/agent-keys/{id}/scopes", m.handleAgentKeyScopes)
	mux.HandleFunc("PUT /api/agent-keys/{id}/owner-workstation", m.handleOwnerWorkstation)
	mux.HandleFunc("GET /api/agentd/step-ups/{challenge_id}", m.handleWorkstationChallenge)
	mux.HandleFunc("POST /api/agent-keys/{id}/trim-proposals", m.handleProposeKeyTrim)
	mux.HandleFunc("GET /api/key-trim-proposals", m.handleListKeyTrims)
	mux.HandleFunc("POST /api/key-trim-proposals/{proposalId}/decision", m.handleDecideKeyTrim)
	mux.HandleFunc("POST /api/key-trim-proposals/{proposalId}/restore", m.handleDecideKeyTrim)
}

// Middleware resolves a session cookie or an agent bearer token onto the
// request context. Unauthenticated /api requests get 401 JSON unless the
// matched pattern is a public declaration (authz.PatternIsPublic).
func (m *Module) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Delegated != nil && m.Delegated(w, r, next) {
			return
		}
		p, kind, err := m.authenticate(r)
		if err != nil && !publicRequest(r) {
			writeInternal(w)
			return
		}
		if err != nil {
			kind = credNone
		}
		switch kind {
		case credStale:
			// Public responses can be cached. A Set-Cookie there would store
			// the session clear next to the page. Auth handlers clear their own.
			if !publicRequest(r) {
				m.clearSessionCookie(w)
			}
		case credSession:
			p.BrowserSession = true
			if !publicRequest(r) {
				if c, cErr := r.Cookie(sessionCookieName); cErr == nil {
					m.setSessionCookie(w, c.Value)
				}
			}
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		case credAgent:
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		if kind != credSession && kind != credAgent && protectedRequest(r) {
			if r.URL.Path == "/api/me" {
				m.writeMeUnauthorized(w)
			} else {
				writeUnauthorized(w)
			}
			return
		}
		if kind == credAgent {
			if err := m.pairingBoundary(r, p); err != nil {
				if authz.OwnerWorkstation(p) && workstationGovernance(r) {
					if err := m.auditWorkstation(r, p, false, "pairing_denied"); err != nil {
						writeInternal(w)
						return
					}
				}
				if errors.Is(err, authz.ErrForbidden) {
					writeForbidden(w)
					return
				}
				agentpairing.WriteError(w, err)
				return
			}
			if r.Pattern == "GET /api/agents/plan" {
				if !hasScope(p.Scopes, "agents.plan.read") {
					httpapi.WriteError(w, http.StatusForbidden, "agents.plan.read scope missing")
					return
				}
				if p.KeyCreatorID == "" {
					httpapi.WriteError(w, http.StatusForbidden, "key has no person owner — adopt it in Settings › Keys")
					return
				}
			}
			scope, controlled := coreAgentScope(r)
			if authz.OwnerWorkstation(p) && workstationGovernance(r) && !strings.HasSuffix(r.Pattern, "/owner-workstation") {
				scope, controlled = authz.RoutePermissions[r.Pattern], true
			}
			if !controlled || scope == "" {
				if authz.OwnerWorkstation(p) && workstationGovernance(r) {
					if err := m.auditWorkstation(r, p, false, "route_denied"); err != nil {
						writeInternal(w)
						return
					}
				}
				if receiptRoute(r) {
					writeReceiptNotFound(w)
				} else {
					httpapi.WriteError(w, http.StatusForbidden, "agent key scope required")
				}
				return
			}
		}
		if (kind == credSession || kind == credAgent) && protectedRequest(r) {
			// SEC4's route table remains the outer agent allowlist. The binding
			// and the exact route permission are checked inside the tenant.
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
					// A failed project retry must keep the original denial:
					// its diagnostic must not reveal that the target exists.
					if resolvedErr := authz.RequirePattern(ctx, r.Pattern, scope); !errors.Is(resolvedErr, authz.ErrForbidden) {
						permissionErr = resolvedErr
					}
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
				if authz.OwnerWorkstation(p) && workstationGovernance(r) {
					if err := m.auditWorkstation(r, p, false, "permission_denied"); err != nil {
						writeInternal(w)
						return
					}
				}
				if errors.Is(err, authz.ErrForbidden) {
					if receiptRoute(r) {
						writeReceiptNotFound(w)
					} else {
						authz.WriteForbidden(w, err)
					}
				} else {
					writeInternal(w)
				}
				return
			}
			r = r.WithContext(ctx)
		}
		if authz.OwnerWorkstation(p) {
			m.serveWorkstation(w, r, p, next)
			return
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
	if isPublicAPI(r.URL.Path) || publicPortalRequest(r) || r.URL.Path == "/api/me" && r.Method == http.MethodGet {
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
	case "stepup-requests":
		if len(parts) == 1 && (read || r.Method == http.MethodPost) || len(parts) == 2 && validRouteUUID(parts[1]) && read || len(parts) == 3 && validRouteUUID(parts[1]) && parts[2] == "withdraw" && r.Method == http.MethodPost {
			return "approvals.request", true
		}
	case "status":
		// Definitions and live limits are read-only tenant metadata. Project
		// overrides remain confined by the handler's project visibility.
		if len(parts) == 2 && parts[1] == "help" && read {
			return authz.AuthenticatedRoute, true
		}
	case "recurrences":
		// This is an explicit agent allowlist. Every recurrence handler also
		// checks the custom role and recurrences.manage key scope in its tenant.
		if len(parts) == 1 && (read || r.Method == http.MethodPost) {
			return "recurrences.manage", true
		}
		if len(parts) == 2 && parts[1] == "preview" && r.Method == http.MethodPost {
			return "recurrences.manage", true
		}
		if len(parts) == 2 && parts[1] != "" && parts[1] != "preview" && (read || r.Method == http.MethodPut || r.Method == http.MethodDelete) {
			return "recurrences.manage", true
		}
		if len(parts) == 3 && parts[1] != "" {
			if read && (parts[2] == "preview" || parts[2] == "history" || parts[2] == "releases" || parts[2] == "guardrails") || r.Method == http.MethodPost && (parts[2] == "pause" || parts[2] == "resume" || parts[2] == "run-now") {
				return "recurrences.manage", true
			}
		}
	case "agent-keys":
		if len(parts) == 3 && parts[2] == "trim-proposals" && validRouteUUID(parts[1]) && r.Method == http.MethodPost {
			return "approvals.request", true
		}
	case "agents":
		if len(parts) == 2 && parts[1] == "plan" && read {
			return "agents.plan.read", true
		}
	case "decision-desk":
		if len(parts) == 1 && read {
			return "questions.read", true
		}
	case "questions":
		if read && (len(parts) == 2 || len(parts) == 3 && parts[2] == "status") {
			return "questions.read", true
		}
	case "releases":
		// Build history is readable by agents. Presentation writes remain person-only.
		if read && (len(parts) == 1 || len(parts) == 2 && parts[1] != "") {
			return "releases.read", true
		}
	case "rules":
		// Dedicated rules routes are an explicit agent allowlist. Publishing and
		// restoring remain person-only regardless of any key's supplied scopes.
		if len(parts) == 2 && (parts[1] == "layers" || parts[1] == "sets") && (read || r.Method == http.MethodPost) {
			return scope("rules")
		}
		if len(parts) == 2 && (parts[1] == "merged" || parts[1] == "doctrine" || parts[1] == "channels") && read {
			return "rules.read", true
		}
		if len(parts) == 2 && parts[1] == "comparisons" && (read || r.Method == http.MethodPost) {
			return scope("rules")
		}
		if len(parts) >= 3 && parts[1] == "sets" {
			if len(parts) == 3 && read {
				return "rules.read", true
			}
			if len(parts) == 4 && parts[3] == "draft" && r.Method == http.MethodPut {
				return "rules.write", true
			}
			if (len(parts) == 4 || len(parts) == 5) && parts[3] == "versions" && read {
				return "rules.read", true
			}
		}
	case "model-prices":
		if len(parts) == 1 && read {
			return "harness.read", true
		}
	case "projects":
		if len(parts) == 1 && read {
			return "nodes.read", true
		}
		if len(parts) < 3 {
			break
		}
		switch parts[2] {
		case "lead-settings":
			if len(parts) == 3 && validRouteUUID(parts[1]) && read {
				return "nodes.read", true
			}
		case "lead":
			if len(parts) == 4 && parts[3] == "usage" && read {
				return "harness.read", true
			}
			// Starting a lead remains person-only, regardless of key scopes.
			if len(parts) == 3 && read {
				return "harness.read", true
			}
			if len(parts) == 4 && r.Method == http.MethodPost && (parts[3] == "claim" || parts[3] == "pause" || parts[3] == "yield") {
				return "harness.worker", true
			}
		case "questions":
			if len(parts) == 3 && read {
				return "questions.read", true
			}
			if len(parts) == 3 && r.Method == http.MethodPost {
				return "questions.ask", true
			}
		case "instruction-provenance":
			if len(parts) == 3 && read {
				return "harness.read", true
			}
		case "release-memberships":
			if len(parts) == 3 && read {
				return "releases.read", true
			}
		case "review-policy":
			// Explicit agent allowlist. Built-in roles still exclude
			// reviewpolicy.manage; a custom role and key scope may grant it.
			if len(parts) == 3 && validRouteUUID(parts[1]) {
				if read {
					return "reviewpolicy.read", true
				}
				if r.Method == http.MethodPut || r.Method == http.MethodDelete {
					return "reviewpolicy.manage", true
				}
			}
		case "messages", "message-targets", "message-deliveries":
			if read {
				return "inbox.read", true
			}
			return "inbox.send", true
		case "intake":
			return scope("intake")
		case "releases":
			if len(parts) == 5 && parts[4] == "note-snapshot" {
				if read {
					return "releases.read", true
				}
				break
			}
			if len(parts) == 5 && parts[4] == "candidate-artifact" {
				if read {
					return "stage_handoffs.read", true
				}
				if r.Method == http.MethodPut {
					return "stage_handoffs.write", true
				}
				break
			}
			if read {
				return "journey.read", true
			}
		case "journey", "requirements":
			if read {
				return "journey.read", true
			}
		case "harness-sessions":
			return harnessScope(parts[3:], read), true
		case "baseline-batches":
			return "stage.<op>", true
		}
	case "nodes":
		// A person marks a delivery for rework. An agent key must not inherit nodes.read here.
		if len(parts) == 3 && parts[2] == "delivery-ratings" {
			return "", false
		}
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
	case "usage":
		if read && len(parts) == 2 && parts[1] == "dashboard" {
			return "harness.read", true
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
		if len(parts) >= 2 && parts[1] == "subscribe" {
			if len(parts) == 2 && read {
				return "events.subscribe", true
			}
			return "", false
		}
		if read {
			return "events.read", true
		}
		return "events.undo", true
	case "search":
		if read {
			return "search.read", true
		}
	case "settings":
		if len(parts) == 2 && parts[1] == "review-policy" {
			if read {
				return "reviewpolicy.read", true
			}
			if r.Method == http.MethodPut {
				return "reviewpolicy.manage", true
			}
		}
	case "views", "preferences", "project-groups":
		return scope("views")
	case "knowledge":
		// Listing candidates is ordinary knowledge read. Accept and dismiss stay
		// with a person: an agent key has no authority on those two routes, and
		// the handler refuses every agent again. A recommendation (PUT
		// .../recommendation) is ordinary knowledge.write: it decides nothing.
		if r.Method == http.MethodPost && len(parts) == 4 && parts[1] == "learnings" && (parts[3] == "accept" || parts[3] == "dismiss" || parts[3] == "draft") {
			return "", false
		}
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
		if r.Method == http.MethodPost && len(parts) == 2 {
			if parts[1] == "refresh" {
				return "models.refresh", true
			}
			if parts[1] == "reports" {
				return "models.report", true
			}
		}
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
	case "queue":
		if read {
			return "nodes.read", true
		}
		return "work_orders.read", true
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
		if len(parts) >= 3 && parts[2] == "delivery-rating" {
			return "", false
		}
		return harnessScope(parts[1:], read), true
	case "harness-recoveries":
		if r.Method == http.MethodPost && (len(parts) == 2 && parts[1] == "claim" || len(parts) == 3 && validRouteUUID(parts[1]) && parts[2] == "complete") {
			return "harness.worker", true
		}
	case "agentd":
		if r.Pattern == "GET /api/agentd/step-ups/{challenge_id}" {
			return "harness.worker", true
		}
	case "agent-pairing":
		if r.Method == "POST" && r.URL.Path == "/api/agent-pairing/account-link" {
			return "account.probe", true
		}
		if r.Method == "POST" && r.URL.Path == "/api/agent-pairing/attach" {
			return "harness.worker", true
		}
		if r.Method == "GET" && r.URL.Path == "/api/agent-pairing/self" || r.Method == "POST" && (r.URL.Path == "/api/agent-pairing/self/disconnect" || r.URL.Path == "/api/agent-pairing/self/capacity" || r.URL.Path == "/api/agent-pairing/self/ledger") {
			return "run.claim", true
		}
	case "agent-accounts":
		if len(parts) == 3 {
			switch parts[2] {
			case "readings":
				if r.Method == http.MethodGet || r.Method == http.MethodPost {
					return "account.probe", true
				}
			case "signals":
				if r.Method == http.MethodPut {
					return "account.probe", true
				}
			case "quota-key":
				if r.Method == http.MethodPost {
					return "account.probe", true
				}
			case "statusline":
				// People opt in. A paired agent may only read the decision.
				if r.Method == http.MethodGet {
					return "account.probe", true
				}
				return "", false
			}
		}
		if read {
			return "account.read", true
		}
		if r.Method == "POST" && len(parts) == 2 && parts[1] == "route" {
			return "account.route", true
		}
		if r.Method == "POST" && len(parts) == 3 && parts[2] == "probe" {
			return "account.probe", true
		}
		return "account.manage", true
	case "stage-handoffs":
		return "stage.<op>", true
	case "outcomes":
		if len(parts) == 2 && parts[1] == "measurement" && read {
			return "outcome.read", true
		}
		if len(parts) == 1 && (read || r.Method == http.MethodPost) {
			return scope("outcome")
		}
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
	// The session read marker is a person's own watermark. No agent key scope
	// reaches it; an empty scope is denied by the agent ceiling.
	if len(parts) > 0 && parts[len(parts)-1] == "read-marker" {
		return ""
	}
	if len(parts) > 0 && parts[len(parts)-1] == "managed-settings" {
		return "harness.control"
	}
	if read {
		return "harness.read"
	}
	for _, part := range parts {
		if part == "controls" || part == "managed-controls" {
			return "harness.control"
		}
	}
	if len(parts) > 0 {
		switch parts[len(parts)-1] {
		case "model-reports", "heartbeat", "yield", "drain", "complete-delivery", "complete", "stop", "confirm-exit", "rules-receipts", "managed-context":
			return "harness.worker"
		}
	}
	return "harness.write"
}

// selfScope marks routes that only reveal the calling agent to itself.
const selfScope = "self"

func agentHasScope(have []string, want string) bool {
	if want == selfScope || want == authz.AuthenticatedRoute {
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
	return hasScope(have, want) || authz.CoordinatorCeiling(have, want)
}

// publicRequest is true only for a route the router matched to a public
// declaration. Percent-encoding, dot segments and repeated slashes are not
// consulted: when the router accepts them, they carry that same pattern.
func publicRequest(r *http.Request) bool {
	return r != nil && authz.PatternIsPublic(r.Pattern)
}
func protectedRequest(r *http.Request) bool {
	return r != nil && r.URL != nil && strings.HasPrefix(r.URL.Path, "/api/") && !publicRequest(r)
}

// isPublicAPI is the path list for readiness checks and the customer allow
// helper. It is not the session gate; that is publicRequest.
func isPublicAPI(path string) bool {
	switch path {
	case "/api/health", "/api/ready", "/api/version", "/api/aithema/jwks":
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
	if err := ctx.Err(); err != nil {
		return nil, oauth2.Config{}, err
	}
	m.mu.Lock()
	if m.provider != nil {
		defer m.mu.Unlock()
		return m.provider, m.oauth, nil
	}
	if m.cfg.OIDCIssuer == "" || m.cfg.OIDCClientID == "" || m.cfg.PublicURL == "" {
		m.mu.Unlock()
		return nil, oauth2.Config{}, errOIDCNotConfigured
	}
	if pending := m.discovery; pending != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, oauth2.Config{}, ctx.Err()
		case <-pending.done:
			return pending.provider, pending.oauth, pending.err
		}
	}
	pending := &oidcDiscovery{done: make(chan struct{})}
	m.discovery = pending
	m.mu.Unlock()
	// No network operation holds the sign-in mutex. Preserve an injected
	// transport but bound both discovery's context and its HTTP client.
	op, cancel := context.WithTimeout(ctx, oidcDiscoveryTimeout)
	defer cancel()
	httpClient := *http.DefaultClient
	if supplied, ok := ctx.Value(oauth2.HTTPClient).(*http.Client); ok && supplied != nil {
		httpClient = *supplied
	}
	if httpClient.Timeout <= 0 || httpClient.Timeout > oidcDiscoveryTimeout {
		httpClient.Timeout = oidcDiscoveryTimeout
	}
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = oidcBoundedTransport{base: transport}
	p, err := oidc.NewProvider(oidc.ClientContext(op, &httpClient), m.cfg.OIDCIssuer)
	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(pending.done)
	m.discovery = nil // failures remain retryable by the next request
	if err != nil {
		pending.err = err
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
	pending.provider, pending.oauth = p, oc
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
