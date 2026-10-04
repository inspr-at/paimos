// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/rules"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestLearningDraftTenantBeforeAdvisory(t *testing.T) {
	for _, undo := range []bool{false, true} {
		name := "draft"
		if undo {
			name = "undo"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			h := rulesHandler(f)
			setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
			publicID := nodeLearningID(f.ticket)
			layer := rulesLayer(t, h, f.a, rules.Scope{Layer: "person", OwnerID: f.a.ID})
			set := rulesSet(t, h, f.a, layer.ID, "Learning draft")
			body, err := json.Marshal(map[string]any{"layer_id": layer.ID, "set_id": set.ID})
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/knowledge/learnings/" + publicID + "/draft"
			wantStatus, wantDecision, wantRules := http.StatusOK, "drafted", 1
			if undo {
				w := call(t, f, f.a, "POST", path, string(body))
				expect(t, w, http.StatusOK)
				decision := decode[LearningDecision](t, w)
				path = "/api/events/" + strconv.FormatInt(decision.EventID, 10) + "/undo"
				body = nil
				wantStatus, wantDecision, wantRules = http.StatusCreated, "", 0
			}
			dbtest.TenantBeforeAdvisory(t, f.db, f.a.TenantID, f.a.TenantID+":learning:"+publicID, 275, func(ctx context.Context) error {
				r := httptest.NewRequestWithContext(tenant.WithPrincipal(ctx, f.a), "POST", path, strings.NewReader(string(body)))
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				f.handler.ServeHTTP(w, r)
				if w.Code != wantStatus {
					return fmt.Errorf("%s: status %d want %d: %s", path, w.Code, wantStatus, w.Body.String())
				}
				return nil
			})
			if got := decisionOf(t, f, publicID); got != wantDecision {
				t.Fatalf("decision %q want %q", got, wantDecision)
			}
			if got := rulesGet(t, h, f.a, set.ID); len(got.Rules) != wantRules || got.PublishedVersion != "" {
				t.Fatalf("draft rules=%d want %d; published=%q", len(got.Rules), wantRules, got.PublishedVersion)
			}
		})
	}
}

func TestLearningRuleDraft(t *testing.T) {
	f := setup(t)
	h := rulesHandler(f)
	setFields(t, f, f.ticket, map[string]any{"tags": []any{"process-learning"}})
	book := createEntry(t, f, f.a, map[string]any{"type": "runbook", "slug": "flywheel", "title": "Flywheel", "body": "Hello\n"})
	w := call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nodeLearningID(f.ticket)+"/accept", map[string]any{"knowledge_id": book.ID})
	expect(t, w, 200)
	if n := adminCount(t, f, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published'`); n != 0 {
		t.Fatalf("changelog accept published %d rules", n)
	}
	if n := adminCount(t, f, `SELECT count(*) FROM nodes WHERE tenant_id=$1 AND rule_resource IS NOT NULL`); n != 0 {
		t.Fatalf("changelog accept wrote %d rule nodes", n)
	}

	note := addNode(t, f, "LEARN-3", "work", "Ship the note once", &f.project)
	setFields(t, f, note, map[string]any{"tags": []any{"process-learning"}})
	noteID := nodeLearningID(note)
	layer := rulesLayer(t, h, f.b, rules.Scope{Layer: "person", OwnerID: f.b.ID})
	set := rulesSet(t, h, f.b, layer.ID, "Personal")
	kept := map[string]any{
		"identity": "keep.me", "text": "Keep this rule.", "why": "It was already here.",
		"strength": "normal", "enabled": true, "source": map[string]any{"reference": "test:keep", "edited_here": false},
	}
	rulesCall(t, h, f.b, "PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{"expected_revision": set.Revision, "name": set.Name, "rules": []any{kept}}, 200)

	w = call(t, f, f.b, "POST", "/api/knowledge/learnings/"+noteID+"/draft", map[string]any{"layer_id": layer.ID, "set_id": set.ID})
	expect(t, w, 200)
	decision := decode[LearningDecision](t, w)
	if decision.Decision != "drafted" || decision.RuleSetID != set.ID || decision.RuleLayerID != layer.ID || decision.RuleIdentity != "learn.n."+note || decision.EventID == 0 {
		t.Fatalf("decision %+v", decision)
	}
	if eventType(t, f, decision.EventID) != evLearningDrafted {
		t.Fatalf("event %s", eventType(t, f, decision.EventID))
	}
	if adminCount(t, f, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published'`) != 0 {
		t.Fatal("draft published a rule")
	}
	drafted := rulesGet(t, h, f.b, set.ID)
	if drafted.PublishedVersion != "" {
		t.Fatalf("published %q", drafted.PublishedVersion)
	}
	learningRule, ok := ruleByIdentity(drafted.Rules, "learn.n."+note)
	keptRule, keptOK := ruleByIdentity(drafted.Rules, "keep.me")
	if !ok || !keptOK || keptRule.Text != "Keep this rule." || len(drafted.Rules) != 2 {
		t.Fatalf("rules %+v", drafted.Rules)
	}
	if learningRule.Text != "Ship the note once" || learningRule.Why != "From LEARN-3" || learningRule.Strength != "normal" || !learningRule.Enabled {
		t.Fatalf("rule %+v", learningRule)
	}
	if learningRule.Source.Reference != "LEARN-3" || learningRule.Source.Revision != noteID || learningRule.Source.Identity != note || learningRule.Source.EditedHere {
		t.Fatalf("source %+v", learningRule.Source)
	}
	for _, part := range []string{"Learning " + noteID, "Node " + note, "Key LEARN-3", "Title Ship the note once", "Link /p/PRJ-1/LEARN-3"} {
		if !strings.Contains(learningRule.Details, part) {
			t.Fatalf("details %q missing %q", learningRule.Details, part)
		}
	}
	if hasLearning(listLearningsHTTP(t, f, f.b, f.project).Items, noteID) {
		t.Fatal("drafted learning stayed in the inbox")
	}

	expect(t, call(t, f, f.b, "POST", "/api/events/"+strconv.FormatInt(decision.EventID, 10)+"/undo", nil), 201)
	if decisionOf(t, f, noteID) != "" {
		t.Fatal("undo left the decision")
	}
	reopened := rulesGet(t, h, f.b, set.ID)
	if _, ok := ruleByIdentity(reopened.Rules, "learn.n."+note); ok || len(reopened.Rules) != 1 || reopened.Rules[0].Identity != "keep.me" {
		t.Fatalf("undo rules %+v", reopened.Rules)
	}
	if reopened.PublishedVersion != "" {
		t.Fatal("undo published")
	}
	if !hasLearning(listLearningsHTTP(t, f, f.b, f.project).Items, noteID) {
		t.Fatal("undo did not reopen the learning")
	}

	w = call(t, f, f.b, "POST", "/api/knowledge/learnings/"+noteID+"/draft", map[string]any{"layer_id": layer.ID, "set_id": set.ID})
	expect(t, w, 200)
	again := decode[LearningDecision](t, w)
	edited := rulesGet(t, h, f.b, set.ID)
	changed, ok := ruleByIdentity(edited.Rules, "learn.n."+note)
	if !ok {
		t.Fatal("second draft missing")
	}
	changed.Text = "Someone rewrote the rule."
	rulesCall(t, h, f.b, "PUT", "/api/rules/sets/"+set.ID+"/draft", map[string]any{"expected_revision": edited.Revision, "name": edited.Name, "rules": []any{kept, changed}}, 200)
	expect(t, call(t, f, f.b, "POST", "/api/events/"+strconv.FormatInt(again.EventID, 10)+"/undo", nil), 409)
	if decisionOf(t, f, noteID) != "drafted" {
		t.Fatalf("edited undo cleared %q", decisionOf(t, f, noteID))
	}
	if hasLearning(listLearningsHTTP(t, f, f.b, f.project).Items, noteID) {
		t.Fatal("failed undo reopened the learning")
	}

	gate := addNode(t, f, "LEARN-4", "work", "Gate the draft", &f.project)
	setFields(t, f, gate, map[string]any{"tags": []any{"process-learning"}})
	gateID := nodeLearningID(gate)
	dbtest.BindRole(t, f.db, f.a.TenantID, f.a.ID, "admin")
	companyLayer := rulesLayer(t, h, f.a, rules.Scope{Layer: "company"})
	company := rulesSet(t, h, f.a, companyLayer.ID, "Company floor")
	projectLayer := rulesLayer(t, h, f.a, rules.Scope{Layer: "project", ProjectID: f.project})
	projectSet := rulesSet(t, h, f.a, projectLayer.ID, "Pharos rules")
	otherLayer := rulesLayer(t, h, f.a, rules.Scope{Layer: "project", ProjectID: f.other})
	otherSet := rulesSet(t, h, f.a, otherLayer.ID, "Glint rules")

	agent := f.a
	agent.Kind = tenant.Agent
	w = call(t, f, agent, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": projectLayer.ID, "set_id": projectSet.ID})
	expect(t, w, 403)
	if code(t, w) != "person_required" || decisionOf(t, f, gateID) != "" {
		t.Fatalf("agent %s decision %q", w.Body.String(), decisionOf(t, f, gateID))
	}
	w = call(t, f, f.viewer, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": projectLayer.ID, "set_id": projectSet.ID})
	expect(t, w, 403)
	if code(t, w) != "forbidden" {
		t.Fatalf("viewer %s", w.Body.String())
	}
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": strings.ToUpper(projectLayer.ID), "set_id": projectSet.ID})
	expect(t, w, 400)
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": projectLayer.ID, "set_id": projectSet.ID, "note": "no"})
	expect(t, w, 400)
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": companyLayer.ID, "set_id": projectSet.ID})
	expect(t, w, 404)
	if code(t, w) != "rule_unavailable" {
		t.Fatalf("wrong layer %s", w.Body.String())
	}
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": otherLayer.ID, "set_id": otherSet.ID})
	expect(t, w, 404)
	if code(t, w) != "rule_unavailable" {
		t.Fatalf("other project %s", w.Body.String())
	}
	w = call(t, f, f.b, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": companyLayer.ID, "set_id": company.ID})
	expect(t, w, 403)
	if code(t, w) != "rule_forbidden" || decisionOf(t, f, gateID) != "" {
		t.Fatalf("member company %s", w.Body.String())
	}

	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": projectLayer.ID, "set_id": projectSet.ID})
	expect(t, w, 200)
	projectDraft := rulesGet(t, h, f.a, projectSet.ID)
	if projectDraft.PublishedVersion != "" || len(projectDraft.Rules) != 1 || projectDraft.Rules[0].Identity != "learn.n."+gate {
		t.Fatalf("project draft %+v", projectDraft)
	}
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+gateID+"/draft", map[string]any{"layer_id": projectLayer.ID, "set_id": projectSet.ID})
	expect(t, w, 409)
	if code(t, w) != "already_decided" {
		t.Fatalf("second %s", w.Body.String())
	}

	floor := addNode(t, f, "LEARN-5", "work", "Keep the floor", &f.project)
	setFields(t, f, floor, map[string]any{"tags": []any{"process-learning"}})
	w = call(t, f, f.a, "POST", "/api/knowledge/learnings/"+nodeLearningID(floor)+"/draft", map[string]any{"layer_id": companyLayer.ID, "set_id": company.ID})
	expect(t, w, 200)
	companyDraft := rulesGet(t, h, f.a, company.ID)
	if companyDraft.PublishedVersion != "" || len(companyDraft.Rules) != 1 {
		t.Fatalf("company draft %+v", companyDraft)
	}
	if adminCount(t, f, `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='rules.published'`) != 0 {
		t.Fatal("admin draft published")
	}
}

func rulesHandler(f fixture) http.Handler {
	mux := http.NewServeMux()
	rules.New(f.db.App).Mount(mux)
	return mux
}

func rulesCall(t *testing.T, h http.Handler, p tenant.Principal, method, path string, body any, want int) []byte {
	t.Helper()
	raw := ""
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		raw = string(encoded)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(raw))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(tenant.WithPrincipal(req.Context(), p))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != want {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.Bytes()
}

func rulesLayer(t *testing.T, h http.Handler, p tenant.Principal, scope rules.Scope) rules.Layer {
	t.Helper()
	var layer rules.Layer
	if err := json.Unmarshal(rulesCall(t, h, p, "POST", "/api/rules/layers", scope, 200), &layer); err != nil {
		t.Fatal(err)
	}
	return layer
}

func rulesSet(t *testing.T, h http.Handler, p tenant.Principal, layerID, name string) rules.Set {
	t.Helper()
	var set rules.Set
	if err := json.Unmarshal(rulesCall(t, h, p, "POST", "/api/rules/sets", map[string]any{"layer_id": layerID, "name": name}, 200), &set); err != nil {
		t.Fatal(err)
	}
	return set
}

func rulesGet(t *testing.T, h http.Handler, p tenant.Principal, id string) rules.Set {
	t.Helper()
	var set rules.Set
	if err := json.Unmarshal(rulesCall(t, h, p, "GET", "/api/rules/sets/"+id, nil, 200), &set); err != nil {
		t.Fatal(err)
	}
	return set
}

func ruleByIdentity(ruleset []rules.Rule, identity string) (rules.Rule, bool) {
	for _, rule := range ruleset {
		if rule.Identity == identity {
			return rule, true
		}
	}
	return rules.Rule{}, false
}

func adminCount(t *testing.T, f fixture, query string) int {
	t.Helper()
	var n int
	if err := f.db.Admin.QueryRow(t.Context(), query, f.a.TenantID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
