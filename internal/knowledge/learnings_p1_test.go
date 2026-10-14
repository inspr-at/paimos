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
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO node_kinds(tenant_id,slug,label,short_prefix,icon) VALUES($1,'incident','Incident','INC','ticket')`, f.a.TenantID); err != nil {
		t.Fatal(err)
	}
	kind := addNode(t, f, "INC-1", "incident", "Recovery failure", &f.project)
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO model_situation_limits(tenant_id,fix_rounds) VALUES($1,4) ON CONFLICT(tenant_id) DO UPDATE SET fix_rounds=4`, f.a.TenantID); err != nil {
		t.Fatal(err)
	}
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
	insertOutcome(t, f, f.project, early, `{"verdict":"changes","round":3,"summary":"First correction"}`, "")
	insertOutcome(t, f, f.project, capped, `{"verdict":"changes","round":4,"summary":"Repeated blocker"}`, "")
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
	if err != nil || n != 7 {
		t.Fatalf("signals=%d err=%v", n, err)
	}
	for _, id := range []string{kind, title, tag, reopened, capped} {
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
	moveNode(t, f, book.ID, f.other)
	if len(learningHTTPPage(t, f, f.a).Items) != 0 {
		t.Fatal("person sees an unavailable target")
	}
	moveNode(t, f, f.ticket, f.other)
	moved := call(t, f, agent, "GET", "/api/knowledge/learnings?project_id="+f.other, nil)
	expect(t, moved, 200)
	for _, item := range decode[LearningPage](t, moved).Items {
		if item.ID == nodeLearningID(f.ticket) && item.Recommendation != nil {
			t.Fatal("moved source carried its old project proposal")
		}
	}
	moveNode(t, f, book.ID, f.project)
	moveNode(t, f, f.ticket, f.project)
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nodeLearningID(f.ticket)+"/accept", map[string]any{"knowledge_id": book.ID, "learning_text": "Changed incident"})
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
	comment := addComment(t, f, f.ticket, "#process-learning Another covered lesson.")
	spoof := call(t, f, agent, "POST", "/api/knowledge/learnings/"+commentLearningID(f.ticket, comment)+"/dismiss", map[string]any{"reason": legacyDismissReason})
	expect(t, spoof, 200)
	var job string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT coalesce(metadata->>'job','') FROM events WHERE tenant_id=$1 AND id=$2`, f.a.TenantID, decode[LearningDecision](t, spoof).EventID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if job != "" {
		t.Fatal("an agent's reason impersonated the cleanup job")
	}

	expect(t, call(t, f, agent, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 403)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.b.ID, "admin")
	expect(t, call(t, f, f.b, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 201)
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
	apiErr, personGate := err.(*apiError)
	if !personGate || apiErr.code != "person_required" || decisionOf(t, f, id) != "" || entryBody(t, f, book.ID) != "Notes\n" {
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
	projectComment := addLegacyComment(t, f, f.project, "#process-learning Old status comment.")
	ticketComment := addLegacyComment(t, f, f.ticket, "#process-learning Old ticket comment.")
	newlyTaggedComment := addLegacyComment(t, f, f.ticket, "An old status without a learning tag.")
	reviseComment(t, f, f.ticket, newlyTaggedComment, "#process-learning A new lesson after the release cutoff.", false)
	reviseComment(t, f, f.project, projectComment, "#process-learning An edited legacy lesson still needs cleanup.", false)
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
	for _, tid := range []string{f.a.TenantID, f.foreign.TenantID} {
		if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO method_learning_legacy_cleanup(tenant_id,cutoff) VALUES($1,'2020-01-01') ON CONFLICT(tenant_id) DO UPDATE SET cutoff=EXCLUDED.cutoff`, tid); err != nil {
			t.Fatal(err)
		}
	}
	const extra = legacyCleanupBatch + 1
	if _, err := f.db.Admin.Exec(t.Context(), `INSERT INTO nodes(tenant_id,key,kind_id,title,parent_id,fields,updated_at)
      SELECT $1,'BATCH-'||g,k.id,'Legacy batch '||g,$2::uuid,'{"tags":["process-learning"]}'::jsonb,timestamptz '2000-01-01'
      FROM generate_series(1,$3) g JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='work'`, f.a.TenantID, f.project, extra); err != nil {
		t.Fatal(err)
	}
	n, err := CleanupLegacyLearnings(t.Context(), f.db.App)
	if err != nil || n != 5+extra {
		t.Fatalf("cleanup=%d err=%v", n, err)
	}
	for _, key := range []string{nodeLearningID(nominated), nodeLearningID(tagged), commentLearningID(f.project, projectComment), commentLearningID(f.ticket, ticketComment)} {
		if decisionOf(t, f, key) != "dismissed" {
			t.Fatalf("missed legacy source %s", key)
		}
	}
	if decisionOf(t, f, nodeLearningID(protected)) != "" || decisionOf(t, f, nodeLearningID(fresh)) != "" || decisionOf(t, f, commentLearningID(f.ticket, newlyTaggedComment)) != "" {
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
	dbtest.BindRole(t, f.db, f.a.TenantID, f.b.ID, "admin")
	expect(t, call(t, f, f.b, "POST", "/api/events/"+strconv.FormatInt(eventID, 10)+"/undo", nil), 201)
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
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "admin")
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	layer := rulesLayer(t, h, f.a, rules.Scope{Layer: "project", ProjectID: f.project})
	set := rulesSet(t, h, f.a, layer.ID, "Prepared recovery rules")
	agent := scopedAgent(t, f, "Rule scout")
	agent.KeyCreatorID = f.a.ID
	agent.Scopes = append(agent.Scopes, "rules.read", "nodes.read")
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

func addLegacyComment(t *testing.T, f fixture, nodeID, body string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,node_id,type,after,at) VALUES($1,$2::uuid,$3::uuid,'comment.created',jsonb_build_object('body_markdown',$4::text),timestamptz '2000-01-01') RETURNING id::text`, f.a.TenantID, f.a.ID, nodeID, body).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Risk: a replaced recommendation writes a different prepared rule or
// changelog line than the one the person reviewed, including on Dismiss.
func TestDeskRecommendationRevisionBindsNativeWrites(t *testing.T) {
	f := setup(t)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	entry := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "desk-revision", "title": "Recovery", "body": "Notes"})
	id := nodeLearningID(f.ticket)
	body := map[string]any{"decision": "accept", "knowledge_id": entry.ID, "lesson": "Rehearse recovery before releasing.", "source_summary": "A recovery incident exposed an untested path."}
	first := call(t, f, f.a, "PUT", recommendPath(id), body)
	expect(t, first, 200)
	revision := decode[Recommendation](t, first).EventID
	body["lesson"] = "Check recovery before releasing."
	second := call(t, f, f.a, "PUT", recommendPath(id), body)
	expect(t, second, 200)
	prepared := decode[Recommendation](t, second)
	for _, action := range []string{"accept", "dismiss", "draft"} {
		input := map[string]any{}
		if action == "accept" {
			input["knowledge_id"] = entry.ID
			input["lesson"] = "Rehearse recovery before releasing."
		}
		if action == "draft" {
			input["layer_id"] = f.project
			input["set_id"] = entry.ID
		}
		w := call(t, f, f.a, "POST", "/api/knowledge/learnings/"+id+"/"+action, input, "If-Match", `"`+strconv.FormatInt(revision, 10)+`"`)
		expect(t, w, 409)
		var problem map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if problem["code"] != "learning_changed" {
			t.Fatalf("wrong refusal for %s: %s", action, w.Body.String())
		}
	}
	page := learningHTTPPage(t, f, f.a)
	if len(page.Items) != 1 || page.Items[0].Recommendation.Lesson != prepared.Lesson || page.Items[0].Recommendation.Line != prepared.Line || page.Items[0].Recommendation.EventID != prepared.EventID {
		t.Fatalf("refused writes changed lesson: %+v", page)
	}
	unchanged := call(t, f, f.a, "GET", "/api/knowledge/"+entry.ID, nil)
	expect(t, unchanged, 200)
	if got := decode[Entry](t, unchanged); got.Body != entry.Body {
		t.Fatal("a refused decision changed the entry")
	}
	current := page.Items[0].Recommendation.EventID
	expect(t, call(t, f, f.a, "POST", "/api/knowledge/learnings/"+id+"/accept", map[string]any{"knowledge_id": entry.ID, "lesson": prepared.Lesson}, "If-Match", `"`+strconv.FormatInt(current, 10)+`"`), 200)
}
