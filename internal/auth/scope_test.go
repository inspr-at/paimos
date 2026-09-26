// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Agent keys reach shared project groups (AEON-136) through the views scope,
// like saved views and preferences.
func TestCoreAgentScopeCoversProjectGroups(t *testing.T) {
	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/project-groups", "views.read"},
		{http.MethodPost, "/api/project-groups", "views.write"},
		{http.MethodPost, "/api/project-groups/assign", "views.write"},
		{http.MethodPatch, "/api/project-groups/7d0d6f36-6f55-4b43-9f42-2a4d7a8a0a11", "views.write"},
		{http.MethodDelete, "/api/project-groups/7d0d6f36-6f55-4b43-9f42-2a4d7a8a0a11", "views.write"},
		{http.MethodGet, "/api/preferences/projects", "views.read"},
	} {
		got, controlled := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if !controlled || got != tc.want {
			t.Fatalf("%s %s = %q (controlled %v), want %q", tc.method, tc.path, got, controlled, tc.want)
		}
	}
}

func TestAgentScopeSeparatesProjectSubpathsAndUnknownRoutes(t *testing.T) {
	project := "/api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/api/projects", "nodes.read"},
		{"GET", project + "/messages/listen", "inbox.read"},
		{"GET", "/api/inbox/messages/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/receipt", "inbox.receipt"},
		{"POST", project + "/messages", "inbox.send"},
		{"GET", project + "/intake", "intake.read"},
		{"POST", project + "/intake/sources", "intake.write"},
		{"GET", project + "/journey", "journey.read"},
		{"GET", project + "/requirements", "journey.read"},
		{"GET", project + "/releases/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/walker", "journey.read"},
		{"GET", "/api/knowledge/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "knowledge.read"},
		{"GET", "/api/tickets/graph", "nodes.read"},
		{"GET", "/api/tickets", ""},
		{"POST", "/api/knowledge", "knowledge.write"},
		{"GET", "/api/models/resolve", "models.read"},
		{"GET", "/api/plugins", "plugins.read"},
		{"POST", project + "/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/heartbeat", "harness.worker"},
		{"GET", "/api/harness-sessions/live", "harness.read"},
		{"GET", "/api/nodes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/time-totals", "hours.read"},
		{"POST", "/api/time-entries", "hours.write"},
		{"POST", "/api/approvals/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/decision", ""},
		{"POST", project + "/requirements", ""},
		{"PUT", "/api/plugins/foo/installation", ""},
		{"GET", "/api/unlisted", ""},
		{"GET", "/api/me", selfScope},
		{"GET", "/api/me/profile", ""},
		{"GET", "/api/tags", "nodes.read"},
		{"PATCH", "/api/tags/bug", "nodes.configure"},
		{"GET", "/api/work-orders/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/runs", "run.read"},
		{"POST", "/api/work-orders/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/runs", "run.create"},
	} {
		got, _ := coreAgentScope(httptest.NewRequest(tc.method, tc.path, nil))
		if got != tc.want {
			t.Errorf("%s %s = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

var registeredRoute = regexp.MustCompile(`^(GET|HEAD|POST|PUT|PATCH|DELETE) /api/`)
var routeValue = regexp.MustCompile(`\{[^}]+\}`)
var policyMux = func() *http.ServeMux {
	mux := http.NewServeMux()
	for pattern := range authz.RoutePermissions {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	return mux
}()

func setPolicyPattern(r *http.Request) {
	_, r.Pattern = policyMux.Handler(r)
}

// Source registration literals include the route arrays mounted by the work
// order, run and harness modules. This catches additions without a scope map.
func registeredAPIRoutes(t *testing.T) []string {
	t.Helper()
	set := map[string]bool{}
	err := filepath.WalkDir("../", func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(literal.Value)
			if err == nil && registeredRoute.MatchString(value) {
				set[value] = true
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	routes := make([]string, 0, len(set))
	for route := range set {
		routes = append(routes, route)
	}
	sort.Strings(routes)
	if len(routes) < 100 {
		t.Fatalf("only %d registered API routes found", len(routes))
	}
	return routes
}

func TestEmptyAgentKeyDeniedAcrossRegisteredAPIRoutes(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "scope-audit", "Scope audit")
	m := newMod(t, Config{})
	key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "empty-audit", "", []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, pattern := range registeredAPIRoutes(t) {
		method, path, _ := strings.Cut(pattern, " ")
		path = routeValue.ReplaceAllString(path, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
		req := httptest.NewRequest(method, path, bytes.NewReader(nil))
		setPolicyPattern(req)
		if scope, _ := coreAgentScope(req); scope == selfScope {
			// Reading its own identity is the one route every key may call.
			continue
		}
		req.Header.Set("Authorization", "Bearer "+key.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		want := http.StatusForbidden
		if pattern == "GET /api/inbox/messages/{messageId}/receipt" {
			want = http.StatusNotFound // Sender-only receipts conceal permission denial.
		}
		if res.Code != want {
			t.Errorf("%s: %d %s", pattern, res.Code, res.Body.String())
		}
	}
}

func TestCoordinatorScopesReachWorkRoutes(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "coordinator-audit", "Coordinator audit")
	m := newMod(t, Config{})
	key, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "aeon-coordinator", "", []string{"harness.worker", "inbox.read", "inbox.send", "nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, route := range []string{
		"POST /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/harness-sessions/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/heartbeat",
		"GET /api/inbox/messages", "POST /api/inbox/messages/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/ack",
		"POST /api/inbox/messages", "GET /api/nodes/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		"GET /api/projects", "GET /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/messages/listen",
		"POST /api/projects/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/messages/bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb/ack",
	} {
		method, path, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, path, nil)
		setPolicyPattern(req)
		req.Header.Set("Authorization", "Bearer "+key.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNoContent {
			t.Errorf("%s: %d %s", route, res.Code, res.Body.String())
		}
	}
}

func TestServicePrincipalsCannotReceiveAgentKeys(t *testing.T) {
	reset(t)
	tenantID := insertTenant(t, "service-audit", "Service audit")
	m := newMod(t, Config{})
	for _, role := range []string{"system", "importer", "operator", "embedding", "quote_public_service", "quote_confirmation_service"} {
		t.Run(role, func(t *testing.T) {
			name := "service-" + role
			err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent',$2,ARRAY[$3]::text[])`, tenantID, name, role)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, name, "", []string{"nodes.read"}, nil); !errors.Is(err, errServicePrincipal) {
				t.Fatalf("service key error: %v", err)
			}
		})
	}
	err := testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'person','person-service',ARRAY['system']::text[])`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "person-service", "", nil, nil); !errors.Is(err, errServicePrincipal) {
		t.Fatalf("person service name collision: %v", err)
	}
	// A colliding ordinary agent must not hide a service principal of the same name.
	normalKey, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "collision", "", []string{"nodes.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1::uuid,'agent','collision',ARRAY['operator']::text[])`, tenantID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.createAgentKey(t.Context(), tenant.Principal{TenantID: tenantID}, "collision", "", nil, nil); !errors.Is(err, errServicePrincipal) {
		t.Fatalf("collision key error: %v", err)
	}
	if got := countInTenant(t, tenantID, `SELECT count(*) FROM agent_keys WHERE name='collision'`); got != 1 {
		t.Fatalf("collision keys: %d", got)
	}
	err = testInTenant(t.Context(), appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET roles=ARRAY['system']::text[] WHERE id=$1::uuid`, normalKey.PrincipalID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	prefix, secret, ok := parseBearer("Bearer " + normalKey.Token)
	if !ok {
		t.Fatal("minted bearer invalid")
	}
	if _, authenticated, err := m.authenticateAgent(httptest.NewRequest(http.MethodGet, "/api/me", nil).Context(), prefix, secret); err != nil || authenticated {
		t.Fatalf("promoted service key authenticated: %v, %v", authenticated, err)
	}
}
