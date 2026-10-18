// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/jackc/pgx/v5"
)

func TestLeafParentRoundTripPreservesPlanningMetadata(t *testing.T) {
	for _, source := range []string{"manual", "requirements"} {
		t.Run(source, func(t *testing.T) {
			f := ticketSetup(t)
			former := f.existing("work", f.feature, "Planned leaf", "open")
			membershipOK(t, f.addExisting([]string{former}, 1, false))
			var original json.RawMessage
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET feature_node_id=$2,source=$3,
 estimated_hours=7.25,access_change=true,scope_revision_required=false,walker_position=42 WHERE ticket_node_id=$1`, former, f.feature, source); err != nil {
					return err
				}
				return tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&original)
			})
			child := f.existing("work", former, "New child", "open")
			for round := 0; round < 2; round++ {
				f.tx(func(tx pgx.Tx) error {
					var live int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&live); err != nil {
						return err
					}
					if live != 0 {
						t.Fatal("parent retained live membership")
					}
					if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child); err != nil {
						return err
					}
					var restored json.RawMessage
					if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&restored); err != nil {
						return err
					}
					if string(restored) != string(original) {
						t.Fatalf("round %d lost planning metadata: before=%s after=%s", round, original, restored)
					}
					var access bool
					if err := tx.QueryRow(t.Context(), `SELECT access_required FROM journey_releases WHERE release_node_id=$1`, f.release).Scan(&access); err != nil {
						return err
					}
					if !access {
						t.Fatal("restored access-changing leaf lost the release access gate")
					}
					if round == 0 {
						_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=NULL WHERE id=$1`, child)
						return err
					}
					return nil
				})
			}
		})
	}
}

func TestParentReassignmentInvalidatesRetainedScopeApproval(t *testing.T) {
	for _, action := range []string{"reassign", "undo", "same-release", "return-to-original", "legacy-intent", "stale-undo"} {
		t.Run(action, func(t *testing.T) {
			f := ticketSetup(t)
			former := f.existing("work", f.feature, "Approved former leaf", "open")
			membershipOK(t, f.addExisting([]string{former}, 1, false))
			var original map[string]any
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `UPDATE journey_tickets SET feature_node_id=$2,source='requirements',
 estimated_hours=7.25,access_change=true,scope_revision_required=false,walker_position=42 WHERE ticket_node_id=$1`, former, f.feature); err != nil {
					return err
				}
				var data []byte
				if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&data); err != nil {
					return err
				}
				return json.Unmarshal(data, &original)
			})
			child := f.existing("work", former, "Temporary child", "open")
			second := f.existing("release", f.project, "Another planning release", "open")
			target := second
			if action == "same-release" {
				target = f.release
			}
			f.tx(func(tx pgx.Tx) error {
				if _, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number) VALUES($1,$2,$3,2)`, f.person.TenantID, second, f.project); err != nil {
					return err
				}
				_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, target)
				return err
			})
			place := func(release string) membershipResult {
				var revision int64
				f.tx(func(tx pgx.Tx) error {
					return tx.QueryRow(t.Context(), `SELECT revision FROM journey_releases WHERE release_node_id=$1`, release).Scan(&revision)
				})
				return membershipOK(t, f.request(f.person, http.MethodPost, "/api/projects/"+f.project+"/releases/"+release+"/membership",
					fmt.Sprintf(`{"expected_revision":%d,"ticket_node_ids":[%q],"confirm_move":true}`, revision, former)))
			}
			out := place(target)
			wantRelease, wantScope := target, action != "same-release"
			if action == "legacy-intent" || action == "stale-undo" {
				// Model an older retained projection, or a projection changed after
				// the event snapshot, without altering its other planning metadata.
				f.tx(func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE work_parent_releases SET retained_membership=jsonb_set(retained_membership,'{scope_revision_required}','false'::jsonb) WHERE parent_node_id=$1`, former)
					return err
				})
			}
			if action == "stale-undo" {
				events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 409 {
					t.Fatalf("stale retained approval must reject Undo: %d %s", w.Code, w.Body.String())
				}
				f.tx(func(tx pgx.Tx) error {
					var release string
					var scope bool
					if err := tx.QueryRow(t.Context(), `SELECT release_node_id::text,(retained_membership->>'scope_revision_required')::boolean FROM work_parent_releases WHERE parent_node_id=$1`, former).Scan(&release, &scope); err != nil {
						return err
					}
					if release != target || scope {
						t.Fatal("rejected Undo changed retained placement or scope")
					}
					return nil
				})
				return
			}
			if action == "undo" {
				events.New(f.db.App, events.WithUndoHandlers(UndoHandlers())).Mount(f.mux)
				if w := f.request(f.person, http.MethodPost, fmt.Sprintf("/api/events/%d/undo", out.EventID), ""); w.Code != 201 {
					t.Fatalf("undo parent reassignment: %d %s", w.Code, w.Body.String())
				}
				wantRelease, wantScope = f.release, false
			} else if action == "return-to-original" {
				f.tx(func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE journey_projects SET current_release_node_id=$2 WHERE project_node_id=$1`, f.project, f.release)
					return err
				})
				place(f.release)
				wantRelease = f.release
			}
			f.tx(func(tx pgx.Tx) error {
				var live int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM journey_tickets WHERE ticket_node_id=$1`, former).Scan(&live); err != nil {
					return err
				}
				if live != 0 {
					t.Fatal("parent retained live membership")
				}
				if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=now() WHERE id=$1`, child); err != nil {
					return err
				}
				var data []byte
				if err := tx.QueryRow(t.Context(), `SELECT to_jsonb(t) FROM journey_tickets t WHERE ticket_node_id=$1`, former).Scan(&data); err != nil {
					return err
				}
				var restored map[string]any
				if err := json.Unmarshal(data, &restored); err != nil {
					return err
				}
				if restored["release_node_id"] != wantRelease || restored["scope_revision_required"] != wantScope {
					t.Fatalf("restored leaf reused scope approval after %s: release=%v scope=%v; want release=%s scope=%v", action, restored["release_node_id"], restored["scope_revision_required"], wantRelease, wantScope)
				}
				original["release_node_id"], original["scope_revision_required"] = wantRelease, wantScope
				want, err := json.Marshal(original)
				if err != nil {
					return err
				}
				got, err := json.Marshal(restored)
				if err != nil {
					return err
				}
				if string(got) != string(want) {
					t.Fatalf("parent reassignment lost retained metadata: got=%s want=%s", got, want)
				}
				var access bool
				if err := tx.QueryRow(t.Context(), `SELECT access_required FROM journey_releases WHERE release_node_id=$1`, wantRelease).Scan(&access); err != nil {
					return err
				}
				if !access {
					t.Fatal("restored access-changing leaf lost the access review gate")
				}
				return nil
			})
		})
	}
}
