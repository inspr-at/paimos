// SPDX-License-Identifier: AGPL-3.0-only

package harness_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

func TestLateSettingCompletionUnderSkewStillReleasesTheNextControl(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			// The database clock sits on the other side of the host clock.
			// now-1s is expired there. In 2099 that instant is still in the
			// host's future; a host-clock check would accept the completion.
			now := time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)
			if year == 2099 && !now.After(time.Now()) {
				t.Fatal("2099 fixture is not ahead of the host")
			}
			if year == 2001 && now.After(time.Now()) {
				t.Fatal("2001 fixture is not behind the host")
			}
			f, path, lease, identity := lateControlFixture(t, func() time.Time { return now })
			rename, next := queueLateControls(t, f, path, lease, identity)
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=$2 WHERE id=$1`, rename, now.Add(-time.Second))
				return err
			})
			w := f.call(f.agent, "POST", path+"/controls/"+rename+"/complete", map[string]any{"outcome": "applied", "reason": "setting_applied"}, lease)
			expect(t, w, 409)
			if !strings.Contains(w.Body.String(), "setting authorization expired") {
				t.Fatal(w.Body.String())
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var label string
				if err := tx.QueryRow(t.Context(), `SELECT display_label FROM harness_sessions WHERE id=$1`, sessionID(path)).Scan(&label); err != nil {
					return err
				}
				if label != "Original" {
					t.Fatalf("label %q", label)
				}
				return nil
			})
			expect(t, f.call(f.agent, "POST", path+"/controls/"+next+"/complete", map[string]any{"outcome": "applied", "reason": "native_interrupt_acknowledged"}, lease), 200)
		})
	}
}

func TestExpiredClaimSettlesAndDoesNotBlockTheNextControl(t *testing.T) {
	f, path, lease, identity := lateControlFixture(t, nil)
	rename, next := queueLateControls(t, f, path, lease, identity)
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_controls SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, rename)
		return err
	})
	w := f.call(f.agent, "POST", path+"/controls/"+rename+"/complete", map[string]any{"outcome": "applied", "reason": "setting_applied"}, lease)
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "setting authorization expired") {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 0 {
		t.Fatal("expired control was offered again")
	}
	w = f.call(f.person, "GET", path+"/controls/"+rename, nil, "")
	expect(t, w, 200)
	if decode(t, w)["state"] != "completed" || decode(t, w)["reason"] != "outcome_unconfirmed" {
		t.Fatal(w.Body.String())
	}
	w = f.call(f.agent, "POST", path+"/controls/"+rename+"/complete", map[string]any{"outcome": "applied", "reason": "setting_applied"}, lease)
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "divergent control completion") {
		t.Fatal(w.Body.String())
	}
	expect(t, f.call(f.agent, "POST", path+"/controls/"+next+"/complete", map[string]any{"outcome": "applied", "reason": "native_interrupt_acknowledged"}, lease), 200)
}

func lateControlFixture(t *testing.T, now func() time.Time) (f *harnessFixture, path, lease string, identity ownedprocess.Identity) {
	t.Helper()
	f = fixtureWithOwnershipClock(t, now)
	order, run := stateRun(t, f, f.project, "running", "LATE-10")
	lease = "late-control-lease-0000000000000001"
	base := "/api/projects/" + f.project + "/harness-sessions"
	w := f.call(f.person, "POST", base, map[string]any{
		"agent_principal_id": f.agent.ID, "harness": "claude", "host": "fixture",
		"harness_session_ref": "late-control-ref-" + uid(), "worker_lease": lease,
		"management_mode": "managed", "role": "worker", "run_id": run, "work_order_id": order,
		"ticket_node_id": order, "work_shape": "ship", "display_label": "Original",
		"advertised_capabilities": []string{"managed_control_v1", "rename", "interrupt", "stop"},
	}, "")
	expect(t, w, 201)
	path = base + "/" + decode(t, w)["id"].(string)
	identity = ownedprocess.Identity{DaemonID: "fixture", Generation: strings.Repeat("a", 32), ProcessID: strings.Repeat("b", 32), RootPID: 1234, GroupID: 1234, StartedAt: time.Now().UTC().Truncate(time.Second)}
	f.tx(t, f.person, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET daemon_id=$2,daemon_generation=$3 WHERE id=$1`, run, identity.DaemonID, identity.Generation)
		return err
	})
	expect(t, f.call(f.agent, "POST", path+"/heartbeat", map[string]any{"phase": "working", "activity_sequence": 1, "process_ownership": identity}, lease), 200)
	return f, path, lease, identity
}

func queueLateControls(t *testing.T, f *harnessFixture, path, lease string, identity ownedprocess.Identity) (rename, next string) {
	t.Helper()
	w := f.call(f.person, "POST", path+"/managed-controls", map[string]any{"request_id": uid(), "kind": "rename", "value": "Changed", "expected_ownership": identity}, "")
	expect(t, w, 201)
	rename = decode(t, w)["id"].(string)
	w = f.call(f.person, "POST", path+"/managed-controls", map[string]any{"request_id": uid(), "kind": "interrupt", "expected_ownership": identity}, "")
	expect(t, w, 201)
	next = decode(t, w)["id"].(string)
	w = f.call(f.agent, "POST", path+"/yield", map[string]any{}, lease)
	expect(t, w, 200)
	if len(decode(t, w)["controls"].([]any)) != 2 {
		t.Fatal(w.Body.String())
	}
	return rename, next
}

func sessionID(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}
