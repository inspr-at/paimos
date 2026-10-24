// SPDX-License-Identifier: AGPL-3.0-only
package outcomes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/auth"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: broad outcome.write or a borrowed review run could fabricate another
// ticket's outcome; retries could multiply rounds or leave an unaudited write.
func TestReviewRoundOutcomes(t *testing.T) {
	d := dbtest.Open(t)
	owner := newPerson(t, d, "review-rounds")
	project := insertNode(t, d, owner, "project", "RND-1", "Rounds", nil)
	ticket := insertNode(t, d, owner, "work", "RND-2", "A design change", &project)
	otherTicket := insertNode(t, d, owner, "work", "RND-3", "Another change", &project)
	reviewer, token := agentKey(t, d, owner.TenantID, "fixture-reviewer", []string{"nodes.read", "outcome.read", "outcome.write"})
	_, unassigned := agentKey(t, d, owner.TenantID, "fixture-unassigned", []string{"nodes.read", "outcome.read", "outcome.write"})
	foreign := newPerson(t, d, "review-rounds-foreign")
	_, foreignToken := agentKey(t, d, foreign.TenantID, "foreign-reviewer", []string{"nodes.read", "outcome.read", "outcome.write"})
	var now time.Time
	if err := d.Admin.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	run := seedOutcomeReviewRun(t, d, owner, project, ticket, reviewer)
	mod, err := auth.New(auth.Config{SessionKey: bytes.Repeat([]byte{7}, 32)}, d.App)
	if err != nil {
		t.Fatal(err)
	}
	api := outcomesAPI{t: t, auth: mod, mod: New(d.App)}
	in := ReviewRoundRecord{Ticket: ticket, Kind: "design", Round: 1, Verdict: "changes", CitedRules: []string{"U7", "A4"}, At: now.Add(-time.Hour), ReviewerRole: "design_reviewer", Artifact: strings.Repeat("a", 40), ReviewRunID: run}
	post := func(token string, in ReviewRoundRecord, key string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(in)
		return api.call(token, "POST", "/api/outcomes/review-rounds", string(raw), key)
	}
	first := post(token, in, "design-round-one")
	if first.Code != 201 {
		t.Fatalf("record own review: %d %s", first.Code, first.Body.String())
	}
	var saved reviewRoundOutcome
	if err := json.Unmarshal(first.Body.Bytes(), &saved); err != nil || saved.EventKind != "review_round" {
		t.Fatalf("outcome: %s %v", first.Body.String(), err)
	}
	// Natural identity as well as the retry key: imports and aliases cannot
	// create a second observation of the same ticket/kind/round.
	in.Ticket = "RND-2"
	in.CitedRules = []string{"A4", "U7"}
	for _, key := range []string{"design-round-one", "another-retry-key"} {
		got := post(token, in, key)
		var replay reviewRoundOutcome
		_ = json.Unmarshal(got.Body.Bytes(), &replay)
		if got.Code != 200 || replay.ID != saved.ID {
			t.Fatalf("idempotency: %d %s", got.Code, got.Body.String())
		}
	}
	for _, tc := range []struct {
		name, token string
		change      func(*ReviewRoundRecord)
		want        int
	}{
		{"unassigned", unassigned, func(*ReviewRoundRecord) {}, 403},
		{"foreign tenant", foreignToken, func(*ReviewRoundRecord) {}, 404},
		{"borrowed ticket", token, func(in *ReviewRoundRecord) { in.Ticket = otherTicket }, 403},
		{"different head", token, func(in *ReviewRoundRecord) { in.Artifact = strings.Repeat("b", 40) }, 403},
		{"different kind", token, func(in *ReviewRoundRecord) { in.Kind = "gate" }, 403},
		{"different verdict", token, func(in *ReviewRoundRecord) { in.Verdict = "ok" }, 403},
		{"agent import", token, func(in *ReviewRoundRecord) { in.Source = "imported" }, 403},
		{"not a role", token, func(in *ReviewRoundRecord) { in.ReviewerRole = "a person name" }, 400},
		{"one run cannot invent another round", token, func(in *ReviewRoundRecord) { in.Round = 2 }, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := in
			tc.change(&copy)
			got := post(tc.token, copy, "denied-round-key")
			if got.Code != tc.want {
				t.Fatalf("got %d %s, want %d", got.Code, got.Body.String(), tc.want)
			}
		})
	}
	// A person can record historical evidence, but cannot overwrite a round.
	personPost := func(in ReviewRoundRecord, key string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(in)
		req := httptest.NewRequest("POST", "/api/outcomes/review-rounds", bytes.NewReader(raw))
		req.Header.Set("Idempotency-Key", key)
		req = req.WithContext(tenant.WithPrincipal(req.Context(), owner))
		mux := http.NewServeMux()
		New(d.App).Mount(mux)
		out := httptest.NewRecorder()
		mux.ServeHTTP(out, req)
		return out
	}
	changed := in
	changed.CitedRules = []string{"U2"}
	if got := personPost(changed, "different-rule-key"); got.Code != 409 {
		t.Fatalf("conflicting round: %d %s", got.Code, got.Body.String())
	}
	wrongTarget := in
	wrongTarget.Ticket = otherTicket
	if got := personPost(wrongTarget, "wrong-target-run"); got.Code != 403 {
		t.Fatalf("person cannot consume another ticket's proof: %d %s", got.Code, got.Body.String())
	}
	imported := in
	imported.Ticket, imported.Source, imported.ReviewRunID = otherTicket, "imported", ""
	if got := personPost(imported, "imported-design-one"); got.Code != 201 {
		t.Fatalf("person import: %d %s", got.Code, got.Body.String())
	}
	if got := personPost(imported, "imported-design-one"); got.Code != 200 {
		t.Fatalf("import replay: %d %s", got.Code, got.Body.String())
	}
	// Complete the fixture's review processes before the work leaf can
	// legitimately become a parent; retain the sessions and submitted proof.
	inTenant(t, d, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason='completed' WHERE ticket_node_id=$1`, ticket)
		return err
	})
	insertNode(t, d, owner, "work", "RND-4", "A new child", &ticket)
	parent := in
	parent.Round = 2
	if got := post(token, parent, "parent-review-denied"); got.Code != 403 {
		t.Fatalf("a reviewed leaf became a parent: %d %s", got.Code, got.Body.String())
	}
	var observations, audit int
	inTenant(t, d, owner, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM outcome_review_rounds), (SELECT count(*) FROM events WHERE type='outcome.review_round')`).Scan(&observations, &audit)
	})
	if observations != 2 || audit != 2 {
		t.Fatalf("denials/retries wrote outcomes or audit: %d/%d", observations, audit)
	}
	var unscoped int
	if err := d.App.QueryRow(t.Context(), `SELECT count(*) FROM outcome_review_rounds`).Scan(&unscoped); err != nil || unscoped != 0 {
		t.Fatalf("RLS: %d %v", unscoped, err)
	}
	if _, err := d.Admin.Exec(t.Context(), `UPDATE outcome_review_rounds SET verdict='ok' WHERE id=$1`, saved.ID); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("immutable: %v", err)
	}
	read := api.call(token, "GET", "/api/projects/"+project+"/delivery/review-rounds?window=7", "", "")
	var numbers reviewNumbers
	if err := json.Unmarshal(read.Body.Bytes(), &numbers); read.Code != 200 || err != nil || numbers.Current.Tickets != 2 || numbers.Current.Open != 2 || numbers.Current.DesignMean != nil {
		t.Fatalf("open-only numbers: %d %s %v", read.Code, read.Body.String(), err)
	}
	for _, forbidden := range []string{"fixture-reviewer", "actor_principal_id", "reviewer_role", "reviewer_principal_id", "model", "harness"} {
		if strings.Contains(read.Body.String(), forbidden) {
			t.Fatalf("report leaked identity field %q", forbidden)
		}
	}
	if got := api.call(foreignToken, "GET", "/api/projects/"+project+"/delivery/review-rounds", "", ""); got.Code != 404 {
		t.Fatalf("foreign report: %d %s", got.Code, got.Body.String())
	}
	// Force the final audit append to fail: the outcome must roll back with it.
	if _, err := d.Admin.Exec(t.Context(), `CREATE FUNCTION reject_round_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.type='outcome.review_round' THEN RAISE EXCEPTION 'fixture audit rejection'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_round_audit BEFORE INSERT ON events FOR EACH ROW EXECUTE FUNCTION reject_round_audit()`); err != nil {
		t.Fatal(err)
	}
	imported.Round = 2
	if got := personPost(imported, "rolled-back-round"); got.Code != 500 {
		t.Fatalf("audit failure: %d %s", got.Code, got.Body.String())
	}
	inTenant(t, d, owner, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM outcome_review_rounds`).Scan(&observations)
	})
	if observations != 2 {
		t.Fatal("audit failure left an outcome behind")
	}
}

func seedOutcomeReviewRun(t *testing.T, d *dbtest.DB, p tenant.Principal, project, ticket, reviewer string) string {
	t.Helper()
	var run string
	inTenant(t, d, p, func(tx pgx.Tx) error {
		var authorSession, reviewerSession string
		for i, id := range []*string{&authorSession, &reviewerSession} {
			if err := tx.QueryRow(t.Context(), `INSERT INTO harness_sessions(tenant_id,project_id,ticket_node_id,agent_principal_id,harness,host,management,role,work_shape,ref_digest,lease_digest)
 VALUES($1,$2,$3,$4,'codex','fixture','unmanaged','worker','ship',decode(md5($5),'hex'),decode(md5($5),'hex')) RETURNING id::text`, p.TenantID, project, ticket, reviewer, "review-session-"+itoa(i)).Scan(id); err != nil {
				return err
			}
		}
		return tx.QueryRow(t.Context(), `INSERT INTO review_runs(tenant_id,ticket_node_id,project_id,repository,pull_request,target_branch,base_sha,head_sha,scope,kind,author_session_id,reviewer_session_id,reviewer_principal_id,author_family,reviewer_family,harness,model,reasoning_effort,created_by,created_at,expires_at,verdict,submitted_at)
 VALUES($1,$2,$3,'fixture/repo',1,'main',$4,$5,'change','visual',$6,$7,$8,'a','b','fixture','fixture','high',$9,clock_timestamp(),clock_timestamp()+interval '1 hour','changes',clock_timestamp()) RETURNING id::text`, p.TenantID, ticket, project, strings.Repeat("b", 40), strings.Repeat("a", 40), authorSession, reviewerSession, reviewer, p.ID).Scan(&run)
	})
	return run
}

// Risk: missing events and open rounds can look like a first-pass success;
// a round before the window can be lost, or late ok observations counted twice.
func TestReviewRoundsUntilOKMath(t *testing.T) {
	t.Run("rule ties use family then numeric order before truncation", func(t *testing.T) {
		// A string sort selects U10 before U4 and changes which tied rules
		// survive the panel's limit. Preserve A-before-U and count priority.
		counts := map[string]int{"U4": 2, "U5": 2, "U6": 2, "U7": 2, "U8": 2, "U9": 2, "U10": 2, "A4": 2, "A17": 2, "U21": 3}
		previous := map[string]int{"U7": 5, "U21": 1}
		want := []string{"U21", "A4", "A17", "U4", "U5", "U6", "U7", "U8", "U9", "U10"}
		for _, limit := range []int{3, 10} {
			rows := topReviewRules(counts, previous, limit)
			if len(rows) != limit {
				t.Fatalf("rule limit: got %d, want %d", len(rows), limit)
			}
			for i, row := range rows {
				if row.Rule != want[i] || row.Count != counts[want[i]] || row.Previous != previous[want[i]] {
					t.Fatalf("numeric rule order at %d: got %+v, want %s (limit %d)", i, row, want[i], limit)
				}
			}
		}
	})
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	all := []reviewObservation{}
	add := func(ticket, kind string, round int, verdict string, days int, rules ...string) {
		all = append(all, reviewObservation{Ticket: ticket, Key: ticket, Title: "Change", Kind: kind, Round: round, Verdict: verdict, At: now.AddDate(0, 0, -days), Rules: rules})
	}
	add("A", "design", 1, "changes", 10, "U2") // earlier than the seven-day window
	add("A", "design", 2, "changes", 4, "U2", "A4")
	add("A", "design", 3, "ok", 3)
	add("A", "design", 4, "ok", 2) // first ok stays round 3
	add("A", "gate", 1, "changes", 4)
	add("A", "gate", 2, "ok", 2)
	add("B", "design", 1, "changes", 2, "U7") // still open, never contributes 1
	add("B", "design", 2, "changes", 1, "U2")
	add("C", "gate", 1, "ok", 2)   // gate-only
	add("D", "design", 1, "ok", 2) // design-only
	out := aggregateReviewNumbers("project", 7, now, all)
	if out.Current.DesignMean == nil || *out.Current.DesignMean != 2 || out.Current.GateMean == nil || *out.Current.GateMean != 1.5 || out.Current.Tickets != 4 || out.Current.Open != 1 || out.Current.Design != 2 || out.Current.Gate != 2 {
		t.Fatalf("means/counts: %+v", out.Current)
	}
	if len(out.Most) != 2 || out.Most[0].Key != "A" || out.Most[1].Key != "B" || !out.Most[1].Open || out.Most[1].Latest != 2 {
		t.Fatalf("panel: %+v", out.Most)
	}
	if len(out.Rules) != 3 || out.Rules[0].Rule != "U2" || out.Rules[0].Count != 2 || out.Rules[0].Previous != 1 {
		t.Fatalf("rule counts: %+v", out.Rules)
	}
	if len(out.Weekly) != 2 || out.Weekly[0].DesignMean != nil || out.Weekly[1].DesignMean == nil || *out.Weekly[1].DesignMean != 2 || out.Weekly[1].Tickets != 4 {
		t.Fatalf("weekly: %+v", out.Weekly)
	}
	empty := aggregateReviewNumbers("project", 7, now, nil)
	if empty.Status != "no_data" || empty.Current.DesignMean != nil || empty.Current.Tickets != 0 || len(empty.Most) != 0 {
		t.Fatalf("absent events: %+v", empty)
	}
	open := aggregateReviewNumbers("project", 7, now, []reviewObservation{{Ticket: "open", Kind: "design", Round: 1, Verdict: "changes", At: now.Add(-time.Hour)}})
	if open.Status != "partial" || open.Current.DesignMean != nil || open.Current.Open != 1 || open.Current.WeeksCovered != 1 {
		t.Fatalf("open-only partial: %+v", open)
	}
	mixed := aggregateReviewNumbers("project", 7, now, []reviewObservation{
		{Ticket: "mixed", Kind: "design", Round: 3, Verdict: "ok", At: now.Add(-time.Hour)},
		{Ticket: "mixed", Kind: "gate", Round: 1, Verdict: "changes", At: now.Add(-time.Minute)},
	})
	if mixed.Current.Open != 1 || mixed.Current.GateMean != nil || len(mixed.Most) != 1 || mixed.Most[0].Latest != 1 {
		t.Fatalf("open round belongs to the open kind: %+v", mixed)
	}
}
