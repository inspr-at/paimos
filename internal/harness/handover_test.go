// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCoordinatorHandover(t *testing.T) {
	for _, mode := range []string{"stopped", "lost", "explicit", "healthy", "different-principal", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			f := fixture(t)
			base := "/api/projects/" + f.project + "/harness-sessions"
			body := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "coordinator", "harness_session_ref": "native-ref-" + uid(), "worker_lease": "old-lease-" + uid()}
			reg := func(b map[string]any) map[string]any {
				t.Helper()
				w := f.call(f.person, "POST", base, b, "")
				expect(t, w, 201)
				return decode(t, w)
			}
			old := reg(body)
			childBody := map[string]any{"agent_principal_id": f.agent.ID, "harness": "codex", "host": "local", "management_mode": "unmanaged", "role": "worker", "harness_session_ref": "child-ref-" + uid(), "worker_lease": "child-lease-" + uid(), "parent_harness_session_id": old["id"]}
			live := reg(childBody)
			childBody["harness_session_ref"] = "ended-ref-" + uid()
			ended := reg(childBody)
			expect(t, f.call(f.agent, "POST", base+"/"+ended["id"].(string)+"/stop", map[string]any{"reason": "process_exited"}, childBody["worker_lease"].(string)), 200)
			if mode == "lost" {
				age(t, f, old["id"].(string), "20 minutes", false)
			} else if mode != "healthy" {
				expect(t, f.call(f.agent, "POST", base+"/"+old["id"].(string)+"/stop", map[string]any{"reason": "process_exited"}, body["worker_lease"].(string)), 200)
			}
			body["worker_lease"] = "new-lease-" + uid()
			if mode == "explicit" {
				body["harness_session_ref"] = "different-ref-" + uid()
				body["succeeds_session_id"] = old["id"]
			}
			if mode == "different-principal" {
				other := uid()
				f.tx(t, f.person, func(tx pgx.Tx) error {
					_, e := tx.Exec(t.Context(), `INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'agent','other')`, f.person.TenantID, other)
					return e
				})
				body["agent_principal_id"] = other
			}
			if mode == "cycle" {
				body["parent_harness_session_id"] = live["id"]
			}
			w := f.call(f.person, "POST", base, body, "")
			if mode == "healthy" || mode == "cycle" {
				expect(t, w, 409)
			} else {
				expect(t, w, 201)
			}
			adopted := mode == "stopped" || mode == "lost" || mode == "explicit"
			var successor string
			if adopted {
				successor = decode(t, w)["id"].(string)
				if reg(body)["id"] != successor {
					t.Fatal("replay created a generation")
				}
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var parent string
				var from, to *string
				if e := tx.QueryRow(t.Context(), `SELECT parent_id::text,adopted_from_id::text FROM harness_sessions WHERE id=$1`, live["id"]).Scan(&parent, &from); e != nil {
					return e
				}
				if adopted {
					if parent != successor || from == nil || *from != old["id"] {
						t.Fatal("live child not adopted")
					}
				} else if parent != old["id"] || from != nil {
					t.Fatal("unauthorized/failed adoption changed child")
				}
				if e := tx.QueryRow(t.Context(), `SELECT handed_over_to_id::text FROM harness_sessions WHERE id=$1`, old["id"]).Scan(&to); e != nil {
					return e
				}
				if adopted && (to == nil || *to != successor) {
					t.Fatal("missing successor")
				}
				if !adopted && to != nil {
					t.Fatal("failed adoption marked predecessor")
				}
				if e := tx.QueryRow(t.Context(), `SELECT parent_id::text FROM harness_sessions WHERE id=$1`, ended["id"]).Scan(&parent); e != nil {
					return e
				}
				if parent != old["id"] {
					t.Fatal("moved stopped child")
				}
				var n int
				if e := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.adopted'`).Scan(&n); e != nil {
					return e
				}
				if adopted && n != 1 || !adopted && n != 0 {
					t.Fatalf("adoption events=%d", n)
				}
				return nil
			})
		})
	}
}
