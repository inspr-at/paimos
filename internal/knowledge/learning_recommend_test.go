// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

func recommendPath(id string) string {
	return "/api/knowledge/learnings/" + id + "/recommendation"
}

func scopedAgent(t *testing.T, f fixture, name string) tenant.Principal {
	t.Helper()
	agent := tenant.Principal{TenantID: f.a.TenantID, Kind: tenant.Agent, Name: name, Roles: []string{"member"}, Scopes: []string{"knowledge.read", "knowledge.write"}}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent',$2,$3) RETURNING id::text`, f.a.TenantID, agent.Name, agent.Roles).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.a.TenantID, agent.ID, "member")
	return agent
}

func learningByID(t *testing.T, f fixture, p tenant.Principal, id string) Learning {
	t.Helper()
	for _, item := range listLearningsHTTP(t, f, p, f.project).Items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("learning %s not listed", id)
	return Learning{}
}

func entryBody(t *testing.T, f fixture, id string) string {
	t.Helper()
	w := call(t, f, f.a, "GET", "/api/knowledge/"+id, nil)
	expect(t, w, 200)
	return decode[Entry](t, w).Body
}

// An agent prepares both decisions; it can neither accept nor dismiss, so the
// learnings stay open and the entry unchanged. Viewers cannot recommend.
func TestRecommendationAgentsCannotApply(t *testing.T) {
	f := setup(t)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "flywheel", "title": "Flywheel", "body": "Notes\n"})
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketID := nodeLearningID(f.ticket)
	note := addComment(t, f, f.ticket, "#process-learning Already covered by the runbook.")
	noteID := commentLearningID(f.ticket, note)
	agent := scopedAgent(t, f, "Scout")

	w := call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": "Rotate keys before the window closes."})
	expect(t, w, 200)
	rec := decode[Recommendation](t, w)
	if rec.Decision != "accept" || rec.KnowledgeTitle != "Flywheel" || rec.Lesson != "Rotate keys before the window closes" || rec.By == nil || rec.By.Name != "Scout" || eventType(t, f, rec.EventID) != evLearningRecommended {
		t.Fatalf("recommendation %+v", rec)
	}
	w = call(t, f, agent, "PUT", recommendPath(noteID), map[string]any{"decision": "dismiss", "reason": "Already in the runbook."})
	expect(t, w, 200)

	for _, action := range []struct {
		path string
		body any
	}{
		{"/api/knowledge/learnings/" + ticketID + "/accept", map[string]any{"knowledge_id": book.ID, "lesson": rec.Lesson}},
		{"/api/knowledge/learnings/" + noteID + "/dismiss", map[string]any{"reason": "Already in the runbook."}},
	} {
		w = call(t, f, agent, "POST", action.path, action.body)
		expect(t, w, 403)
		if code(t, w) != "person_required" {
			t.Fatalf("%s %s", action.path, w.Body.String())
		}
	}
	if decisionOf(t, f, ticketID) != "" || decisionOf(t, f, noteID) != "" || entryBody(t, f, book.ID) != "Notes\n" {
		t.Fatal("an agent's request decided a learning")
	}
	listed := learningByID(t, f, f.a, noteID)
	if listed.Recommendation == nil || listed.Recommendation.Decision != "dismiss" || listed.Recommendation.Reason != "Already in the runbook." || listed.Recommendation.Stale {
		t.Fatalf("listed %+v", listed.Recommendation)
	}
	reviseComment(t, f, f.ticket, note, "#process-learning Now about something else.", false)
	if rec := learningByID(t, f, f.a, noteID).Recommendation; rec == nil || !rec.Stale {
		t.Fatalf("edited learning keeps a fresh recommendation: %+v", rec)
	}

	expect(t, call(t, f, f.viewer, "PUT", recommendPath(ticketID), map[string]any{"decision": "dismiss", "reason": "no"}), 403)
	expect(t, call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "dismiss"}), 400)
	expect(t, call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "accept"}), 400)
	expect(t, call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": strings.Repeat("x", 241)}), 400)
	other := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "elsewhere", "title": "Elsewhere", "body": "", "project_id": f.other})
	expect(t, call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "accept", "knowledge_id": other.ID}), 404)
	w = call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "dismiss", "reason": "password=" + strings.Repeat("x", 5)})
	expect(t, w, 409)
	if code(t, w) != "learning_sensitive" {
		t.Fatalf("sensitive %s", w.Body.String())
	}
	if rec := learningByID(t, f, f.a, ticketID).Recommendation; rec == nil || rec.Decision != "accept" {
		t.Fatalf("a refused recommendation replaced the stored one: %+v", rec)
	}
}

// A person applies the recommendations through accept and dismiss: the
// accepted line carries the recommended lesson, the dismiss event the reason.
// A decided learning takes no further recommendation.
func TestApplyRecommendationIsPersonOnly(t *testing.T) {
	f := setup(t)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "flywheel", "title": "Flywheel", "body": "Notes\n"})
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketID := nodeLearningID(f.ticket)
	noteID := commentLearningID(f.ticket, addComment(t, f, f.ticket, "#process-learning Duplicate of the design."))
	agent := scopedAgent(t, f, "Scout")
	expect(t, call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": "Rotate keys before the window closes"}), 200)
	expect(t, call(t, f, agent, "PUT", recommendPath(noteID), map[string]any{"decision": "dismiss", "reason": "Duplicates the design."}), 200)
	listed := learningByID(t, f, f.b, ticketID).Recommendation

	w := call(t, f, f.b, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": listed.KnowledgeID, "lesson": listed.Lesson})
	expect(t, w, 200)
	accepted := decode[LearningDecision](t, w)
	if !strings.Contains(accepted.Line, ": Rotate keys before the window closes. Source: [PHAROS-7]") || strings.Contains(entryBody(t, f, book.ID), "Rotate the fleet keys") {
		t.Fatalf("line %q", accepted.Line)
	}
	w = call(t, f, f.b, "POST", "/api/knowledge/learnings/"+noteID+"/dismiss", map[string]any{"reason": "Duplicates the design."})
	expect(t, w, 200)
	dismissed := decode[LearningDecision](t, w)
	var after struct {
		Reason string `json:"reason"`
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(t.Context(), `SELECT after FROM events WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, dismissed.EventID).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &after)
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Reason != "Duplicates the design." || decisionOf(t, f, ticketID) != "accepted" || decisionOf(t, f, noteID) != "dismissed" {
		t.Fatalf("dismiss reason %q", after.Reason)
	}
	w = call(t, f, agent, "PUT", recommendPath(ticketID), map[string]any{"decision": "dismiss", "reason": "late"})
	expect(t, w, 409)
	if code(t, w) != "already_decided" {
		t.Fatalf("late recommendation %s", w.Body.String())
	}
	if hasLearning(listLearningsHTTP(t, f, f.b, f.project).Items, ticketID) {
		t.Fatal("applied learning still listed")
	}
	// Older clients dismiss with an empty body and still succeed.
	plain := commentLearningID(f.ticket, addComment(t, f, f.ticket, "#process-learning Plain dismiss."))
	expect(t, call(t, f, f.b, "POST", "/api/knowledge/learnings/"+plain+"/dismiss", nil), 200)
}

// More than 50 open learnings, past the 500-row scan, with equal timestamps:
// following next_cursor reaches every one exactly once, newest first.
func TestLearningsContinuePastFifty(t *testing.T) {
	f := setup(t)
	const total = learningScanLimit + 40
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		// Pairs share one updated_at, so the cursor must break ties by id.
		_, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			SELECT $1, 'PG-'||g::text, k.id, 'Learning '||g::text, $2::uuid,
			       '{"tags":["process-learning"]}'::jsonb, timestamptz '2026-01-01' + make_interval(mins => g/2)
			FROM generate_series(1, $3) AS g
			JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project, total)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	var previous *Learning
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total/learningListLimit+1 {
			t.Fatal("cursor does not advance")
		}
		path := "/api/knowledge/learnings?project_id=" + f.project
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		w := call(t, f, f.a, "GET", path, nil)
		expect(t, w, 200)
		page := decode[LearningPage](t, w)
		if page.Truncated != (page.NextCursor != "") || (page.Truncated && len(page.Items) != learningListLimit) {
			t.Fatalf("page %d truncated %v cursor %q items %d", pages, page.Truncated, page.NextCursor, len(page.Items))
		}
		for i := range page.Items {
			item := page.Items[i]
			if seen[item.ID] {
				t.Fatalf("%s listed twice", item.Key)
			}
			seen[item.ID] = true
			if previous != nil && (item.At.After(previous.At) || (item.At.Equal(previous.At) && item.ID > previous.ID)) {
				t.Fatalf("%s after %s breaks newest-first order", item.Key, previous.Key)
			}
			previous = &item
		}
		if !page.Truncated {
			break
		}
		cursor = page.NextCursor
	}
	// The fixture ticket from setup is not tagged; every PG row is reachable.
	if len(seen) != total {
		t.Fatalf("reached %d of %d", len(seen), total)
	}
	expect(t, call(t, f, f.a, "GET", "/api/knowledge/learnings?project_id="+f.project+"&cursor=nope", nil), 400)
}

// pageThrough follows next_cursor to the end and checks newest-first order
// and that no learning is listed twice.
func pageThrough(t *testing.T, f fixture, p tenant.Principal, project string, maxPages int) map[string]Learning {
	t.Helper()
	seen := map[string]Learning{}
	var previous *Learning
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > maxPages {
			t.Fatal("cursor does not advance")
		}
		path := "/api/knowledge/learnings?project_id=" + project
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		w := call(t, f, p, "GET", path, nil)
		expect(t, w, 200)
		page := decode[LearningPage](t, w)
		if page.Truncated != (page.NextCursor != "") || len(page.Items) > learningListLimit {
			t.Fatalf("page %d truncated %v cursor %q items %d", pages, page.Truncated, page.NextCursor, len(page.Items))
		}
		for i := range page.Items {
			item := page.Items[i]
			if _, ok := seen[item.ID]; ok {
				t.Fatalf("%s listed twice", item.Key)
			}
			seen[item.ID] = item
			if previous != nil && (item.At.After(previous.At) || (item.At.Equal(previous.At) && item.ID > previous.ID)) {
				t.Fatalf("%s after %s breaks newest-first order", item.Key, previous.Key)
			}
			previous = &item
		}
		if !page.Truncated {
			return seen
		}
		cursor = page.NextCursor
	}
}

// More tickets than one scan reads share one updated_at: the cursor's id
// goes into the scan before its limit, so every one is reached (AEON-788).
func TestLearningsContinueThroughTiedScan(t *testing.T) {
	f := setup(t)
	const total = learningScanLimit + 20
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			SELECT $1, 'TIE-'||g::text, k.id, 'Tied '||g::text, $2::uuid,
			       '{"tags":["process-learning"]}'::jsonb, timestamptz '2026-02-01'
			FROM generate_series(1, $3) AS g
			JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project, total)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pageThrough(t, f, f.a, f.project, total/learningListLimit+2)); got != total {
		t.Fatalf("reached %d of %d tied learnings", got, total)
	}
}

// Nominations alone, more than one scan reads: the cursor reaches into the
// nomination scan too, so the oldest are not cut off page after page.
func TestNominationsContinuePastScan(t *testing.T) {
	f := setup(t)
	const total = learningScanLimit + 20
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			WITH closed AS (
			  INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, state, updated_at)
			  SELECT $1, 'NOM-'||g::text, k.id, 'Nominated '||g::text, $2::uuid, 'done',
			         timestamptz '2026-03-01' + make_interval(mins => g/3)
			  FROM generate_series(1, $3) AS g
			  JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'
			  RETURNING id, title
			)
			INSERT INTO method_learning_nominations (tenant_id, source_key, project_id, node_id, origin, excerpt, source_hash, nominated_at)
			SELECT $1, 'n-'||id::text, $2::uuid, id, 'closed_ticket', title, encode(sha256(convert_to(title, 'UTF8')), 'hex'), timestamptz '2026-04-01'
			FROM closed`, f.a.TenantID, f.project, total)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := pageThrough(t, f, f.a, f.project, total/learningListLimit+2)
	if len(seen) != total {
		t.Fatalf("reached %d of %d nominations", len(seen), total)
	}
}

// A scan that fills its limit with rows that are not learnings ends the page
// there: an older learning from another source is not listed ahead of the
// learnings that scan has not read yet, and the cursor moves past the rows.
func TestLearningsPageEndsAtAFullScan(t *testing.T) {
	f := setup(t)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		// Mentioned, not tagged: read by the ticket scan, then dropped.
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			SELECT $1, 'MEN-'||g::text, k.id, 'Mentioned '||g::text, $2::uuid,
			       '{"note":"see process-learning"}'::jsonb, timestamptz '2026-05-01' + make_interval(mins => g)
			FROM generate_series(1, $3) AS g
			JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project, learningScanLimit); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			SELECT $1, 'OLDT-1', k.id, 'The tagged ticket behind them', $2::uuid,
			       '{"tags":["process-learning"]}'::jsonb, timestamptz '2026-04-20'
			FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after, at)
			VALUES ($1, $2::uuid, $3::uuid, 'comment.created',
			        jsonb_build_object('body_markdown', '#process-learning An older comment'), timestamptz '2026-04-10')`,
			f.a.TenantID, f.a.ID, f.ticket)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w := call(t, f, f.a, "GET", "/api/knowledge/learnings?project_id="+f.project, nil)
	expect(t, w, 200)
	first := decode[LearningPage](t, w)
	if len(first.Items) != 0 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("first page %+v: a learning past the full scan was listed, or the page claimed to be complete", first)
	}
	var keys []string
	for _, item := range pageThrough(t, f, f.a, f.project, 4) {
		keys = append(keys, item.Key)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"OLDT-1", "PHAROS-7"}) {
		t.Fatalf("reached %v", keys)
	}
}

// A person decides on the text they reviewed. When the learning reads
// differently now, accept and dismiss refuse and write nothing; the matching
// text applies. Older clients that send no learning_text are not checked.
func TestDecisionRejectsUnreviewedText(t *testing.T) {
	f := setup(t)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "flywheel", "title": "Flywheel", "body": "Notes\n"})
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketID := nodeLearningID(f.ticket)
	note := addComment(t, f, f.ticket, "#process-learning Keep the runbook short.")
	noteID := commentLearningID(f.ticket, note)
	reviewed := learningByID(t, f, f.a, noteID).Text
	reviseComment(t, f, f.ticket, note, "#process-learning Keep the runbook shorter still.", false)

	for _, action := range []struct {
		path string
		body map[string]any
	}{
		{"/api/knowledge/learnings/" + ticketID + "/accept", map[string]any{"knowledge_id": book.ID, "lesson": "Rotate keys early", "learning_text": "Rotate the fleet keys today"}},
		{"/api/knowledge/learnings/" + noteID + "/accept", map[string]any{"knowledge_id": book.ID, "lesson": reviewed, "learning_text": reviewed}},
		{"/api/knowledge/learnings/" + noteID + "/dismiss", map[string]any{"reason": "Covered", "learning_text": reviewed}},
	} {
		w := call(t, f, f.a, "POST", action.path, action.body)
		expect(t, w, 409)
		if code(t, w) != "learning_changed" {
			t.Fatalf("%s %s", action.path, w.Body.String())
		}
	}
	if decisionOf(t, f, ticketID) != "" || decisionOf(t, f, noteID) != "" || entryBody(t, f, book.ID) != "Notes\n" {
		t.Fatal("a decision on text the person did not review was written")
	}
	expect(t, call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": book.ID, "learning_text": strings.Repeat("x", maxReviewedRunes+1)}), 400)

	w := call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": book.ID, "lesson": "Rotate keys early", "learning_text": "Rotate the fleet keys"})
	expect(t, w, 200)
	if line := decode[LearningDecision](t, w).Line; !strings.Contains(line, ": Rotate keys early. Source:") {
		t.Fatalf("line %q", line)
	}
	current := learningByID(t, f, f.a, noteID).Text
	expect(t, call(t, f, f.a, "POST", "/api/knowledge/learnings/"+noteID+"/dismiss", map[string]any{"reason": "Not a method learning", "learning_text": current}), 200)
	if decisionOf(t, f, noteID) != "dismissed" {
		t.Fatal("matching text did not dismiss")
	}
}

// requestAs serves one request outside the test goroutine's helpers, so it
// can run in a goroutine; it never calls t.
func requestAs(f fixture, p tenant.Principal, scope authz.Scope, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(authz.WithRouteScope(tenant.WithPrincipal(r.Context(), p), scope))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

// Learning writes authorize against the source's current project inside the
// write, under the tenant and tree fence, not against the route's earlier
// scope (AEON-788): a ticket moved away, or a grant revoked while the write
// waited, is refused and writes nothing.
func TestLearningWritesAuthorizeCurrentProject(t *testing.T) {
	f := setup(t)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketID := nodeLearningID(f.ticket)
	member := projectPerson(t, f, "Pia Project")
	bindProjectRole(t, f, member.ID, "member", f.project)
	scope := authz.Scope{ProjectID: f.project}
	writes := []struct{ method, path, body string }{
		{"PUT", recommendPath(ticketID), `{"decision":"dismiss","reason":"Covered"}`},
		{"POST", "/api/knowledge/learnings/" + ticketID + "/dismiss", `{"reason":"Covered"}`},
	}
	untouched := func(what string) {
		t.Helper()
		if decisionOf(t, f, ticketID) != "" || learningByID(t, f, f.a, ticketID).Recommendation != nil {
			t.Fatalf("%s: a refused write was stored", what)
		}
	}

	// Moved to a project the member cannot write, after the route resolved
	// the old one.
	moveNode(t, f, f.ticket, f.other)
	for _, write := range writes {
		w := requestAs(f, member, scope, write.method, write.path, write.body)
		expect(t, w, 403)
	}
	moveNode(t, f, f.ticket, f.project)
	untouched("moved")

	// Revoked while the write waits on the fence. The holder takes the
	// access-change lock first; the write must queue behind it and then see
	// the revocation.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	for _, write := range writes {
		holder, err := f.db.Admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := authz.LockProjectMutation(ctx, holder, f.a.TenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2::uuid`, f.a.TenantID, member.ID); err != nil {
			t.Fatal(err)
		}
		var pid int
		if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		result := make(chan *httptest.ResponseRecorder, 1)
		go func() { result <- requestAs(f, member, scope, write.method, write.path, write.body) }()
		ticker := time.NewTicker(5 * time.Millisecond)
		for waiting := false; !waiting; {
			if err := f.db.Admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case w := <-result:
				_ = holder.Rollback(context.Background())
				t.Fatalf("%s %s did not wait for the access fence: %d %s", write.method, write.path, w.Code, w.Body.String())
			case <-ctx.Done():
				t.Fatal("the write never waited for the access fence")
			case <-ticker.C:
			}
		}
		ticker.Stop()
		if err := holder.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		w := <-result
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s after revocation: %d %s", write.method, write.path, w.Code, w.Body.String())
		}
		untouched(write.path)
		bindProjectRole(t, f, member.ID, "member", f.project)
	}
	expect(t, requestAs(f, member, scope, "POST", "/api/knowledge/learnings/"+ticketID+"/dismiss", `{"reason":"Covered"}`), 200)
}

// Every sensitive range field the server sends is in the contract's enum:
// lesson and reason (recommend, dismiss) as well as text (accept).
func TestSensitiveRangeFieldsMatchContract(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	allowed := doc.Components.Schemas["SensitiveRange"].Properties["field"].Enum

	f := setup(t)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "flywheel", "title": "Flywheel", "body": "Notes\n"})
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketID := nodeLearningID(f.ticket)
	secret := "password=" + strings.Repeat("x", 12)
	got := map[string]bool{}
	for _, action := range []struct {
		method, path string
		body         map[string]any
	}{
		{"PUT", recommendPath(ticketID), map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": "Use " + secret}},
		{"PUT", recommendPath(ticketID), map[string]any{"decision": "dismiss", "reason": "Has " + secret}},
		{"POST", "/api/knowledge/learnings/" + ticketID + "/dismiss", map[string]any{"reason": "Has " + secret}},
		{"POST", "/api/knowledge/learnings/" + ticketID + "/accept", map[string]any{"knowledge_id": book.ID, "lesson": "Use " + secret}},
	} {
		w := call(t, f, f.a, action.method, action.path, action.body)
		expect(t, w, 409)
		var problem struct {
			Code   string           `json:"code"`
			Ranges []SensitiveRange `json:"ranges"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.Code != "learning_sensitive" || len(problem.Ranges) == 0 {
			t.Fatalf("%s %s: %s", action.method, action.path, w.Body.String())
		}
		for _, r := range problem.Ranges {
			if !slices.Contains(allowed, r.Field) {
				t.Fatalf("%s %s sends field %q outside the contract enum %v", action.method, action.path, r.Field, allowed)
			}
			got[r.Field] = true
		}
	}
	for _, want := range []string{"lesson", "reason", "text"} {
		if !got[want] {
			t.Fatalf("no %s range was exercised: %v", want, got)
		}
	}
}
