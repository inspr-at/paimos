// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: hostile selectors/cursors must be bounded before database or listener
// allocation, and connection admission must retain only live keys.
func TestSubscriptionInputAndAdmissionBounds(t *testing.T) {
	for _, query := range []string{
		"after=", "after=-1", "after=9223372036854775808", "after=" + strings.Repeat("0", 20),
		"after=0&after=1", "topics=", "topics=plan,plan", "topics=plan,unknown", "topics=plan&topics=delivery", "node_id=x", "topics=%zz", "after=" + strings.Repeat("0", 300),
	} {
		if _, err := parseSubscription(httptest.NewRequest("GET", "/api/events/subscribe?"+query, nil)); err == nil {
			t.Fatalf("accepted invalid selector %q", query)
		}
	}
	req := httptest.NewRequest("GET", "/api/events/subscribe?after=latest&topics=plan,delivery", nil)
	req.Header.Set("Last-Event-ID", "42")
	parsed, err := parseSubscription(req)
	if err != nil || parsed.latest || parsed.after != 42 || len(parsed.topics) != 2 {
		t.Fatal("cursor precedence or topic selection lost")
	}
	req.Header["Last-Event-Id"] = []string{"42", "43"}
	if _, err := parseSubscription(req); err == nil {
		t.Fatal("accepted ambiguous resume header")
	}
	req.Header.Set("Last-Event-ID", "latest")
	if _, err := parseSubscription(req); err == nil {
		t.Fatal("accepted latest resume header")
	}
	var limits subscriptionLimits
	for range 2 {
		if !limits.acquire("one") {
			t.Fatal("early per-key limit")
		}
	}
	if limits.acquire("one") {
		t.Fatal("per-key limit missing")
	}
	for i := 2; i < 64; i++ {
		if !limits.acquire(string(rune(i))) {
			t.Fatal("early total limit")
		}
	}
	if limits.acquire("overflow") {
		t.Fatal("total listener limit missing")
	}
	limits.release("one")
	if !limits.acquire("one") {
		t.Fatal("release lost capacity")
	}
	limits.release("one")
	limits.release("one")
	if _, retained := limits.keys["one"]; retained {
		t.Fatal("dead key retained")
	}
}

// Risk: idle connections must revalidate authority and send cursor-free
// heartbeat frames without creating events. Injected wake barriers replace
// wall-clock waits; timeouts below only guard against hangs.
func TestSubscriptionHeartbeatPreservesCursorAndRevocation(t *testing.T) {
	d, a, _ := fixture(t)
	var owner string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, a.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, a.TenantID, owner, "owner")
	a.KeyCreatorID = owner
	a.Scopes = []string{"events.subscribe", "agents.plan.read"}
	var role string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'subscription','Subscription') RETURNING id::text`, a.TenantID).Scan(&role); err != nil {
		t.Fatal(err)
	}
	for _, permission := range a.Scopes {
		if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, a.TenantID, role, permission); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, a.TenantID, a.ID, role); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','fixture',repeat('0',64),$3,$4) RETURNING id::text`, a.TenantID, a.ID, a.Scopes, owner).Scan(&a.KeyID); err != nil {
		t.Fatal(err)
	}
	ticks := make(chan struct{})
	waiting := make(chan struct{}, 2)
	m := New(d.App).(*module)
	m.subscriptionWait = func(ctx context.Context, _ *pgx.Conn, _ time.Time) error {
		waiting <- struct{}{}
		select {
		case <-ticks:
			return context.DeadlineExceeded
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	mux := http.NewServeMux()
	m.Mount(mux)
	finished := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { finished <- struct{}{} }()
		mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), a)))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/subscribe?topics=plan", nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("heartbeat stream: %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	var opening []string
	for sc.Scan() {
		opening = append(opening, sc.Text())
		if sc.Text() == "" {
			break
		}
	}
	if len(opening) != 4 || !strings.HasPrefix(opening[0], "id: ") || opening[1] != "event: stream.ready" {
		t.Fatalf("opening %q", opening)
	}
	<-waiting
	before := logPosition(t, d, a)
	ticks <- struct{}{}
	for _, want := range []string{"event: stream.ping", "data: {}", ""} {
		if !sc.Scan() || sc.Text() != want {
			t.Fatalf("heartbeat %q want %q", sc.Text(), want)
		}
	}
	<-waiting
	if after := logPosition(t, d, a); after != before {
		t.Fatal("heartbeat appended an event")
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, a.KeyID); err != nil {
		t.Fatal(err)
	}
	ticks <- struct{}{}
	if sc.Scan() {
		t.Fatal("revoked idle key received a ping")
	}
	if sc.Err() != nil && !errors.Is(sc.Err(), context.Canceled) {
		t.Fatal("stream failed to close cleanly")
	}
	<-finished
	if m.subscriptions.total != 0 || len(m.subscriptions.keys) != 0 {
		t.Fatal("revoked stream retained resources")
	}
}
