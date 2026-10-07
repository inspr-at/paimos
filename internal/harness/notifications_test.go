// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/inbox"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func TestHarnessNotificationsCommitWakeLeaseAndRevocation(t *testing.T) {
	// Risk: controls/messages still wait for polling, or the new channel leaks
	// content or survives an exact-key revocation. Reading the initial SSE frame
	// is the subscription barrier; no sleep or elapsed-time assertion is used.
	f := fixture(t)
	lease := "notifications-fixture-lease-000000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "fixture", "harness_session_ref": "notifications-fixture-reference", "worker_lease": lease, "management_mode": "managed", "role": "worker", "advertised_capabilities": []string{"inbox", "stop"}}, "")
	expect(t, w, 201)
	id := decode(t, w)["id"].(string)
	path := base + "/" + id
	expect(t, f.call(f.agent, "GET", path+"/notifications", nil, "wrong-lease-000000000000000000000001"), 403)
	expect(t, f.call(f.person, "GET", path+"/notifications", nil, lease), 403)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+path+"/notifications", nil)
	req.Header.Set("Authorization", "Bearer "+f.key)
	req.Header.Set("X-Aeon-Worker-Lease", lease)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("notification status %d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	next := func() bool {
		t.Helper()
		for scanner.Scan() {
			line := scanner.Text()
			if line == "event: harness.wake" {
				if !scanner.Scan() || scanner.Text() != "data: {}" {
					t.Fatal("wake exposed content or lost its fixed shape")
				}
				return true
			}
		}
		return false
	}
	if !next() {
		t.Fatal("missing reconnect wake")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := inbox.AcceptMessageTx(t.Context(), tx, f.person, inbox.Acceptance{RecipientPrincipalID: f.agent.ID, RecipientSessionID: &id, Body: "private notification fixture", IdempotencyKey: uid(), ProjectID: &f.project})
		return err
	})
	if !next() {
		t.Fatal("committed message did not wake the daemon")
	}
	w = f.call(f.agent, "POST", path+"/drain", map[string]any{}, lease)
	expect(t, w, 200)
	// The wake did not acknowledge or consume the queued control.
	expect(t, f.call(f.person, "POST", path+"/controls/stop", map[string]any{}, ""), 201)
	if !next() {
		t.Fatal("committed Stop did not wake the daemon")
	}
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 1 {
		t.Fatal("wake consumed queued Stop")
	}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=clock_timestamp() WHERE id=$1`, f.agent.AuthKeyID); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &f.project, Type: "harness.control_requested", After: map[string]any{"session_id": id}})
		return err
	})
	if next() {
		t.Fatal("revoked bearer received another wake")
	}
	if scanner.Err() != nil {
		t.Fatal("revocation did not close the stream cleanly", scanner.Err())
	}
	expect(t, f.call(f.agent, "GET", path+"/notifications", nil, lease), 403)
}
