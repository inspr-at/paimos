// SPDX-License-Identifier: AGPL-3.0-only
package harness_test

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestCoordinatorLargeHandover(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		children int
	}{
		{"registration", 64}, {"resume", 128},
		{"registration", 1000}, {"resume", 1000},
		{"registration", 1001}, {"resume", 1001},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.mode, tc.children), func(t *testing.T) {
			mode, children := tc.mode, tc.children
			f := fixtureWithKind(t, nil, "work")
			base := "/api/projects/" + f.project + "/harness-sessions"
			reg := pauseRegistration(f)
			reg["role"], reg["work_shape"] = "coordinator", "unknown"
			delete(reg, "ticket_node_id")
			w := f.call(f.person, "POST", base, reg, "")
			expect(t, w, 201)
			id, lease := decode(t, w)["id"].(string), reg["worker_lease"].(string)
			path := base + "/" + id
			if mode == "resume" {
				w = f.call(f.person, "POST", path+"/pause", map[string]any{"reason": "Continue the large hierarchy"}, "")
				expect(t, w, 200)
				finishPaused(t, f, path, lease, decode(t, w)["pause"].(map[string]any)["control_id"].(string))
			} else {
				expect(t, f.call(f.agent, "POST", path+"/stop", map[string]any{"reason": "process_exited"}, lease), 200)
			}
			// Fixture generations keep distinct references and leases. Resume has
			// live and paused children; n=0 is ordinary ended history in both modes.
			f.tx(t, f.person, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,parent_id,harness,host,management,role,capabilities,ref_digest,lease_digest,owner_principal_id,phase,stopped_at,stop_reason,pause_record)
 SELECT tenant_id,project_id,agent_principal_id,id,harness,host,management,'worker',capabilities,
 sha256(convert_to('large-child-ref-'||n::text,'UTF8')),sha256(convert_to('large-child-lease-'||n::text,'UTF8')),owner_principal_id,
 CASE WHEN n=0 OR ($3 AND n>64) THEN 'stopped' ELSE 'working' END,CASE WHEN n=0 OR ($3 AND n>64) THEN stopped_at END,CASE WHEN n=0 THEN 'process_exited' WHEN $3 AND n>64 THEN 'paused' END,CASE WHEN $3 AND n>64 THEN pause_record END
 FROM harness_sessions CROSS JOIN generate_series(0,$2::int) n WHERE id=$1`, id, children, mode == "resume")
				return err
			})
			var successor string
			if mode == "resume" {
				body := map[string]any{"registration": map[string]any{"harness_session_ref": "large-resume-ref-" + uid(), "worker_lease": "large-resume-lease-" + uid()}}
				w = f.call(f.person, "POST", path+"/resume", body, "")
				if children <= 1000 {
					expect(t, w, 200)
					successor = decode(t, w)["successor"].(map[string]any)["id"].(string)
				}
			} else {
				reg["worker_lease"] = "large-successor-lease-" + uid()
				w = f.call(f.person, "POST", base, reg, "")
				if children <= 1000 {
					expect(t, w, 201)
					successor = decode(t, w)["id"].(string)
					replay := f.call(f.person, "POST", base, reg, "")
					expect(t, replay, 201)
					if decode(t, replay)["id"] != successor {
						t.Fatal("large registration replay created another successor")
					}
				}
			}
			if children > 1000 {
				expect(t, w, 409)
				if decode(t, w)["error"] != "handover exceeds 1000 direct children" {
					t.Fatalf("oversize scope failed for wrong reason: %s", w.Body.String())
				}
				f.tx(t, f.person, func(tx pgx.Tx) error {
					var unchanged, total, audits int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE parent_id=$1 AND adopted_from_id IS NULL),count(*) FROM harness_sessions`, id).Scan(&unchanged, &total); err != nil {
						return err
					}
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type IN ('harness.adopted','harness.handed_over','harness.resume_requested','harness.resumed')`).Scan(&audits); err != nil {
						return err
					}
					if unchanged != children+1 || total != children+2 || audits != 0 {
						return fmt.Errorf("oversize handover left partial writes: unchanged=%d,total=%d,audits=%d", unchanged, total, audits)
					}
					return nil
				})
				return
			}
			f.tx(t, f.person, func(tx pgx.Tx) error {
				var adopted, audits, handovers int
				var retainedHistory int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE parent_id=$1 AND adopted_from_id IS NULL AND stop_reason='process_exited' AND phase='stopped'`, id).Scan(&retainedHistory); err != nil {
					return err
				}
				if retainedHistory != 1 {
					t.Fatal("handover moved or changed ordinary ended history")
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE parent_id=$1 AND adopted_from_id=$2`, successor, id).Scan(&adopted); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.adopted' AND "after"->>'parent_harness_session_id'=$1`, successor).Scan(&audits); err != nil {
					return err
				}
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='harness.handed_over' AND "after"->>'id'=$1`, id).Scan(&handovers); err != nil {
					return err
				}
				if adopted != children || audits != children || handovers != 1 {
					return fmt.Errorf("large handover children=%d, audits=%d, handovers=%d; want %d, %d, 1", adopted, audits, handovers, children, children)
				}
				return nil
			})
		})
	}
}
