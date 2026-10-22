// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Exercise the authenticated store, rather than inventing a coordinator OK.
func submitEvidenceReview(t *testing.T, f *fixture, p Pull, verdict string, start, end time.Time) reviewgate.Run {
	t.Helper()
	ctx := t.Context()
	reviewer := tenant.Principal{TenantID: f.person.TenantID, Kind: tenant.Agent, Scopes: []string{"nodes.read"}, KeyCreatorID: f.person.ID}
	var authorSession, reviewerSession string
	f.tx(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Evidence reviewer') RETURNING id::text`, reviewer.TenantID).Scan(&reviewer.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','fixture',decode(repeat('00',32),'hex'),$3,$4) RETURNING id::text`, reviewer.TenantID, reviewer.ID, reviewer.Scopes, f.person.ID).Scan(&reviewer.KeyID); err != nil {
			return err
		}
		reviewer.AuthKeyID = reviewer.KeyID
		for _, s := range []struct {
			principal, harness, model string
			id                        *string
		}{{f.agent.ID, "codex", "gpt-6.1-sol", &authorSession}, {reviewer.ID, "claude", "claude-opus-4-6", &reviewerSession}} {
			digest := sha256.Sum256([]byte(reviewer.ID + s.principal))
			if err := tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,ticket_node_id,harness,model,reasoning_effort,host,management,role,work_shape,ref_digest,lease_digest,created_at) VALUES($1,$2,$3,$4,$5,$6,'high','fixture','unmanaged','worker','ship',$7,$7,$8) RETURNING id::text`, f.person.TenantID, f.project, s.principal, f.ticket, s.harness, s.model, digest[:], start).Scan(s.id); err != nil {
				return err
			}
		}
		return nil
	})
	dbtest.BindRole(t, f.d, reviewer.TenantID, reviewer.ID, "viewer")
	var run reviewgate.Run
	f.tx(t, func(tx pgx.Tx) error {
		var err error
		run, err = reviewgate.CreateRunTx(ctx, tx, f.person, f.ticket, reviewgate.RunCreate{Repository: f.m.config.Repository, PR: p.Number, Target: p.Target,
			Base: p.Base, Head: p.Head, Scope: p.Base + ".." + p.Head, AuthorSession: authorSession, ReviewerSession: reviewerSession, Kind: "review"}, start)
		if err != nil {
			return err
		}
		credential, _, err := reviewgate.ClaimRunTx(ctx, tx, reviewer, run.ID, start)
		if err != nil {
			return err
		}
		run, err = reviewgate.SubmitRunTx(ctx, tx, reviewer, run.ID, credential,
			reviewgate.RunSubmit{Binding: run.RunBinding, Verdict: verdict, VisualImpact: "no", Reason: "Fixture exact range"}, end)
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, reviewer, events.Change{NodeID: &f.ticket, Type: "coordinator_review.recorded", At: &end,
			After: map[string]any{"run_id": run.ID, "ok": verdict == "ok"}})
		return err
	})
	return run
}

func evidenceCount(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'delivery.evidence.%'`).Scan(&n)
	})
	return n
}

func TestDeliveryEvidenceFeedsNativeAndReportedMeasurements(t *testing.T) {
	// Risk: real harness/OPS work stays absent, duplicate telemetry inflates
	// counts, or measurement claims gain review/merge authority or cross projects.
	f, github := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	p := f.pull()
	p.Target = "main"
	f.gh.pulls[p.Number] = p
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	run := submitEvidenceReview(t, f, p, "changes", f.at.Add(-15*time.Minute), f.at.Add(-7*time.Minute))
	// A legacy assertion with no submitted reviewer capability remains absent.
	appendFlowSource(t, f, events.Change{NodeID: &f.ticket, Type: "coordinator_review.recorded", After: map[string]any{"ok": true}, At: &f.at})
	path := "/api/projects/" + f.project + "/delivery/metrics"
	report := metricFactsInput{Repository: f.m.config.Repository, Facts: []metricFactInput{
		{Kind: "review", Key: "harness-copy", HeadSHA: &p.Head, PullRequest: &p.Number, StartedAt: ptrTime(f.at.Add(-15 * time.Minute)), At: f.at.Add(-7 * time.Minute), Outcome: "changes"},
		{Kind: "merge_round", Key: "merge-script", PullRequest: &p.Number, At: f.at.Add(-6 * time.Minute), Outcome: "scripted"},
		{Kind: "merge_round", Key: "merge-model", PullRequest: &p.Number, At: f.at.Add(-5 * time.Minute), Outcome: "model"},
		{Kind: "review_audit", Key: "audit-week-pr7", PullRequest: &p.Number, At: f.at.Add(-4 * time.Minute), Outcome: "medium"},
	}}
	for i := 0; i < 2; i++ {
		var receipt map[string]int
		f.call(t, f.person, "POST", path+"/facts", report, 200, &receipt)
		if receipt["stored"] != len(report.Facts) || evidenceCount(t, f) != len(report.Facts) {
			t.Fatal("evidence replay lost its receipt or duplicated events")
		}
	}
	rollout := map[string]any{"schema": "aeon.rollout.v1", "repository": f.m.config.Repository, "direction": "forward", "release": "140", "version": "v140", "outcome": "live",
		"cut_at": f.at.Add(-60 * time.Minute), "live_at": f.at.Add(-10 * time.Minute), "healthy_at": f.at.Add(-9 * time.Minute)}
	for i := 0; i < 2; i++ {
		f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", rollout, 200, nil)
		if evidenceCount(t, f) != 5 {
			t.Fatal("rollout replay duplicated release evidence")
		}
	}
	var out DeliveryMetrics
	f.call(t, f.person, "GET", path, nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "review_time"), 7, "partial", 1, f64(8), f64(8), f64(8))
	wantWindow(t, metricByKey(t, out.Metrics, "review_changes_share"), 7, "partial", 1, f64(100), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "merge_rounds_model_share"), 7, "partial", 2, f64(50), nil, nil)
	wantWindow(t, metricByKey(t, out.Metrics, "release_queue_to_live"), 7, "partial", 1, f64(50), f64(50), f64(50))
	wantWindow(t, metricByKey(t, out.Metrics, "review_audits"), 7, "partial", 1, f64(1), nil, nil)
	if metricByKey(t, out.Metrics, "preflight_red_rate").Status != "no_data" {
		t.Fatal("harness evidence fabricated GitHub preflight observations")
	}
	rollout["repository"] = "example/changed-source"
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/flow/rollout", rollout, 409, nil)
	if evidenceCount(t, f) != 5 {
		t.Fatal("stale rollout binding changed evidence")
	}
	// A correction updates one projection row and emits one new observation.
	report.Facts = report.Facts[3:]
	report.Facts[0].Outcome = "high"
	f.call(t, f.person, "POST", path+"/facts", report, 200, nil)
	if evidenceCount(t, f) != 6 {
		t.Fatal("audit correction not recorded exactly once")
	}
	report.Repository = "example/changed-source"
	f.call(t, f.person, "POST", path+"/facts", report, 409, nil)
	f.call(t, f.foreign, "POST", path+"/facts", report, 404, nil)
	reader := f.agent
	reader.Scopes = []string{"delivery.read"}
	f.call(t, reader, "POST", path+"/facts", report, 403, nil)
	if evidenceCount(t, f) != 6 {
		t.Fatal("refused fact mutated evidence")
	}
	// Preflight uses the signed run/attempt feed supplied by AEON-1658.
	// Replaying it creates neither another run nor another evidence event.
	github.source = historyChecks
	preflight := githubRunBody(1659, 1, defaultPreflightWorkflow, "workflow_dispatch", "main", p.Head, f.at.Add(-3*time.Minute), time.Minute, "failure", 1659)
	for _, id := range []string{"evidence-preflight", "evidence-preflight-replay"} {
		auditSend(t, f, "workflow_run", id, map[string]any{"action": "completed", "workflow_run": preflight}, 204)
		if evidenceCount(t, f) != 7 {
			t.Fatal("preflight replay duplicated typed evidence")
		}
	}
	f.call(t, f.person, "GET", path, nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "preflight_red_rate"), 7, "partial", 1, f64(100), nil, nil)
	if metricByKey(t, out.Metrics, "first_attempt_green").Status != "no_data" {
		t.Fatal("preflight became a CI first attempt")
	}
	// Native evidence is available independently of Flow's cursor and a manual
	// report; a source switched to another repository sees neither.
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM delivery_metric_marks WHERE kind='review'`); err != nil {
			return err
		}
		var rows int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM review_runs WHERE id=$1 AND submitted_at IS NOT NULL`, run.ID).Scan(&rows); err != nil || rows != 1 {
			t.Fatal("fixture lost the actual submitted run", err)
		}
		return nil
	})
	f.call(t, f.person, "GET", path, nil, 200, &out)
	wantWindow(t, metricByKey(t, out.Metrics, "review_time"), 7, "partial", 1, f64(8), f64(8), f64(8))
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE delivery_metric_sources SET repository='example/other' WHERE project_id=$1`, f.project)
		return err
	})
	f.call(t, f.person, "GET", path, nil, 200, &out)
	if metricByKey(t, out.Metrics, "review_time").Status != "no_data" {
		t.Fatal("native review leaked across repository bindings")
	}
}

func TestDeliveryMergeCompletionEmitsExplicitResolution(t *testing.T) {
	// Risk: shadow progress invents a model/script classification, emits twice,
	// or records a result for a relinked round. Default execution remains closed.
	f, _ := newMetricsFixture(t)
	p := f.pull()
	f.gh.pulls[p.Number] = p
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	f.m.SetQueueAdmission(func(context.Context, pgx.Tx, tenant.Principal, Round) (bool, string, error) {
		return true, "allowed", nil
	})
	path := queuePath(f)
	s := queueDefaults()
	s.Mode = "shadow"
	f.call(t, f.person, "PUT", path+"/settings", s, 200, nil)
	for i, outcome := range []string{"scripted", "model"} {
		in := roundInput(f, "merge-"+outcome, "merge", i+1)
		in.PR = &p.Number
		var round Round
		f.call(t, f.person, "POST", path, in, 201, &round)
		var claim QueueClaim
		f.call(t, f.person, "POST", path+"/claim", ClaimInput{Request: requestID(900 + i)}, 200, &claim)
		if claim.Round == nil || claim.Round.ID != round.ID || claim.Execute {
			t.Fatal("measurement changed shadow execution", claim)
		}
		round = *claim.Round
		progress := path + "/" + round.ID + "/progress"
		f.call(t, f.person, "POST", progress, map[string]any{"revision": round.Revision, "state": "running"}, 200, &round)
		f.call(t, f.person, "POST", progress, map[string]any{"revision": round.Revision, "state": "done", "merge_outcome": outcome, "repository": f.m.config.Repository, "head_sha": strings.Repeat("e", 40)}, 409, nil)
		f.call(t, f.person, "POST", progress, map[string]any{"revision": round.Revision, "state": "done", "merge_outcome": outcome, "repository": f.m.config.Repository, "head_sha": p.Head}, 200, &round)
		f.call(t, f.person, "POST", progress, map[string]any{"revision": round.Revision, "state": "done", "merge_outcome": outcome, "repository": f.m.config.Repository, "head_sha": p.Head}, 200, &round)
		if evidenceCount(t, f) != i+1 {
			t.Fatal("progress replay duplicated measurement")
		}
	}
}

func TestMergeAuditHarnessAndQueueProvenanceRepair(t *testing.T) {
	// Risk: the #874 case (real queue, harness gate) is falsely labelled a
	// bypass, or generic reports/another SHA/late or unverified reviews clear it.
	f, _ := newMetricsFixture(t)
	f.tx(t, func(tx pgx.Tx) error { return tx.QueryRow(t.Context(), `SELECT now()`).Scan(&f.at) })
	p := f.pull()
	p.Number, p.Target = 874, "main"
	f.gh.pulls[p.Number] = p
	if err := f.m.Reconcile(t.Context(), f.person.TenantID); err != nil {
		t.Fatal(err)
	}
	submitEvidenceReview(t, f, p, "ok", f.at.Add(-20*time.Minute), f.at.Add(-12*time.Minute))
	pr := p.Number
	fact := MergeFact{SHA: strings.Repeat("c", 40), Head: p.Head, Base: p.Base, PR: &pr, At: f.at.Add(-time.Minute), Title: p.Title, Branch: p.Branch, MergedBy: "coordinator"}
	g := &evidenceAuditFake{auditFake: &auditFake{fakeGitHub: f.gh, facts: map[string]*MergeFact{fact.SHA: &fact}, checks: map[string][]Check{fact.SHA: auditGreen()}}, queued: true, t: t, pr: pr, sha: fact.SHA}
	f.m.github = g
	if err := f.m.auditMerge(t.Context(), f.person.TenantID, fact, g); err != nil {
		t.Fatal(err)
	}
	a := auditRead(t, f, fact.SHA)
	if !a.ViaQueue || !a.ReviewOK || !a.ChecksPassed || len(a.Flags) != 0 || len(g.checkSHAs) != 1 || g.checkSHAs[0] != fact.SHA {
		t.Fatalf("canonical queue/harness proof not repaired: %+v checks=%v", a, g.checkSHAs)
	}
	// A reported OK on a different head is measurement only.
	fact.SHA, fact.Head = strings.Repeat("d", 40), strings.Repeat("e", 40)
	g.sha, g.checks[fact.SHA] = fact.SHA, auditGreen()
	f.call(t, f.person, "POST", "/api/projects/"+f.project+"/delivery/metrics/facts", metricFactsInput{Facts: []metricFactInput{{Kind: "review", Key: "not-approval", HeadSHA: &fact.Head, StartedAt: ptrTime(f.at.Add(-10 * time.Minute)), At: f.at.Add(-2 * time.Minute), Outcome: "ok"}}}, 200, nil)
	if err := f.m.auditMerge(t.Context(), f.person.TenantID, fact, g); err != nil {
		t.Fatal(err)
	}
	a = auditRead(t, f, fact.SHA)
	if a.ReviewOK || len(a.Flags) != 1 || a.Flags[0] != "no_review" {
		t.Fatalf("reported OK or wrong head granted review provenance: %+v", a)
	}
	// #847 predates r138 capture. It remains unverified with a truthful label.
	before := MergeAudit{Repository: "inspr-at/paimos", PR: int64ptr(847), At: time.Date(2026, 10, 22, 10, 42, 15, 0, time.UTC), ViaQueue: true, ChecksPassed: true}
	before.Flags = auditFlags(before)
	if len(before.Flags) != 1 || before.Flags[0] != "before_evidence_capture" || !strings.Contains(auditAlertBody(before), "before evidence capture") {
		raw, _ := json.Marshal(before)
		t.Fatalf("pre-capture evidence misclassified: %s", raw)
	}
}

type evidenceAuditFake struct {
	*auditFake
	t      *testing.T
	pr     int64
	sha    string
	queued bool
}

func (g *evidenceAuditFake) AuditQueue(_ context.Context, pr int64, sha string) (bool, error) {
	if pr != g.pr || sha != g.sha {
		g.t.Fatalf("queue history read unbound subject %d %s", pr, sha)
	}
	return g.queued, nil
}

func TestDeliveryEvidenceToolingConfirmsWrites(t *testing.T) {
	// Risk: the audit producer prepares valid facts but never publishes them,
	// or a failed/mismatched receipt is exposed as recorded. No live API calls.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "--test", "../../web/tests/review-tooling.test.ts")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delivery evidence tooling: %v\n%s", err, output)
	}
}
