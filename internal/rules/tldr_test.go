// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
)

const tldrMarker = "EXPLAINED-FOR-PEOPLE"

func explainedRule(id, text string) Rule {
	r := testRule(id, text)
	r.TLDR = &TLDR{EN: tldrMarker + " " + id, DE: "Erklärt " + id}
	return r
}

// The file agents receive is byte-identical with and without explanations,
// and no explanation reaches the merged answer in any field.
func TestMergeNeverServesExplanations(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	floor := lockedRule("safety", "Preserve safety.")
	plain := []Snapshot{
		testSnapshot("floor", Scope{Layer: "company"}, floor),
		testSnapshot("p", Scope{Layer: "project", ProjectID: testProject}, testRule("style", "Use project style."), testRule("tests", "Run the tests.")),
	}
	withFloor := floor
	withFloor.TLDR = &TLDR{EN: tldrMarker + " floor"}
	explainedSet := testSnapshot("p", Scope{Layer: "project", ProjectID: testProject}, explainedRule("style", "Use project style."), explainedRule("tests", "Run the tests."))
	explainedSet.TLDR = &TLDR{EN: tldrMarker + " set", DE: "Satz"}
	explainedSet.SHA256 = SnapshotDigest(explainedSet)
	explained := []Snapshot{testSnapshot("floor", Scope{Layer: "company"}, withFloor), explainedSet}

	a, err := Merge(testContext(), plain, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Merge(testContext(), explained, now)
	if err != nil {
		t.Fatal(err)
	}
	if a.Body != b.Body || a.SHA256 != b.SHA256 || a.ByteSize != b.ByteSize || a.Floor != b.Floor || !reflect.DeepEqual(a.Rules, b.Rules) {
		t.Fatalf("explanations changed the served file:\n%q\n%q", a.Body, b.Body)
	}
	raw, _ := json.Marshal(b)
	if bytes.Contains(raw, []byte(tldrMarker)) || bytes.Contains(raw, []byte(`"tldr"`)) || bytes.Contains(raw, []byte("Erklärt")) {
		t.Fatalf("merged answer carries an explanation: %s", raw)
	}
	stub, err := Stub(b)
	if err != nil || strings.Contains(stub, tldrMarker) {
		t.Fatalf("stub: %v %q", err, stub)
	}
	// A snapshot without explanations keeps the digest it had before AEON-314.
	if snap, _ := json.Marshal(plain[1]); bytes.Contains(snap, []byte("tldr")) {
		t.Fatalf("empty explanations must be omitted: %s", snap)
	}
}

func TestExplanationCheckFollowsTheText(t *testing.T) {
	rule := testRule("style", "Use project style.")
	rule.TLDR = storedTLDR(&TLDR{EN: "Keeps code consistent."}, RuleBasis(rule))
	set := Set{Rules: []Rule{rule}, TLDR: storedTLDR(&TLDR{EN: "Project conventions."}, SetBasis([]Rule{rule}))}
	if p := present(set); p.TLDR.Check || p.Rules[0].TLDR.Check {
		t.Fatalf("fresh explanations need no check: %+v", p)
	}
	set.Rules[0].Enabled = false
	if p := present(set); p.TLDR.Check || p.Rules[0].TLDR.Check {
		t.Fatal("switching a rule off does not change what it says")
	}
	set.Rules[0].Text = "Use the project's own style."
	p := present(set)
	if !p.TLDR.Check || !p.Rules[0].TLDR.Check {
		t.Fatalf("changed text must ask for a check: %+v", p)
	}
	if set.Rules[0].TLDR.Check || set.TLDR.Check {
		t.Fatal("present must not mark the stored set")
	}
	// Stored forms never carry the mark and keep a named basis.
	stored := storedTLDR(p.Rules[0].TLDR, RuleBasis(p.Rules[0]))
	if stored.Check || stored.Basis != rule.TLDR.Basis {
		t.Fatalf("stored: %+v", stored)
	}
}

func TestApplyTLDRsWritesOnlyExplanations(t *testing.T) {
	a, b := testRule("a", "Rule A."), testRule("b", "Rule B.")
	b.TLDR = storedTLDR(&TLDR{EN: "Old B."}, "0000000000000000") // written for other text
	s := Set{ID: "s", Name: "Set", Revision: 3, Rules: []Rule{a, b}}
	if _, _, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 2, Rules: map[string]*TLDRText{"a": {EN: "x"}}}); !isCode(err, "revision_conflict") {
		t.Fatalf("stale revision: %v", err)
	}
	if _, _, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 3, Rules: map[string]*TLDRText{"zzz": {EN: "x"}}}); !isCode(err, "unknown_rule") {
		t.Fatalf("unknown identity: %v", err)
	}
	for _, bad := range []TLDRText{{EN: " "}, {EN: "two\nlines"}, {EN: strings.Repeat("x", MaxTLDRBytes+1)}, {EN: "ok", DE: strings.Repeat("ü", 151)}} {
		if _, _, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 3, Rules: map[string]*TLDRText{"a": &bad}}); !isCode(err, "invalid_tldr") {
			t.Fatalf("invalid %q: %v", bad.EN, err)
		}
	}
	if _, _, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 3}); !isCode(err, "invalid_tldr") {
		t.Fatalf("empty request: %v", err)
	}
	next, changed, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 3, Set: json.RawMessage(`{"en":"  Two rules. ","de":"Zwei Regeln."}`), Rules: map[string]*TLDRText{"a": {EN: "Does A."}, "b": {EN: "Old B."}}})
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if next.TLDR.EN != "Two rules." || next.TLDR.Basis != SetBasis(s.Rules) || next.Rules[0].TLDR.Basis != RuleBasis(a) {
		t.Fatalf("stamping: %+v %+v", next.TLDR, next.Rules[0].TLDR)
	}
	// Rewriting the same words confirms them against the current text.
	if next.Rules[1].TLDR.Basis != RuleBasis(b) {
		t.Fatal("rewriting must confirm the explanation")
	}
	for i := range s.Rules {
		if next.Rules[i].Text != s.Rules[i].Text || next.Rules[i].Why != s.Rules[i].Why || next.Rules[i].Enabled != s.Rules[i].Enabled {
			t.Fatal("only explanations may change")
		}
	}
	if s.Rules[0].TLDR != nil {
		t.Fatal("the input set must not change")
	}
	again, changed, err := ApplyTLDRs(next, TLDRInput{ExpectedRevision: 3, Rules: map[string]*TLDRText{"a": {EN: "Does A."}}})
	if err != nil || changed {
		t.Fatalf("same explanation is no change: %v %v", err, changed)
	}
	cleared, changed, err := ApplyTLDRs(again, TLDRInput{ExpectedRevision: 3, Set: json.RawMessage(`null`), Rules: map[string]*TLDRText{"a": nil}})
	if err != nil || !changed || cleared.TLDR != nil || cleared.Rules[0].TLDR != nil || cleared.Rules[1].TLDR == nil {
		t.Fatalf("clear: %v %+v", err, cleared)
	}
	if _, _, err := ApplyTLDRs(s, TLDRInput{ExpectedRevision: 3, Set: json.RawMessage(`{"en":"x","text":"no"}`)}); !isCode(err, "invalid_tldr") {
		t.Fatalf("unknown set field: %v", err)
	}
}

func isCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

func TestBudgetBoundsAndLayerCaps(t *testing.T) {
	for _, b := range []Budget{{MaxBytes: 1999}, {MaxBytes: 64001}, {MaxBytes: 12000, Layers: LayerBytes{Company: 499}}, {MaxBytes: 12000, Layers: LayerBytes{Agent: 12001}}} {
		if err := b.Validate(); !isCode(err, "invalid_budget") {
			t.Fatalf("%+v: %v", b, err)
		}
	}
	for _, b := range []Budget{DefaultBudget(), {MaxBytes: 2000}, {MaxBytes: 12000, Layers: LayerBytes{Company: 500, Project: 12000}}} {
		if err := b.Validate(); err != nil {
			t.Fatalf("%+v: %v", b, err)
		}
	}
	now := time.Now()
	snaps := []Snapshot{floorSnapshot(), testSnapshot("p", Scope{Layer: "project", ProjectID: testProject}, bulky("proj", 3)...)}
	m, err := Merge(testContext(), snaps, now)
	if err != nil {
		t.Fatal(err)
	}
	projectBytes := 0
	for _, r := range m.Rules {
		if strings.HasPrefix(r.Identity, "proj") {
			projectBytes += len(ruleLine(r))
		}
	}
	_, err = MergeWithin(testContext(), snaps, now, Budget{MaxBytes: 12000, Layers: LayerBytes{Project: projectBytes - 1}})
	var e *Error
	if !errors.As(err, &e) || e.Code != "rules_budget_exceeded" || e.Layer != "project" || e.ActualBytes != projectBytes || e.MaxBytes != projectBytes-1 {
		t.Fatalf("layer cap: %v", err)
	}
	if _, err = MergeWithin(testContext(), snaps, now, Budget{MaxBytes: 12000, Layers: LayerBytes{Project: projectBytes, Company: 500}}); err != nil {
		t.Fatalf("exact cap fits: %v", err)
	}
	_, err = MergeWithin(testContext(), snaps, now, Budget{MaxBytes: m.ByteSize - 1})
	if !errors.As(err, &e) || e.Layer != "" || e.ActualBytes != m.ByteSize || e.MaxBytes != m.ByteSize-1 {
		t.Fatalf("total: %v", err)
	}
	// The default stays 12,000 bytes while upgraded clients support 64,000.
	big := []Snapshot{floorSnapshot(), testSnapshot("p", Scope{Layer: "project", ProjectID: testProject}, bulky("proj", 40)...)}
	if _, err = Merge(testContext(), big, now); !isCode(err, "rules_budget_exceeded") {
		t.Fatalf("default budget: %v", err)
	}
	for size, ok := range map[int]bool{MaxBytes: true, MaxBytes + 1: false} {
		padded := m
		padded.Body = m.Body + strings.Repeat("x", size-len(m.Body)-1) + "\n"
		padded.ByteSize, padded.SHA256 = len(padded.Body), digest([]byte(padded.Body))
		if err = ValidateMerged(padded, testContext(), now); (err == nil) != ok {
			t.Fatalf("client bound at %d bytes: %v", size, err)
		}
	}
}

func TestExplainAttributesEveryServedLine(t *testing.T) {
	now := time.Now()
	company := testSnapshot("c", Scope{Layer: "company"}, lockedRule("safety", "Preserve safety."), explainedRule("shared", "Company wording."))
	project := testSnapshot("p", Scope{Layer: "project", ProjectID: testProject}, explainedRule("shared", "Project wording loses."), explainedRule("style", "Use project style."))
	project.TLDR = &TLDR{EN: "Project conventions.", Basis: "0000000000000000"}
	project.SHA256 = SnapshotDigest(project)
	x, err := Explain(testContext(), []Snapshot{project, company}, now, Budget{MaxBytes: 12000, Layers: LayerBytes{Project: 500}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := Merge(testContext(), []Snapshot{project, company}, now)
	if x.Body != m.Body || x.ByteSize != m.ByteSize || x.Problem != nil || len(x.Rules) != len(m.Rules) {
		t.Fatalf("explained file differs: %+v", x)
	}
	got := map[string]ExplainedRule{}
	sum := len(SessionHeader)
	for _, r := range x.Rules {
		got[r.Identity] = r
		sum += r.Bytes
		if !strings.Contains(x.Body, r.Line+"\n") {
			t.Fatalf("line %q not in the file", r.Line)
		}
	}
	if sum != x.ByteSize || x.Usage.Company+x.Usage.Project != x.ByteSize-len(SessionHeader) {
		t.Fatalf("usage: %+v sum %d size %d", x.Usage, sum, x.ByteSize)
	}
	if got["shared"].SetID != "c" || got["shared"].Layer != "company" || got["shared"].Text != "Company wording." || got["style"].TLDR == nil || got["style"].TLDR.EN != tldrMarker+" style" || got["safety"].TLDR != nil {
		t.Fatalf("attribution: %+v", got)
	}
	if len(x.Sets) != 2 || x.Sets[0].SetID != "c" || x.Sets[1].TLDR == nil || !x.Sets[1].TLDR.Check {
		t.Fatalf("sets in precedence order with check: %+v", x.Sets)
	}
	over, err := Explain(testContext(), []Snapshot{project, company}, now, Budget{MaxBytes: 12000, Layers: LayerBytes{Company: 500 - 1 + 1, Project: 500}})
	if err != nil || over.Problem != nil {
		t.Fatalf("fits: %v %+v", err, over.Problem)
	}
	tight, _ := Explain(testContext(), []Snapshot{project, company}, now, Budget{MaxBytes: 2000, Layers: LayerBytes{Company: 500}})
	if tight.Problem != nil {
		t.Fatalf("small files fit: %+v", tight.Problem)
	}
	noFloor, err := Explain(testContext(), []Snapshot{project}, now, DefaultBudget())
	if err != nil || noFloor.Problem == nil || noFloor.Problem.Code != "floor_missing" || len(noFloor.Rules) != 2 {
		t.Fatalf("missing floor is reported, the file still shown: %v %+v", err, noFloor.Problem)
	}
}

// Explanations live in the draft and in each publication, are written by the
// people and agents who may draft, never reach agents, and survive other edits.
func TestExplanationLifecycleOverHTTP(t *testing.T) {
	w := newBatchWorld(t, "rules-tldr")
	admin := w.principal(tenant.Person, "owner", "admin")
	member := w.principal(tenant.Person, "member", "member")
	company := w.layer(admin, Scope{Layer: "company"})
	safety := w.set(admin, company, "Safety", lockedRule("safety", "Keep the locked company floor."), testRule("git", "Never force-push main."))

	// One request writes the set and rule explanations into the draft.
	var s Set
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+safety.ID+"/tldr", map[string]any{
		"expected_revision": safety.Revision,
		"set":               map[string]string{"en": tldrMarker + " set", "de": "Sicherheit"},
		"rules":             map[string]any{"git": map[string]string{"en": tldrMarker + " git"}},
	}, 200), &s)
	if s.Revision != safety.Revision+1 || s.TLDR == nil || s.TLDR.Check || s.Rules[0].TLDR == nil || s.Rules[0].Identity != "git" {
		t.Fatalf("draft: %+v", s)
	}
	if w.eventsOf("rules.tldr_drafted") != 1 {
		t.Fatal("explanation drafts are recorded as events")
	}
	// A client that does not know explanations keeps the set one on a draft
	// save; changing a rule's text asks for a check.
	rules := s.Rules
	rules[0].Text = "Never force-push shared branches."
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/draft", map[string]any{"expected_revision": s.Revision, "name": s.Name, "rules": rules}, 200), &s)
	if s.TLDR == nil || !s.TLDR.Check || !s.Rules[0].TLDR.Check || s.Rules[1].TLDR != nil {
		t.Fatalf("check after a text change: %+v", s)
	}
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision, "rules": map[string]any{"git": map[string]string{"en": tldrMarker + " git"}}}, 200), &s)
	if s.Rules[0].TLDR.Check {
		t.Fatal("rewriting the explanation confirms it")
	}
	// Stale revisions, unknown rules and people without write access are refused.
	w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision - 1, "rules": map[string]any{"git": map[string]string{"en": "x"}}}, 409)
	w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision, "rules": map[string]any{"nope": map[string]string{"en": "x"}}}, 400)
	w.call(member, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision, "rules": map[string]any{"git": map[string]string{"en": "x"}}}, 403)
	agent := w.agentFor(admin, "drafter")
	agent.KeyCreatorID = admin.ID
	agent.Scopes = []string{"rules.read", "rules.write", "nodes.read"}
	w.call(agent, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision, "rules": map[string]any{"git": map[string]string{"en": "x"}}}, 403)
	// An agent drafts explanations where it may draft: its owner's person rules.
	personal := w.layer(admin, Scope{Layer: "person", OwnerID: admin.ID})
	mine := w.set(admin, personal, "Mine", testRule("tone", "Be brief."))
	var drafted Set
	json.Unmarshal(w.call(agent, "PUT", "/api/rules/sets/"+mine.ID+"/tldr", map[string]any{"expected_revision": mine.Revision, "rules": map[string]any{"tone": map[string]string{"en": tldrMarker + " tone"}}}, 200), &drafted)
	if drafted.Rules[0].TLDR == nil {
		t.Fatal("agent draft")
	}
	w.call(agent, "POST", "/api/rules/sets/"+mine.ID+"/publish", map[string]any{"expected_revision": drafted.Revision, "version": "260929100000.0.0"}, 403)

	// Published with the normal approval, frozen in the snapshot, never served.
	w.call(admin, "POST", "/api/rules/sets/"+s.ID+"/publish", map[string]any{"expected_revision": s.Revision, "version": "260929100001.0.0"}, 200)
	var snap Snapshot
	json.Unmarshal(w.call(admin, "GET", "/api/rules/sets/"+s.ID+"/versions/260929100001.0.0", nil, 200), &snap)
	if snap.TLDR == nil || snap.Rules[0].TLDR == nil || snap.SHA256 != SnapshotDigest(snap) {
		t.Fatalf("snapshot keeps explanations: %+v", snap)
	}
	q := url.Values{"project_id": {w.project}, "person_id": {admin.ID}, "role": {"builder"}, "harness": {"codex"}}
	withRaw := w.call(admin, "GET", "/api/rules/merged?"+q.Encode(), nil, 200)
	if bytes.Contains(withRaw, []byte(tldrMarker)) || bytes.Contains(withRaw, []byte("tldr")) || bytes.Contains(withRaw, []byte("Sicherheit")) {
		t.Fatalf("merged answer carries explanations: %s", withRaw)
	}
	var with Merged
	json.Unmarshal(withRaw, &with)
	// Removing every explanation and publishing again serves identical bytes.
	body := w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/tldr", map[string]any{"expected_revision": s.Revision, "set": nil, "rules": map[string]any{"git": nil}}, 200)
	s = Set{}
	json.Unmarshal(body, &s)
	if s.TLDR != nil || s.Rules[0].TLDR != nil {
		t.Fatalf("cleared: %+v", s)
	}
	w.call(admin, "POST", "/api/rules/sets/"+s.ID+"/publish", map[string]any{"expected_revision": s.Revision, "version": "260929100002.0.0"}, 200)
	var without Merged
	json.Unmarshal(w.call(admin, "GET", "/api/rules/merged?"+q.Encode(), nil, 200), &without)
	if with.Body != without.Body || with.SHA256 != without.SHA256 || with.ByteSize != without.ByteSize || !reflect.DeepEqual(with.Rules, without.Rules) {
		t.Fatalf("served bytes depend on explanations:\n%q\n%q", with.Body, without.Body)
	}
	// Restoring the explained version brings the explanations back into the draft.
	json.Unmarshal(w.call(admin, "POST", "/api/rules/sets/"+s.ID+"/restore", map[string]any{"expected_revision": s.Revision, "version": "260929100001.0.0", "new_version": "260929100003.0.0"}, 200), &snap)
	if snap.TLDR == nil || snap.Rules[0].TLDR == nil {
		t.Fatalf("restore: %+v", snap)
	}

	// People read the file with its explanations next to each exact line.
	var x Explained
	json.Unmarshal(w.call(member, "GET", "/api/rules/explained?"+url.Values{"project_id": {w.project}, "person_id": {member.ID}, "role": {"builder"}, "harness": {"codex"}}.Encode(), nil, 200), &x)
	if x.Budget.MaxBytes != LegacyMaxBytes || x.Problem != nil || len(x.Sets) != 1 || x.Sets[0].TLDR == nil || x.Usage.Company != x.ByteSize-len(SessionHeader) {
		t.Fatalf("explained: %+v", x)
	}
	for _, r := range x.Rules {
		if r.Identity == "git" && (r.TLDR == nil || r.TLDR.EN != tldrMarker+" git") {
			t.Fatalf("rule explanation: %+v", r)
		}
	}
	w.call(member, "GET", "/api/rules/explained?"+q.Encode(), nil, 403)
}

func (w *batchWorld) eventsOf(kind string) int {
	w.t.Helper()
	var n int
	if err := w.d.Admin.QueryRow(w.t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type=$2`, w.tid, kind).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}

// The workspace budget is a setting for people who manage the workspace; it
// never lets a smaller budget break a file served now, and publication and
// session start use it, per layer too.
func TestWorkspaceBudgetSetting(t *testing.T) {
	w := newBatchWorld(t, "rules-budget-setting")
	admin := w.principal(tenant.Person, "owner", "admin")
	member := w.principal(tenant.Person, "member", "member")
	company := w.floor(admin)
	var view BudgetView
	json.Unmarshal(w.call(member, "GET", "/api/rules/budget", nil, 200), &view)
	if view.MaxBytes != LegacyMaxBytes || view.DefaultBytes != LegacyMaxBytes || view.MinBytes != MinBudgetBytes || view.CeilingBytes != LegacyMaxBytes || view.Layers != (LayerBytes{}) {
		t.Fatalf("default: %+v", view)
	}
	w.call(member, "PUT", "/api/rules/budget", Budget{MaxBytes: 8000}, 403)
	agent := w.agentFor(admin, "setter")
	agent.KeyCreatorID = admin.ID
	agent.Scopes = []string{"rules.read", "settings.manage"}
	w.call(agent, "PUT", "/api/rules/budget", Budget{MaxBytes: 8000}, 403)
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 1000}, 400)
	// The product ceiling remains bounded even after compatible clients report.
	w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: MaxBytes + 1}, 400)
	w.call(admin, "PUT", "/api/rules/budget", map[string]any{"max_bytes": 12000, "layer_max_bytes": map[string]int{"robots": 600}}, 400)

	// A smaller budget refuses a file the default would serve.
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	mid := w.set(admin, project, "Mid", bulky("mid", 12)...)
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 5000}, 200), &view)
	if view.MaxBytes != 5000 || view.CeilingBytes != LegacyMaxBytes {
		t.Fatalf("saved: %+v", view)
	}
	body := w.call(admin, "POST", "/api/rules/publish", batch("", item(mid, "auto")), 422)
	var e Error
	if json.Unmarshal(body, &e) != nil || e.Code != "rules_budget_exceeded" || e.MaxBytes != 5000 || e.ActualBytes <= 5000 || w.published(mid.ID) != "" {
		t.Fatalf("smaller budget: %s", body)
	}
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 12000, Layers: LayerBytes{Project: 8000}}, 200), &view)
	if view.MaxBytes != 12000 || view.Layers.Project != 8000 {
		t.Fatalf("saved: %+v", view)
	}
	w.call(admin, "POST", "/api/rules/publish", batch("", item(mid, "auto")), 200)
	q := url.Values{"project_id": {w.project}, "person_id": {admin.ID}, "role": {"builder"}, "harness": {"codex"}}
	var m Merged
	json.Unmarshal(w.call(admin, "GET", "/api/rules/merged?"+q.Encode(), nil, 200), &m)
	if m.ByteSize <= 5000 || m.ByteSize > MaxBytes {
		t.Fatalf("served file: %d", m.ByteSize)
	}
	// Lowering the budget below a served file is refused, total or per layer.
	body = w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 5000}, 422)
	if !strings.Contains(string(body), "Published rules would not fit") {
		t.Fatalf("refusal: %s", body)
	}
	body = w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 12000, Layers: LayerBytes{Project: 4000}}, 422)
	if json.Unmarshal(body, &e) != nil || e.Layer != "project" || e.MaxBytes != 4000 {
		t.Fatalf("layer refusal: %s", body)
	}
	// A layer cap refuses a publication that would cross it, and says which.
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: 12000, Layers: LayerBytes{Project: 8000, Company: 1000}}, 200), &view)
	grow := w.set(admin, company, "Grow", bulky("grow", 3)...)
	body = w.call(admin, "POST", "/api/rules/publish", batch("", item(grow, "auto")), 422)
	if json.Unmarshal(body, &e) != nil || e.Code != "rules_budget_exceeded" || e.Layer != "company" || e.MaxBytes != 1000 {
		t.Fatalf("company cap: %s", body)
	}
	var x Explained
	json.Unmarshal(w.call(admin, "GET", "/api/rules/explained?"+q.Encode(), nil, 200), &x)
	if x.Budget != (Budget{MaxBytes: 12000, Layers: LayerBytes{Project: 8000, Company: 1000}}) || x.Usage.Project == 0 || x.Problem != nil {
		t.Fatalf("explained budget: %+v", x)
	}
	if w.settingsEvents() != 3 {
		t.Fatalf("budget changes are events: %d", w.settingsEvents())
	}
}

// Single-set publication and restoration run the batch's budget check (total
// and per-layer caps) before commit: over the cap they fail and write nothing.
func TestSingleSetPublishAndRestoreKeepTheBudget(t *testing.T) {
	w := newBatchWorld(t, "rules-budget-single")
	admin := w.principal(tenant.Person, "owner", "admin")
	w.floor(admin)
	project := w.layer(admin, Scope{Layer: "project", ProjectID: w.project})
	big := w.set(admin, project, "Project", bulky("big", 2)...)
	w.publish(admin, big, "260929110000.0.0")
	get := func() Set {
		var s Set
		json.Unmarshal(w.call(admin, "GET", "/api/rules/sets/"+big.ID, nil, 200), &s)
		return s
	}
	s := get()
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+s.ID+"/draft", draftInput{ExpectedRevision: s.Revision, Name: s.Name, Rules: []Rule{testRule("tone", "Be brief.")}}, 200), &s)
	w.publish(admin, s, "260929110001.0.0")
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/budget", Budget{MaxBytes: LegacyMaxBytes, Layers: LayerBytes{Project: 500}}, 200), new(BudgetView))
	versions := func() int {
		var list struct{ Versions []Snapshot }
		json.Unmarshal(w.call(admin, "GET", "/api/rules/sets/"+big.ID+"/versions", nil, 200), &list)
		return len(list.Versions)
	}
	refused := func(label string, body []byte, before Set) {
		t.Helper()
		var e Error
		if json.Unmarshal(body, &e) != nil || e.Code != "rules_budget_exceeded" || e.Layer != "project" || e.MaxBytes != 500 || e.ActualBytes <= 500 {
			t.Fatalf("%s: %s", label, body)
		}
		after := get()
		if after.PublishedVersion != "260929110001.0.0" || after.Revision != before.Revision || versions() != 2 {
			t.Fatalf("%s committed: %+v, %d versions", label, after, versions())
		}
	}
	// Restoring the big version would cross the project cap.
	before := get()
	refused("restore", w.call(admin, "POST", "/api/rules/sets/"+big.ID+"/restore", map[string]any{"expected_revision": before.Revision, "version": "260929110000.0.0", "new_version": "260929110002.0.0"}, 422), before)
	// So would publishing a big draft.
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+big.ID+"/draft", draftInput{ExpectedRevision: before.Revision, Name: before.Name, Rules: bulky("big", 2)}, 200), &s)
	before = get()
	refused("publish", w.call(admin, "POST", "/api/rules/sets/"+big.ID+"/publish", map[string]any{"expected_revision": before.Revision, "version": "260929110003.0.0"}, 422), before)
	// Within the cap both still work.
	json.Unmarshal(w.call(admin, "PUT", "/api/rules/sets/"+big.ID+"/draft", draftInput{ExpectedRevision: before.Revision, Name: before.Name, Rules: []Rule{testRule("tone", "Be briefer.")}}, 200), &s)
	w.publish(admin, s, "260929110004.0.0")
	s = get()
	w.call(admin, "POST", "/api/rules/sets/"+big.ID+"/restore", map[string]any{"expected_revision": s.Revision, "version": "260929110001.0.0", "new_version": "260929110005.0.0"}, 200)
}

func (w *batchWorld) settingsEvents() int {
	w.t.Helper()
	var n int
	if err := w.d.Admin.QueryRow(w.t.Context(), `SELECT count(*) FROM events WHERE tenant_id=$1 AND type='settings.rules_budget_changed'`, w.tid).Scan(&n); err != nil {
		w.t.Fatal(err)
	}
	return n
}
