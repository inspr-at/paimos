// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
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

func subscriptionPlanReader(t *testing.T) (*dbtest.DB, tenant.Principal) {
	t.Helper()
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
	return d, a
}

// Risk: idle connections must revalidate authority and send cursor-free
// heartbeat frames without creating events. Injected wake barriers replace
// wall-clock waits; timeouts below only guard against hangs.
func TestSubscriptionHeartbeatPreservesCursorAndRevocation(t *testing.T) {
	d, a := subscriptionPlanReader(t)
	ticks := make(chan struct{})
	waiting := make(chan struct{}, 2)
	m := New(d.App).(*module)
	m.subscriptionWait = func(ctx context.Context, _ db.NotificationListener, _ int64, _ time.Time) error {
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

type subscriptionNotice struct {
	notification *pgconn.Notification
	err          error
}

type subscriptionNoticeSource struct {
	steps   chan subscriptionNotice
	waiting chan struct{}
}

func (s *subscriptionNoticeSource) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case s.waiting <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case step := <-s.steps:
		return step.notification, step.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (*subscriptionNoticeSource) Close(context.Context) error { return nil }

type subscriptionReadTrace struct {
	pages, boundaries atomic.Int64
	pauseAt           atomic.Int64
	paused, release   chan struct{}
}

func (tr *subscriptionReadTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT e.id,e.type,e.at") {
		tr.pages.Add(1)
	}
	if strings.Contains(data.SQL, "SELECT last_id FROM event_counters WHERE tenant_id=$1") {
		if tr.boundaries.Add(1) == tr.pauseAt.Load() {
			close(tr.paused)
			select {
			case <-tr.release:
			case <-ctx.Done():
			}
		}
	}
	return ctx
}
func (*subscriptionReadTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Risk: idle/duplicate hints cause data reads, missed hints lose a durable
// cursor, or a replay backlog bypasses current key authority. Query barriers
// establish the revocation interleaving; injected waits establish heartbeat due.
func TestSubscriptionIdleReadsCoalesceAndCatchUpWithCurrentAuthority(t *testing.T) {
	for _, revokeBacklog := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoke_backlog=%t", revokeBacklog), func(t *testing.T) {
			d, agent := subscriptionPlanReader(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			trace := &subscriptionReadTrace{paused: make(chan struct{}), release: make(chan struct{})}
			defer func() {
				select {
				case <-trace.release:
				default:
					close(trace.release)
				}
			}()
			cfg := d.App.Config()
			cfg.ConnConfig.Tracer = trace
			if _, err := db.ConfigurePool(cfg); err != nil {
				t.Fatal(err)
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			defer db.CloseNotificationConnections(pool)
			source := &subscriptionNoticeSource{steps: make(chan subscriptionNotice), waiting: make(chan struct{})}
			m := New(pool).(*module)
			m.subscriptionWait = func(ctx context.Context, _ db.NotificationListener, after int64, deadline time.Time) error {
				return waitSubscription(ctx, source, after, deadline)
			}
			mux := http.NewServeMux()
			m.Mount(mux)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), agent)))
			}))
			defer srv.Close()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/subscribe?topics=plan", nil)
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("subscription status %d", resp.StatusCode)
			}
			scanner := bufio.NewScanner(resp.Body)
			frame := func() (name, data string, id int64) {
				t.Helper()
				id = -1
				for scanner.Scan() {
					line := scanner.Text()
					if line == "" {
						return
					}
					switch {
					case strings.HasPrefix(line, "id: "):
						var err error
						id, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
						if err != nil {
							t.Fatal(err)
						}
					case strings.HasPrefix(line, "event: "):
						name = strings.TrimPrefix(line, "event: ")
					case strings.HasPrefix(line, "data: "):
						data = strings.TrimPrefix(line, "data: ")
					}
				}
				t.Fatalf("incomplete frame: %v", scanner.Err())
				return
			}
			wait := func() {
				t.Helper()
				select {
				case <-source.waiting:
				case <-ctx.Done():
					t.Fatal("subscription did not reach wait barrier")
				}
			}
			step := func(s subscriptionNotice) {
				t.Helper()
				select {
				case source.steps <- s:
				case <-ctx.Done():
					t.Fatal("subscription did not consume scripted wake")
				}
			}
			name, _, base := frame()
			if name != "stream.ready" || base != logPosition(t, d, agent) {
				t.Fatal("subscription did not establish its durable cursor")
			}
			wait()
			boundaries := trace.boundaries.Load()
			for i := range 64 {
				payload := fmt.Sprintf(`{"tenant_id":%q,"id":%d}`, agent.TenantID, base)
				if i%3 == 0 {
					payload = fmt.Sprintf(`{"tenant_id":%q,"heartbeat_session_id":"ignored"}`, agent.TenantID)
				} else if i%3 == 1 {
					payload = `{"tenant_id":"other","id":9223372036854775807}`
				}
				step(subscriptionNotice{notification: &pgconn.Notification{Payload: payload}})
				wait()
			}
			if trace.pages.Load() != 0 || trace.boundaries.Load() != boundaries {
				t.Fatalf("irrelevant burst caused SQL: pages=%d boundaries=%d", trace.pages.Load(), trace.boundaries.Load()-boundaries)
			}
			// A forged future wake must not become a durable cursor. It reads
			// the real boundary, finds no change, and skips the filtered page.
			step(subscriptionNotice{notification: &pgconn.Notification{Payload: fmt.Sprintf(`{"tenant_id":%q,"id":9223372036854775807}`, agent.TenantID)}})
			wait()
			step(subscriptionNotice{err: context.DeadlineExceeded})
			if name, _, id := frame(); name != "stream.ping" || id != -1 {
				t.Fatal("idle heartbeat changed cursor")
			}
			wait()
			if trace.pages.Load() != 0 {
				t.Fatal("unchanged cursor read a filtered page")
			}
			var last int64
			if err := db.InTenant(dbtest.Seed(ctx), d.App, agent.TenantID, func(tx pgx.Tx) error {
				for range subscriptionBatch + 1 {
					e, err := Append(ctx, tx, tenant.Principal{ID: agent.KeyCreatorID, TenantID: agent.TenantID, Kind: tenant.Person}, Change{
						Type: "agents_plan.changed", After: map[string]any{"principal_id": agent.KeyCreatorID},
					})
					if err != nil {
						return err
					}
					last = e.ID
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if revokeBacklog {
				trace.pauseAt.Store(trace.boundaries.Load() + 2)
			}
			// The source deliberately receives no event notification. The next
			// heartbeat must recover both pages from the original real cursor.
			step(subscriptionNotice{err: context.DeadlineExceeded})
			if name, _, id := frame(); name != "stream.ping" || id != -1 {
				t.Fatal("reconciliation heartbeat changed cursor")
			}
			wantItems := subscriptionBatch + 1
			if revokeBacklog {
				wantItems = subscriptionBatch
			}
			seen, cursors, through := 0, 0, base
			for seen < wantItems || through < base+int64(wantItems) {
				name, data, id := frame()
				switch name {
				case "agents_plan.changed":
					var hint subscriptionHint
					if err := json.Unmarshal([]byte(data), &hint); err != nil || hint.ID != base+int64(seen)+1 || hint.ID != id || hint.Topic != "plan" {
						t.Fatalf("lost or invalid plan hint %s: %v", data, err)
					}
					seen++
				case "stream.cursor":
					cursors++
					through = id
					if id != base+int64(seen) {
						t.Fatal("cursor skipped undelivered hints")
					}
				default:
					t.Fatalf("unexpected frame %s", name)
				}
			}
			if revokeBacklog {
				select {
				case <-trace.paused:
				case <-ctx.Done():
					t.Fatal("second replay page never reached revocation barrier")
				}
			} else {
				wait()
				if through != last || cursors != 2 {
					t.Fatalf("catch-up cursor=%d want=%d pages=%d", through, last, cursors)
				}
			}
			if _, err := d.Admin.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE id=$1`, agent.KeyID); err != nil {
				t.Fatal(err)
			}
			if revokeBacklog {
				close(trace.release)
			} else {
				step(subscriptionNotice{err: context.DeadlineExceeded})
			}
			if scanner.Scan() || scanner.Err() != nil {
				t.Fatalf("revoked stream delivered data or failed to close: %s %v", scanner.Text(), scanner.Err())
			}
			if trace.pages.Load() > 4 || revokeBacklog && trace.pages.Load() != 2 {
				t.Fatalf("unexpected replay reads after revocation: %d", trace.pages.Load())
			}
		})
	}
}

// Risk: the subscription must deliver person-authored project policy changes
// while rejecting unreadable projects, hidden references and unrelated topics.
func TestSubscriptionTopicProjectAndReferenceCeilings(t *testing.T) {
	d, agent, _ := fixture(t)
	ctx := t.Context()
	var owner string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, agent.TenantID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, agent.TenantID, owner, "owner")
	var workspaceRole, projectRole string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'subscription','Subscription') RETURNING id::text`, agent.TenantID).Scan(&workspaceRole); err != nil {
		t.Fatal(err)
	}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'project_subscription','Project subscription') RETURNING id::text`, agent.TenantID).Scan(&projectRole); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'events.subscribe'),($1,$3,'nodes.read'),($1,$3,'reviewpolicy.read')`, agent.TenantID, workspaceRole, projectRole); err != nil {
		t.Fatal(err)
	}
	projects := []string{}
	for _, key := range []string{"VISIBLE-1", "HIDDEN-1"} {
		var id string
		if err := d.Admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,$2,$2 FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, agent.TenantID, key).Scan(&id); err != nil {
			t.Fatal(err)
		}
		projects = append(projects, id)
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'workspace',NULL),($1,$2,$4,'project',$5)`, agent.TenantID, agent.ID, workspaceRole, projectRole, projects[0]); err != nil {
		t.Fatal(err)
	}
	agent.KeyCreatorID = owner
	agent.Scopes = []string{"events.subscribe", "nodes.read", "reviewpolicy.read", "delivery.read"}
	if err := d.Admin.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','fixture',repeat('0',64),$3,$4) RETURNING id::text`, agent.TenantID, agent.ID, agent.Scopes, owner).Scan(&agent.KeyID); err != nil {
		t.Fatal(err)
	}
	actor := tenant.Principal{ID: owner, TenantID: agent.TenantID, Kind: tenant.Person}
	base := logPosition(t, d, agent)
	var visibleID int64
	if err := db.InTenant(dbtest.Seed(ctx), d.App, agent.TenantID, func(tx pgx.Tx) error {
		for i, change := range []Change{
			{Type: "review_policy.changed", NodeID: &projects[0], After: map[string]any{"mode": "other_family"}},
			{Type: "review_policy.changed", NodeID: &projects[1], After: map[string]any{"mode": "other_family"}},
			{Type: "review_policy.changed", NodeID: &projects[0], After: map[string]any{"mode": "other_family", "node_id": projects[0], "project_id": projects[1]}},
			{Type: "delivery.state_changed", NodeID: &projects[0], After: map[string]any{"state": "held"}},
			{Type: "quote.updated", NodeID: &projects[0], After: map[string]any{"private": "never"}},
		} {
			e, err := Append(ctx, tx, actor, change)
			if err != nil {
				return err
			}
			if i == 0 {
				visibleID = e.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Confirm the hidden-reference fixture really retained both references.
	var refs int
	if err := d.Admin.QueryRow(ctx, `SELECT cardinality(node_refs) FROM events WHERE tenant_id=$1 AND id=$2`, agent.TenantID, visibleID+2).Scan(&refs); err != nil || refs != 2 {
		t.Fatalf("hidden reference fixture: refs=%d err=%v", refs, err)
	}
	m := New(d.App).(*module)
	requestCtx := tenant.WithPrincipal(ctx, agent)
	latest := logPosition(t, d, agent)
	hints, through, err := m.subscriptionRead(requestCtx, agent, base, latest, map[string]bool{"policies": true, "delivery": true})
	if err != nil || through != latest || len(hints) != 1 || hints[0].ID != visibleID || hints[0].Topic != "policies" {
		t.Fatalf("scoped hints: %+v cursor=%d err=%v", hints, through, err)
	}
	hints, through, err = m.subscriptionRead(requestCtx, agent, base, latest, map[string]bool{"delivery": true})
	if err != nil || through != latest || len(hints) != 0 {
		t.Fatal("topic filter or delivery role ceiling was bypassed")
	}
	if _, err := d.Admin.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'delivery.read')`, agent.TenantID, projectRole); err != nil {
		t.Fatal(err)
	}
	hints, _, err = m.subscriptionRead(requestCtx, agent, base, latest, map[string]bool{"delivery": true})
	if err != nil || len(hints) != 1 || hints[0].Type != "delivery.state_changed" {
		t.Fatal("live explicit project read grant was not applied")
	}
	if _, err := d.Admin.Exec(ctx, `DELETE FROM role_permissions WHERE tenant_id=$1 AND role_id=$2 AND permission='reviewpolicy.read'`, agent.TenantID, projectRole); err != nil {
		t.Fatal(err)
	}
	hints, _, err = m.subscriptionRead(requestCtx, agent, base, latest, map[string]bool{"policies": true})
	if err != nil || len(hints) != 0 {
		t.Fatal("live role revocation failed")
	}
	if err := authz.Require(authz.BindPool(requestCtx, d.App), "events.subscribe", authz.Scope{}); err != nil {
		t.Fatal("topic revocation incorrectly revoked subscription")
	}
}
