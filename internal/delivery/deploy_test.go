// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/crossreview"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type deployGitHubFixture struct {
	*fakeGitHub
	beforeRead func()
	known      map[string]bool
	ancestors  map[string]bool
	readError  error
}

func (g *deployGitHubFixture) DeploymentRead(_ context.Context, _, repository string, read func(func(string, any) error) error) error {
	if g.beforeRead != nil {
		g.beforeRead()
	}
	if g.readError != nil {
		return g.readError
	}
	return read(func(path string, out any) error {
		var value any
		switch {
		case path == "":
			value = map[string]string{"full_name": repository}
		case strings.HasPrefix(path, "/git/commits/"):
			sha := strings.TrimPrefix(path, "/git/commits/")
			if !g.known[sha] {
				return crossreview.ErrNotFound
			}
			value = map[string]string{"sha": sha}
		case strings.HasPrefix(path, "/compare/"):
			merge, _, _ := strings.Cut(strings.TrimPrefix(path, "/compare/"), "...")
			status, base := "behind", strings.Repeat("0", 40)
			if g.ancestors[merge] {
				status, base = "ahead", merge
			}
			value = map[string]any{"status": status, "base_commit": map[string]string{"sha": merge}, "merge_base_commit": map[string]string{"sha": base}}
		default:
			return errRead
		}
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, out)
	})
}

func deployFixture(t *testing.T) (*fixture, *deployGitHubFixture) {
	t.Helper()
	f := newFixture(t)
	g := &deployGitHubFixture{fakeGitHub: f.gh, known: map[string]bool{strings.Repeat("a", 40): true, strings.Repeat("b", 40): true}, ancestors: map[string]bool{strings.Repeat("a", 40): true}}
	f.m.github = g
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO delivery_metric_sources(tenant_id,project_id,repository) VALUES($1,$2,$3)`, f.person.TenantID, f.project, f.m.config.Repository)
		return err
	})
	return f, g
}

func deployTicket(t *testing.T, f *fixture, key, state, sha, repository string) string {
	t.Helper()
	var id string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,state) SELECT $1,$2,$3,id,'Deploy fixture',$4 FROM node_kinds WHERE slug='work' RETURNING id::text`, f.person.TenantID, f.project, key, state).Scan(&id); err != nil {
			return err
		}
		if sha == "" {
			return nil
		}
		meta, _ := json.Marshal(map[string]any{"repository": repository, "merge_sha": sha, "merged_at": f.at})
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &id, Type: "delivery.merge_done", After: map[string]string{"state": state}, Metadata: meta})
		return err
	})
	return id
}

func deployCount(t *testing.T, f *fixture) int {
	t.Helper()
	var count int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='deployment.recorded'`).Scan(&count)
	})
	return count
}

func TestDeployRecordDeliversMergedLeavesAndReplays(t *testing.T) {
	// Risk: deployment advances unrelated/unmerged/parent work, clears a human
	// check, repeats effects, regresses accepted work or crosses tenant scope.
	f, g := deployFixture(t)
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	repo := f.m.config.Repository
	eligible := deployTicket(t, f, "AIT-1", "done", a, repo)
	exact := deployTicket(t, f, "AIT-2", "done", b, repo)
	delivered := deployTicket(t, f, "AIT-3", "delivered", a, repo)
	accepted := deployTicket(t, f, "AIT-4", "accepted", a, repo)
	unmerged := deployTicket(t, f, "AIT-5", "done", "", repo)
	future := deployTicket(t, f, "AIT-6", "done", c, repo)
	wrongRepo := deployTicket(t, f, "AIT-7", "done", a, "example/unrelated")
	human := deployTicket(t, f, "AIT-8", "done", a, repo)
	parent := deployTicket(t, f, "AIT-9", "done", a, repo)
	noRelease := deployTicket(t, f, "AIT-10", "done", a, repo)
	active := deployTicket(t, f, "AIT-11", "in_progress", a, repo)
	undone := deployTicket(t, f, "AIT-12", "done", a, repo)
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET human_check='Confirm on device' WHERE id=$1`, human); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields='{"no_release_needed":true}' WHERE id=$1`, noRelease); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$2 WHERE id=$1`, unmerged, `{"merge_commit":"`+a+`","merged_at":"2026-10-07T00:00:00Z"}`); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,parent_id,key,kind_id,title,state) SELECT $1,$2,'AIT-13',id,'Child','done' FROM node_kinds WHERE slug='work'`, f.person.TenantID, parent); err != nil {
			return err
		}
		var eventID int64
		if err := tx.QueryRow(t.Context(), `SELECT id FROM events WHERE node_id=$1 AND type='delivery.merge_done'`, undone).Scan(&eventID); err != nil {
			return err
		}
		_, err := events.Append(t.Context(), tx, f.person, events.Change{NodeID: &undone, Type: "delivery.merge_undone", UndoOf: &eventID, After: map[string]string{"state": "done"}})
		return err
	})
	// Use an OPS agent and retain its identity as the live confirmer.
	path := "/api/projects/" + f.project + "/deploys"
	in := deployInput{Environment: "production", SHA: b, Version: "26.10.17"}
	var out Deploy
	f.call(t, f.agent, "POST", path, in, 200, &out)
	if out.EventID < 1 || out.ConfirmedBy != f.agent.ID || out.Project != f.project || out.ConfirmedAt.IsZero() || out.SHA != b || out.Version != in.Version {
		t.Fatalf("live confirmation missing: %+v", out)
	}
	for id, want := range map[string]string{eligible: "delivered", exact: "delivered", delivered: "delivered", accepted: "accepted", unmerged: "done", future: "done", wrongRepo: "done", human: "done", parent: "done", noRelease: "done", active: "in_progress", undone: "done"} {
		if got := mergedState(t, f, id); got != want {
			t.Errorf("ticket %s: got %s want %s", id, got, want)
		}
	}
	results := map[string]string{}
	for _, ticket := range out.Tickets {
		results[ticket.NodeID] = ticket.Result
		if ticket.NodeID == human && !strings.Contains(ticket.Reason, "Confirm on device") {
			t.Fatal("human-check refusal omitted")
		}
	}
	if results[eligible] != "delivered" || results[exact] != "delivered" || results[human] != "skipped" || results[accepted] != "unchanged" || len(results) != 8 {
		t.Fatalf("wrong publication results: %+v", results)
	}
	f.tx(t, func(tx pgx.Tx) error {
		var changed, comments int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE type='status_autopilot.changed'),count(*) FILTER(WHERE type='comment.created') FROM events WHERE node_id=$1 AND actor_principal_id=$2 AND metadata->>'rule'='publish'`, eligible, f.agent.ID).Scan(&changed, &comments); err != nil {
			return err
		}
		if changed != 1 || comments != 1 {
			return fmt.Errorf("shared release transition audit missing: changes=%d comments=%d", changed, comments)
		}
		return nil
	})
	// A retry preserves the original confirmer, timestamp, label and results,
	// even after upstream becomes unavailable or a human changes ticket state.
	g.readError = errors.New("upstream unavailable")
	in.Version = "different label"
	var replay Deploy
	f.call(t, f.person, "POST", path, in, 200, &replay)
	if !reflect.DeepEqual(out, replay) || deployCount(t, f) != 1 {
		t.Fatalf("replay changed the immutable confirmation: %+v", replay)
	}
	g.readError = nil
	g.ancestors = map[string]bool{}
	var older Deploy
	f.call(t, f.person, "POST", path, deployInput{Environment: "production", SHA: a}, 200, &older)
	if mergedState(t, f, exact) != "delivered" || mergedState(t, f, accepted) != "accepted" || deployCount(t, f) != 2 {
		t.Fatal("older deployment regressed completed work")
	}
	f.call(t, f.foreign, "POST", path, in, 404, nil)
	if err := db.InTenant(db.AllProjects(t.Context(), "deployment tenant isolation assertion"), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='deployment.recorded'`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("foreign tenant read %d deployment events", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeployRecordFailsAtomicallyAndRechecksAuthority(t *testing.T) {
	// Risk: malformed/unknown commits, partial reads, failed event writes or
	// access/source changes during GitHub verification leave false delivery.
	for _, scenario := range []string{"unknown SHA", "partial read", "permission revoked", "source changed", "audit failed"} {
		t.Run(scenario, func(t *testing.T) {
			f, g := deployFixture(t)
			a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
			id := deployTicket(t, f, "AIT-20", "done", a, f.m.config.Repository)
			in, want := deployInput{Environment: "production", SHA: b}, 502
			switch scenario {
			case "unknown SHA":
				in.SHA, want = strings.Repeat("f", 40), 422
			case "partial read":
				g.readError = errors.New("partial repository response")
			case "permission revoked":
				want = 403
				g.beforeRead = func() {
					f.tx(t, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE role_bindings b SET role_id=r.id FROM roles r WHERE b.principal_id=$1 AND r.tenant_id=b.tenant_id AND r.key='viewer'`, f.person.ID)
						return err
					})
				}
			case "source changed":
				want = 409
				g.beforeRead = func() {
					f.tx(t, func(tx pgx.Tx) error {
						_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET repository='example/changed' WHERE project_id=$1`, f.project)
						return err
					})
				}
			case "audit failed":
				want = 500
				_, err := f.d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_deploy_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='deployment.recorded' THEN RAISE EXCEPTION 'fixture audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_deploy_fixture BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_deploy_fixture()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			f.call(t, f.person, "POST", "/api/projects/"+f.project+"/deploys", in, want, nil)
			if got := mergedState(t, f, id); got != "done" || deployCount(t, f) != 0 {
				t.Fatalf("failed confirmation left a partial write: state=%s", got)
			}
		})
	}
}

func TestDeployRecordConcurrentConfirmationIsIdempotent(t *testing.T) {
	// Both requests finish upstream verification before either may commit.
	f, g := deployFixture(t)
	id := deployTicket(t, f, "AIT-30", "done", strings.Repeat("a", 40), f.m.config.Repository)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	g.beforeRead = func() {
		arrived <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		wg.Wait()
	})
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			r := httptest.NewRequest(http.MethodPost, "/api/projects/"+f.project+"/deploys", strings.NewReader(`{"environment":"production","sha":"`+strings.Repeat("b", 40)+`"}`))
			r = r.WithContext(tenant.WithPrincipal(ctx, f.person))
			w := httptest.NewRecorder()
			f.mux.ServeHTTP(w, r)
			responses <- w
		})
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("requests did not overlap repository verification")
		}
	}
	close(release)
	wg.Wait()
	close(responses)
	var first *Deploy
	for w := range responses {
		if w.Code != 200 {
			t.Fatalf("concurrent confirmation: %d %s", w.Code, w.Body.String())
		}
		var out Deploy
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = &out
		} else if !reflect.DeepEqual(*first, out) {
			t.Fatal("concurrent confirmations did not converge")
		}
	}
	if deployCount(t, f) != 1 || mergedState(t, f, id) != "delivered" {
		t.Fatal("concurrent confirmation duplicated effects")
	}
}
