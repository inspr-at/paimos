// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/views"
	"github.com/jackc/pgx/v5"
)

type subscriptionFrame struct {
	id   int64
	name string
	data string
}

func subscriptionFrameNext(t *testing.T, sc *bufio.Scanner) subscriptionFrame {
	t.Helper()
	var f subscriptionFrame
	for sc.Scan() {
		line := sc.Text()
		if line == "" && f.name != "" {
			return f
		}
		if strings.HasPrefix(line, "id: ") {
			var err error
			f.id, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err != nil {
				t.Fatal("invalid cursor")
			}
		}
		if strings.HasPrefix(line, "event: ") {
			f.name = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
	t.Fatalf("stream ended before frame: %v", sc.Err())
	return f
}

func subscriptionApp(t *testing.T, m *Module, finished chan<- struct{}) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	events.New(m.pool).Mount(mux)
	secured := m.Middleware(mux)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if finished != nil {
			defer func() { finished <- struct{}{} }()
		}
		_, r.Pattern = mux.Handler(r)
		secured.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func subscriptionOpen(t *testing.T, srv *httptest.Server, key agentKeyCreatedJSON, after, last string) (*http.Response, *bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/subscribe?topics=plan&after="+after, nil)
	req.Header.Set("Authorization", "Bearer "+key.Token)
	if last != "" {
		req.Header.Set("Last-Event-ID", last)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal("subscription request failed")
	}
	t.Cleanup(func() { cancel(); resp.Body.Close() })
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1024), 4096)
	return resp, sc, cancel
}

// Risk: the real UI write and authenticated listener must share an atomic,
// resumable event boundary without revealing other people or tenants.
func TestAgentSubscriptionPlanResumeAndPrivacy(t *testing.T) {
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "listener", "scopes": []string{"events.subscribe", "agents.plan.read"}}))
	srv := subscriptionApp(t, m, nil)
	mux := http.NewServeMux()
	views.New(m.pool).Mount(mux)
	save := func(person tenant.Principal, total int) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/api/preferences/agents.working", strings.NewReader(fmt.Sprintf(`{"value":{"total":%d,"limits":{}}}`, total)))
		req = req.WithContext(tenant.WithPrincipal(req.Context(), person))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("UI plan save: %d", rec.Code)
		}
	}
	resp, sc, cancel := subscriptionOpen(t, srv, key, "latest", "")
	if resp.StatusCode != 200 {
		t.Fatalf("subscription: %d", resp.StatusCode)
	}
	ready := subscriptionFrameNext(t, sc)
	if ready.name != "stream.ready" {
		t.Fatal("missing subscription barrier")
	}
	// A two-second deadline is the acceptance bound, not an interleaving sleep.
	// The ready frame proves LISTEN is established before this real UI save.
	acceptanceTimeout := time.AfterFunc(2*time.Second, cancel)
	save(owner, 12)
	change := subscriptionFrameNext(t, sc)
	acceptanceTimeout.Stop()
	if change.name != "agents_plan.changed" || change.id <= ready.id {
		t.Fatal("wrong plan notification")
	}
	var hint map[string]json.RawMessage
	if json.Unmarshal([]byte(change.data), &hint) != nil || len(hint) != 4 || string(hint["topic"]) != `"plan"` || hint["at"] == nil || hint["type"] == nil || hint["id"] == nil {
		t.Fatal("plan hint leaked values or omitted its projection")
	}
	checkpoint := subscriptionFrameNext(t, sc)
	if checkpoint.name != "stream.cursor" || checkpoint.id != change.id {
		t.Fatal("wrong checkpoint")
	}
	cancel()
	resp.Body.Close()

	ctx := dbtest.Seed(t.Context())
	var bob tenant.Principal
	bob.TenantID, bob.Kind = owner.TenantID, tenant.Person
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Other person',ARRAY['super_admin']) RETURNING id::text`, owner.TenantID).Scan(&bob.ID); err != nil {
			return err
		}
		return dbtest.BindLegacyTx(ctx, tx, owner.TenantID, bob.ID)
	}); err != nil {
		t.Fatal(err)
	}
	save(bob, 0)
	var foreign tenant.Principal
	foreign.Kind = tenant.Person
	if err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('subscription-foreign','Foreign') RETURNING id::text`).Scan(&foreign.TenantID)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, m.pool, foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Foreign') RETURNING id::text`, foreign.TenantID).Scan(&foreign.ID)
	}); err != nil {
		t.Fatal(err)
	}
	appendHint := func(p tenant.Principal, count int) []int64 {
		t.Helper()
		ids := []int64{}
		if err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
			for range count {
				e, err := events.Append(ctx, tx, p, events.Change{Type: "agents_plan.changed", After: map[string]any{"principal_id": p.ID, "unsafe_value": "never-on-stream"}})
				if err != nil {
					return err
				}
				ids = append(ids, e.ID)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	appendHint(foreign, 2)
	// A backlog above the browser stream's cutoff must still replay in full.
	want := appendHint(owner, 1002)
	resp, sc, cancel = subscriptionOpen(t, srv, key, "latest", strconv.FormatInt(change.id, 10))
	if resp.StatusCode != 200 {
		t.Fatalf("reconnect: %d", resp.StatusCode)
	}
	ready = subscriptionFrameNext(t, sc)
	if ready.id != change.id {
		t.Fatal("Last-Event-ID did not win")
	}
	for _, id := range want {
		for {
			frame := subscriptionFrameNext(t, sc)
			if frame.name == "stream.cursor" {
				continue
			}
			if frame.name != "agents_plan.changed" || frame.id != id || strings.Contains(frame.data, "unsafe_value") || strings.Contains(frame.data, bob.ID) || strings.Contains(frame.data, foreign.ID) {
				t.Fatal("replay lost, reordered or leaked an event")
			}
			break
		}
	}
	cancel()
	resp.Body.Close()
	// Topic and live read permission filtering applies even to old replay.
	unscoped := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "scope-only", "scopes": []string{"events.subscribe"}}))
	resp, sc, cancel = subscriptionOpen(t, srv, unscoped, strconv.FormatInt(change.id, 10), "")
	if resp.StatusCode != 200 {
		t.Fatalf("scope-only: %d", resp.StatusCode)
	}
	subscriptionFrameNext(t, sc)
	onlyCursor := subscriptionFrameNext(t, sc)
	if onlyCursor.name != "stream.cursor" || onlyCursor.id != want[len(want)-1] {
		t.Fatal("topic read ceiling was bypassed")
	}
	cancel()
	resp.Body.Close()
}

// Risk: a new route must not inherit events.read, built-in role authority, or
// unlimited listeners across independently constructed server processes.
func TestAgentSubscriptionScopeAndConnectionLimits(t *testing.T) {
	for _, method := range []string{"GET", "HEAD"} {
		scope, ok := coreAgentScope(httptest.NewRequest(method, "/api/events/subscribe", nil))
		if !ok || scope != "events.subscribe" {
			t.Fatal("subscription route lost explicit scope")
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		scope, ok := coreAgentScope(httptest.NewRequest(method, "/api/events/subscribe", nil))
		if ok || scope != "" {
			t.Fatal("subscription gained write authority")
		}
	}
	m, owner := keyFixture(t)
	key := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "bounded-listener", "scopes": []string{"events.subscribe", "agents.plan.read"}}))
	plain := decodeKey(t, keyRequest(m, owner, map[string]any{"name": "history-reader", "scopes": []string{"events.read"}}))
	finished := make(chan struct{}, 16)
	first, second := subscriptionApp(t, m, finished), subscriptionApp(t, m, finished)
	resp, _, cancel := subscriptionOpen(t, first, plain, "latest", "")
	if resp.StatusCode != 403 {
		t.Fatal("unscoped key reached subscription")
	}
	cancel()
	resp.Body.Close()
	<-finished
	ctx := dbtest.Seed(t.Context())
	bind := func(role string) {
		if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=(SELECT id FROM roles WHERE tenant_id=$1 AND key=$3) WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='workspace'`, owner.TenantID, key.PrincipalID, role)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	bind("admin")
	resp, _, cancel = subscriptionOpen(t, first, key, "latest", "")
	if resp.StatusCode != 403 {
		t.Fatal("built-in agent role granted subscription by default")
	}
	cancel()
	resp.Body.Close()
	<-finished
	bind("agent_" + strings.ReplaceAll(key.PrincipalID, "-", ""))
	a, as, ac := subscriptionOpen(t, first, key, "latest", "")
	b, bs, bc := subscriptionOpen(t, second, key, "latest", "")
	if a.StatusCode != 200 || b.StatusCode != 200 {
		t.Fatal("first two subscriptions denied")
	}
	subscriptionFrameNext(t, as)
	subscriptionFrameNext(t, bs)
	third, _, cc := subscriptionOpen(t, first, key, "latest", "")
	if third.StatusCode != 429 || third.Header.Get("Retry-After") != "15" {
		t.Fatal("cross-process key connection bound missing")
	}
	cc()
	third.Body.Close()
	<-finished
	ac()
	a.Body.Close()
	<-finished // Explicit completion barrier, no release sleep.
	replacement, rs, rc := subscriptionOpen(t, first, key, "latest", "")
	if replacement.StatusCode != 200 {
		t.Fatal("closed subscription did not free slot")
	}
	subscriptionFrameNext(t, rs)
	// Wake the captured request after a live scope trim. Its old Principal
	// still has the scope; RequireTx must reject the current key row.
	if err := db.InTenant(ctx, m.pool, owner.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE agent_keys SET scopes=ARRAY['agents.plan.read'] WHERE id=$1`, key.ID); err != nil {
			return err
		}
		_, err := events.Append(ctx, tx, owner, events.Change{Type: "agents_plan.changed", After: map[string]string{"principal_id": owner.ID}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if rs.Scan() {
		t.Fatal("revoked subscription received a new frame")
	}
	rc()
	replacement.Body.Close()
	<-finished
	bc()
	b.Body.Close()
	<-finished
	permission, ok := authz.Lookup("events.subscribe")
	if !ok || len(permission.GrantableAt) != 1 || permission.GrantableAt[0] != "workspace" {
		t.Fatal("subscription was made project-grantable")
	}
}
