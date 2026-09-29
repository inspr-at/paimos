// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDeliveryLeaseFollowsDatabaseClock(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			now := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
			live, expired := now.Add(30*time.Second), now.Add(-time.Second)
			// 2001 is behind the host, so a database-live lease looks expired there.
			// 2099 is ahead of the host, so a database-expired lease still looks live there.
			if year == 2001 && live.After(time.Now()) {
				t.Fatal("2001 lease is not behind the host")
			}
			if year == 2099 && !expired.After(time.Now()) {
				t.Fatal("2099 expiry is not ahead of the host")
			}
			t.Run("claim", func(t *testing.T) {
				w, m, project, srv := leaseClockWorld(t, now, false)
				base := "/api/projects/" + project
				held := claimLease(t, w, m, srv, project, "codex:worker", "codex", "simple", "live-lease")
				setDeliveryLease(t, w, held.ID, live)
				status, data := compatPost(t, srv, w.agent, base+"/messages/delivery-claim", claimInput{To: "codex:worker", Adapter: "codex"})
				if status != 200 || mustJSON[DeliveryWork](t, data).State != "leased" {
					t.Fatalf("live lease reclaimed %d %s", status, data)
				}
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-complete", completeInput{ID: held.ID, LeaseToken: held.LeaseToken, EffectiveLevel: "simple"})
				if status != 200 || mustJSON[MessageDelivery](t, data).State != "delivered" {
					t.Fatalf("live complete %d %s", status, data)
				}

				again := claimLease(t, w, m, srv, project, "codex:worker", "codex", "simple", "expired-lease")
				setDeliveryLease(t, w, again.ID, expired)
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-complete", completeInput{ID: again.ID, LeaseToken: again.LeaseToken, EffectiveLevel: "simple"})
				if status != 409 || !strings.Contains(string(data), "delivery lease expired") {
					t.Fatalf("expired complete %d %s", status, data)
				}
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-claim", claimInput{To: "codex:worker", Adapter: "codex"})
				reclaimed := mustJSON[DeliveryWork](t, data)
				if status != 200 || reclaimed.State == "leased" || reclaimed.LeaseToken == "" || reclaimed.LeaseToken == again.LeaseToken {
					t.Fatalf("expired lease not reclaimable %d %s", status, data)
				}
			})
			t.Run("unavailable", func(t *testing.T) {
				w, m, project, srv := leaseClockWorld(t, now, true)
				base := "/api/projects/" + project
				held := claimLease(t, w, m, srv, project, "codex:worker", "agentd_codex", "steer", "live-reroute")
				setDeliveryLease(t, w, held.ID, live)
				status, data := compatPost(t, srv, w.agent, base+"/messages/delivery-unavailable", unavailableInput{ID: held.ID, LeaseToken: held.LeaseToken, FallbackReason: "idle"})
				if status != 200 || !strings.Contains(string(data), `"rerouted":true`) {
					t.Fatalf("live reroute %d %s", status, data)
				}
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-claim", claimInput{To: "codex:worker", Adapter: "codex"})
				fallback := mustJSON[DeliveryWork](t, data)
				if status != 200 || fallback.LeaseToken == "" {
					t.Fatalf("fallback claim %d %s", status, data)
				}
				// A fresh lease is stamped from Postgres, not the injected clock.
				setDeliveryLease(t, w, fallback.ID, live)
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-complete", completeInput{ID: fallback.ID, LeaseToken: fallback.LeaseToken, EffectiveLevel: "simple", FallbackReason: "idle"})
				if status != 200 {
					t.Fatalf("fallback complete %d %s", status, data)
				}

				again := claimLease(t, w, m, srv, project, "codex:worker", "agentd_codex", "steer", "expired-reroute")
				setDeliveryLease(t, w, again.ID, expired)
				status, data = compatPost(t, srv, w.agent, base+"/messages/delivery-unavailable", unavailableInput{ID: again.ID, LeaseToken: again.LeaseToken, FallbackReason: "idle"})
				if status != 409 {
					t.Fatalf("expired reroute %d %s", status, data)
				}
			})
		})
	}
}

func leaseClockWorld(t *testing.T, now time.Time, withFallback bool) (*world, *messaging, string, *httptest.Server) {
	t.Helper()
	w, m, project, srv := messagingWorld(t)
	m.databaseClock = func(context.Context, pgx.Tx) (time.Time, error) { return now, nil }
	address := "codex:worker"
	if withFallback {
		if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: address, Adapter: "codex", Kind: "codex_thread", Ref: "fallback-thread", Role: "simple_fallback", MaximumLevel: "simple"}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: address, Adapter: "agentd_codex", Kind: "agentd_session", Ref: `{"socket":"/private/tmp/fixture.sock","session_id":"00000000-0000-4000-8000-000000000010"}`, Role: "primary", MaximumLevel: "steer"}); err != nil {
			t.Fatal(err)
		}
		return w, m, project, srv
	}
	if _, err := m.storeTarget(t.Context(), w.admin, project, targetInput{Address: address, Adapter: "codex", Kind: "codex_thread", Ref: "private-thread-fixture", Role: "primary", MaximumLevel: "steer"}); err != nil {
		t.Fatal(err)
	}
	return w, m, project, srv
}

func claimLease(t *testing.T, w *world, m *messaging, srv *httptest.Server, project, address, adapter, level, key string) DeliveryWork {
	t.Helper()
	in := compatInput(address, key)
	in.Level = level
	mustCompatSend(t, m, w.sender, project, in)
	status, data := compatPost(t, srv, w.agent, "/api/projects/"+project+"/messages/delivery-claim", claimInput{To: address, Adapter: adapter})
	work := mustJSON[DeliveryWork](t, data)
	if status != 200 || work.LeaseToken == "" || work.State == "leased" {
		t.Fatalf("claim %s %d %s", key, status, data)
	}
	return work
}

func setDeliveryLease(t *testing.T, w *world, id string, until time.Time) {
	t.Helper()
	if _, err := w.db.Admin.Exec(t.Context(), `UPDATE inbox_message_deliveries SET lease_until=$2 WHERE id=$1::uuid`, id, until); err != nil {
		t.Fatal(err)
	}
}
