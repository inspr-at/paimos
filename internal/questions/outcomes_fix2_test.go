// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Observe actual PostgreSQL lock waits, not a goroutine-start signal. The
// deadline only bounds a broken test; a positive database sample is mandatory.
func waitTenantWaiters(t *testing.T, f *deliveryFixture, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	last := 0
	for {
		var n int
		err := f.d.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database()
 AND query LIKE 'SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE%'
 AND cardinality(pg_blocking_pids(pid))>0`).Scan(&n)
		if err != nil {
			if ctx.Err() != nil {
				t.Fatalf("timed out waiting for %d tenant waiters (last observed %d): %v", want, last, ctx.Err())
			}
			t.Fatalf("querying tenant waiters (wanted %d, last observed %d): %v", want, last, err)
		}
		last = n
		if n >= want {
			return
		}
		runtime.Gosched()
	}
}

func TestFix2DoctrineLifecycleCorrections(t *testing.T) {
	for _, state := range []string{"dismissed", "expired", "published", "edited", "merged"} {
		t.Run(state, func(t *testing.T) {
			f := deliveryFixtureFor(t)
			in := input()
			in.Doctrine = f.doctrineTarget(t)
			q := f.outcome(t, f.ask(t, in), "doctrine", "- 🟡 Keep commits small and reviewed.")
			f.advance(10 * time.Second)
			f.dispatch(t)
			draft := outcomeRow(t, f.status(t, q.ID)).EffectData.DoctrineID
			if draft == "" {
				t.Fatal("missing initial draft")
			}
			switch state {
			case "dismissed":
				w := request(t.Context(), f.mux, f.person, http.MethodPost, "/api/rules/doctrine/inbox/"+draft+"/dismiss", map[string]any{"reason": "Not now: keep the existing rule."})
				if w.Code != 200 {
					t.Fatalf("real dismiss handler: %d: %s", w.Code, w.Body.String())
				}
			case "expired":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET created_at=clock_timestamp()-interval '31 days' WHERE id=$1`, draft); err != nil {
					t.Fatal(err)
				}
				w := request(t.Context(), f.mux, f.person, http.MethodGet, "/api/rules/doctrine/inbox/summary", nil)
				if w.Code != 200 {
					t.Fatalf("expiry sweep: %d: %s", w.Code, w.Body.String())
				}
			case "published":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=data||'{"state":"proposed","pr_number":17,"branch":"doctrine/person-reviewed"}' WHERE id=$1`, draft); err != nil {
					t.Fatal(err)
				}
			case "edited":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=data||jsonb_build_object('edited_by',$2::text) WHERE id=$1`, draft, f.person.ID); err != nil {
					t.Fatal(err)
				}
			case "merged":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE doctrine_proposals SET data=data||'{"state":"promoted","promoted_commit":"2222222222222222222222222222222222222222"}' WHERE id=$1`, draft); err != nil {
					t.Fatal(err)
				}
			}
			var before string
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT data::text FROM doctrine_proposals WHERE id=$1`, draft).Scan(&before); err != nil {
				t.Fatal(err)
			}
			q = f.outcome(t, q, "once", "Keep this answer just for the question.")
			f.advance(10 * time.Second)
			f.dispatch(t)
			if e := outcomeRow(t, f.status(t, q.ID)); e.State != "delivered" {
				t.Fatalf("correction stuck after %s: %+v", state, e)
			}
			var after string
			if err := f.d.Admin.QueryRow(t.Context(), `SELECT data::text FROM doctrine_proposals WHERE id=$1`, draft).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("person-controlled proposal history changed")
			}
			if state == "published" || state == "edited" || state == "merged" {
				if f.count(t, `SELECT count(*) FROM desk_pending WHERE question_id=$1 AND revision=$2 AND kind='outcome' AND effect_data->'review_required'->0->>'ref'=$3`, q.ID, q.Revision, draft) != 1 {
					t.Fatal("missing explicit review provenance")
				}
			}
			f.advance(time.Minute)
			f.dispatch(t)
			if f.count(t, `SELECT count(*) FROM events WHERE type='question.outcome_applied' AND after->>'effect_id'=$1`, outcomeRow(t, f.status(t, q.ID)).ID) != 1 {
				t.Fatal("correction replay duplicated")
			}
		})
	}
}

func TestFix2KnowledgeRetiredBeforeGraceAndFailedReplacement(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.TicketID = f.ticket
	q := f.outcome(t, f.ask(t, in), "always", "Withdraw this reusable answer.")
	f.advance(10 * time.Second)
	f.dispatch(t)
	old := q.Answer.ID
	q = f.outcome(t, q, "requirement", "A new criterion")
	if k := f.knowledge(t, old); k.Status != "archived" || k.Metadata["decision_state"] != "superseded" || k.Metadata["superseded_by"] != q.Answer.ID {
		t.Fatalf("withdrawn Knowledge still active inside grace: %+v", k)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET updated_at=updated_at+interval '1 second' WHERE id=$1`, f.ticket); err != nil {
		t.Fatal(err)
	}
	f.advance(10 * time.Second)
	f.dispatch(t)
	e := outcomeRow(t, f.status(t, q.ID))
	if e.State != "failed" || e.ErrorCode != "ticket_revision_conflict" {
		t.Fatalf("fixture did not fail replacement: %+v", e)
	}
	if f.knowledge(t, old).Status != "archived" {
		t.Fatal("failed replacement resurrected withdrawn Knowledge")
	}
	if f.count(t, `SELECT count(*) FROM desk_pending WHERE id=$1 AND effect_data->>'retryable'='false' AND retry_at IS NULL`, e.ID) != 1 {
		t.Fatal("permanent revision conflict still retries")
	}
	f.advance(time.Minute)
	f.dispatch(t)
	if f.count(t, `SELECT count(*) FROM desk_pending WHERE id=$1 AND effect_data->>'retryable'='false' AND retry_at IS NULL`, e.ID) != 1 {
		t.Fatal("dispatcher retried a final conflict")
	}
}

func TestFix2EditedCriterionPreservedWithReview(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.TicketID = f.ticket
	q := f.outcome(t, f.ask(t, in), "requirement", "Original criterion")
	f.advance(10 * time.Second)
	f.dispatch(t)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET fields=jsonb_set(fields,'{acceptance_criteria}',to_jsonb(replace(fields->>'acceptance_criteria','Original criterion','Person edited criterion'))),updated_at=clock_timestamp() WHERE id=$1`, f.ticket); err != nil {
		t.Fatal(err)
	}
	text := f.criteria(t)
	q = f.outcome(t, q, "always", "Replacement reusable answer")
	f.advance(10 * time.Second)
	f.dispatch(t)
	if e := outcomeRow(t, f.status(t, q.ID)); e.State != "delivered" || e.EffectData.KnowledgeID != q.Answer.ID {
		t.Fatalf("edited criterion blocked replacement: %+v", e)
	}
	if f.criteria(t) != text {
		t.Fatal("person edit overwritten")
	}
	if f.count(t, `SELECT count(*) FROM desk_pending WHERE question_id=$1 AND revision=$2 AND kind='outcome' AND effect_data->'review_required'->0->>'kind'='criterion' AND effect_data->'review_required'->0->>'ref'=$3`, q.ID, q.Revision, f.ticket) != 1 {
		t.Fatal("missing criterion review provenance")
	}
	if f.knowledge(t, q.Answer.ID).Status != "active" {
		t.Fatal("rest of outcome was not applied")
	}
}

func TestFix2ListCriteriaUnavailableBeforeDecision(t *testing.T) {
	f := deliveryFixtureFor(t)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET fields='{"acceptance_criteria":["Keep this criterion"]}' WHERE id=$1`, f.ticket); err != nil {
		t.Fatal(err)
	}
	in := input()
	in.TicketID = f.ticket
	q := f.ask(t, in)
	for _, stamp := range f.status(t, q.ID).Outcomes {
		if stamp.Outcome == "requirement" && (stamp.Available || stamp.Why == "") {
			t.Fatal("list criteria incorrectly available")
		}
	}
	w := request(t.Context(), f.mux, f.person, http.MethodPost, "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Outcome: "requirement", Answer: "Do not append a string to a list"})
	if w.Code != 422 {
		t.Fatalf("list format should fail before grace: %d", w.Code)
	}
}

func TestFix2EncodedDoctrineOnlyStoredForDoctrine(t *testing.T) {
	for _, outcome := range []string{"once", "always", "requirement", "doctrine"} {
		t.Run(outcome, func(t *testing.T) {
			f := deliveryFixtureFor(t)
			in := input()
			in.TicketID = f.ticket
			target := f.doctrineTarget(t)
			in.Doctrine = &DoctrineTarget{SourceID: target.SourceID, Path: strings.Repeat("\x01", 300), RuleKey: strings.Repeat("\x01", 200), RuleSHA: strings.Repeat("a", 64), TLDREN: strings.Repeat("\x01", 300), TLDRDE: strings.Repeat("\x01", 300)}
			q := f.ask(t, in)
			w := request(t.Context(), f.mux, f.person, http.MethodPost, "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: q.Revision, Outcome: outcome, Answer: "Valid answer"})
			if outcome == "doctrine" {
				if w.Code != 422 || !strings.Contains(w.Body.String(), "outcome_data_too_large") {
					t.Fatalf("oversized mapping response: %d: %s", w.Code, w.Body.String())
				}
			} else {
				q = question(t, w, 200)
				if f.count(t, `SELECT count(*) FROM desk_answers WHERE node_id=$1 AND NOT (outcome_data ? 'doctrine')`, q.Answer.ID) != 1 {
					t.Fatal("irrelevant doctrine mapping stored")
				}
			}
		})
	}
}

func TestFix2CriterionPreservesLargeIntegers(t *testing.T) {
	f := deliveryFixtureFor(t)
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE nodes SET fields='{"acceptance_criteria":"Existing","large":9007199254740993,"custom":{"huge":18446744073709551615},"labels":["keep"]}' WHERE id=$1`, f.ticket); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT (fields-'acceptance_criteria')::text FROM nodes WHERE id=$1`, f.ticket).Scan(&before); err != nil {
		t.Fatal(err)
	}
	in := input()
	in.TicketID = f.ticket
	q := f.outcome(t, f.ask(t, in), "requirement", "Append precisely")
	f.advance(10 * time.Second)
	f.dispatch(t)
	for _, outcome := range []string{"requirement", "once"} {
		var after string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT (fields-'acceptance_criteria')::text FROM nodes WHERE id=$1`, f.ticket).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("unrelated numeric fields changed: %s -> %s", before, after)
		}
		q = f.outcome(t, q, outcome, "Correct precisely")
		f.advance(10 * time.Second)
		f.dispatch(t)
		if outcomeRow(t, f.status(t, q.ID)).State != "delivered" {
			t.Fatal("fixture failed to apply")
		}
	}
}

type outcomeQueryCount struct{ grants, cache atomic.Int32 }

func (c *outcomeQueryCount) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "FROM role_bindings") {
		c.grants.Add(1)
	}
	if strings.Contains(d.SQL, "FROM doctrine_cache") || strings.Contains(d.SQL, "FROM doctrine_private_guard") {
		c.cache.Add(1)
	}
	return ctx
}
func (*outcomeQueryCount) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestFix2QuestionListLoadsPermissionsOnceWithoutDoctrineRendering(t *testing.T) {
	f := deliveryFixtureFor(t)
	in := input()
	in.Doctrine = f.doctrineTarget(t)
	for range 5 {
		in.RequestID = uid()
		f.ask(t, in)
	}
	count := &outcomeQueryCount{}
	config := f.d.App.Config()
	config.ConnConfig.Tracer = count
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	f.m.pool = pool
	w := request(t.Context(), f.mux, f.person, http.MethodGet, "/api/projects/"+f.project+"/questions", nil)
	if w.Code != 200 {
		t.Fatalf("list: %d: %s", w.Code, w.Body.String())
	}
	var page Page
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 5 {
		t.Fatal("list fixture lost questions")
	}
	if count.grants.Load() != 1 || count.cache.Load() != 0 {
		t.Fatalf("list work multiplied: grants=%d doctrine cache=%d", count.grants.Load(), count.cache.Load())
	}
}
