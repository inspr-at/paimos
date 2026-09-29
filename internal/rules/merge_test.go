// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testTenant = "10000000-0000-4000-8000-000000000001"
const testProject = "10000000-0000-4000-8000-000000000002"
const testPerson = "10000000-0000-4000-8000-000000000003"
const testAgent = "10000000-0000-4000-8000-000000000004"

func testContext() Context {
	return Context{TenantID: testTenant, ProjectID: testProject, PersonID: testPerson, AgentID: testAgent, Role: "builder", Harness: "codex"}
}
func testRule(id, text string) Rule {
	return Rule{Identity: id, Text: text, Why: "Synthetic test reason.", Strength: "normal", Enabled: true, Source: Source{Reference: "test:ADR004"}}
}
func testSnapshot(id string, s Scope, rs ...Rule) Snapshot {
	v := Snapshot{SetID: id, Scope: s, Name: "test", Revision: 1, Version: "260928100000.0.0", Rules: rs}
	v.SHA256 = SnapshotDigest(v)
	return v
}
func floorSnapshot() Snapshot {
	r := testRule("safety", "Preserve safety.")
	r.Strength = "locked"
	return testSnapshot("floor", Scope{Layer: "company"}, r)
}
func TestMergeIdentityPrecedenceExpiryAndDigest(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	expires := now.Add(time.Minute)
	disabled := testRule("preference", "Temporary off.")
	disabled.Enabled = false
	disabled.ExpiresAt = &expires
	higher := testSnapshot("company", Scope{Layer: "company"}, disabled)
	p := testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, testRule("preference", "Project preference."), testRule("safety", "Try weakening safety."))
	personal := testSnapshot("person", Scope{Layer: "person", OwnerID: testPerson}, testRule("preference", "Person preference."))
	first, err := Merge(testContext(), []Snapshot{personal, p, higher, floorSnapshot()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Body, "preference") || strings.Contains(first.Body, "weakening") || !strings.Contains(first.Body, "Preserve safety") || first.ValidUntil == nil {
		t.Fatalf("merge: %+v", first)
	}
	second, err := Merge(testContext(), []Snapshot{floorSnapshot(), higher, p, personal}, now)
	if err != nil || first.SHA256 != second.SHA256 || first.Body != second.Body {
		t.Fatal("unstable merge", err)
	}
	after, err := Merge(testContext(), []Snapshot{floorSnapshot(), higher, p, personal}, expires)
	if err != nil || !strings.Contains(after.Body, "Project preference") || strings.Contains(after.Body, "Person preference") {
		t.Fatal("expired weakening persisted", err, after.Body)
	}
	if first.SHA256 != digest([]byte(first.Body)) {
		t.Fatal("hash is not exact output")
	}
}
func TestMergeAmbiguityIsolationAndSelectors(t *testing.T) {
	c := testContext()
	now := time.Now()
	a := testSnapshot("a", Scope{Layer: "project", ProjectID: c.ProjectID}, testRule("shared", "A"))
	b := testSnapshot("b", a.Scope, testRule("shared", "B"))
	if _, err := Merge(c, []Snapshot{floorSnapshot(), a, b}, now); err == nil {
		t.Fatal("ambiguous identity accepted")
	}
	b.Scope.ProjectID = "20000000-0000-4000-8000-000000000002"
	b.SHA256 = SnapshotDigest(b)
	other := testRule("reviewer", "Review only")
	other.Roles = []string{"reviewer"}
	a.Rules = append(a.Rules, other)
	a.SHA256 = SnapshotDigest(a)
	m, err := Merge(c, []Snapshot{floorSnapshot(), a, b}, now)
	if err != nil || strings.Contains(m.Body, "Review only") || strings.Contains(m.Body, "] B") {
		t.Fatal("scope leak", err, m.Body)
	}
	a.Rules[0].Text = "tampered"
	if _, err = Merge(c, []Snapshot{floorSnapshot(), a}, now); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
	c.Role = "unknown"
	if _, err = Merge(c, []Snapshot{floorSnapshot()}, now); err == nil {
		t.Fatal("unknown role accepted")
	}
}
func TestMergeUTF8BudgetAndOnDemandDetails(t *testing.T) {
	rs := []Rule{}
	for i := 0; i < 25; i++ {
		r := testRule(fmt.Sprintf("r-%02d", i), strings.Repeat("界", 170))
		r.Details = strings.Repeat("detail", 2000)
		rs = append(rs, r)
	}
	s := testSnapshot("big", Scope{Layer: "project", ProjectID: testProject}, rs...)
	if _, err := Merge(testContext(), []Snapshot{floorSnapshot(), s}, time.Now()); err == nil {
		t.Fatal("unicode byte overflow accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != "rules_budget_exceeded" || e.ActualBytes <= LegacyMaxBytes {
		t.Fatal(err)
	}
	s.Rules = s.Rules[:1]
	s.SHA256 = SnapshotDigest(s)
	m, err := Merge(testContext(), []Snapshot{floorSnapshot(), s}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Body, "detail") || m.Rules[0].Details != "" || len(m.Body) != m.ByteSize {
		t.Fatal("details or byte count")
	}
}
func TestRuleValidation(t *testing.T) {
	var missing Rule
	if json.Unmarshal([]byte(`{"identity":"off","text":"test","why":"reason","strength":"normal","source":{"reference":"fixture"}}`), &missing) == nil {
		t.Fatal("missing enabled silently became a disabling rule")
	}
	for _, text := range []string{"x\ny", "x\ry", "x\u2028y", strings.Repeat("界", 171)} {
		r := testRule("valid", text)
		if ValidateRules([]Rule{r}) == nil {
			t.Fatalf("accepted %q", text)
		}
	}
	r := testRule("valid", "test")
	r.Strength = "locked"
	r.Enabled = false
	if ValidateRules([]Rule{r}) == nil {
		t.Fatal("disabled locked floor accepted")
	}
	for _, s := range []Scope{{Layer: "person"}, {Layer: "company", OwnerID: testPerson}, {Layer: "agent", AgentID: testAgent}, {Layer: "project", ProjectID: testProject, Role: "builder"}} {
		if ValidateScope(s) == nil {
			t.Fatalf("accepted scope %+v", s)
		}
	}
}

func TestMergeLowerLayersAddOrTightenOnly(t *testing.T) {
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	relax := testRule("safety", "Skip the safety floor.")
	relax.Enabled = false
	tighten := testRule("safety.review", "Review every safety exception.")
	project := testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, relax, tighten)
	silence := testRule("safety.review", "Skip the extra review.")
	silence.Enabled = false
	add := testRule("tone", "Be brief.")
	expired := testRule("tone.temp", "Use a temporary aside.")
	expired.ExpiresAt = &past
	person := testSnapshot("person", Scope{Layer: "person", OwnerID: testPerson}, silence, add, expired)
	m, err := Merge(testContext(), []Snapshot{person, project, floorSnapshot()}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Body, "Preserve safety") || strings.Contains(m.Body, "Skip the safety") {
		t.Fatal("lower layer relaxed the locked floor", m.Body)
	}
	if !strings.Contains(m.Body, "Review every safety exception") || strings.Contains(m.Body, "Skip the extra review") {
		t.Fatal("lower layer relaxed a tighter rule", m.Body)
	}
	if !strings.Contains(m.Body, "Be brief") || strings.Contains(m.Body, "temporary aside") {
		t.Fatal("add or expiry", m.Body)
	}
	if !strings.Contains(m.Floor, "Preserve safety") || strings.Contains(m.Floor, "Review every") || strings.Contains(m.Floor, "Be brief") {
		t.Fatal("floor picked up a lower rule", m.Floor)
	}
}

func TestMergedStoreBudget(t *testing.T) {
	savedRules, savedBytes := maxBudgetRules, maxBudgetBytes
	t.Cleanup(func() {
		maxBudgetRules, maxBudgetBytes = savedRules, savedBytes
	})
	snaps := []Snapshot{floorSnapshot(), testSnapshot("project", Scope{Layer: "project", ProjectID: testProject}, testRule("tone", "Be brief."))}
	maxBudgetRules = 1
	if err := storeBudget(context.Background(), snaps); err == nil {
		t.Fatal("rule cap accepted")
	} else if e, ok := err.(*Error); !ok || e.Code != "budget_check_too_large" || e.Status != 422 {
		t.Fatal(err)
	}
	maxBudgetRules = savedRules
	maxBudgetBytes = 1
	if err := storeBudget(context.Background(), snaps); err == nil {
		t.Fatal("byte cap accepted")
	}
	maxBudgetBytes = savedBytes
	if err := storeBudget(context.Background(), snaps); err != nil {
		t.Fatal(err)
	}
}

func TestMergeExactBudgetAndStubFloor(t *testing.T) {
	base := floorSnapshot()
	header := "# Aeon session rules\n\n"
	capacity := LegacyMaxBytes - len(header) - len(ruleLine(base.Rules[0]))
	rs := []Rule{}
	for i := 0; i < 24; i++ {
		rs = append(rs, testRule(fmt.Sprintf("limit-%02d", i), ""))
		capacity -= len(ruleLine(rs[i]))
	}
	for i := range rs {
		n := capacity / len(rs)
		if i < capacity%len(rs) {
			n++
		}
		rs[i].Text = strings.Repeat("x", n)
	}
	s := testSnapshot("limit", Scope{Layer: "project", ProjectID: testProject}, rs...)
	m, err := Merge(testContext(), []Snapshot{base, s}, time.Now())
	if err != nil || m.ByteSize != LegacyMaxBytes {
		t.Fatal("exact byte budget", err, m.ByteSize)
	}
	stub, err := Stub(m)
	if err != nil || !strings.Contains(stub, m.Floor) {
		t.Fatal("stub lost floor", err)
	}
	m.Floor = "unrelated instructions\n"
	if _, err = Stub(m); err == nil {
		t.Fatal("stub accepted unrelated floor")
	}
	s.Rules[0].Text += "界"
	s.SHA256 = SnapshotDigest(s)
	if _, err = Merge(testContext(), []Snapshot{base, s}, time.Now()); err == nil {
		t.Fatal("budget+3 accepted")
	}
	r := testRule("safety", "Disable the floor.")
	r.Enabled = false
	m, err = Merge(testContext(), []Snapshot{base, testSnapshot("weak", Scope{Layer: "person", OwnerID: testPerson}, r)}, time.Now())
	if err != nil || !strings.Contains(m.Body, "Preserve safety") {
		t.Fatal("lower disable suppressed locked floor", err)
	}
}
