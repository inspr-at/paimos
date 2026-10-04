// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// positionServer mounts the middleware the way serve.go does: the route pattern
// is known, and the auth middleware has put the principal on the request unless
// the caller sends X-Anonymous.
func positionServer(t *testing.T, d *dbtest.DB, p tenant.Principal, during func(run int)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	runs := 0
	reply := func(w http.ResponseWriter, r *http.Request) {
		runs++
		if during != nil {
			during(runs)
		}
		w.Header().Set("X-Reply", "kept")
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}, "run": runs})
	}
	mux.HandleFunc("GET /api/models", func(w http.ResponseWriter, r *http.Request) {
		appendEvents(t, d, p, 1)
		httpapi.WriteError(w, http.StatusForbidden, "admins only")
	})
	mux.HandleFunc("GET /api/runs", reply)
	mux.HandleFunc("GET /api/agent-accounts", reply)
	mux.HandleFunc("GET /api/nodes", reply)
	mux.HandleFunc("GET /api/harness-sessions/live", reply)
	mux.HandleFunc("GET /api/health", reply)
	mux.HandleFunc("HEAD /api/runs", reply)
	mux.HandleFunc("POST /api/runs/{runId}/cancel", func(w http.ResponseWriter, r *http.Request) {
		err := db.InTenant(dbtest.Seed(r.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
			_, err := Append(r.Context(), tx, p, Change{Type: "run.cancelled", After: map[string]any{"id": r.PathValue("runId")}})
			return err
		})
		if err != nil {
			httpapi.WriteError(w, http.StatusInternalServerError, "failed")
			return
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "cancelled"})
	})
	mux.HandleFunc("POST /api/runs/{runId}/refuse", func(w http.ResponseWriter, r *http.Request) {
		httpapi.WriteError(w, http.StatusConflict, "only a queued run can be cancelled")
	})
	mux.HandleFunc("DELETE /api/agent-accounts/{accountId}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, r.Pattern = mux.Handler(r)
		if r.Header.Get("X-Anonymous") == "" {
			r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
		}
		PositionMiddleware(d.App)(mux).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func position(t *testing.T, resp *http.Response) (int64, bool) {
	t.Helper()
	raw := resp.Header.Get(PositionHeader)
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		t.Fatalf("%s %q is not a position", PositionHeader, raw)
	}
	return n, true
}

func do(t *testing.T, srv *httptest.Server, method, path string, anonymous bool) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if anonymous {
		req.Header.Set("X-Anonymous", "1")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestPositionNamesTheNewestEventOfTheTenantBeforeAListedRead(t *testing.T) {
	d, a, b := fixture(t)
	base := logPosition(t, d, a)
	srv := positionServer(t, d, a, nil)
	if n, ok := position(t, do(t, srv, "GET", "/api/runs", false)); !ok || n != base {
		t.Fatalf("bootstrap log: position %d, present %v", n, ok)
	}
	appendEvents(t, d, a, 3)
	appendEvents(t, d, b, 5) // another tenant's log never counts
	for _, path := range []string{"/api/runs", "/api/agent-accounts", "/api/nodes", "/api/harness-sessions/live"} {
		if n, ok := position(t, do(t, srv, "GET", path, false)); !ok || n != base+3 {
			t.Fatalf("%s: position %d, present %v; want the tenant counter after three appends", path, n, ok)
		}
	}
}

// A read's position names the snapshot it returned. One that an event committed
// inside is run again, so the body and the position come from the same quiet stretch.
func TestPositionOfAReadNamesTheSnapshotAnInterruptedReadIsRunAgain(t *testing.T) {
	d, a, _ := fixture(t)
	base := logPosition(t, d, a)
	appendEvents(t, d, a, 2)
	srv := positionServer(t, d, a, func(run int) {
		if run == 1 {
			appendEvents(t, d, a, 1)
		}
	})
	resp := do(t, srv, "GET", "/api/runs", false)
	n, ok := position(t, resp)
	if !ok || n != base+3 {
		t.Fatalf("position %d, present %v; want the newest event the second run saw", n, ok)
	}
	var body struct{ Run int }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Run != 2 {
		t.Fatalf("body %+v (%v): the answer must come from the run the position names", body, err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("X-Reply") != "kept" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, headers %v: the buffered answer must reach the client unchanged", resp.StatusCode, resp.Header)
	}
}

// Events that keep committing while every attempt runs leave no snapshot to name:
// the read answers without a position rather than naming one it did not read at.
func TestPositionIsAbsentWhenEventsKeepCommittingDuringTheRead(t *testing.T) {
	d, a, _ := fixture(t)
	appendEvents(t, d, a, 2)
	var runs int
	srv := positionServer(t, d, a, func(int) { runs++; appendEvents(t, d, a, 1) })
	resp := do(t, srv, "GET", "/api/runs", false)
	if n, ok := position(t, resp); ok {
		t.Fatalf("an interrupted read carries position %d", n)
	}
	if resp.StatusCode != 200 || runs != readAttempts {
		t.Fatalf("status %d after %d runs; want 200 after %d", resp.StatusCode, runs, readAttempts)
	}
}

// A node-list read must make progress without waiting for a quiet tenant. Its
// position is the counter before the handler, never a later event it may not see.
func TestListPositionIsALowerBoundWithoutRepeatingUnderContinuousEvents(t *testing.T) {
	d, a, b := fixture(t)
	base := logPosition(t, d, a)
	appendEvents(t, d, a, 2)
	appendEvents(t, d, b, 20)
	srv := positionServer(t, d, a, func(int) { appendEvents(t, d, a, 1) })
	for run := 1; run <= 10; run++ {
		resp := do(t, srv, "GET", "/api/nodes", false)
		n, ok := position(t, resp)
		if !ok || n != base+int64(run+1) {
			t.Fatalf("read %d: position %d, present %v; want the before counter %d", run, n, ok, base+int64(run+1))
		}
		var body struct{ Run int }
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Run != run {
			t.Fatalf("read %d: body %+v (%v); the handler must run once per request", run, body, err)
		}
		if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Reply") != "kept" {
			t.Fatalf("status %d, headers %v: buffered answer changed", resp.StatusCode, resp.Header)
		}
	}
}

// A refused read is not repeated, and never carries a position.
func TestPositionIsNotReadForARefusedRead(t *testing.T) {
	d, a, _ := fixture(t)
	base := logPosition(t, d, a)
	srv := positionServer(t, d, a, nil)
	resp := do(t, srv, "GET", "/api/models", false)
	if n, ok := position(t, resp); ok || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d, position %d, present %v; want a bare 403", resp.StatusCode, n, ok)
	}
	var count int
	if err := d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1`, a.TenantID).Scan(&count); err != nil || int64(count) != base+1 {
		t.Fatalf("the refused read ran %d times (%v); want once", int64(count)-base, err)
	}
}

func TestPositionOfAnAcceptedWriteIsAtOrAboveItsOwnEvent(t *testing.T) {
	d, a, _ := fixture(t)
	base := logPosition(t, d, a)
	appendEvents(t, d, a, 4)
	srv := positionServer(t, d, a, nil)
	resp := do(t, srv, "POST", "/api/runs/r1/cancel", false)
	n, ok := position(t, resp)
	if resp.StatusCode != 200 || !ok || n != base+5 {
		t.Fatalf("status %d, position %d, present %v; want 200 and the write's own event", resp.StatusCode, n, ok)
	}
	// A read started after the write answers at or above it: nothing older can look newer.
	if read, ok := position(t, do(t, srv, "GET", "/api/runs", false)); !ok || read < n {
		t.Fatalf("read position %d is below the write's %d", read, n)
	}
	if n, ok := position(t, do(t, srv, "DELETE", "/api/agent-accounts/a", false)); !ok || n != base+5 {
		t.Fatalf("a 204 write: position %d, present %v", n, ok)
	}
}

func TestPositionIsAbsentWhereItWouldMislead(t *testing.T) {
	d, a, _ := fixture(t)
	appendEvents(t, d, a, 1)
	srv := positionServer(t, d, a, nil)
	cases := []struct {
		name, method, path string
		anonymous          bool
	}{
		{"a read outside the list", "GET", "/api/health", false},
		{"a refused write", "POST", "/api/runs/r1/refuse", false},
		{"a HEAD", "HEAD", "/api/runs", false},
		{"an unauthenticated read", "GET", "/api/runs", true},
		{"an unauthenticated write", "POST", "/api/runs/r1/cancel", true},
	}
	for _, c := range cases {
		if n, ok := position(t, do(t, srv, c.method, c.path, c.anonymous)); ok {
			t.Errorf("%s carries position %d", c.name, n)
		}
	}
}

func TestPositionWriterKeepsStreamingWorking(t *testing.T) {
	d, a, _ := fixture(t)
	base := logPosition(t, d, a)
	appendEvents(t, d, a, 1)
	rec := httptest.NewRecorder()
	w := &positionWriter{ResponseWriter: rec, r: httptest.NewRequest("POST", "/api/x", nil), pool: d.App, tenantID: a.TenantID}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatalf("flush through the wrapper: %v", err)
	}
	if !rec.Flushed || rec.Header().Get(PositionHeader) != strconv.FormatInt(base+1, 10) {
		t.Fatalf("flushed %v, position %q", rec.Flushed, rec.Header().Get(PositionHeader))
	}
}

type positionDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *positionDeadlineRecorder) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestPositionReadForwardsDeadlinesWithoutFlushing(t *testing.T) {
	d, p, _ := fixture(t)
	w := &positionDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	w.Header().Set("X-Initial", "kept")
	r := httptest.NewRequest(http.MethodGet, "/api/runs", nil)
	r.Pattern = "GET /api/runs"
	r = r.WithContext(tenant.WithPrincipal(r.Context(), p))
	deadline := time.Unix(123, 0)
	handler := http.HandlerFunc(func(buffer http.ResponseWriter, _ *http.Request) {
		if _, ok := buffer.(*bufferedResponse); !ok {
			t.Error("listed read did not enter the production response buffer")
			return
		}
		controller := http.NewResponseController(buffer)
		for _, value := range []time.Time{deadline, {}} {
			if err := controller.SetReadDeadline(value); err != nil {
				t.Errorf("forward read deadline %v: %v", value, err)
			}
		}
		httpapi.WriteJSON(buffer, http.StatusOK, map[string]bool{"ok": true})
		if err := controller.Flush(); err != nil {
			t.Errorf("flush buffered answer: %v", err)
		}
		if w.Flushed || w.Body.Len() != 0 {
			t.Error("response escaped before its event position was known")
		}
	})
	before, err := newestEvent(t.Context(), d.App, p.TenantID)
	if err != nil {
		t.Fatal(err)
	}
	PositionMiddleware(d.App)(handler).ServeHTTP(w, r)
	if len(w.deadlines) != 2 || w.deadlines[0] != deadline || !w.deadlines[1].IsZero() {
		t.Fatalf("connection deadlines = %v; want installed then cleared", w.deadlines)
	}
	if w.Code != http.StatusOK || w.Header().Get(PositionHeader) != strconv.FormatInt(before, 10) || w.Header().Get("X-Initial") != "kept" || strings.TrimSpace(w.Body.String()) != `{"ok":true}` {
		t.Fatalf("buffered response changed: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body)
	}
}

// The middleware matches the mux's pattern text exactly; a typo would silently drop the header.
func TestPositionReadsNameRegisteredRoutes(t *testing.T) {
	for pattern := range positionReads {
		if _, ok := authz.PermissionForPattern(pattern); !ok {
			t.Errorf("%q is not a registered API pattern", pattern)
		}
	}
}
