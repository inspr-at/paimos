// SPDX-License-Identifier: AGPL-3.0-only

package approvals

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestScopeWithinKey(t *testing.T) {
	cases := []struct {
		scope  string
		keys   []string
		within bool
	}{
		{"run.claim", []string{"run.claim"}, true},
		{"run.claim", []string{"run"}, true},
		{"nodes.read.fields", []string{"nodes.read"}, true},
		{"run.claim", []string{"nodes.read", "run.claim"}, true},
		{"run.claim", []string{"run.claim.once"}, false},
		{"run.claim", []string{"run.claimant"}, false},
		{"run.claim", nil, false},
		{"run.claim", []string{}, false},
		{"run", []string{"run"}, false},
		{"Run.claim", []string{"Run.claim"}, false},
		{"", []string{""}, false},
	}
	for _, tc := range cases {
		if got := ScopeWithinKey(tc.scope, tc.keys); got != tc.within {
			t.Errorf("ScopeWithinKey(%q, %v) = %v, want %v", tc.scope, tc.keys, got, tc.within)
		}
	}
}

type fixture struct {
	db         *dbtest.DB
	mux        *http.ServeMux
	tenantA    string
	tenantB    string
	personA    tenant.Principal
	personB    tenant.Principal
	agentA     tenant.Principal
	agentB     tenant.Principal
	wide       string
	exact      string
	narrow     string
	once       string
	claimant   string
	empty      string
	revoked    string
	tokenB     string
	nodeA      string
	deleted    string
	nodeB      string
	runA       string
	runB       string
	eventStart int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: dbtest.Open(t)}
	ctx := t.Context()
	f.tenantA = insertTenant(t, f.db.Admin, "approvals-a")
	f.tenantB = insertTenant(t, f.db.Admin, "approvals-b")
	f.personA = insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "ada")
	f.personA.Roles = []string{"admin"}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE principals SET roles=$2 WHERE id=$1::uuid`, f.personA.ID, f.personA.Roles)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, f.db, f.tenantA, f.personA.ID)
	f.personB = insertPrincipal(t, f.db.Admin, f.tenantB, tenant.Person, "bea")
	f.agentA = insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Agent, "agent-a")
	f.agentB = insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Agent, "agent-b")
	f.agentA.Scopes = []string{"approvals.read", "harness.read"}
	f.agentB.Scopes = []string{"approvals.read", "harness.read"}
	// Agents see project data only through a binding (ADR-003 P2).
	dbtest.BindRole(t, f.db, f.tenantA, f.agentA.ID, "member")
	dbtest.BindRole(t, f.db, f.tenantA, f.agentB.ID, "member")
	f.wide = insertKey(t, f.db.Admin, f.agentA, []string{"run", "nodes.read", "approvals.read", "harness.read"}, false)
	f.exact = insertKey(t, f.db.Admin, f.agentA, []string{"run.claim"}, false)
	f.narrow = insertKey(t, f.db.Admin, f.agentA, []string{"nodes.read"}, false)
	f.once = insertKey(t, f.db.Admin, f.agentA, []string{"run.claim.once"}, false)
	f.claimant = insertKey(t, f.db.Admin, f.agentA, []string{"run.claimant"}, false)
	f.empty = insertKey(t, f.db.Admin, f.agentA, []string{}, false)
	f.revoked = insertKey(t, f.db.Admin, f.agentA, []string{"run.claim"}, true)
	f.tokenB = insertKey(t, f.db.Admin, f.agentB, []string{"run", "nodes.read", "approvals.read", "harness.read"}, false)
	f.nodeA = insertNode(t, f.db.Admin, f.tenantA, "work", "TKT-1", "Target")
	f.deleted = insertNode(t, f.db.Admin, f.tenantA, "work", "TKT-2", "Gone")
	if _, err := f.db.Admin.Exec(ctx, `UPDATE nodes SET deleted_at = now() WHERE id = $1::uuid`, f.deleted); err != nil {
		t.Fatal(err)
	}
	f.nodeB = insertNode(t, f.db.Admin, f.tenantB, "work", "TKT-1", "Foreign")
	order := insertNode(t, f.db.Admin, f.tenantA, "work_order", "WOR-1", "Order")
	if _, err := f.db.Admin.Exec(ctx, `
		INSERT INTO work_orders (tenant_id, node_id, requested_by_principal_id, assignee_principal_id, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'ready')`,
		f.tenantA, order, f.personA.ID, f.agentA.ID); err != nil {
		t.Fatal(err)
	}
	f.runA = insertRun(t, f.db.Admin, f.tenantA, order, f.agentA.ID)
	f.runB = insertRun(t, f.db.Admin, f.tenantA, order, f.agentB.ID)
	if err := f.db.Admin.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, f.tenantA).Scan(&f.eventStart); err != nil {
		t.Fatal(err)
	}
	f.mux = http.NewServeMux()
	New(f.db.App).Mount(f.mux)
	return f
}

func insertTenant(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, pool *pgxpool.Pool, tenantID string, kind tenant.PrincipalKind, name string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name}
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO principals (tenant_id, kind, name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		tenantID, string(kind), name).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

func insertKey(t *testing.T, pool *pgxpool.Pool, p tenant.Principal, scopes []string, revoked bool) string {
	t.Helper()
	secretRaw := make([]byte, 32)
	prefixRaw := make([]byte, 8)
	if _, err := rand.Read(secretRaw); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(prefixRaw); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(secretRaw)
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + hex.EncodeToString(prefixRaw)
	var revokedAt *time.Time
	if revoked {
		now := time.Now()
		revokedAt = &now
	}
	if scopes == nil {
		scopes = []string{}
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash, scopes, revoked_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7)`,
		p.TenantID, p.ID, p.Name, prefix, hex.EncodeToString(sum[:]), scopes, revokedAt); err != nil {
		t.Fatal(err)
	}
	return "aeon_" + prefix + "_" + secret
}

func insertNode(t *testing.T, pool *pgxpool.Pool, tenantID, slug, key, title string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO nodes (tenant_id, key, kind_id, title)
		SELECT $1::uuid, $2, id, $4 FROM node_kinds
		WHERE tenant_id = $1::uuid AND slug = $3
		RETURNING id::text`, tenantID, key, slug, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertRun(t *testing.T, pool *pgxpool.Pool, tenantID, orderID, agentID string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `
		INSERT INTO agent_runs (tenant_id, work_order_id, agent_principal_id, status)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'queued') RETURNING id::text`,
		tenantID, orderID, agentID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) do(principal tenant.Principal, token, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req = req.WithContext(tenant.WithPrincipal(req.Context(), principal))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func (f *fixture) anon(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, req)
	return w
}

func proposalJSON(scope, kind string, resource, run *string) string {
	payload := map[string]any{
		"scope":         scope,
		"resource_kind": kind,
		"rationale":     "because the work needs it",
		"expires_at":    time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
	}
	if resource != nil {
		payload["resource_id"] = *resource
	}
	if run != nil {
		payload["run_id"] = *run
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodeApproval(t *testing.T, w *httptest.ResponseRecorder) Approval {
	t.Helper()
	var out Approval
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("status %d body %s: %v", w.Code, w.Body.String(), err)
	}
	return out
}

func (f *fixture) eventTypes(t *testing.T) []string {
	t.Helper()
	rows, err := f.db.Admin.Query(t.Context(), `
		SELECT type FROM events WHERE tenant_id = $1::uuid AND id>$2 ORDER BY id`, f.tenantA, f.eventStart)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		types = append(types, eventType)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return types
}

func (f *fixture) live(t *testing.T, tenantID, agent, scope, kind string, resource *string, scopes []string) bool {
	t.Helper()
	var live bool
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, tenantID, func(tx pgx.Tx) error {
		var err error
		live, err = LiveGrant(t.Context(), tx, agent, scope, kind, resource, scopes)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return live
}

func TestApprovals(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	if w := f.anon(http.MethodGet, "/api/approvals", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("anon list %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.personA, "", http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil)); w.Code != http.StatusForbidden {
		t.Fatalf("person propose %d %s", w.Code, w.Body.String())
	}

	bad := []string{
		`{"scope":"run","resource_kind":"tenant","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"Run.Claim","resource_kind":"tenant","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"file","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"tenant","rationale":"  ","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"tenant","rationale":"x","expires_at":"2000-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"tenant","resource_id":"` + f.nodeA + `","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"node","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"run","resource_id":"` + f.runA + `","run_id":"` + f.runB + `","rationale":"x","expires_at":"2999-01-01T00:00:00Z"}`,
		`{"scope":"run.claim","resource_kind":"tenant","rationale":"x","expires_at":"2999-01-01T00:00:00Z","agent_principal_id":"` + f.agentB.ID + `"}`,
		`{}`,
	}
	for _, body := range bad {
		if w := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", body); w.Code != http.StatusBadRequest {
			t.Fatalf("bad proposal %d %s body %s", w.Code, w.Body.String(), body)
		}
	}
	if len(f.eventTypes(t)) != 0 {
		t.Fatalf("rejected proposals wrote events %#v", f.eventTypes(t))
	}

	for _, token := range []string{"", f.narrow, f.once, f.claimant, f.empty, f.revoked, f.tokenB} {
		w := f.do(f.agentA, token, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("ceiling token %q status %d %s", token, w.Code, w.Body.String())
		}
	}
	missing := "00000000-0000-4000-8000-000000000001"
	if w := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "node", &missing, nil)); w.Code != http.StatusForbidden {
		t.Fatalf("missing node %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "node", &f.deleted, nil)); w.Code != http.StatusForbidden {
		t.Fatalf("deleted node %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "node", &f.nodeB, nil)); w.Code != http.StatusForbidden {
		t.Fatalf("foreign node %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.agentA, f.exact, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "run", &f.runB, nil)); w.Code != http.StatusForbidden {
		t.Fatalf("other run %d %s", w.Code, w.Body.String())
	}

	created := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("propose %d %s", created.Code, created.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["decision"] != nil || raw["agent_principal_id"] != f.agentA.ID || raw["resource_id"] != nil {
		t.Fatalf("proposal json %#v", raw)
	}
	proposal := decodeApproval(t, created)
	var grants int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM agent_permission_grants WHERE approval_request_id = $1::uuid`, proposal.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || len(f.eventTypes(t)) != 1 || f.eventTypes(t)[0] != "approval.proposed" {
		t.Fatalf("propose granted or skipped the event grants=%d events=%v", grants, f.eventTypes(t))
	}

	if w := f.do(f.agentA, f.exact, http.MethodPost, "/api/approvals/"+proposal.ID+"/decision", `{"decision":"approved"}`); w.Code != http.StatusForbidden {
		t.Fatalf("agent decide %d %s", w.Code, w.Body.String())
	}
	if w := f.do(f.personB, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/decision", `{"decision":"approved"}`); w.Code != http.StatusNotFound {
		t.Fatalf("foreign decide %d %s", w.Code, w.Body.String())
	}
	approved := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/decision", `{"decision":"approved","reason":" yes "}`)
	if approved.Code != http.StatusOK {
		t.Fatalf("approve %d %s", approved.Code, approved.Body.String())
	}
	decision := decodeApproval(t, approved)
	if decision.Decision == nil || *decision.Decision != "approved" || decision.DecidedByPrincipalID == nil || *decision.DecidedByPrincipalID != f.personA.ID {
		t.Fatalf("decision %#v", decision)
	}
	var reason string
	var matched bool
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT d.reason, g.valid_until = r.expires_at
		FROM approval_decisions d
		JOIN approval_requests r ON r.tenant_id = d.tenant_id AND r.id = d.request_id
		JOIN agent_permission_grants g ON g.tenant_id = d.tenant_id AND g.approval_request_id = d.request_id
		WHERE d.request_id = $1::uuid`, proposal.ID).Scan(&reason, &matched); err != nil {
		t.Fatal(err)
	}
	if reason != "yes" || !matched {
		t.Fatalf("reason %q matched %v", reason, matched)
	}
	if !f.live(t, f.tenantA, f.agentA.ID, "run.claim", "tenant", nil, []string{"run.claim"}) {
		t.Fatal("grant not live for an exact key scope")
	}
	if !f.live(t, f.tenantA, f.agentA.ID, "run.claim", "tenant", nil, []string{"run"}) {
		t.Fatal("grant not live under a broader key scope")
	}
	if f.live(t, f.tenantA, f.agentA.ID, "run.claim", "tenant", nil, []string{"nodes.read"}) ||
		f.live(t, f.tenantA, f.agentA.ID, "run.claim", "tenant", nil, nil) ||
		f.live(t, f.tenantB, f.agentA.ID, "run.claim", "tenant", nil, []string{"run.claim"}) ||
		f.live(t, f.tenantA, f.agentA.ID, "run.claim", "node", &f.nodeA, []string{"run.claim"}) {
		t.Fatal("grant was live outside its ceiling, tenant or resource")
	}
	again := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/decision", `{"decision":"denied"}`)
	if again.Code != http.StatusConflict {
		t.Fatalf("second decision %d %s", again.Code, again.Body.String())
	}

	if w := f.do(f.agentA, f.exact, http.MethodPost, "/api/approvals/"+proposal.ID+"/revoke", ""); w.Code != http.StatusForbidden {
		t.Fatalf("agent revoke %d %s", w.Code, w.Body.String())
	}
	revoked := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/revoke", "")
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke %d %s", revoked.Code, revoked.Body.String())
	}
	if decodeApproval(t, revoked).Decision == nil || *decodeApproval(t, revoked).Decision != "approved" {
		t.Fatalf("revoked response %s", revoked.Body.String())
	}
	repeat := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/revoke", "")
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat revoke %d %s", repeat.Code, repeat.Body.String())
	}
	var revokedEvents int
	var revokedAt *time.Time
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT count(*) FROM events WHERE tenant_id = $1::uuid AND type = 'approval.revoked'`, f.tenantA).Scan(&revokedEvents); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT revoked_at FROM agent_permission_grants WHERE approval_request_id = $1::uuid`, proposal.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if revokedEvents != 1 || revokedAt == nil {
		t.Fatalf("revoke events %d at %v", revokedEvents, revokedAt)
	}
	var beforeDecision, afterDecision string
	var hasRevokedAt bool
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT before->>'decision', after->>'decision', after->>'revoked_at' IS NOT NULL
		FROM events WHERE tenant_id = $1::uuid AND type = 'approval.revoked'`, f.tenantA).Scan(&beforeDecision, &afterDecision, &hasRevokedAt); err != nil {
		t.Fatal(err)
	}
	if beforeDecision != "approved" || afterDecision != "approved" || !hasRevokedAt {
		t.Fatalf("revoke event before %s after %s revoked_at %v", beforeDecision, afterDecision, hasRevokedAt)
	}
	if f.live(t, f.tenantA, f.agentA.ID, "run.claim", "tenant", nil, []string{"run"}) {
		t.Fatal("revoked grant still live")
	}

	denied := f.do(f.agentA, f.exact, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "run", &f.runA, &f.runA))
	if denied.Code != http.StatusCreated {
		t.Fatalf("run propose %d %s", denied.Code, denied.Body.String())
	}
	runApproval := decodeApproval(t, denied)
	deny := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+runApproval.ID+"/decision", `{"decision":"denied","reason":"no"}`)
	if deny.Code != http.StatusOK {
		t.Fatalf("deny %d %s", deny.Code, deny.Body.String())
	}
	if decodeApproval(t, deny).Decision == nil || *decodeApproval(t, deny).Decision != "denied" {
		t.Fatalf("deny body %s", deny.Body.String())
	}
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM agent_permission_grants WHERE approval_request_id = $1::uuid`, runApproval.ID).Scan(&grants); err != nil {
		t.Fatal(err)
	}
	if grants != 0 || f.live(t, f.tenantA, f.agentA.ID, "run.claim", "run", &f.runA, []string{"run.claim"}) {
		t.Fatal("denial granted authority")
	}
	if w := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+runApproval.ID+"/revoke", ""); w.Code != http.StatusConflict {
		t.Fatalf("revoke denial %d %s", w.Code, w.Body.String())
	}

	nodeBody := proposalJSON("nodes.read.fields", "node", &f.nodeA, &f.runA)
	nodeResp := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", nodeBody)
	if nodeResp.Code != http.StatusCreated {
		t.Fatalf("node propose %d %s", nodeResp.Code, nodeResp.Body.String())
	}
	nodeApproval := decodeApproval(t, nodeResp)
	var eventNode *string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT node_id::text FROM events
		WHERE tenant_id = $1::uuid AND type = 'approval.proposed' AND after->>'id' = $2`,
		f.tenantA, nodeApproval.ID).Scan(&eventNode); err != nil {
		t.Fatal(err)
	}
	if eventNode == nil || *eventNode != f.nodeA {
		t.Fatalf("node event %v", eventNode)
	}
	if w := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+nodeApproval.ID+"/decision", `{"decision":"approved"}`); w.Code != http.StatusOK {
		t.Fatalf("approve node %d %s", w.Code, w.Body.String())
	}
	if !f.live(t, f.tenantA, f.agentA.ID, "nodes.read.fields", "node", &f.nodeA, []string{"nodes.read"}) {
		t.Fatal("node grant not live")
	}
	if _, err := f.db.Admin.Exec(ctx, `
		UPDATE agent_permission_grants SET valid_until = now() - interval '1 minute'
		WHERE approval_request_id = $1::uuid`, nodeApproval.ID); err != nil {
		t.Fatal(err)
	}
	if f.live(t, f.tenantA, f.agentA.ID, "nodes.read.fields", "node", &f.nodeA, []string{"nodes.read"}) {
		t.Fatal("expired grant still live")
	}

	extra := f.do(f.agentB, f.tokenB, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil))
	if extra.Code != http.StatusCreated {
		t.Fatalf("agent b propose %d %s", extra.Code, extra.Body.String())
	}
	listA := f.do(f.agentA, f.wide, http.MethodGet, "/api/approvals?limit=50", "")
	if listA.Code != http.StatusOK {
		t.Fatalf("list %d %s", listA.Code, listA.Body.String())
	}
	var mine []Approval
	if err := json.Unmarshal(listA.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	for _, item := range mine {
		if item.AgentPrincipalID != f.agentA.ID {
			t.Fatalf("agent list included %s", item.AgentPrincipalID)
		}
	}
	if len(mine) != 3 {
		t.Fatalf("agent A list %d", len(mine))
	}
	page := f.do(f.personA, "", http.MethodGet, "/api/approvals?limit=2", "")
	var people []Approval
	if err := json.Unmarshal(page.Body.Bytes(), &people); err != nil {
		t.Fatal(err)
	}
	if page.Code != http.StatusOK || len(people) != 2 {
		t.Fatalf("person page %d len %d", page.Code, len(people))
	}
	if people[0].ProposedAt.Before(people[1].ProposedAt) {
		t.Fatal("list is not newest first")
	}
	foreignList := f.do(f.personB, "", http.MethodGet, "/api/approvals", "")
	var others []Approval
	if err := json.Unmarshal(foreignList.Body.Bytes(), &others); err != nil {
		t.Fatal(err)
	}
	if foreignList.Code != http.StatusOK || len(others) != 0 {
		t.Fatalf("foreign list %d %s", foreignList.Code, foreignList.Body.String())
	}
	for _, limit := range []string{"0", "201", "nope"} {
		if w := f.do(f.personA, "", http.MethodGet, "/api/approvals?limit="+limit, ""); w.Code != http.StatusBadRequest {
			t.Fatalf("limit %s %d", limit, w.Code)
		}
	}
	if w := f.do(f.personA, "", http.MethodPost, "/api/approvals/not-a-uuid/decision", `{"decision":"approved"}`); w.Code != http.StatusNotFound {
		t.Fatalf("bad id %d", w.Code)
	}

	var expiredID string
	if err := f.db.Admin.QueryRow(ctx, `
		INSERT INTO approval_requests (
			tenant_id, proposed_by_principal_id, agent_principal_id,
			scope, resource_kind, rationale, proposed_at, expires_at)
		VALUES ($1::uuid, $2::uuid, $2::uuid, 'run.claim', 'tenant', 'too late',
		        now() - interval '2 hours', now() - interval '1 hour')
		RETURNING id::text`, f.tenantA, f.agentA.ID).Scan(&expiredID); err != nil {
		t.Fatal(err)
	}
	if w := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+expiredID+"/decision", `{"decision":"approved"}`); w.Code != http.StatusConflict {
		t.Fatalf("expired %d %s", w.Code, w.Body.String())
	}
	var decisions int
	if err := f.db.Admin.QueryRow(ctx, `SELECT count(*) FROM approval_decisions WHERE request_id = $1::uuid`, expiredID).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if decisions != 0 {
		t.Fatal("expired request was decided")
	}

	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO approval_requests (
				tenant_id, proposed_by_principal_id, agent_principal_id,
				scope, resource_kind, rationale, expires_at)
			VALUES ($1::uuid, $2::uuid, $2::uuid, 'run.claim', 'tenant', 'person', now() + interval '1 hour')`,
			f.tenantA, f.personA.ID)
		return err
	}); err == nil || !strings.Contains(err.Error(), "only an agent") {
		t.Fatalf("person insert request: %v", err)
	}
	guardID := insertGuardRequest(t, f)
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO approval_decisions (tenant_id, request_id, decided_by_principal_id, decision)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'approved')`, f.tenantA, guardID, f.agentA.ID)
		return err
	}); err == nil || !strings.Contains(err.Error(), "only a person") {
		t.Fatalf("agent insert decision: %v", err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO agent_permission_grants (
				tenant_id, approval_request_id, agent_principal_id, scope, resource_kind, valid_until)
			SELECT tenant_id, id, agent_principal_id, 'nodes.read', resource_kind, expires_at
			FROM approval_requests WHERE id = $1::uuid`, guardID)
		return err
	}); err == nil || !strings.Contains(err.Error(), "permission grant") {
		t.Fatalf("mismatched grant: %v", err)
	}
	if _, err := f.db.Admin.Exec(ctx, `UPDATE approval_requests SET rationale = 'changed' WHERE id = $1::uuid`, guardID); err == nil {
		t.Fatal("approval request was updated")
	}
}

func insertGuardRequest(t *testing.T, f *fixture) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			INSERT INTO approval_requests (
				tenant_id, proposed_by_principal_id, agent_principal_id,
				scope, resource_kind, rationale, expires_at)
			VALUES ($1::uuid, $2::uuid, $2::uuid, 'run.claim', 'tenant', 'guard', now() + interval '1 hour')
			RETURNING id::text`, f.tenantA, f.agentA.ID).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestLiveGrantRejectsBadIDs(t *testing.T) {
	f := newFixture(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.tenantA, func(tx pgx.Tx) error {
		_, err := LiveGrant(t.Context(), tx, "not-a-uuid", "run.claim", "tenant", nil, []string{"run.claim"})
		return err
	})
	if err == nil {
		t.Fatal("expected uuid error")
	}
	w := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("empty body %d", w.Code)
	}
}

func TestListAndGetApprovalAgentName(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	created := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("propose %d %s", created.Code, created.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(created.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if raw["agent_name"] != f.agentA.Name {
		t.Fatalf("propose agent_name %#v", raw["agent_name"])
	}
	proposal := decodeApproval(t, created)
	var snap *string
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT after->>'agent_name' FROM events
		WHERE tenant_id = $1::uuid AND type = 'approval.proposed' AND after->>'id' = $2`,
		f.tenantA, proposal.ID).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatalf("event exposed agent_name %q", *snap)
	}

	if _, err := f.db.Admin.Exec(ctx, `UPDATE principals SET name = $2 WHERE id = $1::uuid`, f.agentA.ID, "Harbor Clerk"); err != nil {
		t.Fatal(err)
	}
	listed := f.do(f.personA, "", http.MethodGet, "/api/approvals", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list %d %s", listed.Code, listed.Body.String())
	}
	var items []Approval
	if err := json.Unmarshal(listed.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].AgentName == nil || *items[0].AgentName != "Harbor Clerk" || items[0].ID != proposal.ID {
		t.Fatalf("list agent_name %#v", items)
	}
	if err := f.db.Admin.QueryRow(ctx, `
		SELECT after->>'agent_name' FROM events
		WHERE tenant_id = $1::uuid AND type = 'approval.proposed' AND after->>'id' = $2`,
		f.tenantA, proposal.ID).Scan(&snap); err != nil {
		t.Fatal(err)
	}
	if snap != nil {
		t.Fatalf("event exposed agent_name after rename %q", *snap)
	}

	other := f.do(f.agentB, f.tokenB, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "tenant", nil, nil))
	if other.Code != http.StatusCreated {
		t.Fatalf("agent b propose %d %s", other.Code, other.Body.String())
	}
	if decodeApproval(t, other).AgentName == nil || *decodeApproval(t, other).AgentName != f.agentB.Name {
		t.Fatalf("agent b name %s", other.Body.String())
	}
	mine := f.do(f.agentA, f.wide, http.MethodGet, "/api/approvals", "")
	var own []Approval
	if err := json.Unmarshal(mine.Body.Bytes(), &own); err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 || own[0].AgentName == nil || *own[0].AgentName != "Harbor Clerk" {
		t.Fatalf("agent list leaked or dropped the name %#v", own)
	}
	foreign := f.do(f.personB, "", http.MethodGet, "/api/approvals", "")
	var others []Approval
	if err := json.Unmarshal(foreign.Body.Bytes(), &others); err != nil {
		t.Fatal(err)
	}
	if foreign.Code != http.StatusOK || len(others) != 0 {
		t.Fatalf("foreign list %d %s", foreign.Code, foreign.Body.String())
	}

	decided := f.do(f.personA, "", http.MethodPost, "/api/approvals/"+proposal.ID+"/decision", `{"decision":"approved","reason":"named"}`)
	if decided.Code != http.StatusOK {
		t.Fatalf("decide %d %s", decided.Code, decided.Body.String())
	}
	got := decodeApproval(t, decided)
	if got.AgentName == nil || *got.AgentName != "Harbor Clerk" {
		t.Fatalf("decide agent_name %#v", got.AgentName)
	}
}

func TestApprovalAgentNameRequiresExistingNamePermission(t *testing.T) {
	f := newFixture(t)
	created := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil))
	if created.Code != http.StatusCreated {
		t.Fatalf("propose %d %s", created.Code, created.Body.String())
	}
	role := func(key string, permissions ...string) tenant.Principal {
		t.Helper()
		p := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, key)
		var roleID string
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1::uuid,$2,$2) RETURNING id::text`, f.tenantA, key).Scan(&roleID); err != nil {
			t.Fatal(err)
		}
		for _, permission := range permissions {
			if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1::uuid,$2::uuid,$3)`, f.tenantA, roleID, permission); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1::uuid,$2::uuid,$3::uuid,'workspace')`, f.tenantA, p.ID, roleID); err != nil {
			t.Fatal(err)
		}
		return p
	}
	limited := role("approval_viewer", "approvals.read")
	harness := role("approval_harness_reader", "approvals.read", "harness.read")
	member := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "approval member")
	dbtest.BindRole(t, f.db, f.tenantA, member.ID, "member")
	for _, tc := range []struct {
		name string
		p    tenant.Principal
		want bool
	}{
		{"viewer without name permission", limited, false},
		{"harness reader", harness, true},
		{"member", member, true},
		{"admin", f.personA, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := f.do(tc.p, "", http.MethodGet, "/api/approvals", "")
			if w.Code != http.StatusOK {
				t.Fatalf("list %d %s", w.Code, w.Body.String())
			}
			var raw []map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
				t.Fatal(err)
			}
			if len(raw) != 1 {
				t.Fatalf("approvals %#v", raw)
			}
			name, present := raw[0]["agent_name"]
			if present != tc.want || (present && name != f.agentA.Name) {
				t.Fatalf("agent_name present=%v value=%#v", present, name)
			}
		})
	}
}

func TestProjectScopedApprovalNames(t *testing.T) {
	f := newFixture(t)
	projectA := insertNode(t, f.db.Admin, f.tenantA, "project", "PA-1", "Visible")
	projectB := insertNode(t, f.db.Admin, f.tenantA, "project", "PB-1", "Hidden")
	ticket := func(project, key string) string {
		t.Helper()
		var id string
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,kind_id,key,title) SELECT $1::uuid,$2::uuid,id,$3,$3 FROM node_kinds WHERE tenant_id=$1::uuid AND slug='work' RETURNING id::text`, f.tenantA, project, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ticketA, ticketB := ticket(projectA, "PA-2"), ticket(projectB, "PB-2")
	if w := f.do(f.agentA, f.wide, http.MethodPost, "/api/approvals", proposalJSON("run.claim", "tenant", nil, nil)); w.Code != http.StatusCreated {
		t.Fatalf("workspace propose %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{ticketA, ticketB} {
		w := f.do(f.agentA, f.narrow, http.MethodPost, "/api/approvals", proposalJSON("nodes.read", "node", &id, nil))
		if w.Code != http.StatusCreated {
			t.Fatalf("propose %d %s", w.Code, w.Body.String())
		}
	}
	reader := insertPrincipal(t, f.db.Admin, f.tenantA, tenant.Person, "project reader")
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1::uuid,$2::uuid,id,'project',$3::uuid FROM roles WHERE tenant_id=$1::uuid AND key='member'`, f.tenantA, reader.ID, projectA); err != nil {
		t.Fatal(err)
	}
	if !authz.ProjectFilteredRoutes["GET /api/approvals"] || authz.RequirePattern(authz.BindPool(tenant.WithPrincipal(t.Context(), reader), f.db.App), "GET /api/approvals", authz.Scope{AnyProject: true}) != nil {
		t.Fatal("project reader cannot reach the filtered approvals route")
	}
	w := f.do(reader, "", http.MethodGet, "/api/approvals", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}
	var items []Approval
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ResourceID == nil || *items[0].ResourceID != ticketA || items[0].AgentName == nil || *items[0].AgentName != f.agentA.Name {
		t.Fatalf("project-scoped approvals %#v", items)
	}
}
