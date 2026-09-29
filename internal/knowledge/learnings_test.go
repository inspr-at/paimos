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

	"github.com/inspr-at/paimos/internal/authz"
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
	onNested := addComment(t, f, nested, "#process-learning Keep this inside the nested project.")
	nestedCommentID := commentLearningID(nested, onNested)
	if hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, nestedCommentID) {
		t.Fatal("comment on a nested project listed under the parent")
	}
	nestedPage = listLearningsHTTP(t, f, f.a, nested)
	var nestedComment Learning
	for _, item := range nestedPage.Items {
		if item.ID == nestedCommentID {
			nestedComment = item
		}
	}
	if nestedComment.Key != "NEST-1" || nestedComment.Href != "/p/NEST-1" || nestedComment.Text != "Keep this inside the nested project" {
		t.Fatalf("nested project comment %+v", nestedComment)
	}
	parentBook := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "parent-notes", "title": "Parent notes", "body": "Parent.\n"})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nestedCommentID+"/accept", map[string]any{"knowledge_id": parentBook.ID})
	expect(t, w, 404)
	if code(t, w) != "not_found" || decisionOf(t, f, nestedCommentID) != "" {
		t.Fatalf("parent accept %s", w.Body.String())
	}
	if decode[Entry](t, call(t, f, f.a, "GET", "/api/knowledge/"+parentBook.ID, nil)).Body != parentBook.Body {
		t.Fatal("parent changelog changed")
	}
	nestedBook := createEntry(t, f, f.a, map[string]any{"project_id": nested, "type": "runbook", "slug": "nested-notes", "title": "Nested notes", "body": "Nested.\n"})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nestedCommentID+"/accept", map[string]any{"knowledge_id": nestedBook.ID})
	expect(t, w, 200)
	nestedDecision := decode[LearningDecision](t, w)
	if !strings.Contains(nestedDecision.Line, "](/p/NEST-1).") || strings.Contains(nestedDecision.Line, "PRJ-1") || decisionProject(t, f, nestedCommentID) != nested {
		t.Fatalf("nested accept %q project %s", nestedDecision.Line, decisionProject(t, f, nestedCommentID))
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

func TestOpenLearningsSurviveDecidedWindow(t *testing.T) {
	f := setup(t)
	// One more decided row than the scan reads. Filtering after LIMIT would
	// hide the older open learning and still report a short page.
	n := learningScanLimit + 1
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `
			WITH tagged AS (
			  INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			  SELECT $1, 'LD-'||g::text, k.id, 'Decided '||g::text, $2::uuid,
			         '{"tags":["process-learning"]}'::jsonb, clock_timestamp()
			  FROM generate_series(1, $3) AS g
			  JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='ticket'
			  RETURNING id
			), ev AS (
			  INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after)
			  SELECT $1, $4::uuid, id, 'knowledge.learning_dismissed',
			         jsonb_build_object('source_key', 'n-'||id::text)
			  FROM tagged
			  RETURNING id, node_id
			)
			INSERT INTO method_learning_decisions (tenant_id, project_id, source_key, decision, decided_by, event_id)
			SELECT $1, $2::uuid, 'n-'||node_id::text, 'dismissed', $4::uuid, id FROM ev`,
			f.a.TenantID, f.project, n, f.a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, fields, updated_at)
			SELECT $1, 'OLD-1', k.id, 'The older ticket still open', $2::uuid,
			       '{"tags":["process-learning"]}'::jsonb, clock_timestamp() - interval '40 days'
			FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='ticket'`,
			f.a.TenantID, f.project); err != nil {
			return err
		}
		var host string
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id)
			SELECT $1, 'HOST-8', k.id, 'Comment host', $2::uuid
			FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='ticket'
			RETURNING id::text`, f.a.TenantID, f.project).Scan(&host); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			WITH comments AS (
			  INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after, at)
			  SELECT $1, $2::uuid, $3::uuid, 'comment.created',
			         jsonb_build_object('body_markdown', '#process-learning Decided '||g::text),
			         clock_timestamp() - make_interval(secs => g)
			  FROM generate_series(1, $4) AS g
			  RETURNING id, node_id
			), ev AS (
			  INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after)
			  SELECT $1, $2::uuid, node_id, 'knowledge.learning_dismissed',
			         jsonb_build_object('source_key', 'c-'||node_id::text||'-'||comments.id::text)
			  FROM comments
			  RETURNING id, after
			)
			INSERT INTO method_learning_decisions (tenant_id, project_id, source_key, decision, decided_by, event_id)
			SELECT $1, $5::uuid, after->>'source_key', 'dismissed', $2::uuid, id FROM ev`,
			f.a.TenantID, f.a.ID, host, n, f.project); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after, at)
			VALUES ($1, $2::uuid, $3::uuid, 'comment.created',
			        jsonb_build_object('body_markdown', '#process-learning The older comment still open'),
			        clock_timestamp() - interval '30 days')`,
			f.a.TenantID, f.a.ID, host)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var decisions int
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM method_learning_decisions WHERE tenant_id=$1`, f.a.TenantID).Scan(&decisions)
	})
	if err != nil {
		t.Fatal(err)
	}
	if decisions != 2*n {
		t.Fatalf("decisions %d", decisions)
	}
	page := listLearningsHTTP(t, f, f.a, f.project)
	if page.Truncated {
		t.Fatal("truncated")
	}
	got := map[string]Learning{}
	for _, item := range page.Items {
		got[item.Key+" "+item.Text] = item
		if strings.HasPrefix(item.Key, "LD-") || strings.HasPrefix(item.Text, "Decided") {
			t.Fatalf("decided item listed: %+v", item)
		}
	}
	ticket := got["OLD-1 The older ticket still open"]
	comment := got["HOST-8 The older comment still open"]
	if len(page.Items) != 2 || ticket.Source != "ticket" || comment.Source != "comment" || decisionOf(t, f, ticket.ID) != "" || decisionOf(t, f, comment.ID) != "" {
		t.Fatalf("page %+v", page.Items)
	}
}

func TestLearningsProjectGrant(t *testing.T) {
	f := setup(t)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	ticketComment := addComment(t, f, f.ticket, "#process-learning Quote the ticket in the changelog.")
	onProject := addComment(t, f, f.project, "#process-learning The project speaks for itself.")
	elsewhere := addNode(t, f, "OTH-3", "ticket", "Other learning", &f.other)
	setFields(t, f, elsewhere, map[string]any{"tags": []any{"process-learning"}})
	nested := addNode(t, f, "NEST-1", "project", "Nested", &f.project)
	onNested := addComment(t, f, nested, "#process-learning Stay with the nested project.")

	member := projectPerson(t, f, "Nia Project")
	bindProjectRole(t, f, member.ID, "member", f.project)
	guest := projectPerson(t, f, "Gus Guest")
	bindProjectRole(t, f, guest.ID, "guest", f.project)

	memberCtx := authz.BindPool(tenant.WithPrincipal(t.Context(), member), f.db.App)
	const listRoute = "GET /api/knowledge/learnings"
	if err := authz.RequirePattern(memberCtx, listRoute, authz.Scope{}); err == nil {
		t.Fatal("workspace scope authorized a project member")
	}
	if !authz.ProjectFilteredRoutes[listRoute] {
		t.Fatal("learnings list is not project-filtered")
	}
	listScope, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, listRoute, "/api/knowledge/learnings")
	if err != nil || !ok || !listScope.AnyProject || listScope.ProjectID != "" {
		t.Fatalf("list scope %+v ok=%v err=%v", listScope, ok, err)
	}
	if err := authz.RequirePattern(memberCtx, listRoute, listScope); err != nil {
		t.Fatal(err)
	}
	ticketID := nodeLearningID(f.ticket)
	page := listLearningsHTTP(t, f, member, f.project)
	if !hasLearning(page.Items, ticketID) || !hasLearning(page.Items, commentLearningID(f.ticket, ticketComment)) || !hasLearning(page.Items, commentLearningID(f.project, onProject)) {
		t.Fatalf("own project %+v", page.Items)
	}
	if hasLearning(page.Items, nodeLearningID(elsewhere)) || hasLearning(page.Items, commentLearningID(nested, onNested)) {
		t.Fatal("project member saw another project")
	}
	w := call(t, f, member, "GET", "/api/knowledge/learnings?project_id="+f.other, nil)
	expect(t, w, 404)

	acceptPattern := "POST /api/knowledge/learnings/{learningId}/accept"
	dismissPattern := "POST /api/knowledge/learnings/{learningId}/dismiss"
	for _, pattern := range []string{acceptPattern, dismissPattern} {
		if err := authz.RequirePattern(memberCtx, pattern, authz.Scope{}); err == nil {
			t.Fatalf("%s allowed without a project", pattern)
		}
		action := "accept"
		if strings.HasSuffix(pattern, "/dismiss") {
			action = "dismiss"
		}
		scope, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, pattern, "/api/knowledge/learnings/"+ticketID+"/"+action)
		if err != nil || !ok || scope.AnyProject || scope.ProjectID != f.project {
			t.Fatalf("%s ticket scope %+v ok=%v err=%v", action, scope, ok, err)
		}
		if err := authz.RequirePattern(memberCtx, pattern, scope); err != nil {
			t.Fatal(err)
		}
		commentScope, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, pattern, "/api/knowledge/learnings/"+commentLearningID(f.project, onProject)+"/"+action)
		if err != nil || !ok || commentScope.AnyProject || commentScope.ProjectID != f.project {
			t.Fatalf("%s project comment %+v ok=%v err=%v", action, commentScope, ok, err)
		}
		hidden, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, pattern, "/api/knowledge/learnings/"+nodeLearningID(elsewhere)+"/"+action)
		if err != nil || ok {
			t.Fatalf("%s other project %+v ok=%v err=%v", action, hidden, ok, err)
		}
		bad, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, pattern, "/api/knowledge/learnings/not-an-id/"+action)
		if err != nil || ok {
			t.Fatalf("%s bad id %+v ok=%v", action, bad, ok)
		}
	}
	ownerCtx := authz.BindPool(tenant.WithPrincipal(t.Context(), f.a), f.db.App)
	nestedScope, ok, err := authz.ResolveRouteScope(ownerCtx, f.db.App, acceptPattern, "/api/knowledge/learnings/"+commentLearningID(nested, onNested)+"/accept")
	if err != nil || !ok || nestedScope.ProjectID != nested || nestedScope.AnyProject {
		t.Fatalf("nested project comment %+v ok=%v err=%v", nestedScope, ok, err)
	}
	if _, ok, err := authz.ResolveRouteScope(memberCtx, f.db.App, acceptPattern, "/api/knowledge/learnings/"+commentLearningID(nested, onNested)+"/accept"); err != nil || ok {
		t.Fatal("parent grant resolved a nested project comment")
	}

	guestCtx := authz.BindPool(tenant.WithPrincipal(t.Context(), guest), f.db.App)
	if err := authz.RequirePattern(guestCtx, listRoute, authz.Scope{}); err == nil {
		t.Fatal("guest workspace scope authorized the list")
	}
	if err := authz.RequirePattern(guestCtx, listRoute, authz.Scope{AnyProject: true}); err != nil {
		t.Fatal(err)
	}
	if !hasLearning(listLearningsHTTP(t, f, guest, f.project).Items, ticketID) {
		t.Fatal("guest cannot list the project")
	}
	w = call(t, f, guest, "GET", "/api/knowledge/learnings?project_id="+f.other, nil)
	expect(t, w, 404)
	projectScope := authz.Scope{ProjectID: f.project}
	if err := authz.RequirePattern(guestCtx, acceptPattern, projectScope); err == nil {
		t.Fatal("guest can accept")
	}
	entry := createEntry(t, f, member, map[string]any{"type": "runbook", "slug": "member-notes", "title": "Member notes", "body": "Notes.\n"})
	w = callScoped(t, f, guest, projectScope, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": entry.ID})
	expect(t, w, 403)
	if code(t, w) != "forbidden" || decisionOf(t, f, ticketID) != "" {
		t.Fatalf("guest accept %s", w.Body.String())
	}
	w = call(t, f, member, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": entry.ID})
	expect(t, w, 403)
	if decisionOf(t, f, ticketID) != "" {
		t.Fatal("unscoped accept wrote a decision")
	}
	w = callScoped(t, f, member, projectScope, "POST", "/api/knowledge/learnings/"+ticketID+"/accept", map[string]any{"knowledge_id": entry.ID})
	expect(t, w, 200)
	if decode[LearningDecision](t, w).Decision != "accepted" || decisionProject(t, f, ticketID) != f.project {
		t.Fatal("member accept")
	}
	commentID := commentLearningID(f.ticket, ticketComment)
	w = call(t, f, member, "POST", "/api/knowledge/learnings/"+commentID+"/dismiss", nil)
	expect(t, w, 403)
	if decisionOf(t, f, commentID) != "" {
		t.Fatal("unscoped dismiss wrote a decision")
	}
	w = callScoped(t, f, member, projectScope, "POST", "/api/knowledge/learnings/"+commentID+"/dismiss", nil)
	expect(t, w, 200)
	if decode[LearningDecision](t, w).Decision != "dismissed" || decisionProject(t, f, commentID) != f.project {
		t.Fatal("member dismiss")
	}
}

func projectPerson(t *testing.T, f fixture, name string) tenant.Principal {
	t.Helper()
	p := tenant.Principal{TenantID: f.a.TenantID, Kind: tenant.Person, Name: name}
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person',$2) RETURNING id::text`, f.a.TenantID, name).Scan(&p.ID); err != nil {
		t.Fatal(err)
	}
	return p
}

func bindProjectRole(t *testing.T, f fixture, principalID, roleKey, projectID string) {
	t.Helper()
	tag, err := f.db.Admin.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id)
	  SELECT $1,$2,id,'project',$3::uuid FROM roles WHERE tenant_id=$1 AND key=$4`, f.a.TenantID, principalID, projectID, roleKey)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("binding %s: %d rows", roleKey, tag.RowsAffected())
	}
}

func callScoped(t *testing.T, f fixture, p tenant.Principal, scope authz.Scope, method, path string, body any, headers ...string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == nil {
		reader = strings.NewReader("")
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	r := httptest.NewRequest(method, path, reader)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	r = r.WithContext(authz.WithRouteScope(tenant.WithPrincipal(r.Context(), p), scope))
	w := httptest.NewRecorder()
	f.handler.ServeHTTP(w, r)
	return w
}

func decisionProject(t *testing.T, f fixture, source string) string {
	t.Helper()
	var project string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(t.Context(), `SELECT project_id::text FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, f.a.TenantID, source).Scan(&project)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
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
