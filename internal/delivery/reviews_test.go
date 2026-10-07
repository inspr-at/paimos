// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func reviewsPath(f *fixture) string { return "/api/projects/" + f.project + "/delivery-reviews" }
func newReviewFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t)
	f.tx(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET state='active' WHERE id=$1`, f.project)
		return err
	})
	return f
}
func grantReviewAgent(t *testing.T, f *fixture) {
	t.Helper()
	perms := []string{"delivery_reviews.read", "delivery_reviews.manage", "delivery_reviews.claim", "delivery_reviews.report"}
	f.agent.Scopes = append(f.agent.Scopes, perms...)
	f.tx(t, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'review_controller','Review controller') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
			return err
		}
		for _, permission := range append(perms, "nodes.read") {
			if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, f.person.TenantID, role, permission); err != nil {
				return err
			}
		}
		return nil
	})
	dbtest.BindRole(t, f.d, f.agent.TenantID, f.agent.ID, "review_controller")
}
func reviewSource(t *testing.T, f *fixture, slug, kind string, n int, head string) (ReviewInput, Round) {
	t.Helper()
	var run string
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,status)
 SELECT $1,$2,$3,model_profile_id,'completed' FROM agent_runs WHERE id=$4 RETURNING id::text`, f.person.TenantID, f.buildOrder, f.agent.ID, f.authorRun).Scan(&run)
	})
	source := Round{RoundInput: roundInput(f, slug, kind, n), ID: requestID(100 + n), Project: f.project, Key: "AEON-848", State: "done", Position: int64(100 + n), Revision: 1, Updated: f.at, Reason: "shadow_done"}
	f.tx(t, func(tx pgx.Tx) error { return saveRoundTx(t.Context(), tx, f.person.TenantID, source) })
	return ReviewInput{SourceRound: source.ID, AuthorRun: run, Repository: f.m.config.Repository, Base: strings.Repeat("a", 40), Head: head}, source
}
func seedReviewRoute(t *testing.T, f *fixture) string {
	t.Helper()
	request := httptest.NewRequest("GET", "/api/models", nil)
	if err := modelregistry.PrepareCatalog(t.Context(), f.d.App, f.person, modelregistry.CatalogPreparation{Operation: modelregistry.CatalogRead, Request: request}); err != nil {
		t.Fatal(err)
	}
	var profile, account string
	f.tx(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM model_role_routes WHERE role='review-gate'`); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO model_profiles(tenant_id,slug,version,harness,family,model,effort,tier) VALUES($1,'shadow-reviewer','test','claude','anthropic','claude-opus-4-6','xhigh','frontier') RETURNING id::text`, f.person.TenantID).Scan(&profile); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO model_role_routes(tenant_id,role,priority,profile_id,state) VALUES($1,'review-gate',1,$2,'available')`, f.person.TenantID, profile); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner)
 VALUES($1,'shadow-review-account','claude','test-daemon',$2,'Shadow account',$3,true,'test-generation',$4) RETURNING id::text`, f.person.TenantID, f.agent.ID, f.at, f.person.ID).Scan(&account); err != nil {
			return err
		}
		s := capacity.DefaultSchedule()
		for i := range s.Week {
			s.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		s.Reserve = capacity.ReserveOff
		s.Override = "sprint"
		raw, _ := json.Marshal(s)
		if _, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3::text,$3::uuid,$4)`, f.person.TenantID, f.person.ID, account, raw); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance) VALUES($1,$2,$3,$4,'tokens',10000000)`, f.person.TenantID, account, f.at.Add(-time.Hour), f.at.Add(time.Hour))
		return err
	})
	return profile
}
func reviewEvents(t *testing.T, f *fixture) int {
	t.Helper()
	var count int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type LIKE 'delivery.review.%'`).Scan(&count)
	})
	return count
}

func TestDeliveryReviewsSeverityFloorAndBoundedObservations(t *testing.T) {
	// Risk: Low/Nit creates a blocking loop, Medium incorrectly passes, or
	// malformed/oversize findings enter durable events and proposed tickets.
	base := ReviewVerdictInput{Request: requestID(1), Revision: 1, Head: strings.Repeat("b", 40), Profile: requestID(2), Verdict: "changes"}
	for _, severity := range []string{"high", "medium", "low", "nit"} {
		in := base
		in.Findings = []reviewgate.Finding{{Severity: severity, File: "internal/example.go", Line: 4, Message: "Fix the condition."}}
		if err := validateReviewVerdict(in); err != nil {
			t.Fatal(err)
		}
		out := ReviewRound{ID: requestID(3), Project: requestID(4), Key: "AEON-890"}
		applyReviewVerdict(requestID(5), &out, in, Round{}, nil, 1)
		blocking := severity == "high" || severity == "medium"
		if blocking && (out.Action != "fix" || out.EffectiveVerdict != "changes" || out.Fix == nil || out.FollowUp != nil) || !blocking && (out.Action != "ready_to_ship" || out.EffectiveVerdict != "ok" || out.Fix != nil || out.FollowUp == nil) {
			t.Fatalf("wrong floor for %s: %+v", severity, out)
		}
		if blocking {
			in.Verdict = "ok"
			if validateReviewVerdict(in) == nil {
				t.Fatalf("ok accepted a %s blocker", severity)
			}
		}
	}
	for _, change := range []func(*ReviewVerdictInput){
		func(in *ReviewVerdictInput) { in.Findings = nil },
		func(in *ReviewVerdictInput) { in.Findings = make([]reviewgate.Finding, 101) },
		func(in *ReviewVerdictInput) { in.Findings[0].Severity = "unknown" },
		func(in *ReviewVerdictInput) { in.Findings[0].File = "../secret" },
		func(in *ReviewVerdictInput) { in.Findings[0].File = "/absolute" },
		func(in *ReviewVerdictInput) { in.Findings[0].Message = strings.Repeat("x", 2001) },
		func(in *ReviewVerdictInput) { in.Findings[0].Line = 0 },
	} {
		in := base
		in.Findings = []reviewgate.Finding{{Severity: "low", File: "example.go", Line: 1, Message: "Note"}}
		change(&in)
		if validateReviewVerdict(in) == nil {
			t.Fatal("invalid observation accepted")
		}
	}
}

func TestDeliveryReviewsShadowFixCapFollowUpReplayAndIsolation(t *testing.T) {
	// Risks: a script observation opens real approvals, a caller resets the
	// gate cap, a replay repeats actions, revoked scopes or another tenant reads
	// bindings/findings, or stale heads and mismatched reviewer profiles pass.
	f := newReviewFixture(t)
	f.build(t)
	path := reviewsPath(f)
	var page ReviewPage
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if page.Settings.Mode != "off" || len(page.Items) != 0 {
		t.Fatal("reviews were not default off")
	}
	// A business leaf with a build work order remains eligible for the queue;
	// otherwise real build completions could never reach review orchestration.
	f.call(t, f.person, "POST", queuePath(f), roundInput(f, "attached-build-ticket", "land", 12), 201, nil)
	in, _ := reviewSource(t, f, "review-loop", "first_build", 1, strings.Repeat("b", 40))
	var first ReviewRound
	f.call(t, f.person, "POST", path, in, 201, &first)
	n := reviewEvents(t, f)
	var retry ReviewRound
	f.call(t, f.person, "POST", path, in, 200, &retry)
	if !reflect.DeepEqual(first, retry) || reviewEvents(t, f) != n {
		t.Fatal("enqueue was not idempotent")
	}
	canonical := in
	canonical.AuthorRun = strings.ToUpper(in.AuthorRun)
	f.call(t, f.person, "POST", "/api/projects/"+strings.ToUpper(f.project)+"/delivery-reviews", canonical, 200, &retry)
	if !reflect.DeepEqual(first, retry) || reviewEvents(t, f) != n {
		t.Fatal("UUID case changed the review identity")
	}
	other := in
	other.Head = strings.Repeat("f", 40)
	f.call(t, f.person, "POST", path, other, 409, nil)
	f.call(t, f.foreign, "GET", path, nil, 404, nil)
	f.call(t, f.foreign, "POST", path, in, 404, nil)
	f.call(t, f.foreign, "POST", path+"/"+first.ID+"/claim", ReviewClaimInput{Request: requestID(1)}, 404, nil)
	var off ReviewClaim
	f.call(t, f.person, "POST", path+"/"+first.ID+"/claim", ReviewClaimInput{Request: requestID(1)}, 200, &off)
	if off.Execute || off.Round != nil || off.Reason != "review_off" {
		t.Fatal("disabled reviews claimed work")
	}
	f.agent.Scopes = append(f.agent.Scopes, "delivery_reviews.manage", "delivery_reviews.claim", "delivery_reviews.report")
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", ReviewClaimInput{Request: requestID(2)}, 403, nil)
	grantReviewAgent(t, f)
	s := ReviewSettings{Mode: "shadow"}
	f.call(t, f.person, "PUT", path+"/settings", s, 200, &s)
	f.call(t, f.person, "PUT", path+"/settings", ReviewSettings{Mode: "act", Revision: s.Revision}, 400, nil)
	f.call(t, f.person, "PUT", path+"/settings", ReviewSettings{Mode: "shadow"}, 409, nil)
	var cold ReviewClaim
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", ReviewClaimInput{Request: requestID(3)}, 200, &cold)
	if cold.Execute || cold.Round != nil || cold.Reason != "catalog_unavailable" {
		t.Fatal("cold catalog did not fail closed", cold)
	}
	profile := seedReviewRoute(t, f)
	var otherEvents int
	f.tx(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type NOT LIKE 'delivery.review.%'`).Scan(&otherEvents)
	})
	// A policy that allows only the author family cannot manufacture a route.
	setPolicy := func(families []string) {
		f.tx(t, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `INSERT INTO cross_family_policies(tenant_id,project_id,mode,allowed_families,updated_by) VALUES($1,$2,'allowlist',$3,$4)
 ON CONFLICT(tenant_id,(coalesce(project_id,'00000000-0000-0000-0000-000000000000'::uuid))) DO UPDATE SET allowed_families=EXCLUDED.allowed_families`, f.person.TenantID, f.project, families, f.person.ID)
			return err
		})
	}
	setPolicy([]string{"openai"})
	var denied ReviewClaim
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", ReviewClaimInput{Request: requestID(4)}, 200, &denied)
	if denied.Round != nil || denied.Execute || denied.Reason != "reviewer_unavailable" {
		t.Fatal("family policy ignored", denied)
	}
	setPolicy([]string{"anthropic"})
	reviewer := "anthropic"
	claimInput := ReviewClaimInput{Request: requestID(5), ScriptFamily: &reviewer}
	var claim ReviewClaim
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", claimInput, 200, &claim)
	if claim.Execute || claim.Round == nil || claim.Round.Route == nil || claim.Round.Route.Profile != profile || claim.Round.Route.Family == claim.Round.AuthorFamily || claim.Agreement == nil || !*claim.Agreement {
		t.Fatal("wrong shadow route", claim)
	}
	// Keep a detached receipt: later response decoding reuses pointer fields.
	var originalClaim ReviewClaim
	claimBytes, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(claimBytes, &originalClaim); err != nil {
		t.Fatal(err)
	}
	n = reviewEvents(t, f)
	var duplicate ReviewClaim
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", claimInput, 200, &duplicate)
	if !reflect.DeepEqual(claim, duplicate) || n != reviewEvents(t, f) {
		t.Fatal("claim replay changed ownership")
	}
	f.call(t, f.person, "POST", path+"/"+first.ID+"/claim", claimInput, 409, nil)
	verdict := ReviewVerdictInput{Request: requestID(6), Revision: claim.Round.Revision, Head: first.Head, Profile: profile, Verdict: "changes", Findings: []reviewgate.Finding{{Severity: "medium", File: "internal/example.go", Line: 4, Message: "Recheck the target."}, {Severity: "low", File: "internal/example.go", Line: 5, Message: "Simplify when cheap."}, {Severity: "nit", File: "internal/example.go", Line: 6, Message: "Adjust wording."}}}
	wrong := verdict
	wrong.Head = strings.Repeat("f", 40)
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/verdict", wrong, 409, nil)
	wrong = verdict
	wrong.Profile = requestID(900)
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/verdict", wrong, 409, nil)
	f.call(t, f.person, "POST", path+"/"+first.ID+"/verdict", verdict, 403, nil)
	f.call(t, f.foreign, "POST", path+"/"+first.ID+"/verdict", verdict, 404, nil)
	setPolicy([]string{"openai"})
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/verdict", verdict, 409, nil)
	setPolicy([]string{"anthropic"})
	action := "fix"
	verdict.ScriptAction = &action
	var completed ReviewRound
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/verdict", verdict, 200, &completed)
	if completed.Fix == nil || completed.GateFixRounds != 0 || completed.Action != "fix" || completed.FollowUp == nil || len(completed.FollowUp.Findings) != 2 || completed.FollowUp.Parent == nil || *completed.FollowUp.Parent != f.project || !completed.FollowUp.Hidden || completed.FollowUp.PillEN == "" || completed.FollowUp.PillDE == "" || completed.Agreement == nil || !*completed.Agreement {
		t.Fatal("first fix/follow-up missing", completed)
	}
	n = reviewEvents(t, f)
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/verdict", verdict, 200, &retry)
	if !reflect.DeepEqual(completed, retry) || n != reviewEvents(t, f) {
		t.Fatal("verdict duplicated proposals/events")
	}
	// The cap belongs to this slug's review chain, not the global fix number.
	// A CI/static fix with number 8 consumes no extra gate-triggered round.
	for i := 1; i <= 2; i++ {
		next, source := reviewSource(t, f, "review-loop", "fix", 7+i, strings.Repeat(string(rune('b'+i)), 40))
		var queued ReviewRound
		f.call(t, f.person, "POST", path, next, 201, &queued)
		if queued.Previous == nil || *queued.Previous != completed.ID || queued.DeltaBase == nil || *queued.DeltaBase != completed.Head || queued.GateFixRounds != i {
			t.Fatal("delta re-gate or cap lineage lost", queued)
		}
		f.call(t, f.agent, "POST", path+"/"+queued.ID+"/claim", ReviewClaimInput{Request: requestID(20 + i)}, 200, &claim)
		if claim.Round == nil {
			t.Fatal("fix review not claimable", claim)
		}
		verdict = ReviewVerdictInput{Request: requestID(30 + i), Revision: claim.Round.Revision, Head: queued.Head, Profile: profile, Verdict: "changes", Findings: []reviewgate.Finding{{Severity: "high", File: "internal/example.go", Line: 4, Message: "Still requires a fix."}}}
		f.call(t, f.agent, "POST", path+"/"+queued.ID+"/verdict", verdict, 200, &completed)
		if i == 1 && (completed.Action != "fix" || completed.Fix == nil || completed.Fix.Number != source.Number+1) || i == 2 && (completed.Action != "lead_decision" || completed.Lead == nil || len(completed.Lead.Options) != 3 || completed.Fix != nil) {
			t.Fatal("incorrect fix cap", completed)
		}
	}
	next, _ := reviewSource(t, f, "review-loop", "fix", 10, strings.Repeat("e", 40))
	f.call(t, f.person, "POST", path, next, 409, nil)
	// A separate slug with Low/Nit-only changes passes the severity floor and
	// groups both notes into one proposed follow-up, with no fix round.
	noteInput, _ := reviewSource(t, f, "notes-only", "first_build", 11, strings.Repeat("e", 40))
	var notes ReviewRound
	f.call(t, f.person, "POST", path, noteInput, 201, &notes)
	f.call(t, f.agent, "POST", path+"/"+notes.ID+"/claim", ReviewClaimInput{Request: requestID(50)}, 200, &claim)
	if claim.Round == nil {
		t.Fatal("note review not claimable", claim)
	}
	verdict = ReviewVerdictInput{Request: requestID(51), Revision: claim.Round.Revision, Head: notes.Head, Profile: profile, Verdict: "changes", Findings: []reviewgate.Finding{{Severity: "low", File: "example.go", Line: 1, Message: "Low note"}, {Severity: "nit", File: "example.go", Line: 2, Message: "Nit note"}}, ScriptAction: &action}
	f.call(t, f.agent, "POST", path+"/"+notes.ID+"/verdict", verdict, 200, &notes)
	if notes.Action != "ready_to_ship" || notes.EffectiveVerdict != "ok" || notes.Fix != nil || notes.FollowUp == nil || len(notes.FollowUp.Findings) != 2 || notes.Agreement == nil || *notes.Agreement {
		t.Fatal("Low/Nit floor or comparison wrong", notes)
	}
	f.call(t, f.person, "GET", path+"?limit=1", nil, 200, &page)
	if len(page.Items) != 1 || page.Next == nil {
		t.Fatal("missing keyset cursor")
	}
	f.call(t, f.person, "GET", path+"?after="+*page.Next, nil, 200, &page)
	if len(page.Items) != 3 || page.Items[0].Position <= first.Position {
		t.Fatal("keyset lost reviews")
	}
	f.call(t, f.person, "GET", path, nil, 200, &page)
	beforeBytes, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	// Queue projection replay deletes/re-inserts its physical rows. Review
	// observations must not prevent that independent replay with an FK.
	f.tx(t, func(tx pgx.Tx) error {
		source, err := loadRoundTx(t.Context(), tx, f.project, first.SourceRound)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(t.Context(), `DELETE FROM delivery_work_rounds WHERE id=$1`, source.ID); err != nil {
			return err
		}
		return saveRoundTx(t.Context(), tx, f.person.TenantID, source)
	})
	if err := f.m.RebuildReviews(t.Context(), f.person, f.project); err != nil {
		t.Fatal(err)
	}
	f.call(t, f.person, "GET", path, nil, 200, &page)
	afterBytes, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Fatal("replay changed projections")
	}
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", claimInput, 200, &duplicate)
	if !reflect.DeepEqual(duplicate, originalClaim) {
		t.Fatal("replay lost original claim receipt")
	}
	// No real downstream work was created by shadow orchestration.
	f.tx(t, func(tx pgx.Tx) error {
		var orders, approvals, followups int
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM work_order_reviews),(SELECT count(*) FROM nodes WHERE title LIKE '%follow-up:%'),(SELECT count(*) FROM events WHERE type NOT LIKE 'delivery.review.%')`).Scan(&orders, &followups, &approvals); err != nil {
			return err
		}
		if orders != 0 || followups != 0 || approvals != otherEvents {
			return fmt.Errorf("shadow created real actions: reviews=%d followups=%d other events=%d (before %d)", orders, followups, approvals, otherEvents)
		}
		_, err := tx.Exec(t.Context(), `DELETE FROM role_permissions WHERE permission='delivery_reviews.report' AND role_id=(SELECT id FROM roles WHERE key='review_controller')`)
		return err
	})
	f.call(t, f.agent, "POST", path+"/"+notes.ID+"/verdict", verdict, 403, nil)
	// Even an original receipt is hidden after its current ticket is deleted.
	f.tx(t, func(tx pgx.Tx) error {
		// Retain the execution row and run evidence while respecting the
		// repository's parent-deletion guard: retire its child first.
		if _, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.buildOrder); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET deleted_at=clock_timestamp() WHERE id=$1`, f.ticket)
		return err
	})
	f.call(t, f.agent, "POST", path+"/"+first.ID+"/claim", claimInput, 404, nil)
	f.call(t, f.person, "GET", path, nil, 200, &page)
	if len(page.Items) != 0 {
		t.Fatal("deleted ticket still exposed review bindings")
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"delivery_review_rounds", "delivery_review_settings", "delivery_review_claims"} {
			var n int
			if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM `+table+` WHERE tenant_id=$1`, f.person.TenantID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("tenant leaked through %s", table)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryReviewsConcurrentClaimsHaveOneOwner(t *testing.T) {
	// Risk: concurrent launchers receive the same review. Hold the tenant/tree
	// fence until both claim requests are visibly blocked on it; no sleeps.
	f := newReviewFixture(t)
	f.build(t)
	grantReviewAgent(t, f)
	seedReviewRoute(t, f)
	path := reviewsPath(f)
	f.call(t, f.person, "PUT", path+"/settings", ReviewSettings{Mode: "shadow"}, 200, nil)
	in, _ := reviewSource(t, f, "claim-race", "first_build", 1, strings.Repeat("b", 40))
	var round ReviewRound
	f.call(t, f.person, "POST", path, in, 201, &round)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type result struct {
		code  int
		claim ReviewClaim
		err   error
	}
	results := make(chan result, 2)
	err := db.InTenant(tenant.WithPrincipal(ctx, f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := db.LockTree(ctx, tx, f.person.TenantID); err != nil {
			return err
		}
		var blocker int32
		if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
			return err
		}
		for _, id := range []int{1, 2} {
			go func() {
				raw, _ := json.Marshal(ReviewClaimInput{Request: requestID(id)})
				r := httptest.NewRequest("POST", path+"/"+round.ID+"/claim", strings.NewReader(string(raw)))
				r = r.WithContext(tenant.WithPrincipal(ctx, f.agent))
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				var out ReviewClaim
				err := json.Unmarshal(w.Body.Bytes(), &out)
				results <- result{w.Code, out, err}
			}()
		}
		for {
			var waiters int
			// PostgreSQL can queue the second waiter behind the first waiter
			// rather than directly behind the holder. Prove the entire chain.
			if err := f.d.Admin.QueryRow(ctx, `WITH RECURSIVE waiters AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT a.pid FROM pg_stat_activity a JOIN waiters w ON w.pid=ANY(pg_blocking_pids(a.pid))
 ) SELECT count(*) FROM waiters`, blocker).Scan(&waiters); err != nil {
				return err
			}
			if waiters >= 2 {
				return nil
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	winners := 0
	for range 2 {
		select {
		case r := <-results:
			if r.code != 200 || r.err != nil || r.claim.Execute {
				t.Fatalf("claim failed: %+v", r)
			}
			if r.claim.Round != nil {
				winners++
			} else if r.claim.Reason != "review_owned_or_completed" {
				t.Fatalf("wrong loser reason: %+v", r.claim)
			}
		case <-ctx.Done():
			t.Fatal("concurrent claims did not finish")
		}
	}
	if winners != 1 {
		t.Fatalf("%d launchers own the review", winners)
	}
}
