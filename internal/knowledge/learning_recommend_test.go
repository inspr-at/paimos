// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

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
