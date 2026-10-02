// SPDX-License-Identifier: AGPL-3.0-only
package questions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Test-only P4 fixture: production P2 never publishes active Always decisions.
func (f *fixture) activeAlways(t *testing.T, in Input) Question {
	t.Helper()
	q := f.ask(t, in)
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := treeLock(t.Context(), tx, f.person.TenantID); err != nil {
			return err
		}
		if err := writeCapability(t.Context(), tx); err != nil {
			return err
		}
		id, err := createNode(t.Context(), tx, f.person, q.ID, "decision")
		if err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO desk_answers(tenant_id,project_id,node_id,question_id,revision,request_id,request_digest,decided_by,option_id,answer,outcome,deliver_after)
 VALUES($1,$2,$3,$4,2,$5,'fixture',$6,$7,$8,'always',statement_timestamp()+interval '10 seconds')`, f.person.TenantID, f.project, id, q.ID, uid(), f.person.ID, in.Options[0].ID, in.Options[0].Answer); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO desk_decisions(tenant_id,project_id,question_id,revision,state) VALUES($1,$2,$3,2,'active')`, f.person.TenantID, f.project, q.ID); err != nil {
			return err
		}
		_, err = tx.Exec(t.Context(), `UPDATE desk_questions SET state='answered',revision=2 WHERE tenant_id=$1 AND node_id=$2`, f.person.TenantID, q.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return question(t, request(t.Context(), f.mux, f.person, "GET", "/api/questions/"+q.ID, nil), 200)
}

func (f *fixture) reuseCount(t *testing.T, q Question) int {
	t.Helper()
	var n int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT reuse_count FROM desk_decisions WHERE tenant_id=$1 AND question_id=$2 AND revision=2`, f.person.TenantID, q.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExactFingerprintCompatibility(t *testing.T) {
	f := newFixture(t)
	fingerprint := func(in Input) *string {
		t.Helper()
		b, _ := json.Marshal(in)
		var hash *string
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT aeon_desk_fingerprint($1::jsonb)`, b).Scan(&hash); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	base := input()
	want := fingerprint(base)
	if want == nil {
		t.Fatal("ordinary Q&A has no fingerprint")
	}
	compatible := input()
	compatible.SessionID = f.otherSession
	compatible.Options[0], compatible.Options[1] = compatible.Options[1], compatible.Options[0]
	compatible.SuggestedOutcome = "always"
	if hash := fingerprint(compatible); hash == nil || *hash != *want {
		t.Fatal("route, option order or explicit default split compatible Q&A")
	}
	for _, c := range []struct {
		name string
		edit func(*Input)
	}{
		{"question", func(i *Input) { i.Question += "?" }},
		{"context", func(i *Input) { i.Context += " new facts" }},
		{"findings", func(i *Input) { i.Findings += " changed" }},
		{"ticket scope", func(i *Input) { i.TicketID = f.ticket }},
		{"outcome scope", func(i *Input) { i.SuggestedOutcome = "once" }},
		{"blocked work", func(i *Input) { i.BlockedNodeIDs = []string{f.ticket} }},
		{"option id", func(i *Input) { i.Options[0].ID = "different" }},
		{"option title", func(i *Input) { i.Options[0].Title += " changed" }},
		{"option description", func(i *Input) { i.Options[0].Description += " changed" }},
		{"option answer", func(i *Input) { i.Options[0].Answer += " changed" }},
		{"missing option", func(i *Input) { i.Options = i.Options[:1] }},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := input()
			c.edit(&in)
			if hash := fingerprint(in); hash != nil && *hash == *want {
				t.Fatal("incompatible input shared fingerprint")
			}
		})
	}
	for _, edit := range []func(*Input){
		func(i *Input) { i.AnywayReason = "New evidence" },
		func(i *Input) { i.SourceRequestID = uid() },
		func(i *Input) { i.SourceHandoverID = uid() },
		func(i *Input) { i.SuggestedOutcome = "requirement" },
		func(i *Input) { i.SuggestedOutcome = "doctrine" },
	} {
		in := input()
		edit(&in)
		if fingerprint(in) != nil {
			t.Fatal("protected/forced-fresh input can match")
		}
	}
}

func TestConcurrentDuplicateMembershipsAndFanout(t *testing.T) {
	f := newFixture(t)
	start := make(chan struct{})
	got := make(chan *httptest.ResponseRecorder, 8)
	inputs := make([]Input, 8)
	var wg sync.WaitGroup
	for n := range inputs {
		in := input()
		in.SessionID = f.session
		p := f.agent
		if n%2 == 1 {
			p = f.otherAgent
			in.SessionID = f.otherSession
			in.Options[0], in.Options[1] = in.Options[1], in.Options[0]
		}
		inputs[n] = in
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got <- request(t.Context(), f.mux, p, "POST", "/api/projects/"+f.project+"/questions", in)
		}()
	}
	close(start)
	wg.Wait()
	close(got)
	var id string
	for w := range got {
		q := question(t, w, 201)
		if id != "" && q.ID != id {
			t.Fatal("parallel duplicates made more than one item")
		}
		id = q.ID
	}
	q := question(t, request(t.Context(), f.mux, f.person, "GET", "/api/questions/"+id, nil), 200)
	if len(q.Askers) != 8 {
		t.Fatalf("members=%d", len(q.Askers))
	}
	roots, requests := map[string]bool{}, map[string]bool{}
	for _, a := range q.Askers {
		if roots[a.ReplyRootID] || requests[a.RequestID] || a.CommentNodeID != id || a.SessionID != a.Input.SessionID {
			t.Fatal("membership route/correlation lost")
		}
		roots[a.ReplyRootID], requests[a.RequestID] = true, true
	}
	// Exact option IDs remain valid in every member despite display order.
	q = question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+id+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: 1, OptionID: "a", Outcome: "once"}), 200)
	if len(q.Pending) != 17 {
		t.Fatalf("fan-out effects=%d want 17", len(q.Pending))
	}
	seen := map[string]bool{}
	for _, e := range q.Pending {
		key := e.AskerID + "/" + e.Kind
		if seen[key] || e.DeliverAfter != q.Answer.DeliverAfter {
			t.Fatal("duplicate effect or different grace deadline")
		}
		seen[key] = true
	}
	for n, in := range inputs {
		p := f.agent
		if n%2 == 1 {
			p = f.otherAgent
		}
		replayed := question(t, request(t.Context(), f.mux, p, "POST", "/api/projects/"+f.project+"/questions", in), 200)
		if replayed.ID != id || replayed.Answer.Answer != "Use local storage." || len(replayed.Askers) != 4 || len(replayed.Pending) != 8 {
			t.Fatal("replay leaked other asker routes or changed fan-out")
		}
	}
}

func TestAlwaysReuseImmediateAtomicAndRetrySafe(t *testing.T) {
	f := newFixture(t)
	source := f.activeAlways(t, input())
	in := input()
	in.SessionID = f.otherSession
	q := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", in), 201)
	if q.ID != source.ID || q.State != "answered" || q.Answer.ID != source.Answer.ID || len(q.Askers) != 1 || len(q.Pending) != 2 {
		t.Fatalf("wrong reuse: %+v", q)
	}
	reuse := q.Askers[0].FromRecord
	if reuse == nil || reuse.Label != "From the record" || reuse.DecisionID != source.Answer.ID || reuse.Revision != 2 || f.reuseCount(t, q) != 1 {
		t.Fatal("missing provenance/count")
	}
	for _, e := range q.Pending {
		if e.AskerID != q.Askers[0].ID || e.Kind == "outcome" || !e.DeliverAfter.Before(source.Answer.DeliverAfter) {
			t.Fatal("reuse added a human grace or outcome effect")
		}
		var due bool
		if err := f.d.Admin.QueryRow(t.Context(), `SELECT deliver_after<=statement_timestamp() FROM desk_pending WHERE id=$1`, e.ID).Scan(&due); err != nil || !due {
			t.Fatal("reused effect not due immediately", err)
		}
	}
	// Delivery retry fixture consumes existing identities; a repeated enqueue
	// cannot create a second effect or increment the approved answer's count.
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := writeCapability(t.Context(), tx); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE desk_pending SET state='delivered',effect_ref='fixture-delivery' WHERE question_id=$1 AND asker_id=$2`, q.ID, q.Askers[0].ID); err != nil {
			return err
		}
		tag, err := tx.Exec(t.Context(), `INSERT INTO desk_pending(tenant_id,project_id,question_id,revision,asker_id,kind,deliver_after)
 SELECT tenant_id,project_id,question_id,revision,asker_id,kind,deliver_after FROM desk_pending WHERE question_id=$1 ON CONFLICT DO NOTHING`, q.ID)
		if err == nil && tag.RowsAffected() != 0 {
			return fmt.Errorf("retry created duplicate effects")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	replay := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", in), 200)
	if f.reuseCount(t, q) != 1 || len(replay.Pending) != 2 || replay.Askers[0].ID != q.Askers[0].ID {
		t.Fatal("retry increased reuse count or effects")
	}
	for n, e := range replay.Pending {
		if e.ID != q.Pending[n].ID || e.State != "delivered" {
			t.Fatal("retry replaced a delivered identity")
		}
	}
	conflict := in
	conflict.Context += " changed"
	if w := request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", conflict); w.Code != 409 || f.reuseCount(t, q) != 1 {
		t.Fatal("conflicting replay changed approved answer count")
	}
	// Actual post-dispatch corrections remain explicitly P3-owned.
	if w := request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: 2, OptionID: "b", Outcome: "once"}); w.Code != 409 {
		t.Fatal("post-delivery correction bypassed P3")
	}
}

func TestReuseBoundariesAndAnyway(t *testing.T) {
	f := newFixture(t)
	source := f.activeAlways(t, input())
	for _, edit := range []func(*Input){
		func(i *Input) { i.Options[0].Answer += " changed" },
		func(i *Input) { i.Options[0].ID = "other"; i.Recommend = "other" },
		func(i *Input) { i.TicketID = f.ticket },
		func(i *Input) { i.SuggestedOutcome = "once" },
		func(i *Input) { i.SuggestedOutcome = "requirement" },
		func(i *Input) { i.SuggestedOutcome = "doctrine" },
		func(i *Input) { i.AnywayReason = "Reconsider" },
	} {
		in := input()
		edit(&in)
		q := f.ask(t, in)
		if q.ID == source.ID || q.State != "open" || q.Askers[0].FromRecord != nil {
			t.Fatal("out-of-scope/changed options/forced fresh reused a decision")
		}
		if in.AnywayReason != "" {
			in.RequestID = uid()
			if another := f.ask(t, in); another.ID == q.ID {
				t.Fatal("anyway merged with a previously forced-fresh question")
			}
		}
	}
	// A second project in the same tenant and a different tenant stay separate.
	for _, target := range []struct {
		p       tenant.Principal
		project string
	}{{f.person, f.hidden}, {f.foreign, f.foreignProject}} {
		q := question(t, request(t.Context(), f.mux, target.p, "POST", "/api/projects/"+target.project+"/questions", input()), 201)
		if q.ID == source.ID || q.State != "open" {
			t.Fatal("cross-project/tenant reuse")
		}
	}
	if w := request(t.Context(), f.mux, f.agent, "POST", "/api/projects/"+f.hidden+"/questions", input()); w.Code != 404 {
		t.Fatal("reuse lookup bypassed project authorization")
	}
	if f.reuseCount(t, source) != 0 {
		t.Fatal("nonmatching requests incremented reuse count")
	}
}

// Observe the database's lock wait as a barrier, rather than assuming a sleep
// made the contender reach its final authority check. Time bounds failures only.
func waitForBlocker(t *testing.T, f *fixture, blocker int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		var waiting bool
		err := f.d.Admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity s WHERE $1::int=ANY(pg_blocking_pids(s.pid)))`, blocker).Scan(&waiting)
		if err != nil {
			t.Fatal("contender never reached lock barrier", err)
		}
		if waiting {
			return
		}
	}
}

func TestSupersedeWinsConcurrentLookup(t *testing.T) {
	f := newFixture(t)
	source := f.activeAlways(t, input())
	tx, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var pid int32
	if err := tx.QueryRow(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if err := writeCapability(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `UPDATE desk_decisions SET state='superseded' WHERE question_id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	in := input()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", in)
	}()
	waitForBlocker(t, f, pid)
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	q := question(t, <-done, 201)
	if q.ID == source.ID || q.State != "open" || f.reuseCount(t, source) != 0 {
		t.Fatal("lookup used superseded candidate snapshot")
	}
}

func TestReuseCorrectionKeepsProvenance(t *testing.T) {
	f := newFixture(t)
	source := f.activeAlways(t, input())
	in := input()
	q := question(t, request(t.Context(), f.mux, f.otherAgent, "POST", "/api/projects/"+f.project+"/questions", in), 201)
	updated := question(t, request(t.Context(), f.mux, f.person, "POST", "/api/questions/"+q.ID+"/decision", DecisionInput{RequestID: uid(), ExpectedRevision: 2, OptionID: "b", Outcome: "once"}), 200)
	if updated.Revision != 3 || len(updated.Pending) != 5 || f.reuseCount(t, source) != 1 {
		t.Fatal("correction lost source or did not schedule each member")
	}
	var replaced int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM desk_pending WHERE question_id=$1 AND revision=2 AND state='replaced'`, q.ID).Scan(&replaced); err != nil || replaced != 2 {
		t.Fatal("correction left reused pending effects live", err)
	}
	current := question(t, request(t.Context(), f.mux, f.otherAgent, "GET", "/api/questions/"+q.ID, nil), 200)
	if current.Askers[0].FromRecord == nil || current.Askers[0].FromRecord.DecisionID != source.Answer.ID || current.Askers[0].FromRecord.Revision != 2 || current.Answer.Revision != 3 {
		t.Fatal("correction rewrote original reuse provenance")
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.person), f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
		if err := writeCapability(t.Context(), tx); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `UPDATE desk_askers SET reused_decision_id=NULL,reused_revision=NULL WHERE id=$1`, q.Askers[0].ID)
		return err
	})
	if err == nil {
		t.Fatal("service write erased immutable provenance")
	}
	// Superseded source must never answer the next ask.
	in.RequestID = uid()
	if fresh := f.ask(t, in); fresh.ID == source.ID || fresh.State != "open" {
		t.Fatal("corrected Always remained reusable")
	}
}

func TestMatchingFanoutBound(t *testing.T) {
	f := newFixture(t)
	q := f.ask(t, input())
	err := db.InTenant(tenant.WithPrincipal(t.Context(), f.agent), f.d.App, f.agent.TenantID, func(tx pgx.Tx) error {
		if err := treeLock(t.Context(), tx, f.agent.TenantID); err != nil {
			return err
		}
		if err := writeCapability(t.Context(), tx); err != nil {
			return err
		}
		for range maxAskers - 1 {
			in := input()
			hash, body, _ := digest(in)
			if _, err := addAsker(t.Context(), tx, f.agent, f.project, q.ID, in, hash, body, questionMatch{}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if next := f.ask(t, input()); next.ID == q.ID || len(next.Askers) != 1 {
		t.Fatal("full item accepted unbounded fan-out")
	}
}
