// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestAppendChangelog(t *testing.T) {
	const line = "- 2026-09-29: learned."
	body := "# Runbook\n\n## 12. Method changelog\n\n- 2026-09-01: older.\n\n## 13. Next\n\nStay.\n"
	out, heading := appendChangelog(body, line)
	if heading != "12. Method changelog" {
		t.Fatalf("heading %q", heading)
	}
	section, after, ok := splitSection(out, "## 12. Method changelog", "## 13. Next")
	if !ok || !strings.Contains(section, "- 2026-09-01: older.\n"+line+"\n") || !strings.Contains(after, "Stay.") {
		t.Fatalf("section insert:\n%s", out)
	}

	empty, _ := appendChangelog("## 12. Method changelog\n## 13. Next\n", line)
	if !strings.Contains(empty, "## 12. Method changelog\n\n"+line+"\n## 13. Next\n") {
		t.Fatalf("empty section:\n%s", empty)
	}

	fresh, heading := appendChangelog("Hello\n", line)
	if heading != "Changelog" || fresh != "Hello\n\n## Changelog\n\n"+line+"\n" {
		t.Fatalf("new section %q\n%s", heading, fresh)
	}
	blank, heading := appendChangelog("", line)
	if heading != "Changelog" || blank != "## Changelog\n\n"+line+"\n" {
		t.Fatalf("blank %q\n%s", heading, blank)
	}

	fenced := "## Notes\n\n```\n## Changelog\nfake\n```\n\n## 12. Method changelog\n\n- kept\n"
	out, heading = appendChangelog(fenced, line)
	if heading != "12. Method changelog" || strings.Contains(out, "fake\n"+line) || !strings.Contains(out, "- kept\n"+line+"\n") {
		t.Fatalf("fence %q\n%s", heading, out)
	}
	if strings.Count(out, "## Changelog") != 1 || !strings.Contains(out, "```\n## Changelog\nfake\n```") {
		t.Fatalf("fence disturbed:\n%s", out)
	}

	text, ok := commentLearningText("#process-learning Renumber at integration.")
	if !ok || text != "Renumber at integration" {
		t.Fatalf("comment text %q %v", text, ok)
	}
	if _, ok := commentLearningText("#process-learning"); ok {
		t.Fatal("a comment that is only the tag is not a learning")
	}
	if _, ok := commentLearningText("my-process-learning is a slug"); ok {
		t.Fatal("hyphenated token matched")
	}
	text, ok = commentLearningText("Please #process-learning renumber soon.")
	if !ok || text != "Please renumber soon" {
		t.Fatalf("stripped %q %v", text, ok)
	}
	if got := oneLine("Use [this] `code` <tag>", false); got != "Use (this) 'code' (tag)" {
		t.Fatalf("neutral %q", got)
	}
	if !hasLearningTag([]byte(`{"tags":["process-learning"]}`)) || !hasLearningTag([]byte(`{"tags":[{"name":"Process-Learning"}]}`)) {
		t.Fatal("tag shapes")
	}
	if hasLearningTag([]byte(`{"tags":["my-process-learning"]}`)) || hasLearningTag([]byte(`{"body":"process-learning"}`)) {
		t.Fatal("non-tags matched")
	}
}

func splitSection(body, start, end string) (string, string, bool) {
	at := strings.Index(body, start)
	next := strings.Index(body, end)
	if at < 0 || next < at {
		return "", "", false
	}
	return body[at:next], body[next:], true
}

func TestMethodLearnings(t *testing.T) {
	f := setup(t)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	mention := addNode(t, f, "LEARN-1", "ticket", "Mention only", &f.project)
	setBody(t, f, mention, "The notes mention process-learning without a tag.")
	object := addNode(t, f, "LEARN-2", "ticket", "Ship the checklist", &f.project)
	setFields(t, f, object, map[string]any{"tags": []any{map[string]any{"name": "Process-Learning", "color": "gray"}}})
	task := addNode(t, f, "TASK-1", "task", "Rename the staging host", &f.project)
	setFields(t, f, task, map[string]any{"tags": []any{"process-learning"}})
	elsewhere := addNode(t, f, "OTH-9", "ticket", "Other project learning", &f.other)
	setFields(t, f, elsewhere, map[string]any{"tags": []any{"process-learning"}})
	nested := addNode(t, f, "NEST-1", "project", "Nested", &f.project)
	buried := addNode(t, f, "NEST-9", "ticket", "Buried learning", &nested)
	setFields(t, f, buried, map[string]any{"tags": []any{"process-learning"}})

	renumber := addComment(t, f, f.ticket, "#process-learning Renumber at integration.")
	onlyTag := addComment(t, f, f.ticket, "#process-learning")
	hyphenated := addComment(t, f, f.ticket, "my-process-learning is a slug")
	edited := addComment(t, f, f.ticket, "#process-learning Drop me later.")
	reviseComment(t, f, f.ticket, edited, "The tag is gone.", false)
	removed := addComment(t, f, f.ticket, "#process-learning Delete me.")
	reviseComment(t, f, f.ticket, removed, "#process-learning Delete me.", true)
	onProject := addComment(t, f, f.project, "#process-learning Write the release note in the same turn.")
	_ = onlyTag
	_ = hyphenated

	w := call(t, f, f.a, "GET", "/api/knowledge/learnings", nil)
	expect(t, w, 400)
	w = call(t, f, f.foreign, "GET", "/api/knowledge/learnings?project_id="+f.project, nil)
	expect(t, w, 404)
	if code(t, w) != "not_found" {
		t.Fatalf("foreign %s", w.Body.String())
	}

	page := listLearningsHTTP(t, f, f.a, f.project)
	if page.Truncated {
		t.Fatal("truncated")
	}
	ids := map[string]Learning{}
	for _, item := range page.Items {
		ids[item.ID] = item
	}
	ticketID := nodeLearningID(f.ticket)
	objectID := nodeLearningID(object)
	taskID := nodeLearningID(task)
	commentID := commentLearningID(f.ticket, renumber)
	projectCommentID := commentLearningID(f.project, onProject)
	for _, id := range []string{ticketID, objectID, taskID, commentID, projectCommentID} {
		if _, ok := ids[id]; !ok {
			t.Fatalf("missing %s in %v", id, keysOf(ids))
		}
	}
	for _, id := range []string{nodeLearningID(mention), nodeLearningID(elsewhere), nodeLearningID(buried), commentLearningID(f.ticket, edited), commentLearningID(f.ticket, removed)} {
		if _, ok := ids[id]; ok {
			t.Fatalf("listed %s", id)
		}
	}
	if ids[ticketID].Source != "ticket" || ids[ticketID].Author != nil || ids[ticketID].Text != "Rotate the fleet keys" || ids[ticketID].Href != "/p/PRJ-1/PHAROS-7" {
		t.Fatalf("ticket %+v", ids[ticketID])
	}
	if ids[taskID].Source != "ticket" || ids[taskID].Key != "TASK-1" {
		t.Fatalf("task %+v", ids[taskID])
	}
	if ids[commentID].Source != "comment" || ids[commentID].Text != "Renumber at integration" || ids[commentID].Author == nil || ids[commentID].Author.Name != "Markus Barta" || !strings.Contains(ids[commentID].Href, "PHAROS-7") {
		t.Fatalf("comment %+v", ids[commentID])
	}
	if ids[projectCommentID].Href != "/p/PRJ-1" || ids[projectCommentID].Text != "Write the release note in the same turn" {
		t.Fatalf("project comment %+v", ids[projectCommentID])
	}
	otherPage := listLearningsHTTP(t, f, f.a, f.other)
	if len(otherPage.Items) != 1 || otherPage.Items[0].Key != "OTH-9" {
		t.Fatalf("other project %+v", otherPage.Items)
	}
	nestedPage := listLearningsHTTP(t, f, f.a, nested)
	if len(nestedPage.Items) != 1 || nestedPage.Items[0].Key != "NEST-9" {
		t.Fatalf("nested %+v", nestedPage.Items)
	}

	agent := tenant.Principal{TenantID: f.a.TenantID, Kind: tenant.Agent, Name: "Scout", Roles: []string{"owner"}, Scopes: []string{"knowledge.write"}}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent',$2,$3) RETURNING id::text`, f.a.TenantID, agent.Name, agent.Roles).Scan(&agent.ID)
	})
	if err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, f.db, f.a.TenantID, agent.ID, "owner")
	agentPage := listLearningsHTTP(t, f, agent, f.project)
	if !hasLearning(agentPage.Items, ticketID) {
		t.Fatal("agent cannot see the candidate")
	}
	w = call(t, f, agent, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": f.project})
	expect(t, w, 403)
	if code(t, w) != "person_required" {
		t.Fatalf("agent accept %s", w.Body.String())
	}
	w = call(t, f, agent, "POST", "/api/knowledge/learnings/"+ticketID+"/dismiss", map[string]any{})
	expect(t, w, 403)
	if code(t, w) != "person_required" {
		t.Fatalf("agent dismiss %s", w.Body.String())
	}
	if !hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, ticketID) {
		t.Fatal("agent refusal removed the candidate")
	}

	w = call(t, f, f.viewer, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": f.project})
	expect(t, w, 403)
	if code(t, w) != "forbidden" {
		t.Fatalf("viewer %s", w.Body.String())
	}

	runbook := createEntry(t, f, f.a, map[string]any{
		"type": "runbook", "slug": "flywheel", "title": "Flywheel",
		"body": "# Flywheel\n\n## 12. Method changelog\n\n- 2026-09-01: older.\n\n## 13. Next\n\nStay.\n",
	})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/not-an-id/accept", map[string]any{"knowledge_id": runbook.ID})
	expect(t, w, 400)
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": runbook.ID, "note": "no"})
	expect(t, w, 400)

	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": runbook.ID}, "If-Unmodified-Since", "2000-01-01T00:00:00Z")
	expect(t, w, 412)
	if code(t, w) != "stale" {
		t.Fatalf("stale %s", w.Body.String())
	}
	unchanged := decode[Entry](t, call(t, f, f.a, "GET", "/api/knowledge/"+runbook.ID, nil))
	if unchanged.Body != runbook.Body || !hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, ticketID) {
		t.Fatal("stale accept wrote")
	}

	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": runbook.ID})
	expect(t, w, 200)
	accepted := decode[LearningDecision](t, w)
	if accepted.Decision != "accepted" || accepted.EventID == 0 || accepted.Heading != "12. Method changelog" || accepted.Entry == nil || accepted.Entry.EventID == nil || *accepted.Entry.EventID != accepted.EventID {
		t.Fatalf("accept %+v", accepted)
	}
	if !strings.HasPrefix(accepted.Line, "- ") || !strings.Contains(accepted.Line, "Rotate the fleet keys") || !strings.Contains(accepted.Line, "[PHAROS-7](/p/PRJ-1/PHAROS-7)") {
		t.Fatalf("line %q", accepted.Line)
	}
	section, after, ok := splitSection(accepted.Entry.Body, "## 12. Method changelog", "## 13. Next")
	if !ok || !strings.Contains(section, accepted.Line) || strings.Contains(after, accepted.Line) || !strings.Contains(after, "Stay.") {
		t.Fatalf("body\n%s", accepted.Entry.Body)
	}
	if eventType(t, f, accepted.EventID) != evLearningAccepted {
		t.Fatalf("event %s", eventType(t, f, accepted.EventID))
	}
	if decisionOf(t, f, ticketID) != "accepted" {
		t.Fatal("decision missing")
	}
	if hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, ticketID) {
		t.Fatal("accepted learning still listed")
	}
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": runbook.ID})
	expect(t, w, 409)
	if code(t, w) != "already_decided" {
		t.Fatalf("second %s", w.Body.String())
	}

	plain := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "notes", "title": "Notes", "body": "Just text.\n"})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+taskID+"/accept", map[string]any{"knowledge_id": plain.ID})
	expect(t, w, 200)
	notes := decode[LearningDecision](t, w)
	if notes.Heading != "Changelog" || !strings.Contains(notes.Entry.Body, "Just text.\n\n## Changelog\n\n"+notes.Line+"\n") {
		t.Fatalf("new changelog\n%s", notes.Entry.Body)
	}

	otherEntry := createEntry(t, f, f.a, map[string]any{"project_id": f.other, "type": "runbook", "slug": "glint", "title": "Glint", "body": "There.\n"})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+objectID+"/accept", map[string]any{"knowledge_id": otherEntry.ID})
	expect(t, w, 404)
	if decisionOf(t, f, objectID) != "" || !hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, objectID) {
		t.Fatal("cross-project accept wrote a decision")
	}
	glint := decode[Entry](t, call(t, f, f.a, "GET", "/api/knowledge/"+otherEntry.ID, nil))
	if glint.Body != otherEntry.Body {
		t.Fatal("cross-project changed the body")
	}

	w = call(t, f, agent, "POST", "/api/events/"+strconv.FormatInt(accepted.EventID, 10)+"/undo", nil)
	expect(t, w, 403)
	still := decode[Entry](t, call(t, f, f.a, "GET", "/api/knowledge/"+runbook.ID, nil))
	if !strings.Contains(still.Body, accepted.Line) || hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, ticketID) {
		t.Fatal("agent undo changed the accept")
	}
	w = call(t, f, f.a, "POST", "/api/events/"+strconv.FormatInt(accepted.EventID, 10)+"/undo", nil)
	expect(t, w, 201)
	restored := decode[Entry](t, call(t, f, f.a, "GET", "/api/knowledge/"+runbook.ID, nil))
	if strings.Contains(restored.Body, accepted.Line) || restored.Body != runbook.Body {
		t.Fatalf("undo body\n%s", restored.Body)
	}
	if !hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, ticketID) || decisionOf(t, f, ticketID) != "" {
		t.Fatal("undo did not reopen the learning")
	}

	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+commentID+"/dismiss", nil)
	expect(t, w, 200)
	dismissed := decode[LearningDecision](t, w)
	if dismissed.Decision != "dismissed" || dismissed.Entry != nil || eventType(t, f, dismissed.EventID) != evLearningDismissed {
		t.Fatalf("dismiss %+v %s", dismissed, eventType(t, f, dismissed.EventID))
	}
	if hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, commentID) || decisionOf(t, f, commentID) != "dismissed" {
		t.Fatal("dismiss did not close the comment")
	}
	w = call(t, f, f.a, "POST", "/api/events/"+strconv.FormatInt(dismissed.EventID, 10)+"/undo", nil)
	expect(t, w, 201)
	if !hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, commentID) || decisionOf(t, f, commentID) != "" {
		t.Fatal("undo dismiss did not reopen the comment")
	}
}

func listLearningsHTTP(t *testing.T, f fixture, p tenant.Principal, project string) LearningPage {
	t.Helper()
	w := call(t, f, p, "GET", "/api/knowledge/learnings?project_id="+project, nil)
	expect(t, w, 200)
	page := decode[LearningPage](t, w)
	if page.Items == nil {
		t.Fatal("null items")
	}
	return page
}

func hasLearning(items []Learning, id string) bool {
	for _, item := range items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func keysOf(items map[string]Learning) []string {
	out := make([]string, 0, len(items))
	for id, item := range items {
		out = append(out, id+" "+item.Key)
	}
	return out
}

func code(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return body.Code
}

func addNode(t *testing.T, f fixture, key, kind, title string, parent *string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id)
		  SELECT $1,$2,id,$3,$5::uuid FROM node_kinds WHERE tenant_id=$1 AND slug=$4 RETURNING id::text`, f.a.TenantID, key, title, kind, parent).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func setFields(t *testing.T, f fixture, id string, fields map[string]any) {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(t.Context(), `UPDATE nodes SET fields=$3::jsonb WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, id, raw)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("node %s not updated", id)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func setBody(t *testing.T, f fixture, id, body string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET body=$3 WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, id, body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func addComment(t *testing.T, f fixture, nodeID, body string) string {
	t.Helper()
	var id int64
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		ev, err := events.Append(t.Context(), tx, f.a, events.Change{NodeID: &nodeID, Type: "comment.created", After: map[string]string{"body_markdown": body}})
		if err != nil {
			return err
		}
		id = ev.ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatInt(id, 10)
}

func reviseComment(t *testing.T, f fixture, nodeID, commentID, body string, deleted bool) {
	t.Helper()
	kind := "comment.updated"
	after := map[string]any{"comment_id": commentID, "body_markdown": body}
	if deleted {
		kind = "comment.deleted"
		after["deleted"] = true
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.a, events.Change{NodeID: &nodeID, Type: kind, After: after})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func eventType(t *testing.T, f fixture, id int64) string {
	t.Helper()
	var kind string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT type FROM events WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, id).Scan(&kind)
	})
	if err != nil {
		t.Fatal(err)
	}
	return kind
}

func decisionOf(t *testing.T, f fixture, source string) string {
	t.Helper()
	var decision string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(t.Context(), `SELECT decision FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, f.a.TenantID, source).Scan(&decision)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return decision
}
