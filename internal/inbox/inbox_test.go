// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

const secretBody = "BODY-SECRET-do-not-leak"

type world struct {
	db                                     *dbtest.DB
	sender, recipient, other, admin, agent tenant.Principal
	outsider                               tenant.Principal
}

func newWorld(t *testing.T) *world {
	t.Helper()
	d := dbtest.Open(t)
	w := &world{db: d}
	tenantA := insertTenant(t, d, "a")
	tenantB := insertTenant(t, d, "b")
	w.sender = insertPrincipal(t, d, tenantA, tenant.Person, "Sender", nil)
	w.recipient = insertPrincipal(t, d, tenantA, tenant.Person, "Recipient", nil)
	w.other = insertPrincipal(t, d, tenantA, tenant.Person, "Other", nil)
	w.admin = insertPrincipal(t, d, tenantA, tenant.Person, "Admin", []string{"admin"})
	w.agent = insertPrincipal(t, d, tenantA, tenant.Agent, "Worker", nil)
	w.outsider = insertPrincipal(t, d, tenantB, tenant.Person, "Outsider", nil)
	return w
}

func insertTenant(t *testing.T, d *dbtest.DB, name string) string {
	t.Helper()
	var id string
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $2) RETURNING id::text`, d.Name+"-"+name, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPrincipal(t *testing.T, d *dbtest.DB, tenantID string, kind tenant.PrincipalKind, name string, roles []string) tenant.Principal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name, Roles: roles}
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals (tenant_id, kind, name, roles)
			VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, tenantID, string(kind), name, roles).Scan(&p.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindLegacy(t, d, tenantID, p.ID)
	if len(roles) == 0 {
		// Handlers see project data only through a binding (ADR-003 P2).
		dbtest.BindRole(t, d, tenantID, p.ID, "member")
	}
	return p
}

func serve(t *testing.T, d *dbtest.DB, ps ...tenant.Principal) (*httptest.Server, *module) {
	t.Helper()
	byID := map[string]tenant.Principal{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	m := newModule(d.App)
	srv := httptest.NewServer((&httpapi.Server{
		Pool:    d.App,
		Modules: []httpapi.Module{m},
		Middleware: []func(http.Handler) http.Handler{
			func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if id := r.Header.Get("X-Principal"); id != "" {
						if p, ok := byID[id]; ok {
							r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
						}
					}
					next.ServeHTTP(w, r)
				})
			},
		},
	}).Handler())
	t.Cleanup(srv.Close)
	return srv, m
}

func do(t *testing.T, srv *httptest.Server, principalID, method, path, body string, hdr http.Header) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if principalID != "" {
		req.Header.Set("X-Principal", principalID)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func mustJSON[T any](t *testing.T, b []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("json: %v body %s", err, b)
	}
	return v
}

func sendJSON(recipient, body, key string, reply *string, expires *time.Time) string {
	payload := map[string]any{
		"recipient_principal_id": recipient,
		"body":                   body,
		"idempotency_key":        key,
	}
	if reply != nil {
		payload["reply_to_id"] = *reply
	}
	if expires != nil {
		payload["expires_at"] = *expires
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func countSQL(t *testing.T, pool *pgxpool.Pool, tenantID, query string, args ...any) int {
	t.Helper()
	var n int
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func eventText(t *testing.T, pool *pgxpool.Pool, tenantID, eventType string) string {
	t.Helper()
	var b strings.Builder
	err := db.InTenant(dbtest.Seed(t.Context()), pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(t.Context(), `SELECT coalesce(before::text,'') || coalesce(after::text,'')
			FROM events WHERE type = $1 ORDER BY id`, eventType)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var part string
			if err := rows.Scan(&part); err != nil {
				return err
			}
			b.WriteString(part)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestMessages(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	baseline := countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events`)
	srv, _ := serve(t, w.db, w.sender, w.recipient, w.other, w.outsider)
	status, body := do(t, srv, "", http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "x", "k", nil, nil), nil)
	if status != 401 || mustJSON[apiErr](t, body).Code != "unauthorized" {
		t.Fatalf("auth %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.sender.ID, secretBody, "self", nil, nil), nil)
	if status != 400 {
		t.Fatalf("self %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.outsider.ID, secretBody, "foreign", nil, nil), nil)
	if status != 404 {
		t.Fatalf("foreign recipient %d %s", status, body)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events`) != baseline {
		t.Fatal("rejected send wrote an event")
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "k1", nil, nil), nil)
	if status != 201 {
		t.Fatalf("send %d %s", status, body)
	}
	first := mustJSON[Message](t, body)
	if first.SenderPrincipalID != w.sender.ID || first.RecipientPrincipalID != w.recipient.ID || first.Body != secretBody || first.SentEventID < 1 || first.AckedAt != nil {
		t.Fatalf("message %+v", first)
	}
	later := time.Now().Add(time.Hour)
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "k1", nil, &later), nil)
	if status != 201 || mustJSON[Message](t, body).ID != first.ID {
		t.Fatalf("replay %d %s", status, body)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.sent'`) != 1 {
		t.Fatal("replay wrote another sent event")
	}
	status, _ = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "other", "k1", nil, nil), nil)
	if status != 409 {
		t.Fatalf("conflict %d", status)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.sender.ID, "from recipient", "k1", nil, nil), nil)
	if status != 201 || mustJSON[Message](t, body).ID == first.ID {
		t.Fatalf("same key other sender %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodGet, "/api/inbox/messages", "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 {
		t.Fatalf("sender inbox %d %s", status, body)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?wait_ms=0", "", nil)
	page := mustJSON[Page](t, body)
	if status != 200 || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextAfter != first.SentEventID {
		t.Fatalf("listen %d %+v", status, page)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?after=0", "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 1 {
		t.Fatal("unacked message was not redelivered")
	}
	status, body = do(t, srv, w.other.ID, http.MethodGet, "/api/inbox/messages", "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("other saw %s", body)
	}
	reply := first.ID
	status, body = do(t, srv, w.other.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.sender.ID, "nope", "reply-other", &reply, nil), nil)
	if status != 404 {
		t.Fatalf("hidden reply %d %s", status, body)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.sender.ID, "reply", "reply-1", &reply, nil), nil)
	if status != 201 || mustJSON[Message](t, body).ReplyToID == nil || *mustJSON[Message](t, body).ReplyToID != first.ID {
		t.Fatalf("reply %d %s", status, body)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages/"+first.ID+"/ack", "", nil)
	if status != 403 {
		t.Fatalf("sender ack %d %s", status, body)
	}
	status, body = do(t, srv, w.other.ID, http.MethodPost, "/api/inbox/messages/"+first.ID+"/ack", "", nil)
	if status != 404 {
		t.Fatalf("other ack %d", status)
	}
	status, body = do(t, srv, w.outsider.ID, http.MethodPost, "/api/inbox/messages/"+first.ID+"/ack", "", nil)
	if status != 404 {
		t.Fatalf("cross-tenant ack %d", status)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/messages/"+first.ID+"/ack", "", nil)
	acked := mustJSON[Message](t, body)
	if status != 200 || acked.AckedAt == nil || acked.Body != secretBody {
		t.Fatalf("ack %d %s", status, body)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/messages/"+first.ID+"/ack", "", nil)
	if status != 200 || mustJSON[Message](t, body).AckedAt == nil {
		t.Fatalf("ack replay %d %s", status, body)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.acked'`) != 1 {
		t.Fatal("ack was not idempotent")
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?after=0", "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("acked still pending %s", body)
	}
	sent := eventText(t, w.db.App, w.sender.TenantID, "inbox.sent")
	ackedText := eventText(t, w.db.App, w.sender.TenantID, "inbox.acked")
	if strings.Contains(sent, secretBody) || strings.Contains(ackedText, secretBody) || strings.Contains(sent, `"body"`) {
		t.Fatalf("event leaked body %s %s", sent, ackedText)
	}
	var leaked int
	if err := w.db.App.QueryRow(t.Context(), `SELECT count(*) FROM inbox_messages`).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked != 0 {
		t.Fatal("RLS hid nothing")
	}
	if countSQL(t, w.db.App, w.outsider.TenantID, `SELECT count(*) FROM inbox_messages`) != 0 {
		t.Fatal("tenant leak")
	}

	for i, key := range []string{"p1", "p2", "p3"} {
		status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, key, key, nil, nil), nil)
		if status != 201 {
			t.Fatalf("page send %d %d %s", i, status, body)
		}
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?limit=2", "", nil)
	page = mustJSON[Page](t, body)
	if status != 200 || len(page.Items) != 2 {
		t.Fatalf("page %d %+v", status, page)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?limit=2&after="+strconv.FormatInt(page.NextAfter, 10), "", nil)
	rest := mustJSON[Page](t, body)
	if status != 200 || len(rest.Items) != 1 || rest.Items[0].SentEventID <= page.NextAfter {
		t.Fatalf("next page %+v", rest)
	}
	exp := time.Now().Add(time.Hour)
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "expires", "exp", nil, &exp), nil)
	if status != 201 {
		t.Fatalf("exp send %d %s", status, body)
	}
	expID := mustJSON[Message](t, body).ID
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_messages
			SET created_at = clock_timestamp() - interval '2 minutes',
			    expires_at = clock_timestamp() - interval '1 minute'
			WHERE id = $1::uuid`, expID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?after="+strconv.FormatInt(rest.NextAfter, 10), "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("expired still listed %s", body)
	}
	for _, path := range []string{"?wait_ms=30001", "?limit=0", "?limit=201", "?after=-1", "?wait_ms=-1"} {
		status, _ = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages"+path, "", nil)
		if status != 400 {
			t.Fatalf("%s: %d", path, status)
		}
	}
	status, _ = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", `{"recipient_principal_id":"`+w.recipient.ID+`","body":"a\u0000b","idempotency_key":"nul"}`, nil)
	if status != 400 {
		t.Fatalf("nul %d", status)
	}
	status, _ = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, strings.Repeat("a", 65537), "long", nil, nil), nil)
	if status != 400 {
		t.Fatalf("long %d", status)
	}
}

func TestAgentScope(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	srv, _ := serve(t, w.db, w.agent, w.recipient)
	status, _ := do(t, srv, w.agent.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "x", "no-key", nil, nil), nil)
	if status != 403 {
		t.Fatalf("missing key %d", status)
	}
	denied := insertKey(t, w.db, w.agent, []string{"events:read"}, "secret-denied")
	status, _ = do(t, srv, w.agent.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "x", "bad-scope", nil, nil), headerAuth(denied))
	if status != 403 {
		t.Fatalf("bad scope %d", status)
	}
	allowed := insertKey(t, w.db, w.agent, []string{"inbox.send"}, "secret-allowed")
	status, body := do(t, srv, w.agent.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "scoped", "ok-scope", nil, nil), headerAuth(allowed))
	if status != 201 || mustJSON[Message](t, body).SenderPrincipalID != w.agent.ID {
		t.Fatalf("scoped send %d %s", status, body)
	}
	revoked := insertKey(t, w.db, w.agent, []string{"inbox.send"}, "secret-revoked")
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.agent.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at = now() WHERE hash = $1`, hashSecret("secret-revoked"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, _ = do(t, srv, w.agent.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "x", "revoked", nil, nil), headerAuth(revoked))
	if status != 403 {
		t.Fatalf("revoked %d", status)
	}
}

func insertKey(t *testing.T, d *dbtest.DB, p tenant.Principal, scopes []string, secret string) string {
	t.Helper()
	compact := strings.ReplaceAll(p.TenantID, "-", "")
	prefix := compact + hex.EncodeToString([]byte(secret))
	if len(prefix) < 48 {
		prefix += strings.Repeat("ab", 48)
	}
	prefix = prefix[:48]
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash, scopes,created_by_principal_id)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6,(SELECT id FROM principals WHERE tenant_id=$1::uuid AND kind='person' ORDER BY created_at,id LIMIT 1))`, p.TenantID, p.ID, p.Name+"-"+secret, prefix, hashSecret(secret), scopes)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return "aeon_" + prefix + "_" + secret
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func headerAuth(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}

func TestStreamAndPoll(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	srv, mod := serve(t, w.db, w.sender, w.recipient)
	status, body := do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?wait_ms=0", "", nil)
	if status != 200 || len(mustJSON[Page](t, body).Items) != 0 {
		t.Fatalf("immediate %d %s", status, body)
	}
	// Pause after LISTEN and the empty page, then send. The real listener must
	// consume the queued notification; a timeout followed by a read cannot pass.
	waiting := make(chan struct{}, 1)
	resume := make(chan struct{})
	notified := make(chan error, 1)
	mod.waitNotify = func(ctx context.Context, conn *pgx.Conn, tenantID string, deadline time.Time) error {
		waiting <- struct{}{}
		select {
		case <-resume:
		case <-ctx.Done():
			return ctx.Err()
		}
		err := waitTenantNotify(ctx, conn, tenantID, deadline)
		notified <- err
		return err
	}
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	found := make(chan Page, 1)
	go func() {
		status, body := do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/messages?wait_ms=30000", "", nil)
		if status != 200 {
			found <- Page{}
			return
		}
		found <- mustJSON[Page](t, body)
	}()
	select {
	case <-waiting:
	case page := <-found:
		t.Fatalf("long poll returned before waiting: %+v", page)
	case <-time.After(10 * time.Second):
		t.Fatal("long poll never entered its notification wait")
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "live", nil, nil), nil)
	if status != 201 {
		t.Fatal(status, string(body))
	}
	sent := mustJSON[Message](t, body)
	close(resume)
	select {
	case page := <-found:
		if len(page.Items) != 1 || page.Items[0].ID != sent.ID || page.Items[0].Body != secretBody {
			t.Fatalf("long poll %+v", page)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("long poll timed out")
	}
	if err := <-notified; err != nil {
		t.Fatalf("long poll read without its notification: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.recipient.ID)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	if kind, text := readFrame(t, sc); kind != "comment" || text != "connected" {
		t.Fatalf("connected %s %s", kind, text)
	}
	kind, data := readFrame(t, sc)
	if kind != "message" {
		t.Fatalf("replay %s %s", kind, data)
	}
	replay := mustJSON[Message](t, []byte(data))
	if replay.ID != sent.ID || replay.Body != secretBody {
		t.Fatalf("replay %+v", replay)
	}
	resp.Body.Close()

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.recipient.ID)
	req.Header.Set("Last-Event-ID", strconv.FormatInt(sent.SentEventID, 10))
	live, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Body.Close()
	liveScan := bufio.NewScanner(live.Body)
	liveScan.Buffer(make([]byte, 4096), 1<<20)
	if kind, text := readFrame(t, liveScan); kind != "comment" || text != "connected" {
		t.Fatalf("resume connected %s %s", kind, text)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "second", "live-2", nil, nil), nil)
	if status != 201 {
		t.Fatal(string(body))
	}
	second := mustJSON[Message](t, body)
	kind, data = readFrame(t, liveScan)
	got := mustJSON[Message](t, []byte(data))
	if kind != "message" || got.ID != second.ID {
		t.Fatalf("live %s %+v", kind, got)
	}
	live.Body.Close()

	status, _ = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/messages/"+sent.ID+"/ack", "", nil)
	if status != 200 {
		t.Fatal(status)
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream?after=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.recipient.ID)
	again, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Body.Close()
	againScan := bufio.NewScanner(again.Body)
	againScan.Buffer(make([]byte, 4096), 1<<20)
	readFrame(t, againScan)
	kind, data = readFrame(t, againScan)
	if kind != "message" || mustJSON[Message](t, []byte(data)).ID == sent.ID {
		t.Fatalf("acked replayed %s %s", kind, data)
	}
	again.Body.Close()

	mod.heartbeat = 150 * time.Millisecond
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/inbox/stream?after=999999", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Principal", w.recipient.ID)
	beat, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer beat.Body.Close()
	beatScan := bufio.NewScanner(beat.Body)
	readFrame(t, beatScan)
	if kind, text := readFrame(t, beatScan); kind != "comment" || text != "keepalive" {
		t.Fatalf("heartbeat %s %s", kind, text)
	}
	for _, id := range []string{"-1", "abc", "1,2"} {
		req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/api/inbox/stream", nil)
		req.Header.Set("X-Principal", w.recipient.ID)
		req.Header.Set("Last-Event-ID", id)
		resp, err = srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("resume %q: %d", id, resp.StatusCode)
		}
	}
}

func readFrame(t *testing.T, sc *bufio.Scanner) (kind, data string) {
	t.Helper()
	var id, event, payload string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if payload != "" {
				var msg Message
				if event != "message" || json.Unmarshal([]byte(payload), &msg) != nil || strconv.FormatInt(msg.SentEventID, 10) != id {
					t.Fatalf("bad frame id=%s event=%s data=%s", id, event, payload)
				}
				return "message", payload
			}
			continue
		}
		if v, ok := strings.CutPrefix(line, ": "); ok && id == "" && payload == "" {
			return "comment", v
		}
		if v, ok := strings.CutPrefix(line, "id: "); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			payload = v
		}
		if event != "" && event != "message" && payload != "" {
			return event, payload
		}
	}
	t.Fatalf("stream ended: %v", sc.Err())
	return "", ""
}

func TestTargets(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	srv, _ := serve(t, w.db, w.sender, w.recipient, w.admin, w.other)
	status, body := do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"pull"}`, nil)
	if status != 201 {
		t.Fatalf("pull %d %s", status, body)
	}
	pull := mustJSON[Target](t, body)
	if !pull.Enabled || pull.Kind != "pull" || pull.WebhookURL != nil {
		t.Fatalf("pull %+v", pull)
	}
	status, _ = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"pull"}`, nil)
	if status != 409 {
		t.Fatalf("second pull %d", status)
	}
	if countSQL(t, w.db.App, w.recipient.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.target_created'`) != 1 {
		t.Fatal("conflict wrote a target event")
	}
	status, _ = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"webhook","webhook_url":"https://1.1.1.1/hook"}`, nil)
	if status != 403 {
		t.Fatalf("foreign webhook %d", status)
	}
	status, body = do(t, srv, w.admin.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"webhook","webhook_url":"https://1.1.1.1/hook"}`, nil)
	if status != 201 {
		t.Fatalf("admin webhook %d %s", status, body)
	}
	hook := mustJSON[Target](t, body)
	if hook.WebhookURL == nil || *hook.WebhookURL != "https://1.1.1.1/hook" || hook.PrincipalID != w.recipient.ID {
		t.Fatalf("hook %+v", hook)
	}
	created := eventText(t, w.db.App, w.recipient.TenantID, "inbox.target_created")
	if strings.Contains(created, "1.1.1.1") || strings.Contains(created, "webhook_url") {
		t.Fatalf("target event leaked url %s", created)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/targets", "", nil)
	listed := mustJSON[[]Target](t, body)
	if status != 200 || len(listed) != 2 {
		t.Fatalf("list %d %s", status, body)
	}
	status, body = do(t, srv, w.admin.ID, http.MethodGet, "/api/inbox/targets", "", nil)
	if status != 200 || len(mustJSON[[]Target](t, body)) != 0 {
		t.Fatalf("admin list %s", body)
	}
	status, _ = do(t, srv, w.other.ID, http.MethodDelete, "/api/inbox/targets/"+hook.ID, "", nil)
	if status != 403 {
		t.Fatalf("other delete %d", status)
	}
	status, _ = do(t, srv, w.recipient.ID, http.MethodDelete, "/api/inbox/targets/"+hook.ID, "", nil)
	if status != 204 {
		t.Fatalf("delete %d", status)
	}
	status, _ = do(t, srv, w.recipient.ID, http.MethodDelete, "/api/inbox/targets/"+hook.ID, "", nil)
	if status != 204 {
		t.Fatalf("delete again %d", status)
	}
	if countSQL(t, w.db.App, w.recipient.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.target_disabled'`) != 1 {
		t.Fatal("disable was not idempotent")
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodGet, "/api/inbox/targets", "", nil)
	var still bool
	for _, item := range mustJSON[[]Target](t, body) {
		if item.ID == hook.ID && !item.Enabled {
			still = true
		}
	}
	if !still {
		t.Fatalf("disabled target missing %s", body)
	}
	status, _ = do(t, srv, w.recipient.ID, http.MethodDelete, "/api/inbox/targets/not-a-uuid", "", nil)
	if status != 404 {
		t.Fatalf("bad id %d", status)
	}
	for _, raw := range []string{
		`{"principal_id":"` + w.recipient.ID + `","kind":"webhook","webhook_url":"http://1.1.1.1/hook"}`,
		`{"principal_id":"` + w.recipient.ID + `","kind":"webhook","webhook_url":"https://127.0.0.1/hook"}`,
		`{"principal_id":"` + w.recipient.ID + `","kind":"pull","webhook_url":"https://1.1.1.1/hook"}`,
		`{"principal_id":"` + w.recipient.ID + `","kind":"email"}`,
	} {
		status, _ = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/targets", raw, nil)
		if status != 400 {
			t.Fatalf("bad target %s -> %d", raw, status)
		}
	}
}

func TestWebhookPolicy(t *testing.T) {
	t.Parallel()
	bad := []string{
		"",
		"http://1.1.1.1/hook",
		"https://user:pass@1.1.1.1/hook",
		"https://127.0.0.1/hook",
		"https://127.0.0.2:8443/hook",
		"https://10.1.2.3/hook",
		"https://192.168.1.1/hook",
		"https://172.16.0.1/hook",
		"https://169.254.169.254/latest",
		"https://100.100.100.200/latest",
		"https://[::1]/hook",
		"https://[::ffff:127.0.0.1]/hook",
		"https://[::ffff:169.254.169.254]/latest",
		"https://[fd00:ec2::254]/",
		"https://[fe80::1]/",
		"https://localhost/hook",
		"https://metadata.google.internal/computeMetadata/v1/",
		"https://foo.cluster.local/hook",
		"https://0177.0.0.1/hook",
		"https://2130706433/hook",
		"https://0x7f.0.0.1/hook",
		"https://1.1.1.1:0/hook",
		"https://1.1.1.1/hook#frag",
		"https://192.0.2.1/hook",
		"https://0.0.0.0/hook",
	}
	for _, raw := range bad {
		if err := validateWebhookURL(t.Context(), raw); !errors.Is(err, errWebhookURL) {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	if err := validateWebhookURL(t.Context(), "https://1.1.1.1/hook"); err != nil {
		t.Fatal(err)
	}
	if err := allowAll([]net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("10.0.0.1")}); !errors.Is(err, errWebhookURL) {
		t.Fatalf("mixed %v", err)
	}
	if _, err := dialPublic(t.Context(), "tcp", "127.0.0.1:1"); !errors.Is(err, errWebhookURL) {
		t.Fatalf("dial %v", err)
	}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		http.Redirect(w, r, "http://169.254.169.254/latest", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	client := newWebhookClient((&net.Dialer{Timeout: time.Second}).DialContext)
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"message_id":"x","event_id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, errWebhookRedirect) || hits != 1 {
		t.Fatalf("redirect err=%v hits=%d", err, hits)
	}
	if backoff(1) != time.Second || backoff(2) != 2*time.Second || backoff(7) != 60*time.Second || backoff(30) != 60*time.Second {
		t.Fatalf("%s %s %s", backoff(1), backoff(2), backoff(7))
	}
}

func TestWakeDelivery(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	srv, _ := serve(t, w.db, w.sender, w.recipient)
	status, body := do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"webhook","webhook_url":"https://1.1.1.1/hook"}`, nil)
	if status != 201 {
		t.Fatalf("target %d %s", status, body)
	}
	target := mustJSON[Target](t, body)
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "wake", nil, nil), nil)
	if status != 201 {
		t.Fatal(string(body))
	}
	msg := mustJSON[Message](t, body)
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_wakes`) != 1 {
		t.Fatal("wake was not queued")
	}
	queued := eventText(t, w.db.App, w.sender.TenantID, "inbox.wake_queued")
	if strings.Contains(queued, secretBody) || strings.Contains(queued, "1.1.1.1") {
		t.Fatalf("queued event leaked %s", queued)
	}
	poster := &capturePoster{status: 204}
	worker := NewWorker(w.db.App, WorkerOptions{MaxAttempts: 2})
	worker.poster = poster
	n, err := worker.ProcessOnce(t.Context())
	if err != nil || n != 1 || poster.calls != 1 {
		t.Fatalf("deliver n=%d calls=%d err=%v", n, poster.calls, err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(poster.bodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || string(payload["message_id"]) != `"`+msg.ID+`"` {
		t.Fatalf("payload %s", poster.bodies[0])
	}
	if strings.Contains(string(poster.bodies[0]), secretBody) {
		t.Fatalf("webhook carried the body %s", poster.bodies[0])
	}
	if _, ok := payload["event_id"]; !ok {
		t.Fatalf("payload %s", poster.bodies[0])
	}
	delivered := eventText(t, w.db.App, w.sender.TenantID, "inbox.wake_delivered")
	if strings.Contains(delivered, secretBody) || strings.Contains(delivered, "1.1.1.1") {
		t.Fatalf("delivered event leaked %s", delivered)
	}
	var done bool
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT delivered_at IS NOT NULL AND last_status = 204 FROM inbox_wakes WHERE message_id = $1`, msg.ID).Scan(&done)
	}); err != nil || !done {
		t.Fatalf("delivered row %v %v", done, err)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "wake", nil, nil), nil)
	if status != 201 || mustJSON[Message](t, body).ID != msg.ID {
		t.Fatal("replay")
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM inbox_wakes`) != 1 {
		t.Fatal("replay queued another wake")
	}

	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, "fail", "wake-fail", nil, nil), nil)
	if status != 201 {
		t.Fatal(string(body))
	}
	poster.status = 500
	poster.calls = 0
	if _, err := worker.ProcessOnce(t.Context()); err != nil || poster.calls != 1 {
		t.Fatalf("fail call %d %v", poster.calls, err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_wakes SET next_attempt_at = clock_timestamp() - interval '1 second'
			WHERE delivered_at IS NULL`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(t.Context()); err != nil || poster.calls != 2 {
		t.Fatalf("second fail %d %v", poster.calls, err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_wakes SET next_attempt_at = clock_timestamp() - interval '1 second'
			WHERE delivered_at IS NULL`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.ProcessOnce(t.Context()); err != nil || poster.calls != 2 {
		t.Fatalf("abandoned still called %d %v", poster.calls, err)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.wake_abandoned'`) != 1 {
		t.Fatal("missing abandoned event")
	}

	status, _ = do(t, srv, w.recipient.ID, http.MethodDelete, "/api/inbox/targets/"+target.ID, "", nil)
	if status != 204 {
		t.Fatal(status)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_wakes SET attempts = 100 WHERE delivered_at IS NULL`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var hits int
	hookSrv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hits++
	}))
	t.Cleanup(hookSrv.Close)
	rawURL := "https://" + strings.TrimPrefix(hookSrv.URL, "http://") + "/hook"
	if _, err := w.db.Admin.Exec(t.Context(), `INSERT INTO inbox_delivery_targets (tenant_id, principal_id, kind, webhook_url)
		VALUES ($1::uuid, $2::uuid, 'webhook', $3)`, w.recipient.TenantID, w.recipient.ID, rawURL); err != nil {
		t.Fatal(err)
	}
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "loop", nil, nil), nil)
	if status != 201 {
		t.Fatal(string(body))
	}
	safe := NewWorker(w.db.App, WorkerOptions{})
	if _, err := safe.ProcessOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if hits != 0 {
		t.Fatalf("loopback webhook was contacted %d times", hits)
	}

	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_wakes SET attempts = 100 WHERE delivered_at IS NULL`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	status, body = do(t, srv, w.recipient.ID, http.MethodPost, "/api/inbox/targets", `{"principal_id":"`+w.recipient.ID+`","kind":"webhook","webhook_url":"https://8.8.8.8/hook"}`, nil)
	if status != 201 {
		t.Fatalf("second hook %d %s", status, body)
	}
	exp := time.Now().Add(time.Hour)
	status, body = do(t, srv, w.sender.ID, http.MethodPost, "/api/inbox/messages", sendJSON(w.recipient.ID, secretBody, "expired-wake", nil, &exp), nil)
	if status != 201 {
		t.Fatal(string(body))
	}
	expiredID := mustJSON[Message](t, body).ID
	if err := db.InTenant(dbtest.Seed(t.Context()), w.db.App, w.sender.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE inbox_messages
			SET created_at = clock_timestamp() - interval '2 minutes',
			    expires_at = clock_timestamp() - interval '1 minute'
			WHERE id = $1::uuid`, expiredID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	poster.calls = 0
	if _, err := worker.ProcessOnce(t.Context()); err != nil || poster.calls != 0 {
		t.Fatalf("expired wake posted calls=%d err=%v", poster.calls, err)
	}
	if countSQL(t, w.db.App, w.sender.TenantID, `SELECT count(*) FROM events WHERE type = 'inbox.wake_dropped'`) < 1 {
		t.Fatal("expired wake was not dropped")
	}
	if strings.Contains(eventText(t, w.db.App, w.sender.TenantID, "inbox.wake_dropped"), secretBody) {
		t.Fatal("dropped wake leaked the body")
	}
}

type capturePoster struct {
	mu     sync.Mutex
	bodies [][]byte
	status int
	calls  int
}

func (s *capturePoster) Post(_ context.Context, _ string, body []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.bodies = append(s.bodies, append([]byte(nil), body...))
	return s.status, nil
}

type apiErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
