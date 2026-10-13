// SPDX-License-Identifier: AGPL-3.0-only

package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

// AEON-1231 risk: cancelling admission after response middleware captures the
// request removes a successful long mutation's watermark and its read floor.
func TestLongMutationPreservesEventPositionAfterAuthentication(t *testing.T) {
	d := dbtest.Open(t)
	p := tenant.Principal{Kind: tenant.Person}
	if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('budget-position','Test') RETURNING id::text`).Scan(&p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), d.App, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Writer') RETURNING id::text`, p.TenantID).Scan(&p.ID)
	}); err != nil {
		t.Fatal(err)
	}

	for _, route := range []struct{ pattern, path string }{
		{"POST /api/projects/{projectId}/delivery/metrics/backfill", "/api/projects/project/delivery/metrics/backfill"},
		{"POST /api/projects/{projectId}/delivery/metrics/check-again", "/api/projects/project/delivery/metrics/check-again"},
		{"POST /api/projects/{projectId}/delivery/merge-backfill", "/api/projects/project/delivery/merge-backfill"},
	} {
		t.Run(route.pattern, func(t *testing.T) {
			parent, stop := context.WithTimeout(t.Context(), time.Minute)
			defer stop()
			callerDeadline, _ := parent.Deadline()
			var admission context.Context
			var committed events.Event
			type authGuardKey struct{}
			type catalogKey struct{}
			mux := http.NewServeMux()
			mux.HandleFunc(route.pattern, func(w http.ResponseWriter, r *http.Request) {
				current, ok := tenant.PrincipalFrom(r.Context())
				if !ok || current.ID != p.ID || r.Context().Value(authGuardKey{}) != "live guard" || r.Context().Value(catalogKey{}) != "catalog" {
					t.Fatal("handler lost authenticated principal or middleware values")
				}
				if admission.Err() != context.Canceled || r.Context().Err() != nil || context.Cause(r.Context()) != nil {
					t.Fatal("handler lifetime still depends on admission cancellation")
				}
				if got, _ := r.Context().Deadline(); !got.Equal(callerDeadline) {
					t.Fatal("handler lost the caller deadline")
				}
				if err := db.InTenant(dbtest.Seed(r.Context()), d.App, current.TenantID, func(tx pgx.Tx) error {
					var err error
					committed, err = events.Append(r.Context(), tx, current, events.Change{Type: "test.changed", After: map[string]any{"path": r.URL.Path}})
					return err
				}); err != nil {
					t.Fatalf("mutation failed: %v", err)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			s := &httpapi.Server{Mux: mux, Middleware: []func(http.Handler) http.Handler{
				func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						admission = r.Context()
						deadline, bounded := admission.Deadline()
						if !bounded || deadline.After(time.Now().Add(5*time.Second)) || !deadline.Before(callerDeadline) {
							t.Fatal("authentication lost its five-second admission bound")
						}
						ctx := context.WithValue(tenant.WithPrincipal(admission, p), authGuardKey{}, "live guard")
						next.ServeHTTP(w, r.WithContext(ctx))
					})
				},
				func(next http.Handler) http.Handler {
					return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), catalogKey{}, "catalog")))
					})
				},
				events.PositionMiddleware(d.App),
			}}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, route.path, nil).WithContext(parent))
			if w.Code != http.StatusNoContent || committed.ID <= 0 {
				t.Fatalf("status %d, event %d: mutation did not succeed", w.Code, committed.ID)
			}
			if got, want := w.Header().Get(events.PositionHeader), strconv.FormatInt(committed.ID, 10); got != want {
				t.Fatalf("%s = %q, want committed mutation position %s", events.PositionHeader, got, want)
			}
		})
	}
}
