// SPDX-License-Identifier: AGPL-3.0-only

package journey_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/journey"
)

func TestDisposableOperatorSeedUsesJourneyActions(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-361", "Disposable project")
	t.Setenv("AEON_ENV", "prod")
	var stdout bytes.Buffer
	if err := journey.RunOperator(t.Context(), f.db.App, []string{"mark-disposable", "--tenant", "journey-a", "--project", "PRJ-361", "--production", "--confirm-project", "PRJ-361"}, &stdout); err != nil || !strings.Contains(stdout.String(), `"disposable":true`) {
		t.Fatalf("production mark: %v %s", err, stdout.String())
	}
	t.Setenv("AEON_ENV", "dev")
	if again, err := journey.MarkDisposable(t.Context(), f.db.App, "journey-a", "PRJ-361"); err != nil || !again.Already {
		t.Fatalf("mark replay: %+v %v", again, err)
	}
	release := f.node(t, "release", "REL-361", "Release")
	ticket := f.node(t, "ticket", "TKT-361", "Completed ticket")
	ctx := t.Context()
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id,brief_confirmed_at,requirements_revision,agreed_requirements_revision,agreed_requirements_digest_sha256,current_release_node_id)
			VALUES($1::uuid,$2::uuid,now(),1,1,$4,$3::uuid)`, f.tenant, project, release, digest); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state)
			VALUES($1::uuid,$2::uuid,$3::uuid,1,'planning')`, f.tenant, release, project); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source)
			VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,0,'manual')`, f.tenant, ticket, project, release); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	view := f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
	if !view.Disposable {
		t.Fatal("journey document omitted disposable marker")
	}
	if _, err := journey.SeedDisposable(ctx, f.db.App, "journey-a", "PRJ-361", "candidate"); err == nil || !strings.Contains(err.Error(), "wait_for_build") {
		t.Fatalf("open ticket allowed candidate seed: %v", err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
			return err
		}
		if state != "planning" {
			t.Fatalf("blocked seed left partial state %s", state)
		}
		_, err := tx.Exec(ctx, `UPDATE nodes SET state='done' WHERE id=$1::uuid`, ticket)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AEON_ENV", "prod")
	stdout.Reset()
	if err := journey.RunOperator(ctx, f.db.App, []string{"seed", "--tenant", "journey-a", "--project", "PRJ-361", "--to-stage", "candidate", "--production", "--confirm-project", "PRJ-361"}, &stdout); err != nil || !strings.Contains(stdout.String(), `"disposable":true`) {
		t.Fatalf("production seed candidate: %v %s", err, stdout.String())
	}
	t.Setenv("AEON_ENV", "dev")
	view = f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
	if view.NextAction.Key != "approve_candidate" || view.NextAction.Available {
		t.Fatalf("candidate approval was bypassed: %+v", view)
	}
	buildStage := func() journey.JourneyStage {
		t.Helper()
		for _, stage := range view.Stages {
			if stage.Key == "build" {
				return stage
			}
		}
		t.Fatal("build stage missing")
		return journey.JourneyStage{}
	}
	if gate := buildStage(); gate.GateScope != journey.ScopeCandidate || gate.GateLive || gate.GateApprovalID != nil {
		t.Fatalf("candidate gate absent in journey document: %+v", gate)
	}
	var state, actor string
	var events, productionEvents int
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*),min(p.name),count(*) FILTER (WHERE e.after->>'production'='true')
			FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
			WHERE e.node_id=$1::uuid AND e.type LIKE 'journey.seed_%'`, project).Scan(&events, &actor, &productionEvents)
	}); err != nil {
		t.Fatal(err)
	}
	if state != "candidate" || events != 2 || productionEvents != events || actor != "Access operator" {
		t.Fatalf("state=%s events=%d production=%d actor=%s", state, events, productionEvents, actor)
	}
	var markedEvents, createdEvents int
	if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
			WHERE e.node_id=$1::uuid AND e.type='journey.disposable_marked' AND e.after->>'production'='true' AND p.name='Access operator'`, project).Scan(&markedEvents); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
			WHERE e.type='principal.created' AND e.after->>'production'='true' AND p.name='Access operator'`).Scan(&createdEvents)
	}); err != nil || markedEvents != 1 || createdEvents != 1 {
		t.Fatalf("production marker event=%d operator creation=%d err=%v", markedEvents, createdEvents, err)
	}
	pending, err := journey.SeedDisposable(ctx, f.db.App, "journey-a", "PRJ-361", "deploy")
	if err != nil || pending.TargetReached || pending.PendingAction != "approve_candidate" {
		t.Fatalf("deploy crossed candidate gate: %+v %v", pending, err)
	}
	candidate := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeCandidate, release)
	view = f.journey(t, f.person, "POST", "/api/projects/"+project+"/journey/actions", actionJSON("approve_candidate", view.Revision, "ds1-candidate", candidate, release, ""))
	if gate := buildStage(); gate.GateScope != journey.ScopeCandidate || !gate.GateLive || gate.GateApprovalID == nil || *gate.GateApprovalID != candidate {
		t.Fatalf("candidate gate not reported in journey document: %+v", gate)
	}
	pending, err = journey.SeedDisposable(ctx, f.db.App, "journey-a", "PRJ-361", "deploy")
	if err != nil || pending.TargetReached || pending.PendingAction != "approve_deploy" {
		t.Fatalf("deploy crossed deployment gate: %+v %v", pending, err)
	}
	deployment := f.grant(t, f.agent.ID, f.person.ID, journey.ScopeDeploy, release)
	view = f.journey(t, f.person, "POST", "/api/projects/"+project+"/journey/actions", actionJSON("approve_deploy", view.Revision, "ds1-deploy", deployment, release, ""))
	reached, err := journey.SeedDisposable(ctx, f.db.App, "journey-a", "PRJ-361", "deploy")
	if err != nil || !reached.TargetReached || reached.PendingAction != "" {
		t.Fatalf("live gate target: %+v %v", reached, err)
	}
	t.Setenv("AEON_ENV", "prod")
	stdout.Reset()
	if err := journey.RunOperator(ctx, f.db.App, []string{"mark-disposable", "--tenant", "journey-a", "--project", "PRJ-361", "--production", "--confirm-project", "PRJ-361"}, &stdout); err == nil || !strings.Contains(err.Error(), "deployed or released") {
		t.Fatalf("deployed marker replay: %v", err)
	}
	t.Setenv("AEON_ENV", "dev")
	if again, err := journey.SeedDisposable(ctx, f.db.App, "journey-a", "PRJ-361", "candidate"); err != nil || !again.Already {
		t.Fatalf("seed replay: %+v %v", again, err)
	}
	stdout.Reset()
	if err := journey.RunOperator(ctx, f.db.App, []string{"seed", "--tenant", "journey-a", "--project", "PRJ-361", "--to-stage", "candidate"}, &stdout); err != nil || !strings.Contains(stdout.String(), `"disposable":true`) {
		t.Fatalf("operator command: %v %s", err, stdout.String())
	}
}

func TestDisposableOperatorGuards(t *testing.T) {
	t.Setenv("AEON_ENV", "dev")
	f := newFixture(t)
	project := f.node(t, "project", "PRJ-362", "Unmarked project")
	if _, err := journey.SeedDisposable(t.Context(), f.db.App, "journey-a", "PRJ-362", "build"); err == nil || !strings.Contains(err.Error(), "not disposable") {
		t.Fatalf("unmarked project seeded: %v", err)
	}
	if _, err := journey.SeedDisposableBrief(t.Context(), f.db.App, "journey-a", "PRJ-362", "deploy", "1"); err == nil || !strings.Contains(err.Error(), "not disposable") {
		t.Fatalf("unmarked project accepted fixed brief: %v", err)
	}
	var marks int
	if err := db.InTenant(dbtest.Seed(context.Background()), f.db.App, f.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM journey_disposable_projects WHERE project_node_id=$1::uuid`, project).Scan(&marks)
	}); err != nil || marks != 0 {
		t.Fatalf("unexpected marker: %d %v", marks, err)
	}
	t.Setenv("AEON_ENV", "prod")
	var stdout bytes.Buffer
	if err := journey.RunOperator(t.Context(), f.db.App, []string{"seed", "--tenant", "journey-a", "--project", "PRJ-362", "--to-stage", "build", "--production", "--confirm-project", "PRJ-362"}, &stdout); err == nil || !strings.Contains(err.Error(), "not disposable") {
		t.Fatalf("production seed of unmarked project: %v", err)
	}
	if _, err := journey.MarkDisposable(t.Context(), f.db.App, "journey-a", "PRJ-362"); err == nil {
		t.Fatal("production marker allowed")
	}
}

func TestDisposableFixedBriefsReachPendingCandidateGate(t *testing.T) {
	briefs := []struct{ number, title, body string }{
		{"1", "Host status page", "A read-only status page for a small fleet of hosts: current state per host, last deploy, open incidents. Success: one page, loads under a second, no write actions."},
		{"2", "Release notes digest", "A weekly digest of released tickets per project: groups by feature and fix, links each ticket, sent nowhere (rendered page only). Success: the digest for last week matches the release history."},
		{"3", "Maintenance window planner", "Plan maintenance windows for hosts: propose a window, check it against the release calendar, record the decision. Success: a window can be proposed, checked and recorded; conflicts are shown."},
	}
	for _, brief := range briefs {
		t.Run(brief.number, func(t *testing.T) {
			t.Setenv("AEON_ENV", "dev")
			f := newFixture(t)
			project := f.node(t, "project", "PRJ-801", "Disposable brief fixture")
			if _, err := journey.MarkDisposable(t.Context(), f.db.App, "journey-a", "PRJ-801"); err != nil {
				t.Fatal(err)
			}
			args := []string{"seed", "--tenant", "journey-a", "--project", "PRJ-801", "--to-stage", "deploy", "--brief", brief.number}
			if brief.number == "1" {
				t.Setenv("AEON_ENV", "prod")
				args = append(args, "--production", "--confirm-project", "PRJ-801")
			}
			var out bytes.Buffer
			if err := journey.RunOperator(t.Context(), f.db.App, args, &out); err != nil {
				t.Fatalf("seed brief %s: %v", brief.number, err)
			}
			if !strings.Contains(out.String(), `"pending_action":"approve_candidate"`) {
				t.Fatalf("seed crossed or missed candidate gate: %s", out.String())
			}
			view := f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
			if view.NextAction.Key != "approve_candidate" || view.NextAction.Available || view.CurrentReleaseID == nil {
				t.Fatalf("candidate gate: %+v", view)
			}
			var title, body, state string
			var count, bad, productionBad, gateDecisions, receipts, allBad, personDecisions int
			ctx := t.Context()
			if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT title,body FROM nodes WHERE id=$1::uuid`, project).Scan(&title, &body); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, *view.CurrentReleaseID).Scan(&state); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE after->>'brief' IS DISTINCT FROM $2 OR after->>'disposable' IS DISTINCT FROM 'true'),
					count(*) FILTER (WHERE $3::bool AND after->>'production' IS DISTINCT FROM 'true')
					FROM events WHERE type LIKE 'journey.seed_%' AND node_id=$1::uuid`, project, brief.number, brief.number == "1").Scan(&count, &bad, &productionBad); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE id >=
					(SELECT min(id) FROM events WHERE node_id=$1::uuid AND type='journey.seed_brief_proposed')
					AND (after->>'brief' IS DISTINCT FROM $2 OR after->>'disposable' IS DISTINCT FROM 'true'
					OR ($3::bool AND after->>'production' IS DISTINCT FROM 'true'))`, project, brief.number, brief.number == "1").Scan(&allBad); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_action_receipts WHERE project_node_id=$1::uuid AND idempotency_key LIKE $2`, project, "operator-brief:"+brief.number+":%").Scan(&receipts); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE node_id=$1::uuid AND type IN ('journey.candidate_approved','journey.deploy_approved')`, project).Scan(&gateDecisions); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT count(*) FROM approval_decisions d JOIN approval_requests a ON a.tenant_id=d.tenant_id AND a.id=d.request_id
					WHERE a.resource_id=$1::uuid AND a.scope IN ('journey.candidate','journey.deploy')`, *view.CurrentReleaseID).Scan(&personDecisions)
			}); err != nil {
				t.Fatal(err)
			}
			if title != brief.title || body != brief.body || state != "candidate" || count < 7 || bad != 0 || allBad != 0 || productionBad != 0 || gateDecisions != 0 || personDecisions != 0 || receipts < 4 {
				t.Fatalf("title=%q body=%q state=%q seed events=%d bad=%d all bad=%d production bad=%d receipts=%d gate decisions=%d person decisions=%d", title, body, state, count, bad, allBad, productionBad, receipts, gateDecisions, personDecisions)
			}
			out.Reset()
			if err := journey.RunOperator(ctx, f.db.App, args, &out); err != nil || !strings.Contains(out.String(), `"pending_action":"approve_candidate"`) || !strings.Contains(out.String(), `"already":true`) {
				t.Fatalf("brief replay: %v %s", err, out.String())
			}
			if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
				var replayCount int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE type LIKE 'journey.seed_%' AND node_id=$1::uuid`, project).Scan(&replayCount); err != nil {
					return err
				}
				if replayCount != count {
					t.Fatalf("replay added events: %d -> %d", count, replayCount)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			other := "1"
			if brief.number == "1" {
				other = "2"
			}
			otherArgs := []string{"seed", "--tenant", "journey-a", "--project", "PRJ-801", "--to-stage", "deploy", "--brief", other}
			if brief.number == "1" {
				otherArgs = append(otherArgs, "--production", "--confirm-project", "PRJ-801")
			}
			if err := journey.RunOperator(ctx, f.db.App, otherArgs, &out); err == nil || !strings.Contains(err.Error(), "different brief") {
				t.Fatalf("different brief accepted: %v", err)
			}
			if brief.number == "1" || brief.number == "2" {
				nextState := "deploying"
				if brief.number == "2" {
					nextState = "released"
				}
				if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `UPDATE journey_releases SET state=$2,released_at=CASE WHEN $2='released' THEN now() ELSE NULL END WHERE release_node_id=$1::uuid`, *view.CurrentReleaseID, nextState)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := journey.RunOperator(ctx, f.db.App, args, &out); err == nil || !strings.Contains(err.Error(), "deployed or released") {
					t.Fatalf("%s project was seeded: %v", nextState, err)
				}
			}
		})
	}
}

func TestDisposableBriefsUseExistingBuildingRelease(t *testing.T) {
	for _, tc := range []struct{ brief, target string }{
		{"1", "deploy"},
		{"2", "candidate"},
		{"3", "deploy"},
	} {
		t.Run(tc.brief, func(t *testing.T) {
			t.Setenv("AEON_ENV", "dev")
			f := newFixture(t)
			project := f.node(t, "project", "PRJ-36", "Existing disposable project")
			if _, err := journey.MarkDisposable(t.Context(), f.db.App, "journey-a", "PRJ-36"); err != nil {
				t.Fatal(err)
			}
			release := f.node(t, "release", "REL-1", "Existing release")
			ticket := f.node(t, "ticket", "TKT-1", "Earlier completed ticket")
			ctx := t.Context()
			if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, `SELECT aeon_seed_requirement_kind($1::uuid)`, f.tenant); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO journey_projects(tenant_id,project_node_id,current_release_node_id)
					VALUES($1::uuid,$2::uuid,$3::uuid)`, f.tenant, project, release); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `INSERT INTO journey_releases(tenant_id,release_node_id,project_node_id,number,state)
					VALUES($1::uuid,$2::uuid,$3::uuid,1,'building')`, f.tenant, release, project); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE nodes SET state='done' WHERE id=$1::uuid`, ticket); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `INSERT INTO journey_tickets(tenant_id,ticket_node_id,project_node_id,release_node_id,walker_position,source)
					VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,0,'manual')`, f.tenant, ticket, project, release)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			before := f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
			if before.NextAction.Key != "continue_intake" {
				t.Fatalf("expected unaccepted brief before seed, got %+v", before.NextAction)
			}
			args := []string{"seed", "--tenant", "journey-a", "--project", "PRJ-36", "--to-stage", tc.target, "--brief", tc.brief}
			if tc.brief == "1" {
				t.Setenv("AEON_ENV", "prod")
				args = append(args, "--production", "--confirm-project", "PRJ-36")
			}
			var out bytes.Buffer
			if err := journey.RunOperator(ctx, f.db.App, args, &out); err != nil {
				t.Fatalf("seed existing building release: %v", err)
			}
			if !strings.Contains(out.String(), `"pending_action":"approve_candidate"`) {
				t.Fatalf("seed did not stop at candidate gate: %s", out.String())
			}
			view := f.journey(t, f.person, "GET", "/api/projects/"+project+"/journey", "")
			if view.NextAction.Key != "approve_candidate" || view.NextAction.Available || view.CurrentReleaseID == nil || *view.CurrentReleaseID != release {
				t.Fatalf("existing release or human gate changed: %+v", view)
			}
			var releaseCount, accepted, selected, decisions int
			var state string
			if err := db.InTenant(dbtest.Seed(ctx), f.db.App, f.tenant, func(tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_releases WHERE project_node_id=$1::uuid`, project).Scan(&releaseCount); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT state FROM journey_releases WHERE release_node_id=$1::uuid`, release).Scan(&state); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM intake_draft_acceptances WHERE project_node_id=$1::uuid`, project).Scan(&accepted); err != nil {
					return err
				}
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM journey_tickets WHERE project_node_id=$1::uuid AND release_node_id=$2::uuid`, project, release).Scan(&selected); err != nil {
					return err
				}
				return tx.QueryRow(ctx, `SELECT count(*) FROM events WHERE node_id=$1::uuid AND type IN ('journey.candidate_approved','journey.deploy_approved')`, project).Scan(&decisions)
			}); err != nil {
				t.Fatal(err)
			}
			if releaseCount != 1 || state != "candidate" || accepted != 1 || selected != 2 || decisions != 0 {
				t.Fatalf("release count=%d state=%s accepted=%d selected=%d decisions=%d", releaseCount, state, accepted, selected, decisions)
			}
			out.Reset()
			if err := journey.RunOperator(ctx, f.db.App, args, &out); err != nil || !strings.Contains(out.String(), `"already":true`) || !strings.Contains(out.String(), `"pending_action":"approve_candidate"`) {
				t.Fatalf("seed replay: %v %s", err, out.String())
			}
		})
	}
}
