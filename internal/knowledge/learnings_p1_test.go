// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: normal delivery, status comments and early/clean gates create human work.
func TestLearningSignalSelection(t *testing.T) {
	f := setup(t)
	ordinary := addNode(t, f, "SIG-1", "work", "Routine delivery", &f.project)
	closeAged(t, f, ordinary, "done", "1 hour")
	title := addNode(t, f, "SIG-2", "work", "INCIDENT: rollback failed", &f.project)
	tag := addNode(t, f, "SIG-3", "work", "Database recovery", &f.project)
	setFields(t, f, tag, map[string]any{"tags": []any{map[string]any{"name": "incident"}}})
	reopened := addNode(t, f, "SIG-4", "work", "A reopened delivery", &f.project)
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := events.Append(t.Context(), tx, f.a, events.Change{NodeID: &reopened, Type: "node.updated", Before: map[string]any{"state": "done"}, After: map[string]any{"state": "in_progress"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	early := addNode(t, f, "SIG-5", "work", "Early gate", &f.project)
	capped := addNode(t, f, "SIG-6", "work", "Capped gate", &f.project)
	clean := addNode(t, f, "SIG-7", "work", "Clean gate", &f.project)
	insertOutcome(t, f, f.project, early, `{"verdict":"changes","round":2,"summary":"First correction"}`, "")
	insertOutcome(t, f, f.project, capped, `{"verdict":"changes","round":3,"summary":"Repeated blocker"}`, "")
	insertOutcome(t, f, f.project, clean, `{"verdict":"ok","round":3,"summary":"Ready"}`, "")
	explicit := addComment(t, f, ordinary, "#process-learning Check rollback before releasing.")
	bare := addComment(t, f, ordinary, "The incident was resolved; VERDICT changes round 3.")
	sensitive := addComment(t, f, ordinary, "#process-learning Never paste "+fakeGitHubToken())
	agent := scopedAgent(t, f, "Signal scout")
	var agentComment int64
	err = db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		e, err := events.Append(t.Context(), tx, agent, events.Change{NodeID: &ordinary, Type: "comment.created", After: map[string]any{"body_markdown": "#process-learning Agents can nominate explicit lessons."}})
		agentComment = e.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil || n != 6 {
		t.Fatalf("signals=%d err=%v", n, err)
	}
	for _, id := range []string{title, tag, reopened, capped} {
		if _, _, ok := nominationOfKey(t, f, nodeLearningID(id)); !ok {
			t.Fatalf("missing signal %s", id)
		}
	}
	for _, id := range []string{ordinary, early, clean} {
		if _, _, ok := nominationOfKey(t, f, nodeLearningID(id)); ok {
			t.Fatalf("nominated routine work %s", id)
		}
	}
	for _, id := range []string{explicit, strconv.FormatInt(agentComment, 10)} {
		if _, _, ok := nominationOfKey(t, f, commentLearningID(ordinary, id)); !ok {
			t.Fatalf("missing explicit comment %s", id)
		}
	}
	for _, id := range []string{bare, sensitive} {
		if _, _, ok := nominationOfKey(t, f, commentLearningID(ordinary, id)); ok {
			t.Fatalf("nominated status/sensitive comment %s", id)
		}
	}
}

func learningHTTPPage(t *testing.T, f fixture, p tenant.Principal) LearningPage {
	t.Helper()
	w := call(t, f, p, "GET", "/api/knowledge/learnings?project_id="+f.project, nil)
	expect(t, w, 200)
	return decode[LearningPage](t, w)
}

// Risk: a large unprepared window hides a real, older prepared lesson; the
// person is asked to accept a title, stale source or unavailable target.
func TestUnpreparedLearningsHiddenFromPeople(t *testing.T) {
	f := setup(t)
	agent := scopedAgent(t, f, "Lesson scout")
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "prepared-lessons", "title": "Prepared lessons", "body": "Notes\n"})
	if len(learningHTTPPage(t, f, f.a).Items) != 0 {
		t.Fatal("person sees an unprepared learning")
	}
	if !hasLearning(learningHTTPPage(t, f, agent).Items, nodeLearningID(f.ticket)) {
		t.Fatal("agent cannot prepare a hidden candidate")
	}
	body := map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": "Validate rollback before releasing."}
	expect(t, call(t, f, agent, "PUT", recommendPath(nodeLearningID(f.ticket)), body), 200)
	if len(learningHTTPPage(t, f, f.a).Items) != 0 {
		t.Fatal("person sees an incomplete recommendation")
	}
	body["source_summary"] = "The rollback incident exposed a missing recovery check; validation prevents recurrence."
	w := call(t, f, agent, "PUT", recommendPath(nodeLearningID(f.ticket)), body)
	expect(t, w, 200)
	rec := decode[Recommendation](t, w)
	if !rec.Prepared || rec.Line == "" || rec.SourceSummary == "" {
		t.Fatalf("incomplete prepared response %+v", rec)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields)
   SELECT $1,'NOISE-'||g,k.id,'Routine noise '||g,$2::uuid,'{"tags":["process-learning"]}'::jsonb
   FROM generate_series(1,501) g JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	page := learningHTTPPage(t, f, f.a)
	if len(page.Items) != 1 || page.Truncated || page.Items[0].ID != nodeLearningID(f.ticket) || page.Items[0].Recommendation.Line != rec.Line {
		t.Fatalf("prepared lesson hidden behind noise: %+v", page)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET title='Changed incident' WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, f.ticket); err != nil {
		t.Fatal(err)
	}
	if len(learningHTTPPage(t, f, f.a).Items) != 0 {
		t.Fatal("person sees a stale proposal")
	}
	body["lesson"] = "Rehearse recovery before the next rollout."
	expect(t, call(t, f, agent, "PUT", recommendPath(nodeLearningID(f.ticket)), body), 200)
	rec = *learningHTTPPage(t, f, f.a).Items[0].Recommendation
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nodeLearningID(f.ticket)+"/accept", map[string]any{"knowledge_id": book.ID, "lesson": rec.Lesson, "learning_text": "Changed incident"})
	expect(t, w, 200)
	if got := decode[LearningDecision](t, w); got.Line != rec.Line || !strings.Contains(entryBody(t, f, book.ID), rec.Line) {
		t.Fatalf("accept differs from prepared line: %+v", got)
	}
}

// Risk: an agent clears noise without a reason or audit; a person cannot undo.
func TestAgentDismissAuditedAndPersonUndo(t *testing.T) {
	f := setup(t)
	agent := scopedAgent(t, f, "Noise scout")
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	id := nodeLearningID(f.ticket)
	path := "/api/knowledge/learnings/" + id + "/dismiss"
	for _, body := range []any{nil, map[string]any{}, map[string]any{"reason": "  "}} {
		w := call(t, f, agent, "POST", path, body)
		expect(t, w, 400)
		if code(t, w) != "invalid_request" {
			t.Fatal(w.Body.String())
		}
	}
	w := call(t, f, agent, "POST", path, map[string]any{"reason": "Already covered by the recovery runbook."})
	expect(t, w, 200)
	decision := decode[LearningDecision](t, w)
	var actor, kind string
	var audit learningAudit
	var raw []byte
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT p.id::text,p.kind,e.after FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.tenant_id=$1 AND e.id=$2`, f.a.TenantID, decision.EventID).Scan(&actor, &kind, &raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if actor != agent.ID || kind != "agent" || audit.Reason != "Already covered by the recovery runbook." || decisionOf(t, f, id) != "dismissed" {
		t.Fatalf("wrong agent audit %+v %s %s", audit, actor, kind)
	}
	expect(t, call(t, f, agent, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 403)
	expect(t, call(t, f, f.a, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 201)
	if decisionOf(t, f, id) != "" || !hasLearning(learningHTTPPage(t, f, agent).Items, id) {
		t.Fatal("person undo did not reopen candidate")
	}
}

func TestAcceptLearningRemainsPersonOnly(t *testing.T) {
	f := setup(t)
	agent := scopedAgent(t, f, "Accept scout")
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	id := nodeLearningID(f.ticket)
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "human-accept", "title": "Human accept", "body": "Notes\n"})
	w := call(t, f, agent, "POST", "/api/knowledge/learnings/"+id+"/accept", map[string]any{"knowledge_id": book.ID, "lesson": "Agent approval attempt."})
	expect(t, w, 403)
	if code(t, w) != "person_required" {
		t.Fatal(w.Body.String())
	}
	_, err := (&module{pool: f.db.App}).acceptLearning(t.Context(), agent, id, f.ticket, "", false, acceptInput{KnowledgeID: book.ID}, nil)
	if err == nil || decisionOf(t, f, id) != "" || entryBody(t, f, book.ID) != "Notes\n" {
		t.Fatal("agent acceptance crossed the person gate")
	}
}

// Risk: cleanup crosses tenants, repeats after restart/undo, misses tag-only
// sources, or dismisses a recommendation made since the inventory scan.
func TestLegacyCleanupIdempotentPreservesPrepared(t *testing.T) {
	f := setup(t)
	nominated := f.ticket
	tagged := addNode(t, f, "OLD-1", "work", "Old tagged title", &f.other)
	protected := addNode(t, f, "OLD-2", "work", "Prepared source", &f.project)
	fresh := addNode(t, f, "NEW-1", "work", "New candidate", &f.project)
	for _, id := range []string{tagged, protected, fresh} {
		setFields(t, f, id, map[string]any{"tags": []any{"process-learning"}})
	}
	projectComment := addComment(t, f, f.project, "#process-learning Old status comment.")
	ticketComment := addComment(t, f, f.ticket, "#process-learning Old ticket comment.")
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "cleanup-safe", "title": "Prepared target", "body": "Notes\n"})
	agent := scopedAgent(t, f, "Cleanup scout")
	expect(t, call(t, f, agent, "PUT", recommendPath(nodeLearningID(protected)), map[string]any{"decision": "accept", "knowledge_id": book.ID, "lesson": "Check recovery before rollout.", "source_summary": "An incident exposed missing recovery validation."}), 200)
	var foreignID string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields,updated_at) SELECT $1,'FOREIGN-1',id,'Old foreign title',$2::uuid,'{"tags":["process-learning"]}',timestamptz '2000-01-01' FROM node_kinds WHERE tenant_id=$1 AND slug='work' RETURNING id::text`, f.foreign.TenantID, f.elsewhere).Scan(&foreignID)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_nominations(tenant_id,source_key,project_id,node_id,origin,excerpt,source_hash,nominated_at) VALUES($1,$2,$3,$4,'closed_ticket','Old title',$5,'2000-01-01')`, f.a.TenantID, nodeLearningID(nominated), f.project, nominated, sourceHash("Old title")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET updated_at='2000-01-01' WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, f.a.TenantID, []string{tagged, protected}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE events SET at='2000-01-01' WHERE tenant_id=$1 AND id=ANY($2::bigint[])`, f.a.TenantID, []string{projectComment, ticketComment}); err != nil {
		t.Fatal(err)
	}
	for _, tid := range []string{f.a.TenantID, f.foreign.TenantID} {
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_legacy_cleanup(tenant_id,cutoff) VALUES($1,'2020-01-01') ON CONFLICT(tenant_id) DO UPDATE SET cutoff=EXCLUDED.cutoff`, tid); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CleanupLegacyLearnings(t.Context(), f.db.App)
	if err != nil || n != 5 {
		t.Fatalf("cleanup=%d err=%v", n, err)
	}
	for _, key := range []string{nodeLearningID(nominated), nodeLearningID(tagged), commentLearningID(f.project, projectComment), commentLearningID(f.ticket, ticketComment)} {
		if decisionOf(t, f, key) != "dismissed" {
			t.Fatalf("missed legacy source %s", key)
		}
	}
	if decisionOf(t, f, nodeLearningID(protected)) != "" || decisionOf(t, f, nodeLearningID(fresh)) != "" {
		t.Fatal("cleanup touched prepared or post-cutoff source")
	}
	var eventID int64
	var kind, name, reason string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT e.id,p.kind,p.name,e.after->>'reason' FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.tenant_id=$1 AND e.after->>'source_key'=$2 AND e.type=$3`, f.a.TenantID, nodeLearningID(nominated), evLearningDismissed).Scan(&eventID, &kind, &name, &reason); err != nil {
		t.Fatal(err)
	}
	if kind != "agent" || name != "System" || reason != legacyDismissReason {
		t.Fatalf("wrong cleanup actor/reason %s %s %s", kind, name, reason)
	}
	expect(t, call(t, f, f.a, "POST", "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", nil), 201)
	again, err := CleanupLegacyLearnings(t.Context(), f.db.App)
	if err != nil || again != 0 || decisionOf(t, f, nodeLearningID(nominated)) != "" {
		t.Fatalf("restart undid human undo: %d %v", again, err)
	}
	var foreignDecisions int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2`, f.foreign.TenantID, nodeLearningID(foreignID)).Scan(&foreignDecisions); err != nil {
		t.Fatal(err)
	}
	if foreignDecisions != 1 {
		t.Fatalf("foreign tenant cleanup decisions=%d", foreignDecisions)
	}
}

func TestPreparedLearningRuleDraft(t *testing.T) {
	f := setup(t)
	h := rulesHandler(f)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	layer := rulesLayer(t, h, f.a, rules.Scope{Layer: "project", ProjectID: f.project})
	set := rulesSet(t, h, f.a, layer.ID, "Prepared recovery rules")
	agent := scopedAgent(t, f, "Rule scout")
	agent.KeyCreatorID = f.a.ID
	agent.Scopes = append(agent.Scopes, "rules.read")
	w := call(t, f, agent, "PUT", recommendPath(nodeLearningID(f.ticket)), map[string]any{"decision": "accept", "knowledge_id": set.ID, "rule_layer_id": layer.ID, "lesson": "Rehearse rollback before releasing.", "source_summary": "The incident showed recovery was untested."})
	expect(t, w, 200)
	rec := decode[Recommendation](t, w)
	if !rec.Prepared || rec.RuleText == "" || rec.Line != "" {
		t.Fatalf("wrong rule proposal %+v", rec)
	}
	page := learningHTTPPage(t, f, f.a)
	if len(page.Items) != 1 || !page.Items[0].Recommendation.Prepared {
		t.Fatalf("prepared rule hidden %+v", page)
	}
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nodeLearningID(f.ticket)+"/draft", map[string]any{"layer_id": layer.ID, "set_id": set.ID})
	expect(t, w, 200)
	drafted := rulesGet(t, h, f.a, set.ID)
	if len(drafted.Rules) != 1 || drafted.Rules[0].Text != rec.RuleText || drafted.PublishedVersion != "" {
		t.Fatalf("rule differs from prepared text %+v", drafted)
	}
}
