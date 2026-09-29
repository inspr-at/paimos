// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/jackc/pgx/v5"
)

func TestSettingsCompletionUsesDatabaseClockUnderSkew(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			// Simulate DB clocks on either side of the real API/daemon clock.
			// Set the stored deadline on that same clock; do not change the host.
			now := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			f := fixtureWithOwnershipClock(t, func() time.Time { return now })
			order, run := stateRun(t, f, f.project, "running", "SCK-10")
			lease := "settings-clock-lease-0000000000000001"
			base := "/api/projects/" + f.project + "/harness-sessions"
			w := f.call(f.person, "POST", base, map[string]any{"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture", "harness_session_ref": "settings-clock-ref-00000000000001", "worker_lease": lease, "management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order, "ticket_node_id": order, "work_shape": "ship", "display_label": "Original", "advertised_capabilities": []string{"managed_control_v1", "rename", "stop"}}, "")
			expect(t, w, 201)
			id := decode(t, w)["id"].(string)
			path := base + "/" + id
			// A daemon-ahead start stamp is identity, not a DB freshness claim.
			identity := ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
				return err
			})
			expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
			for _, remaining := range []time.Duration{-time.Second, 0, time.Second} {
				body := map[string]any{"request_id": uid(), "kind": "rename", "value": "Changed", "expected_ownership": identity}
				w = f.call(f.person, "POST", path+"/managed-controls", body, "")
				expect(t, w, 201)
				control := decode(t, w)["id"].(string)
				// Isolate completion from SQL queue expiry: the claim has already
				// happened, and the response is now arriving at this DB time.
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET state='claimed',claimed_at=clock_timestamp(),expires_at=$2 WHERE id=$1`, control, now.Add(remaining))
					return err
				})
				status, label := 409, "Original"
				if remaining > 0 {
					status, label = 200, "Changed"
				}
				expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "applied", "reason": "setting_applied"}, lease), status)
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var actual string
					if err := tx.QueryRow(t.Context(), `SELECT display_label FROM harness_sessions WHERE id=$1`, id).Scan(&actual); err != nil {
						return err
					}
					if actual != label {
						t.Fatalf("remaining %s: label=%q, want %q", remaining, actual, label)
					}
					return nil
				})
				if remaining <= 0 {
					expect(t, f.call(f.agent, "POST", path+"/controls/"+control+"/complete", map[string]any{"outcome": "rejected", "reason": "authorization_expired"}, lease), 200)
				}
			}
		})
	}
}
