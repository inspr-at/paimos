// SPDX-License-Identifier: AGPL-3.0-only

package events

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

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
func positionServer(t *testing.T, d *dbtest.DB, p tenant.Principal, during func()) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, r *http.Request) {
		if during != nil {
			during()
		}
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
	}
	mux.HandleFunc("GET /api/runs", reply)
	mux.HandleFunc("GET /api/agent-accounts", reply)
	mux.HandleFunc("GET /api/nodes", reply)
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
	srv := positionServer(t, d, a, nil)
	if n, ok := position(t, do(t, srv, "GET", "/api/runs", false)); !ok || n != 0 {
		t.Fatalf("empty log: position %d, present %v", n, ok)
	}
	appendEvents(t, d, a, 3)
	appendEvents(t, d, b, 5) // another tenant's log never counts
	for _, path := range []string{"/api/runs", "/api/agent-accounts"} {
		if n, ok := position(t, do(t, srv, "GET", path, false)); !ok || n != 3 {
			t.Fatalf("%s: position %d, present %v; want the tenant's 3", path, n, ok)
		}
	}
}

func TestPositionOfAReadIsAFloorForEventsThatCommitWhileItRuns(t *testing.T) {
	d, a, _ := fixture(t)
	appendEvents(t, d, a, 2)
	// An event commits inside the handler: the read may include it, the position must not claim it.
	srv := positionServer(t, d, a, func() { appendEvents(t, d, a, 1) })
	n, ok := position(t, do(t, srv, "GET", "/api/runs", false))
	if !ok || n != 2 {
		t.Fatalf("position %d, present %v; want 2, the newest before the handler", n, ok)
	}
}

func TestPositionOfAnAcceptedWriteIsAtOrAboveItsOwnEvent(t *testing.T) {
	d, a, _ := fixture(t)
	appendEvents(t, d, a, 4)
	srv := positionServer(t, d, a, nil)
	resp := do(t, srv, "POST", "/api/runs/r1/cancel", false)
	n, ok := position(t, resp)
	if resp.StatusCode != 200 || !ok || n != 5 {
		t.Fatalf("status %d, position %d, present %v; want 200 and the write's own event 5", resp.StatusCode, n, ok)
	}
	// A read started after the write answers at or above it: nothing older can look newer.
	if read, ok := position(t, do(t, srv, "GET", "/api/runs", false)); !ok || read < n {
		t.Fatalf("read position %d is below the write's %d", read, n)
	}
	if n, ok := position(t, do(t, srv, "DELETE", "/api/agent-accounts/a", false)); !ok || n != 5 {
		t.Fatalf("a 204 write: position %d, present %v", n, ok)
	}
}

func TestPositionIsAbsentWhereItWouldMislead(t *testing.T) {
	d, a, _ := fixture(t)
	appendEvents(t, d, a, 1)
	srv := positionServer(t, d, a, nil)
	cases := []struct{ name, method, path string; anonymous bool }{
		{"a read outside the list", "GET", "/api/nodes", false},
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
	appendEvents(t, d, a, 1)
	rec := httptest.NewRecorder()
	w := &positionWriter{ResponseWriter: rec, r: httptest.NewRequest("POST", "/api/x", nil), pool: d.App, tenantID: a.TenantID}
	if err := http.NewResponseController(w).Flush(); err != nil {
		t.Fatalf("flush through the wrapper: %v", err)
	}
	if !rec.Flushed || rec.Header().Get(PositionHeader) != "1" {
		t.Fatalf("flushed %v, position %q", rec.Flushed, rec.Header().Get(PositionHeader))
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
