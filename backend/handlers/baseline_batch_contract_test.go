package handlers_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/baselinebatch"
)

// ait7ReviewedHandoverFixture is the synthetic AIT-7 reviewed handover vendored
// from aithema source 60c453d (portable handover QA corpus).
const ait7ReviewedHandoverFixturePath = "../contracts/fixtures/baseline-batch/ait7-reviewed-r1.json"
const baselineWorkflowFixturePath = "../contracts/fixtures/baseline-batch/workflow-ait7-import.json"

// TestBaselineBatchAIT7WorkflowContract imports the committed AIT-7 reviewed handover,
// asserts the list-shaped API contract, and keeps a serialized workflow fixture
// that the Vue integration tests mount unchanged.
func TestBaselineBatchAIT7WorkflowContract(t *testing.T) {
	handover, err := os.ReadFile(ait7ReviewedHandoverFixturePath)
	if err != nil {
		t.Fatalf("read AIT-7 handover: %v", err)
	}

	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "AIT-7 import", "key": "AIT7"}))
	optIn(t, ts, projectID)

	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID), map[string]any{
		"handover": json.RawMessage(handover),
	})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	if draft.Baseline.ImportedClaimedApprovedBy != "party:approver" {
		t.Fatalf("retained imported claim=%q", draft.Baseline.ImportedClaimedApprovedBy)
	}
	if len(draft.Requirements) != 1 || draft.Requirements[0].Ref != "req.unicode" {
		t.Fatalf("requirements=%+v", draft.Requirements)
	}
	if len(draft.Constraints) != 1 || draft.Constraints[0].Ref != "constraint:data.eu" {
		t.Fatalf("constraints=%+v", draft.Constraints)
	}
	if len(draft.Selected.RequirementRefs) != 1 || draft.Selected.RequirementRefs[0] != "req.unicode" {
		t.Fatalf("default selection=%v", draft.Selected.RequirementRefs)
	}
	if draft.Unresolved == nil {
		t.Fatal("import response unresolved must be [] not nil")
	}
	if len(draft.Unresolved) != 0 {
		t.Fatalf("AIT-7 reviewed handover unresolved=%+v", draft.Unresolved)
	}

	workflow := workflowOf(t, ts, projectID)
	assertBaselineListContract(t, workflow)
	if workflow.Draft == nil {
		t.Fatal("workflow draft missing after import")
	}
	if workflow.Draft.Impact.UnresolvedCount != 0 {
		t.Fatalf("impact unresolved_count=%d", workflow.Draft.Impact.UnresolvedCount)
	}

	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID), map[string]any{
		"execution_mode":            baselinebatch.ModeManual,
		"selected_requirement_refs": []string{"req.unicode"},
	})
	if review.StatusCode != 200 {
		t.Fatalf("manual review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if !draft.ReviewValid || draft.ExecutionMode != baselinebatch.ModeManual {
		t.Fatalf("manual review not bound: %+v", draft)
	}

	workflow = workflowOf(t, ts, projectID)
	assertBaselineListContract(t, workflow)

	raw, err := json.MarshalIndent(workflow, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_BASELINE_FIXTURES") == "1" {
		if err := os.MkdirAll(filepath.Dir(baselineWorkflowFixturePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(baselineWorkflowFixturePath, append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := os.ReadFile(baselineWorkflowFixturePath)
	if err != nil {
		t.Fatalf("committed workflow fixture missing at %s: %v (run with UPDATE_BASELINE_FIXTURES=1)", baselineWorkflowFixturePath, err)
	}
	var gotFixture, wantFixture baselinebatch.Workflow
	decodeBytes(t, committed, &wantFixture)
	decodeBytes(t, raw, &gotFixture)
	if gotFixture.Draft == nil || wantFixture.Draft == nil {
		t.Fatal("fixture draft missing")
	}
	if gotFixture.Draft.Baseline.ContentDigest != wantFixture.Draft.Baseline.ContentDigest {
		t.Fatalf("fixture drift: regenerate with UPDATE_BASELINE_FIXTURES=1")
	}
	if len(gotFixture.Draft.Unresolved) != 0 || len(wantFixture.Draft.Unresolved) != 0 {
		t.Fatalf("fixture unresolved drift")
	}
}

func assertBaselineListContract(t *testing.T, workflow baselinebatch.Workflow) {
	t.Helper()
	raw, err := json.Marshal(workflow)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, forbidden := range []string{
		`"unresolved":null`,
		`"requirements":null`,
		`"constraints":null`,
		`"batches":null`,
		`"runtimes":null`,
		`"forecasts":null`,
		`"stages":null`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("workflow JSON contains %s", forbidden)
		}
	}
	if workflow.Draft != nil && workflow.Draft.Unresolved == nil {
		t.Fatal("draft unresolved slice is nil after normalization")
	}
	if workflow.Batches == nil {
		t.Fatal("workflow batches slice is nil after normalization")
	}
}

func decodeBytes(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
