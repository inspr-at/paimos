// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"net/http"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/jackc/pgx/v5"
)

func TestResolveTicketRoute(t *testing.T) {
	reset(t)
	p := makePrincipal(t, "ticket-route", "person", "lead", []string{"admin"})
	if code, body := call(t, &p, http.MethodGet, "/api/models", ""); code != http.StatusOK {
		t.Fatalf("seed: %d %s", code, body)
	}
	profiles := decode[[]Profile](t, &p, http.MethodGet, "/api/models", "", http.StatusOK)
	minimalAccount(t, p, profileBySlug(profiles, "codex-6-1-sol-high"))
	minimalAccount(t, p, profileBySlug(profiles, "codex-luna-medium"))
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		if err := modelprefs.SeedKinds(t.Context(), tx, p.TenantID); err != nil {
			return err
		}
		var routes, events int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_role_routes`).Scan(&routes); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&events); err != nil {
			return err
		}
		now := time.Now()
		backend, err := ResolveTicketRoute(t.Context(), tx, "build", "backend", now)
		if err != nil {
			return err
		}
		frontend, err := ResolveTicketRoute(t.Context(), tx, " build ", "frontend", now)
		if err != nil {
			return err
		}
		if backend == nil || frontend == nil || backend.Profile.ID == "" || backend.Profile.Model == "" || backend.CommandTemplate == "" {
			t.Fatalf("missing selection: %#v %#v", backend, frontend)
		}
		if backend.Role != "build" || backend.Area != "backend" || frontend.Area != "frontend" || backend.Profile.ID != frontend.Profile.ID {
			t.Fatalf("area changed the profile: %#v %#v", backend, frontend)
		}
		for _, pair := range [][2]string{{"", "backend"}, {"build", ""}, {"gruntwork", "backend"}, {"build", "mobile"}, {"review-gate", "backend"}} {
			got, err := ResolveTicketRoute(t.Context(), tx, pair[0], pair[1], now)
			if err != nil || got != nil {
				t.Fatalf("%q %q => %#v %v", pair[0], pair[1], got, err)
			}
		}
		var routesAfter, eventsAfter int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM model_role_routes`).Scan(&routesAfter); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events`).Scan(&eventsAfter); err != nil {
			return err
		}
		if routesAfter != routes || eventsAfter != events {
			t.Fatalf("resolve wrote state: routes %d→%d events %d→%d", routes, routesAfter, events, eventsAfter)
		}
		// A kind whose saved order runs nothing waits (a deleted build ladder
		// before Models v2). Quick jobs and the Default stay available to scout.
		if err := dbtest.ModelsOrder(t.Context(), tx, p.TenantID, "infra", "first", "wait"); err != nil {
			return err
		}
		missing, err := ResolveTicketRoute(t.Context(), tx, "build", "infra", now)
		if err != nil || missing != nil {
			t.Fatalf("missing route: %#v %v", missing, err)
		}
		if other, err := ResolveTicketRoute(t.Context(), tx, "build", "backend", now); err != nil || other == nil || other.Profile.ID != backend.Profile.ID {
			t.Fatalf("other kinds lost their route: %#v %v", other, err)
		}
		for _, area := range []string{"docs", "infra"} {
			still, err := ResolveTicketRoute(t.Context(), tx, "scout", area, now)
			if err != nil || still == nil || still.Profile.Model == "" {
				t.Fatalf("other roles removed on %s: %#v %v", area, still, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
