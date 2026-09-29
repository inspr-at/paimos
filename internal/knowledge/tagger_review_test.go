// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules"
)

// Synthetic credentials, assembled at run time so no scanner mistakes the
// source for a leak. None of them is a real key.
func fakeGitHubToken() string { return "gh" + "p_" + strings.Repeat("a1B2c3D4", 5) }
func fakeAWSKey() string      { return "AK" + "IA" + "QZ7XW3EXAMPLE9K2" }
func fakeJWT() string {
	return "ey" + "JhbGciOiJIUzI1NiJ9." + "ey" + "JzdWIiOiJ0ZXN0In0." + "c2lnbmF0dXJlLXRlc3Q"
}

func TestLooksSensitive(t *testing.T) {
	flagged := []string{
		"The incident: we pasted " + fakeGitHubToken() + " into the log",
		"rotate " + fakeAWSKey() + " today",
		"header was Bearer " + strings.Repeat("Zm9vYmFy", 3),
		"session " + fakeJWT(),
		"-----BEGIN OPENSSH PRIVATE KEY----- b3Blbn",
		"db url postgres://aeon:hunter22@db.internal/aeon",
		"set password=Winter2026! on the box",
		`"api_key": "abcdef123456"`,
		"x" + "ai-" + strings.Repeat("Q9w8E7r6", 3),
		"s" + "k-proj-" + strings.Repeat("Ab12Cd34", 3),
		"gl" + "pat-" + strings.Repeat("xY9", 7),
		"random " + "Q2hhbmdlTWVQbGVhc2U9MTIzNDU2Nzg5MEFCQ0RFRkdISUpL",
	}
	for _, text := range flagged {
		if !looksSensitive(text) {
			t.Errorf("missed %q", text)
		}
	}
	clean := []string{
		"Rotate the password every quarter",
		"The incident stopped the deploy",
		"VERDICT pass. The token refresh path held.",
		"merged at e9b3dd4ea0c30f4083e79657205b420976ac0d0c",
		"node 5f0c2a4e-7b1d-4c3e-9a8f-0123456789ab moved",
		"see internal/knowledge/learning_draft_test.go and web/src/components/knowledge/MethodLearnings",
		"sha256 3a7bd3e2360a3d29eea436fcfb7e44c735d117c42d1c1835420b6b9942dd4f1b",
		"task-runner and risk-assessment are separate",
	}
	for _, text := range clean {
		if looksSensitive(text) {
			t.Errorf("flagged %q", text)
		}
	}
}

// Finding 1: candidates that look like they hold a credential are never
// tagged or stored, and a stored excerpt is never served after its source
// changed.
func TestTaggerSkipsCredentialsAndServesLiveSource(t *testing.T) {
	f := setup(t)
	host := addNode(t, f, "INC-1", "ticket", "Incident host", &f.project)
	leakyTicket := addNode(t, f, "DONE-7", "ticket", "Rotate "+fakeAWSKey()+" before Friday", &f.project)
	closeAged(t, f, leakyTicket, "done", "1 hour")
	leakyIncident := addComment(t, f, host, "The incident: someone pasted "+fakeGitHubToken()+" in chat")
	leakyVerdict := addComment(t, f, host, "VERDICT fail. Found password=Winter2026! in the fixture")
	cleanIncident := addComment(t, f, host, "The incident: rotate the keys weekly")
	cleanVerdict := addComment(t, f, host, "VERDICT pass. The rotation held")

	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	if nodeTagged(t, f, leakyTicket) {
		t.Fatal("a ticket whose title holds a key was tagged")
	}
	for _, key := range []string{nodeLearningID(leakyTicket), commentLearningID(host, leakyIncident), commentLearningID(host, leakyVerdict)} {
		if _, _, ok := nominationOfKey(t, f, key); ok {
			t.Fatalf("stored a credential candidate %s", key)
		}
	}
	for _, key := range []string{commentLearningID(host, cleanIncident), commentLearningID(host, cleanVerdict)} {
		if _, _, ok := nominationOfKey(t, f, key); !ok {
			t.Fatalf("clean candidate %s not nominated", key)
		}
	}

	// The comment is edited after it was nominated: the inbox shows the new
	// text, never the stored excerpt.
	verdictID := commentLearningID(host, cleanVerdict)
	reviseComment(t, f, host, cleanVerdict, "VERDICT pass. The rotation held after the second try", false)
	if got := learningText(t, f, verdictID); got != "VERDICT pass. The rotation held after the second try" {
		t.Fatalf("edited verdict text %q", got)
	}
	// A stored excerpt that disagrees with its source (the reviewer's case:
	// secret in the old copy, source since cleaned) is not served.
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE method_learning_nominations SET excerpt=$3
		WHERE tenant_id=$1 AND source_key=$2`, f.a.TenantID, verdictID, "VERDICT fail. password=Winter2026! was here"); err != nil {
		t.Fatal(err)
	}
	if got := learningText(t, f, verdictID); strings.Contains(got, "Winter2026") || got != "VERDICT pass. The rotation held after the second try" {
		t.Fatalf("served a stale excerpt %q", got)
	}
	// A source edited to hold a credential disappears, and cannot be accepted.
	incidentID := commentLearningID(host, cleanIncident)
	reviseComment(t, f, host, cleanIncident, "The incident: the key is "+fakeGitHubToken(), false)
	if hasLearning(listLearningsHTTP(t, f, f.a, f.project).Items, incidentID) {
		t.Fatal("a source that now holds a key is still listed")
	}
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "incidents", "title": "Incidents", "body": "Notes\n"})
	w := call(t, f, f.a, "POST", "/api/knowledge/learnings/"+incidentID+"/accept", map[string]any{"knowledge_id": book.ID})
	expect(t, w, 404)
	if decisionOf(t, f, incidentID) != "" {
		t.Fatal("accepted a source that holds a key")
	}
	// A person's own tag on a comment that holds a key is refused on accept.
	manual := addComment(t, f, host, "#process-learning never paste "+fakeJWT())
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+commentLearningID(host, manual)+"/accept", map[string]any{"knowledge_id": book.ID})
	expect(t, w, 409)
	if code(t, w) != "learning_sensitive" {
		t.Fatalf("manual %s", w.Body.String())
	}
}

func TestTaggerVerdictSourceChanged(t *testing.T) {
	f := setup(t)
	installOutcomeTable(t, f)
	ticket := addNode(t, f, "REV-2", "ticket", "Review the backups", &f.project)
	outcome := insertOutcome(t, f, f.project, ticket, `{"verdict":"pass","summary":"Backups restore in minutes"}`, "")
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	var outcomeID, project string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT outcome_id, project_id::text FROM method_learning_nominations WHERE tenant_id=$1 AND source_key=$2`,
		f.a.TenantID, nodeLearningID(ticket)).Scan(&outcomeID, &project); err != nil {
		t.Fatal(err)
	}
	if outcomeID != outcome || project != f.project {
		t.Fatalf("nomination identity %q %q", outcomeID, project)
	}
	if got := learningText(t, f, nodeLearningID(ticket)); got != "Backups restore in minutes" {
		t.Fatalf("verdict %q", got)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE outcome_events SET payload='{"verdict":"pass","summary":"Backups restore in seconds"}' WHERE id::text=$1`, outcome); err != nil {
		t.Fatal(err)
	}
	if got := learningText(t, f, nodeLearningID(ticket)); got != "Backups restore in seconds" {
		t.Fatalf("edited verdict %q", got)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE outcome_events SET payload=jsonb_build_object('verdict','pass','summary','token='||$2::text) WHERE id::text=$1`, outcome, fakeGitHubToken()); err != nil {
		t.Fatal(err)
	}
	if got := learningText(t, f, nodeLearningID(ticket)); strings.Contains(got, "Backups") || strings.Contains(got, "token=") {
		t.Fatalf("verdict now holding a key served %q", got)
	}

	leaky := addNode(t, f, "REV-3", "ticket", "Review the vault", &f.project)
	insertOutcome(t, f, f.project, leaky, `{"verdict":"fail","summary":"the header was Bearer `+strings.Repeat("Zm9vYmFy", 3)+`"}`, "")
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := nominationOfKey(t, f, nodeLearningID(leaky)); ok || nodeTagged(t, f, leaky) {
		t.Fatal("verdict with a bearer token was nominated")
	}
}

// Finding 2: the tag is merged into the ticket's fields as they are when it
// is written, not as they were when the tagger scanned.
func TestTaggerKeepsConcurrentFieldEdits(t *testing.T) {
	f := setup(t)
	ticket := addNode(t, f, "DONE-8", "ticket", "Keep the priority", &f.project)
	setFields(t, f, ticket, map[string]any{"priority": "low", "tags": []any{"ops"}})
	closeAged(t, f, ticket, "done", "1 hour")
	var edited time.Time
	taggerBeforeStamp = func(nodeID string) {
		if nodeID != ticket {
			return
		}
		// Another writer commits between the scan and the tag.
		if err := f.db.Admin.QueryRow(t.Context(), `UPDATE nodes SET fields=fields || '{"priority":"high","assignee":"mira"}'::jsonb,
			updated_at=clock_timestamp() WHERE tenant_id=$1 AND id=$2::uuid RETURNING updated_at`, f.a.TenantID, ticket).Scan(&edited); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { taggerBeforeStamp = nil })
	n, err := TagOnce(t.Context(), f.db.App)
	if err != nil || n != 1 {
		t.Fatalf("tagged %d %v", n, err)
	}
	var raw []byte
	var updated time.Time
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT fields, updated_at FROM nodes WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, ticket).Scan(&raw, &updated); err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Priority string `json:"priority"`
		Assignee string `json:"assignee"`
		Tags     []any  `json:"tags"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields.Priority != "high" || fields.Assignee != "mira" || len(fields.Tags) != 2 || fields.Tags[0] != "ops" || fields.Tags[1] != processLearningTag {
		t.Fatalf("fields after tagging %s", raw)
	}
	if edited.IsZero() || !updated.After(edited) {
		t.Fatalf("updated_at %v did not move past the concurrent edit %v", updated, edited)
	}
}

// Finding 3: a review verdict keeps its own id and project. After the ticket
// moves from A to B, people who see only B do not get A's verdict.
func TestTaggerVerdictStaysInItsProject(t *testing.T) {
	f := setup(t)
	installOutcomeTable(t, f)
	ticket := addNode(t, f, "REV-4", "ticket", "Review the migration", &f.project)
	insertOutcome(t, f, f.project, ticket, `{"verdict":"fail","summary":"Project A internal verdict text"}`, "")
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	onlyA := projectPerson(t, f, "Ada A")
	bindProjectRole(t, f, onlyA.ID, "member", f.project)
	onlyB := projectPerson(t, f, "Ben B")
	bindProjectRole(t, f, onlyB.ID, "admin", f.other)
	id := nodeLearningID(ticket)
	if got := itemText(listLearningsHTTP(t, f, onlyA, f.project).Items, id); got != "Project A internal verdict text" {
		t.Fatalf("A before the move %q", got)
	}

	moveNode(t, f, ticket, f.other)
	for _, item := range listLearningsHTTP(t, f, onlyB, f.other).Items {
		if strings.Contains(item.Text, "Project A internal") {
			t.Fatalf("B-only viewer saw A's verdict: %+v", item)
		}
	}
	if got := itemText(listLearningsHTTP(t, f, f.a, f.other).Items, id); got != "Review the migration" {
		t.Fatalf("B inbox shows %q, want the ticket's own title", got)
	}
	if hasLearning(listLearningsHTTP(t, f, onlyA, f.project).Items, id) {
		t.Fatal("A-only viewer still sees a ticket that moved away")
	}
	entry := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "glint-notes", "title": "Glint notes", "body": "Notes\n", "project_id": f.other})
	w := callScoped(t, f, onlyB, authz.Scope{ProjectID: f.other}, "POST", "/api/knowledge/learnings/"+id+"/accept", map[string]any{"knowledge_id": entry.ID})
	expect(t, w, 200)
	decision := decode[LearningDecision](t, w)
	if strings.Contains(decision.Line, "Project A internal") || !strings.Contains(decision.Line, "Review the migration") {
		t.Fatalf("B accepted line %q", decision.Line)
	}

	// A verdict recorded in A for a ticket already in B is not nominated.
	late := addNode(t, f, "REV-5", "ticket", "Already moved", &f.other)
	insertOutcome(t, f, f.project, late, `{"verdict":"pass","summary":"Late A verdict"}`, "")
	if _, err := TagOnce(t.Context(), f.db.App); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := nominationOfKey(t, f, nodeLearningID(late)); ok || nodeTagged(t, f, late) {
		t.Fatal("A verdict nominated a ticket that lives in B")
	}
}

// Finding 4: many rows at one timestamp do not stall the scan.
func TestTaggerCursorSameTimestamp(t *testing.T) {
	f := setup(t)
	installOutcomeTable(t, f)
	previous := taggerBatch
	taggerBatch = 2
	t.Cleanup(func() { taggerBatch = previous })
	var at time.Time
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT date_trunc('second', clock_timestamp() - interval '1 hour')`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	host := addNode(t, f, "HOST-1", "ticket", "Comment host", &f.project)
	var closed, reviewed, comments []string
	for i := range 5 {
		c := addNode(t, f, "SAME-"+strconv.Itoa(i+1), "ticket", "Closed together "+strconv.Itoa(i), &f.project)
		if _, err := f.db.Admin.Exec(t.Context(), `UPDATE nodes SET state='done', updated_at=$3 WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, c, at); err != nil {
			t.Fatal(err)
		}
		closed = append(closed, c)
		r := addNode(t, f, "VER-"+strconv.Itoa(i+1), "ticket", "Reviewed together "+strconv.Itoa(i), &f.project)
		insertOutcome(t, f, f.project, r, `{"verdict":"pass","summary":"Held `+strconv.Itoa(i)+`"}`, at.Format(time.RFC3339Nano))
		reviewed = append(reviewed, r)
		var id int64
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after, at)
			VALUES ($1, $2::uuid, $3::uuid, 'comment.created', jsonb_build_object('body_markdown', 'The incident number '||$4::text), $5)
			RETURNING id`, f.a.TenantID, f.a.ID, host, strconv.Itoa(i), at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		comments = append(comments, commentLearningID(host, strconv.FormatInt(id, 10)))
	}
	for pass := 0; pass < 4; pass++ {
		if _, err := TagOnce(t.Context(), f.db.App); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 5 {
		for _, key := range []string{nodeLearningID(closed[i]), nodeLearningID(reviewed[i]), comments[i]} {
			if _, _, ok := nominationOfKey(t, f, key); !ok {
				t.Fatalf("row %d %s never nominated: the cursor stalled", i, key)
			}
		}
	}
	var until time.Time
	var after *string
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT verdicts_until, verdicts_after_id FROM method_learning_tag_cursor WHERE tenant_id=$1`, f.a.TenantID).Scan(&until, &after); err != nil {
		t.Fatal(err)
	}
	if after != nil || !until.After(at) {
		t.Fatalf("verdict cursor %v %v did not finish the window", until, after)
	}
}

// Finding 5: decided nominations are dropped before the scan limit.
func TestOpenNominationSurvivesDecidedWindow(t *testing.T) {
	f := setup(t)
	n := learningScanLimit + 1
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `
			WITH closed AS (
			  INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, state)
			  SELECT $1, 'ND-'||g::text, k.id, 'Decided '||g::text, $2::uuid, 'done'
			  FROM generate_series(1, $3) AS g
			  JOIN node_kinds k ON k.tenant_id=$1 AND k.slug='ticket'
			  RETURNING id, title
			), nominated AS (
			  INSERT INTO method_learning_nominations (tenant_id, source_key, project_id, node_id, origin, excerpt, source_hash, nominated_at)
			  SELECT $1, 'n-'||id::text, $2::uuid, id, 'closed_ticket', title, encode(sha256(convert_to(title, 'UTF8')), 'hex'), clock_timestamp()
			  FROM closed
			  RETURNING node_id
			), ev AS (
			  INSERT INTO events (tenant_id, actor_principal_id, node_id, type, after)
			  SELECT $1, $4::uuid, node_id, 'knowledge.learning_dismissed', jsonb_build_object('source_key', 'n-'||node_id::text)
			  FROM nominated
			  RETURNING id, node_id
			)
			INSERT INTO method_learning_decisions (tenant_id, project_id, source_key, decision, decided_by, event_id)
			SELECT $1, $2::uuid, 'n-'||node_id::text, 'dismissed', $4::uuid, id FROM ev`,
			f.a.TenantID, f.project, n, f.a.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			WITH old AS (
			  INSERT INTO nodes (tenant_id, key, kind_id, title, parent_id, state)
			  SELECT $1, 'OLDN-1', k.id, 'The older nomination is still open', $2::uuid, 'done'
			  FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug='ticket'
			  RETURNING id, title
			)
			INSERT INTO method_learning_nominations (tenant_id, source_key, project_id, node_id, origin, excerpt, source_hash, nominated_at)
			SELECT $1, 'n-'||id::text, $2::uuid, id, 'closed_ticket', title, encode(sha256(convert_to(title, 'UTF8')), 'hex'), clock_timestamp() - interval '40 days'
			FROM old`, f.a.TenantID, f.project)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	page := listLearningsHTTP(t, f, f.a, f.project)
	if len(page.Items) != 1 || page.Items[0].Key != "OLDN-1" || page.Items[0].Text != "The older nomination is still open" || page.Truncated {
		t.Fatalf("page %+v truncated %v", page.Items, page.Truncated)
	}
}

// Finding 6: a person who sees only this project can undo their own draft.
func TestLearningDraftProjectOnlyUndo(t *testing.T) {
	f := setup(t)
	h := rulesHandler(f)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "admin")
	layer := rulesLayer(t, h, f.a, rules.Scope{Layer: "project", ProjectID: f.project})
	set := rulesSet(t, h, f.a, layer.ID, "Pharos rules")
	note := addNode(t, f, "LEARN-9", "ticket", "Draft and take it back", &f.project)
	setFields(t, f, note, map[string]any{"tags": []any{"process-learning"}})
	noteID := nodeLearningID(note)

	lead := projectPerson(t, f, "Pia Project")
	bindProjectRole(t, f, lead.ID, "admin", f.project)
	scope := authz.Scope{ProjectID: f.project}
	w := callScoped(t, f, lead, scope, "POST", "/api/knowledge/learnings/"+noteID+"/draft", map[string]any{"layer_id": layer.ID, "set_id": set.ID})
	expect(t, w, 200)
	decision := decode[LearningDecision](t, w)
	if decision.RuleSetID != set.ID || decision.RuleLayerID != layer.ID {
		t.Fatalf("decision %+v", decision)
	}
	var refsRules bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT $3::uuid = ANY(node_refs) OR $4::uuid = ANY(node_refs) FROM events WHERE tenant_id=$1 AND id=$2`,
		f.a.TenantID, decision.EventID, layer.ID, set.ID).Scan(&refsRules); err != nil {
		t.Fatal(err)
	}
	if refsRules {
		t.Fatal("the draft event names rules nodes")
	}
	w = callScoped(t, f, lead, scope, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil)
	expect(t, w, 201)
	if decisionOf(t, f, noteID) != "" {
		t.Fatal("undo left the decision")
	}
	if _, ok := ruleByIdentity(rulesGet(t, h, f.a, set.ID).Rules, "learn.n."+note); ok {
		t.Fatal("undo left the rule")
	}
	if !hasLearning(listLearningsHTTP(t, f, lead, f.project).Items, noteID) {
		t.Fatal("undo did not reopen the learning")
	}
}

func insertOutcome(t *testing.T, f fixture, project, ticket, payload, at string) string {
	t.Helper()
	var id string
	err := db.InTenant(db.AllProjects(t.Context(), "outcome fixture"), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO outcome_events (tenant_id, kind, project_id, ticket_node_id, payload, recorded_at)
			VALUES ($1, 'review_verdict', $2::uuid, $3::uuid, $4::jsonb, coalesce(NULLIF($5, '')::timestamptz, clock_timestamp()))
			RETURNING id::text`, f.a.TenantID, project, ticket, payload, at).Scan(&id)
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func moveNode(t *testing.T, f fixture, id, parent string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), f.db.App, f.a.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE nodes SET parent_id=$3::uuid WHERE tenant_id=$1 AND id=$2::uuid`, f.a.TenantID, id, parent)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func learningText(t *testing.T, f fixture, id string) string {
	t.Helper()
	return itemText(listLearningsHTTP(t, f, f.a, f.project).Items, id)
}

func itemText(items []Learning, id string) string {
	for _, item := range items {
		if item.ID == id {
			return item.Text
		}
	}
	return ""
}
