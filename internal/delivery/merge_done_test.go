// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/statusautopilot"
	"github.com/jackc/pgx/v5"
)

type mergeGitHubFixture struct {
	*fakeGitHub
	facts      map[int64]MergeFact
	beforeRead func()
	reads      int
}

func (g *mergeGitHubFixture) MergedPull(_ context.Context, n int64) (*MergeFact, error) {
	g.reads++
	if g.beforeRead != nil {
		fn := g.beforeRead
		g.beforeRead = nil
		fn()
	}
	if g.err != nil {
		return nil, g.err
	}
	f, ok := g.facts[n]
	if !ok {
		return nil, nil
	}
	return &f, nil
}
func (g *mergeGitHubFixture) MergedGroup(_ context.Context, sha string) ([]MergeFact, error) {
	if g.err != nil {
		return nil, g.err
	}
	out := []MergeFact{}
	for _, f := range g.facts {
		if f.SHA == sha {
			out = append(out, f)
		}
	}
	return out, nil
}

const mergeBenefits = `{"pill_en":"Status stays true","pill_de":"Ticketstatus bleibt korrekt","benefit_en":"The board follows merged work.","benefit_de":"Das Board folgt gemergten Änderungen."}`

func mergeFixture(t *testing.T) (*fixture, *mergeGitHubFixture) {
	t.Helper()
	f := newFixture(t)
	g := &mergeGitHubFixture{fakeGitHub: f.gh, facts: map[int64]MergeFact{}}
	f.m.github = g
	f.gh.pulls[7] = f.pull()
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET state='qa',fields=$2 WHERE id=$1`, f.ticket, mergeBenefits); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO project_lead_settings(tenant_id,project_id,owner_person_id,updated_by) VALUES($1,$2,$3,$3)`, f.person.TenantID, f.project, f.person.ID)
		return err
	})
	statusautopilot.New(f.d.App).Mount(f.mux)
	return f, g
}

func mergeFactFixture(f *fixture, n int64, key, sha string) MergeFact {
	return MergeFact{PR: &n, SHA: sha, Head: strings.Repeat("b", 40), Title: key + ": merge status", Branch: "work/" + strings.ToLower(key) + "-merge", At: f.at, MergedBy: "github-merge-queue[bot]"}
}

func mergedState(t *testing.T, f *fixture, id string) string {
	t.Helper()
	var state string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT state FROM nodes WHERE id=$1`, id).Scan(&state)
	})
	return state
}

// Replay scrubbed, checked-in GitHub App event documents, keeping unrelated
// payload fields so event-shape drift is distinct from hand-built envelopes.
func recordedMergeWebhook(t *testing.T, f *fixture, file, id string, want int, mutate func(map[string]any)) {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + file + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		t.Fatal("invalid payload fixture")
	}
	if mutate != nil {
		mutate(payload)
		raw, err = json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/api/github/webhook", strings.NewReader(string(raw)))
	r.Header.Set("X-GitHub-Event", strings.Split(file, ".")[0])
	r.Header.Set("X-GitHub-Delivery", id)
	r.Header.Set("X-Hub-Signature-256", signed(raw, f.m.secret))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s: HTTP %d, want %d: %s", file, w.Code, want, w.Body.String())
	}
}

func TestMergeDoneRecordedWebhookReplay(t *testing.T) {
	// Risk: duplicate or reordered authenticated observations finish a ticket
	// twice, regress Delivered, or complete an unmerged/foreign PR.
	f, g := mergeFixture(t)
	g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
	p := f.gh.pulls[7]
	p.Open = false
	p.Merged = true
	f.gh.pulls[7] = p
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-closed", 204, nil)
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-closed", 204, nil)
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-new-id", 204, nil)
	if state := mergedState(t, f, f.ticket); state != "done" {
		t.Fatalf("state %s", state)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.merge_done' AND node_id=$1`, f.ticket).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("completion receipts %d", n)
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='delivered' WHERE id=$1`, f.ticket)
		return err
	})
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-late", 204, nil)
	if mergedState(t, f, f.ticket) != "delivered" {
		t.Fatal("late delivery regressed Delivered")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='qa' WHERE id=$1`, f.ticket)
		return err
	})
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-after-reopen", 204, nil)
	if mergedState(t, f, f.ticket) != "qa" {
		t.Fatal("old merge receipt completed explicitly reopened work")
	}
	reads := g.reads
	recordedMergeWebhook(t, f, "pull_request.closed", "merge-foreign", 404, func(p map[string]any) { p["installation"] = map[string]int{"id": 999} })
	if g.reads != reads {
		t.Fatal("unbound installation reached completion reader")
	}
}

func TestMergeDoneQueueConstituentsRequireActualMerge(t *testing.T) {
	// Risk: green/destroyed queue groups complete unmerged work; a disappearing
	// queue ref or distinct merge SHAs lose all but the last PR in a group.
	f, g := mergeFixture(t)
	var second string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,state,fields) SELECT $1,$2,'AEON-849',id,'Second queue ticket','qa',$3 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, f.project, mergeBenefits).Scan(&second)
	})
	p := f.pull()
	p.Number = 8
	p.Title = "AEON-849: queue"
	p.Branch = "work/aeon-849-queue"
	f.gh.pulls[8] = p
	recordedMergeWebhook(t, f, "merge_group.checks_requested", "queue-request", 204, nil)
	recordedMergeWebhook(t, f, "merge_group.destroyed", "queue-destroy", 204, nil)
	if mergedState(t, f, f.ticket) != "qa" || mergedState(t, f, second) != "qa" {
		t.Fatal("queue checks or destruction proved merge")
	}
	g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
	g.facts[8] = mergeFactFixture(f, 8, "AEON-849", strings.Repeat("d", 40))
	for n, p := range f.gh.pulls {
		p.Open = false
		p.Merged = true
		f.gh.pulls[n] = p
	}
	recordedMergeWebhook(t, f, "merge_group.destroyed", "queue-destroy", 204, nil)
	if mergedState(t, f, f.ticket) != "done" || mergedState(t, f, second) != "done" {
		t.Fatal("confirmed group constituents not completed")
	}
}

func TestMergeDoneRefusalNeedsYouAndRecovery(t *testing.T) {
	// Risk: automation bypasses benefit gates or silently leaves merged work.
	f, g := mergeFixture(t)
	g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{}' WHERE id=$1`, f.ticket)
		return err
	})
	recordedMergeWebhook(t, f, "pull_request.closed", "refused-merge", 204, nil)
	recordedMergeWebhook(t, f, "pull_request.closed", "refused-merge", 204, nil)
	var page struct {
		Items []struct {
			NodeID     string `json:"node_id"`
			Reason     string `json:"reason"`
			Applicable bool   `json:"applicable"`
		} `json:"items"`
	}
	f.call(t, f.person, "GET", "/api/status-autopilot/attention", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].NodeID != f.ticket || page.Items[0].Applicable || !strings.Contains(page.Items[0].Reason, "AEON-848") || !strings.Contains(page.Items[0].Reason, "pill_de is required") {
		t.Fatalf("Needs You refusal: %+v", page)
	}
	if mergedState(t, f, f.ticket) != "qa" {
		t.Fatal("completion gate bypassed")
	}
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2,updated_at=clock_timestamp() WHERE id=$1`, f.ticket, mergeBenefits)
		return err
	})
	recordedMergeWebhook(t, f, "pull_request.closed", "refused-merge", 204, nil)
	if mergedState(t, f, f.ticket) != "done" {
		t.Fatal("replay did not recover fixed gate")
	}
	f.call(t, f.person, "GET", "/api/status-autopilot/attention", nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("recovered refusal remained pending")
	}
}

func TestMergeDoneProtectsOwnershipParentsAndHumanChecks(t *testing.T) {
	// Risk: access revocation during the GitHub read or a parent/human gate is
	// ignored by a service writer. Each fixture retains the refused work.
	for _, gate := range []string{"owner revoked", "parent", "human check", "worker", "unmerged"} {
		t.Run(gate, func(t *testing.T) {
			f, g := mergeFixture(t)
			g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
			switch gate {
			case "owner revoked":
				g.beforeRead = func() {
					f.tx(t, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1`, f.person.ID)
						return err
					})
				}
			case "parent":
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title) SELECT $1,$2,'AEON-849',id,'Still open child' FROM node_kinds WHERE slug='work'`, f.person.TenantID, f.ticket)
					return err
				})
			case "human check":
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check='Person verifies release safety' WHERE id=$1`, f.ticket)
					return err
				})
			case "worker":
				f.build(t)
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE work_orders SET status='running' WHERE node_id=$1`, f.buildOrder)
					return err
				})
			case "unmerged":
				delete(g.facts, 7)
			}
			recordedMergeWebhook(t, f, "pull_request.closed", "gate-merge", 204, nil)
			if mergedState(t, f, f.ticket) != "qa" {
				t.Fatal("protected ticket completed")
			}
			f.tx(t, func(tx pgx.Tx) error {
				var reason string
				if gate == "unmerged" {
					var count int
					if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM status_autopilot_proposals WHERE node_id=$1`, f.ticket).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						t.Fatal("unmerged PR created refusal")
					}
					return nil
				}
				if err := tx.QueryRow(t.Context(), `SELECT decision->>'Reason' FROM status_autopilot_proposals WHERE node_id=$1 AND status='pending'`, f.ticket).Scan(&reason); err != nil {
					return err
				}
				want := map[string]string{"owner revoked": "nodes.write", "parent": "parent status", "human check": "needs a human check", "worker": "worker is still running"}[gate]
				if !strings.Contains(reason, want) {
					t.Fatalf("wrong refusal: %s", reason)
				}
				return nil
			})
		})
	}
}

func TestMergeDoneRefusesWaitingLifecycleAction(t *testing.T) {
	// Risk: a person-authorized split or cancel remains in work_lifecycle_actions
	// after the worker has stopped. aeon_work_busy does not see that row, so a
	// main-merge must still refuse on the Needs You path and leave state unchanged.
	f, g := mergeFixture(t)
	g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO work_lifecycle_actions(tenant_id,id,node_id,requested_by,kind,targets) VALUES($1,gen_random_uuid(),$2::uuid,$3,'split',jsonb_build_array(jsonb_build_object('id',$2::text,'updated_at','2000-01-01T00:00:00Z')))`, f.person.TenantID, f.ticket, f.person.ID)
		return err
	})
	f.tx(t, func(tx pgx.Tx) error {
		var busy, pending bool
		var sessions, liveRuns, runningOrders int
		err := tx.QueryRow(t.Context(), `SELECT aeon_work_busy($1),aeon_work_pending($1) IS NOT NULL,
 (SELECT count(*) FROM harness_sessions WHERE ticket_node_id=$1),
 (SELECT count(*) FROM agent_runs r WHERE r.status IN ('queued','starting','running','waiting')
   AND (r.queue_node_id=$1 OR r.work_order_id IN (SELECT id FROM nodes WHERE parent_id=$1))),
 (SELECT count(*) FROM work_orders w JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=$1 AND w.status='running')`, f.ticket).Scan(&busy, &pending, &sessions, &liveRuns, &runningOrders)
		if err != nil {
			return err
		}
		if busy || !pending || sessions != 0 || liveRuns != 0 || runningOrders != 0 {
			return fmt.Errorf("fixture is not a stopped worker with a waiting action: busy=%v pending=%v sessions=%d runs=%d orders=%d", busy, pending, sessions, liveRuns, runningOrders)
		}
		return nil
	})
	recordedMergeWebhook(t, f, "pull_request.closed", "handover-merge", 204, nil)
	if mergedState(t, f, f.ticket) != "qa" {
		t.Fatal("waiting lifecycle action did not block completion")
	}
	var page struct {
		Items []struct {
			NodeID     string `json:"node_id"`
			Reason     string `json:"reason"`
			Applicable bool   `json:"applicable"`
		} `json:"items"`
	}
	f.call(t, f.person, "GET", "/api/status-autopilot/attention", nil, 200, &page)
	if len(page.Items) != 1 || page.Items[0].NodeID != f.ticket || page.Items[0].Applicable || !strings.Contains(page.Items[0].Reason, "waiting for handover") {
		t.Fatalf("Needs You handover refusal: %+v", page)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var receipts int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.merge_done' AND node_id=$1`, f.ticket).Scan(&receipts); err != nil {
			return err
		}
		if receipts != 0 {
			return fmt.Errorf("refused merge wrote %d completion receipts", receipts)
		}
		return nil
	})
}

func TestMergeBackfillPreviewApplyAndSafety(t *testing.T) {
	// Risk: historical repair writes before preview, completes open follow-up
	// work, replays mutations, trusts stale revisions, or uses revoked access.
	for _, scenario := range []string{"apply once", "stale revision", "open followup", "revoked access", "partial inventory"} {
		t.Run(scenario, func(t *testing.T) {
			f, g := mergeFixture(t)
			g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
			p := f.gh.pulls[7]
			p.Open = false
			p.Merged = true
			f.gh.pulls[7] = p
			path := "/api/projects/" + f.project + "/delivery/merge-backfill"
			input := mergeBackfillInput{Pulls: []int64{7}}
			f.call(t, f.person, "POST", path, mergeBackfillInput{Pulls: []int64{7}, Apply: true}, 400, nil)
			var preview mergeBackfillResult
			f.call(t, f.person, "POST", path, input, 200, &preview)
			if preview.Preview < 1 || preview.Applied || len(preview.Items) != 1 || preview.Items[0].Result != "ready" || mergedState(t, f, f.ticket) != "qa" {
				t.Fatalf("dry-run %+v", preview)
			}
			input.Apply = true
			input.Preview = preview.Preview
			want := 200
			switch scenario {
			case "stale revision":
				f.tx(t, func(tx pgx.Tx) error {
					_, err := tx.Exec(t.Context(), `UPDATE nodes SET title='Changed while preview was open',updated_at=clock_timestamp() WHERE id=$1`, f.ticket)
					return err
				})
				want = 409
			case "open followup":
				p := f.pull()
				p.Number = 9
				f.gh.pulls[9] = p
				want = 409
			case "revoked access":
				g.beforeRead = func() {
					f.tx(t, func(tx pgx.Tx) error {
						var role string
						if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'merge_readonly','Merge read only') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
							return err
						}
						if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest(ARRAY['nodes.read','projects.read','delivery.manage'])`, f.person.TenantID, role); err != nil {
							return err
						}
						// Retain project visibility and delivery.manage; revoke only
						// node mutation, which must be checked after this network read.
						_, err := tx.Exec(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.person.ID, role)
						return err
					})
				}
				want = 403
			case "partial inventory":
				g.err = errors.New("incomplete inventory")
				want = 502
			}
			var result mergeBackfillResult
			f.call(t, f.person, "POST", path, input, want, &result)
			if want != 200 {
				if mergedState(t, f, f.ticket) != "qa" {
					t.Fatal("unsafe backfill wrote state")
				}
				return
			}
			if !result.Applied || result.Items[0].Result != "applied" || mergedState(t, f, f.ticket) != "done" {
				t.Fatalf("apply %+v", result)
			}
			var replay mergeBackfillResult
			f.call(t, f.person, "POST", path, input, 200, &replay)
			if !reflect.DeepEqual(result, replay) {
				t.Fatal("apply replay result changed")
			}
			f.tx(t, func(tx pgx.Tx) error {
				var count int
				if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='delivery.merge_backfill_applied'`).Scan(&count); err != nil {
					return err
				}
				if count != 1 {
					t.Fatalf("apply receipts %d", count)
				}
				return nil
			})
		})
	}
}

func TestMergeBackfillDeliveredNeedsPublishedMembership(t *testing.T) {
	// Risk: a tag or release date alone invents Delivered for unrelated work.
	f, g := mergeFixture(t)
	g.facts[7] = mergeFactFixture(f, 7, "AEON-848", strings.Repeat("c", 40))
	p := f.gh.pulls[7]
	p.Open = false
	p.Merged = true
	f.gh.pulls[7] = p
	var release string
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO journey_projects(tenant_id,project_node_id) VALUES($1,$2)`, f.person.TenantID, f.project); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title) SELECT $1,$2,'REL-1',id,'Published release' FROM node_kinds WHERE slug='release' RETURNING id::text`, f.person.TenantID, f.project).Scan(&release); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state,released_at,version_scheme,version) VALUES($1,$2,$3,1,'released',$4,'inspr-calendar-v2','261009120000.0.0')`, f.person.TenantID, release, f.project, f.at.Add(1))
		return err
	})
	path := "/api/projects/" + f.project + "/delivery/merge-backfill"
	input := mergeBackfillInput{Pulls: []int64{7}, Target: "delivered", Release: release}
	var preview mergeBackfillResult
	f.call(t, f.person, "POST", path, input, 200, &preview)
	if len(preview.Items) != 1 || !strings.Contains(preview.Items[0].Reason, "no frozen published release membership") {
		t.Fatalf("unproven Delivered %+v", preview)
	}
	f.tx(t, func(tx pgx.Tx) error {
		snapshot := map[string]any{"schema": "aeon.release-note-snapshot.v1", "membership_source": "release-manifest-tickets", "label": "backfilled", "backfilled": true, "tenant_id": f.person.TenantID, "project_node_id": f.project, "version": "261009120000.0.0", "tickets": []map[string]string{{"id": f.ticket}}}
		raw, _ := json.Marshal(snapshot)
		_, err := tx.Exec(t.Context(), `INSERT INTO release_manifest_note_snapshots(tenant_id,project_node_id,version,snapshot) VALUES($1,$2,'261009120000.0.0',$3)`, f.person.TenantID, f.project, raw)
		return err
	})
	f.call(t, f.person, "POST", path, input, 200, &preview)
	if preview.Items[0].Result != "ready" {
		t.Fatalf("published membership %+v", preview)
	}
	input.Apply = true
	input.Preview = preview.Preview
	f.call(t, f.person, "POST", path, input, 200, nil)
	if mergedState(t, f, f.ticket) != "delivered" {
		t.Fatal("proven Delivered not applied")
	}
}

func TestMergeDoneReferencesAndCanonicalGroupReads(t *testing.T) {
	keys, err := mergeKeys("AEON-848, AEON-849: both references", "work/aeon-848-merge")
	if err != nil || !reflect.DeepEqual(keys, []string{"AEON-848", "AEON-849"}) {
		t.Fatalf("references %v: %v", keys, err)
	}
	if keys, err := mergeKeys("XAEON-4 AEON-4x AEON-0", "work/aeon-5-valid"); err != nil || !reflect.DeepEqual(keys, []string{"AEON-5"}) {
		t.Fatalf("boundaries %v %v", keys, err)
	}
	if _, err := mergeKeys(strings.Repeat("AEON-848 ", 51), ""); err == nil {
		t.Fatal("reference limit ignored")
	}
	get := func(path string, out any) error {
		if path != "/pulls/7" {
			return fmt.Errorf("unexpected read %s", path)
		}
		raw, err := os.ReadFile("testdata/pull_request.closed.json")
		if err != nil {
			return err
		}
		var e struct {
			Pull json.RawMessage `json:"pull_request"`
		}
		if err := json.Unmarshal(raw, &e); err != nil {
			return err
		}
		return json.Unmarshal(e.Pull, out)
	}
	fact, err := mergedPull(get, "example/delivery", 7)
	if err != nil || fact == nil || fact.SHA != strings.Repeat("c", 40) {
		t.Fatalf("recorded canonical pull: %+v %v", fact, err)
	}
	if fact, err := mergedPull(get, "other/repo", 7); err != nil || fact != nil {
		t.Fatal("foreign base proved merge")
	}
}
