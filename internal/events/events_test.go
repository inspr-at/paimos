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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

func fixture(t *testing.T) (*dbtest.DB, tenant.Principal, tenant.Principal) {
	t.Helper()
	d := dbtest.Open(t)
	ps := make([]tenant.Principal, 2)
	for i := range ps {
		p := &ps[i]
		p.Kind = tenant.Agent
		if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES($1,'Test') RETURNING id::text`, fmt.Sprintf("event%d", i)).Scan(&p.TenantID); err != nil {
			t.Fatal(err)
		}
		if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Writer') RETURNING id::text`, p.TenantID).Scan(&p.ID)
		}); err != nil {
			t.Fatal(err)
		}
	}
	return d, ps[0], ps[1]
}

// Bootstrap writes are audited too. Tests assert their own event sequence from
// the observed starting position rather than depending on an empty tenant log.
func logPosition(t *testing.T, d *dbtest.DB, p tenant.Principal) int64 {
	t.Helper()
	var n int64
	if err := d.Admin.QueryRow(t.Context(), `SELECT coalesce(max(id),0) FROM events WHERE tenant_id=$1`, p.TenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func appendEvents(t *testing.T, d *dbtest.DB, p tenant.Principal, n int) []Event {
	t.Helper()
	result := make([]Event, 0, n)
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		for i := range n {
			e, err := (Writer{}).Append(t.Context(), tx, p, Change{Type: "test.changed", After: map[string]any{"title": fmt.Sprintf("Snapshot %d\nsecond line", i)}})
			if err != nil {
				return err
			}
			result = append(result, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestWriterAtomicityIsolationAndAppendOnly(t *testing.T) {
	d, a, b := fixture(t)
	base, foreignBase := logPosition(t, d, a), logPosition(t, d, b)
	sentinel := errors.New("rollback")
	err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		if _, err := Append(t.Context(), tx, a, Change{Type: "test.changed", Before: map[string]string{"value": "before"}, After: map[string]string{"value": "after"}}); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	first := appendEvents(t, d, a, 1)[0]
	if first.ID != base+1 || first.Before != nil || first.NodeID != nil || first.UndoOf != nil {
		t.Fatalf("invalid first event %+v", first)
	}
	foreign := appendEvents(t, d, b, 1)[0]
	if foreign.ID != foreignBase+1 {
		t.Fatal("IDs are not tenant-local")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&n); err != nil {
			return err
		}
		if int64(n) != base+1 {
			t.Fatalf("RLS exposed %d events", n)
		}
		for _, query := range []string{`UPDATE events SET type='test.modified'`, `DELETE FROM events`} {
			tag, err := tx.Exec(t.Context(), query)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 0 {
				t.Fatal("append-only RLS permitted change")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`UPDATE events SET type='test.modified'`, `DELETE FROM events`} {
		if _, err := d.Admin.Exec(t.Context(), query); err == nil {
			t.Fatal("append-only trigger permitted privileged mutation")
		}
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		_, err := Append(t.Context(), tx, b, Change{Type: "test.changed", After: map[string]string{"x": "x"}})
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant event inserted")
	}
	for _, change := range []Change{{Type: "test.changed"}, {Type: "test.changed", Before: json.RawMessage("null")}, {Type: "test.changed", After: make(chan int)}, {Type: "bad\ninjection", After: map[string]string{"x": "x"}}} {
		err = db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error { _, err := Append(t.Context(), tx, a, change); return err })
		if err == nil {
			t.Fatalf("invalid change accepted %+v", change)
		}
	}
	next := appendEvents(t, d, a, 1)[0]
	if next.ID != base+2 {
		t.Fatal("failed append consumed an ID")
	}
}

func TestCounterWaitsForCommitAndNotificationContainsOnlyHint(t *testing.T) {
	d, a, _ := fixture(t)
	base := logPosition(t, d, a)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, d.App.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err = conn.Exec(ctx, "LISTEN aeon_events"); err != nil {
		t.Fatal(err)
	}
	written := make(chan Event, 1)
	firstPID := make(chan int, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- db.InTenant(dbtest.Seed(ctx), d.App, a.TenantID, func(tx pgx.Tx) error {
			firstPID <- int(tx.Conn().PgConn().PID())
			e, err := Append(ctx, tx, a, Change{Type: "test.changed", After: map[string]string{"sensitive_content": "snapshot"}})
			if err != nil {
				return err
			}
			written <- e
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case e := <-written:
		if e.ID != base+1 {
			t.Fatal(e.ID)
		}
	case err := <-firstDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondDone := make(chan error, 1)
	secondPID := make(chan int, 1)
	go func() {
		secondDone <- db.InTenant(dbtest.Seed(ctx), d.App, a.TenantID, func(tx pgx.Tx) error {
			secondPID <- int(tx.Conn().PgConn().PID())
			_, err := Append(ctx, tx, a, Change{Type: "test.changed", After: map[string]string{"x": "second"}})
			return err
		})
	}()
	var waiter int
	select {
	case waiter = <-secondPID:
	case err := <-secondDone:
		t.Fatalf("second transaction failed before append: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := dbtest.WaitForBlocked(ctx, d.Admin, waiter, <-firstPID, "INSERT INTO events"); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	_, err = conn.WaitForNotification(waitCtx)
	stop()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("notification before commit: %v", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{base + 1, base + 2} {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var hint map[string]json.RawMessage
		if err := json.Unmarshal([]byte(n.Payload), &hint); err != nil {
			t.Fatal(err)
		}
		if len(hint) != 2 || string(hint["id"]) != strconv.FormatInt(want, 10) || string(hint["tenant_id"]) != strconv.Quote(a.TenantID) {
			t.Fatalf("invalid hint %s", n.Payload)
		}
	}
	m := New(d.App).(*module)
	got, err := m.read(ctx, a, "", 0, 50)
	if err != nil || len(got.Items) != 2 || got.Items[0].ID != base+1 || got.Items[1].ID != base+2 {
		t.Fatalf("bad commit order %+v %v", got, err)
	}
}

func testServer(t *testing.T, d *dbtest.DB, a, b tenant.Principal) *httptest.Server {
	t.Helper()
	handler := (&httpapi.Server{Pool: d.App, Modules: []httpapi.Module{New(d.App)}, Middleware: []func(http.Handler) http.Handler{
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := a
				if r.Header.Get("X-Test-Tenant") == "b" {
					p = b
				}
				next.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), p)))
			})
		},
	}}).Handler()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func stream(t *testing.T, srv *httptest.Server, after string) (*http.Response, *bufio.Scanner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Second)
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response %d %v", resp.StatusCode, resp.Header)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 4096), 1<<20)
	if !sc.Scan() || sc.Text() != ": connected" || !sc.Scan() || sc.Text() != "" {
		t.Fatalf("missing connection comment: %v", sc.Err())
	}
	return resp, sc
}

func nextEvent(t *testing.T, sc *bufio.Scanner) Event {
	t.Helper()
	var id, typ, data string
	for sc.Scan() {
		line := sc.Text()
		if line == "" && data != "" {
			break
		}
		if value, ok := strings.CutPrefix(line, "id: "); ok {
			id = value
		}
		if value, ok := strings.CutPrefix(line, "event: "); ok {
			typ = value
		}
		if value, ok := strings.CutPrefix(line, "data: "); ok {
			data = value
		}
	}
	var e Event
	if err := json.Unmarshal([]byte(data), &e); err != nil {
		t.Fatalf("read SSE: %v (scanner: %v)", err, sc.Err())
	}
	if id != strconv.FormatInt(e.ID, 10) || typ != e.Type {
		t.Fatalf("invalid framing %s %s %+v", id, typ, e)
	}
	return e
}

func TestSSEReplayLiveResumeAndPoolCapacity(t *testing.T) {
	d, a, b := fixture(t)
	replayed := appendEvents(t, d, a, 205)
	appendEvents(t, d, b, 3)
	srv := testServer(t, d, a, b)
	resp, sc := stream(t, srv, "")
	for _, want := range replayed {
		e := nextEvent(t, sc)
		if e.ID != want.ID || e.ActorPrincipalID != a.ID || e.Type != "test.changed" {
			t.Fatalf("bad replay event %+v", e)
		}
	}
	resp.Body.Close()
	// More streams than query-pool slots must still replay and accept writes.
	scanners := make([]*bufio.Scanner, 0, 5)
	responses := make([]*http.Response, 0, 5)
	for range 5 {
		r, s := stream(t, srv, strconv.FormatInt(replayed[len(replayed)-1].ID, 10))
		responses = append(responses, r)
		scanners = append(scanners, s)
	}
	appendEvents(t, d, b, 1)
	own := appendEvents(t, d, a, 1)[0]
	for _, scanner := range scanners {
		e := nextEvent(t, scanner)
		if e.ID != own.ID || e.ActorPrincipalID != a.ID || !e.At.Equal(own.At) {
			t.Fatalf("bad live event %+v", e)
		}
	}
	for _, r := range responses {
		r.Body.Close()
	}
	last := appendEvents(t, d, a, 1)[0]
	resumed, scanner := stream(t, srv, strconv.FormatInt(own.ID, 10))
	defer resumed.Body.Close()
	if e := nextEvent(t, scanner); e.ID != last.ID {
		t.Fatalf("bad resumed event %+v", e)
	}
}

func TestSSEHeartbeatAndInvalidResume(t *testing.T) {
	d, a, b := fixture(t)
	srv := testServer(t, d, a, b)
	for _, id := range []string{"-1", "abc", "9223372036854775808", "", "1,2"} {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", srv.URL+"/api/events/stream", nil)
		req.Header.Set("Last-Event-ID", id)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("resume %q: %d", id, resp.StatusCode)
		}
	}
	resp, sc := stream(t, srv, "")
	defer resp.Body.Close()
	if !sc.Scan() || sc.Text() != ": keepalive" {
		t.Fatalf("missing heartbeat: %q %v", sc.Text(), sc.Err())
	}
	appended := appendEvents(t, d, a, 1)[0]
	if e := nextEvent(t, sc); e.ID != appended.ID {
		t.Fatal(e.ID)
	}
}

func TestUndoAdapterRollbackAndUnknownType(t *testing.T) {
	d, a, _ := fixture(t)
	event := appendEvents(t, d, a, 1)[0]
	for i, opts := range [][]Option{nil, {WithUndo("test.changed", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, e Event) (Change, error) {
		if _, err := tx.Exec(ctx, `UPDATE principals SET name='Should rollback' WHERE id=$1`, p.ID); err != nil {
			return Change{}, err
		}
		return Change{}, ErrConflict
	})}, {WithUndo("test.changed", func(ctx context.Context, tx pgx.Tx, p tenant.Principal, e Event) (Change, error) {
		if _, err := tx.Exec(ctx, `UPDATE principals SET name='Should rollback' WHERE id=$1`, p.ID); err != nil {
			return Change{}, err
		}
		return Change{Type: "invalid"}, nil
	})}} {
		mux := http.NewServeMux()
		New(d.App, opts...).Mount(mux)
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/events/%d/undo", event.ID), nil).WithContext(tenant.WithPrincipal(t.Context(), a))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != []int{409, 409, 500}[i] {
			t.Fatalf("unexpected undo %d %s", w.Code, w.Body.String())
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, a.TenantID, func(tx pgx.Tx) error {
		var name string
		if err := tx.QueryRow(t.Context(), `SELECT name FROM principals WHERE id=$1`, a.ID).Scan(&name); err != nil {
			return err
		}
		if name != "Writer" {
			t.Fatal("failed undo did not roll back")
		}
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&count); err != nil {
			return err
		}
		if int64(count) != event.ID {
			t.Fatal("failed undo changed history")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUUIDAndIDValidation(t *testing.T) {
	for _, s := range []string{"", "123", "00000000x0000x0000x0000x000000000001", "00000000-0000-0000-0000-00000000000g"} {
		if validUUID(s) {
			t.Fatalf("accepted invalid UUID %q", s)
		}
	}
	if !validUUID("AAAAAAAA-1234-5678-9012-123456789012") {
		t.Fatal("rejected valid UUID")
	}
	for _, s := range []string{"", "-1", "+1", "1.0", " 1", "9223372036854775808"} {
		if _, err := parseID(s); err == nil {
			t.Fatalf("accepted invalid ID %q", s)
		}
	}
}
