// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/externalstage"
)

func httpBuiltReceiptJSON(key string) map[string]any {
	return map[string]any{
		"idempotency_key":                         key,
		"expected_attempt_id":                     1,
		"expected_plan_revision":                  1,
		"expected_implementation_execution":       0,
		"expected_implementation_authority_epoch": 0,
		"commit":                      bridgeHTTPCommit,
		"oci_config_digest":           "sha256:" + bridgeHTTPConfig,
		"release_manifest_digest":     "sha256:" + bridgeHTTPReleaseSet,
		"release_manifest_coordinate": bridgeHTTPCoordinate,
		"oci_index_digest":            "sha256:" + bridgeHTTPIndex,
		"version_scheme":              string(externalstage.VersionSchemeINSPRCalendar),
		"release_channel":             bridgeHTTPChannel,
		"release_sequence":            bridgeHTTPSequence,
		"version":                     bridgeHTTPVersion,
		"qa_digest":                   bridgeHTTPQADigest,
	}
}

func postBaselineBearer(t *testing.T, ts *testServer, token, path string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.srv.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func mintHTTPAPIKey(t *testing.T, ts *testServer, cookie, name string, scopes []string) string {
	t.Helper()
	var keyBody struct {
		Key string `json:"key"`
	}
	decode(t, ts.post(t, "/api/auth/api-keys", cookie, map[string]any{"name": name, "scopes": scopes}), &keyBody)
	if keyBody.Key == "" {
		t.Fatal("api key missing")
	}
	return keyBody.Key
}

func startAutomaticHTTPBatch(t *testing.T, ts *testServer, projectID int64, daemon *ownedDaemon, key string) baselinebatch.Batch {
	t.Helper()
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "automatic", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	if draft.Worker.AccountKey != daemon.accountKey || draft.Worker.AccountLabel != daemon.accountLabel {
		t.Fatalf("review stored %+v", draft.Worker)
	}
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": key, "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "automatic", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start")
	}
	got := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d", projectID, batch.ID), ts.adminCookie)
	decode(t, got, &batch)
	return batch
}

func postHTTPBuiltReceipt(t *testing.T, ts *testServer, projectID int64, batch baselinebatch.Batch) baselinebatch.Batch {
	t.Helper()
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID),
		httpBuiltReceiptJSON(batch.BatchKey+"-built"))
	if resp.StatusCode != 200 {
		t.Fatalf("built-receipt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	decode(t, resp, &batch)
	return batch
}

func startManualHTTPBatch(t *testing.T, ts *testServer, projectID int64, key string) baselinebatch.Batch {
	t.Helper()
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	if imported.StatusCode != 201 {
		t.Fatalf("import=%d %s", imported.StatusCode, baselineReadBody(imported))
	}
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}})
	if review.StatusCode != 200 {
		t.Fatalf("review=%d %s", review.StatusCode, baselineReadBody(review))
	}
	decode(t, review, &draft)
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": key, "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "manual", "selected_requirement_refs": []string{"req.login"}, "worker": map[string]any{},
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	return batch
}

func TestBaselineBatchBuiltReceiptHTTPToPharosHandoff(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Receipt bridge", "key": "RCB"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness intent")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "receipt-http-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatalf("start=%d %s", started.StatusCode, baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start intent")
	}
	got := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d", projectID, batch.ID), ts.adminCookie)
	decode(t, got, &batch)
	if batch.Progress.NextAction != baselinebatch.NextActionImplementationEvidence {
		t.Fatalf("before receipt next=%q", batch.Progress.NextAction)
	}
	batch = postHTTPBuiltReceipt(t, ts, projectID, batch)
	if batch.Progress.NextAction == baselinebatch.NextActionImplementationEvidence || batch.Progress.NextAction == baselinebatch.NextActionQAEvidence {
		t.Fatalf("receipt left producer next_action: %+v", batch.Progress)
	}
	snap, err := delivery.NewStore(db.DB, delivery.Options{}).SnapshotByIssue(context.Background(), batch.IssueID)
	if err != nil {
		t.Fatal(err)
	}
	if stageOf(snap, delivery.StageSpecification).ExecutionNumber != 1 {
		t.Fatalf("specification rewritten: %+v", stageOf(snap, delivery.StageSpecification))
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	ext, err := externalstage.NewService(db.DB, externalstage.Options{
		FixtureDigest: contracts.ExternalStageV1FixtureDigest(), Random: cryptorand.Reader,
		Clock: clockNow{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	operator := externalstage.Principal{UserID: userID, Kind: "session", SessionCredentialID: operatorSessionCredential(t, userID)}
	registerHTTPPharos(t, ext, operator, projectID, batch.IssueID)
	activated := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/reconcile", projectID, batch.ID), map[string]any{})
	if activated.StatusCode != 200 {
		t.Fatalf("reconcile=%d %s", activated.StatusCode, baselineReadBody(activated))
	}
	decode(t, activated, &batch)
	if batch.Progress.SetupRequired != baselinebatch.SetupRequiredPrerequisiteSeal && batch.Progress.Handoff == nil {
		t.Fatalf("expected Pharos activation after receipt, got %+v", batch.Progress)
	}
}

func TestBaselineBatchBuiltReceiptHTTPNegatives(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Receipt negatives", "key": "RCN"}))
	batch := startManualHTTPBatch(t, ts, projectID, "receipt-neg-start")
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID)

	unknown := httpBuiltReceiptJSON("receipt-unknown-01")
	unknown["implementation_result_digest"] = bridgeHTTPConfig
	if resp := postBaseline(t, ts, ts.adminCookie, path, unknown); resp.StatusCode != 400 {
		t.Fatalf("unknown field=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	malformed := httpBuiltReceiptJSON("receipt-bad-version-01")
	malformed["version"] = "26.13.40"
	if resp := postBaseline(t, ts, ts.adminCookie, path, malformed); resp.StatusCode != 400 {
		t.Fatalf("malformed version=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	got := ts.get(t, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d", projectID, batch.ID), ts.adminCookie)
	decode(t, got, &batch)
	if snapshotStageMust(t, batch.IssueID, delivery.StageImplementation).PolicySatisfied {
		t.Fatal("malformed receipt mutated implementation")
	}

	okBody := httpBuiltReceiptJSON("receipt-ok-01")
	if resp := postBaseline(t, ts, ts.adminCookie, path, okBody); resp.StatusCode != 200 {
		t.Fatalf("valid receipt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if resp := postBaseline(t, ts, ts.adminCookie, path, okBody); resp.StatusCode != 200 {
		t.Fatalf("replay=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	conflict := httpBuiltReceiptJSON("receipt-ok-01")
	conflict["commit"] = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if resp := postBaseline(t, ts, ts.adminCookie, path, conflict); resp.StatusCode != 409 {
		t.Fatalf("conflicting tuple=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	stale := httpBuiltReceiptJSON("receipt-stale-01")
	stale["expected_attempt_id"] = 9
	if resp := postBaseline(t, ts, ts.adminCookie, path, stale); resp.StatusCode != 409 {
		t.Fatalf("stale attempt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	viewerProject := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Viewer receipt", "key": "RCV"}))
	viewerBatch := startManualHTTPBatch(t, ts, viewerProject, "receipt-viewer-start")
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')
		ON CONFLICT(project_id,user_id) DO UPDATE SET access_level='viewer'`, viewerProject, memberID); err != nil {
		t.Fatal(err)
	}
	if resp := postBaseline(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", viewerProject, viewerBatch.ID),
		httpBuiltReceiptJSON("receipt-viewer-01")); resp.StatusCode < 400 {
		t.Fatalf("viewer receipt=%d", resp.StatusCode)
	}
}

func TestBaselineBatchBuiltReceiptLegacyHTTP(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Legacy receipt", "key": "RCL"}))
	batch := startManualHTTPBatch(t, ts, projectID, "receipt-legacy-http")
	body := httpBuiltReceiptJSON("receipt-legacy-http-01")
	body["version_scheme"] = string(externalstage.VersionSchemeLegacy)
	body["version"] = "0.1.94"
	body["release_channel"] = "rollback"
	body["release_sequence"] = 94
	body["release_manifest_coordinate"] = "ghcr:inspr-at/pharos/releases/0.1.94"
	resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID), body)
	if resp.StatusCode != 200 {
		t.Fatalf("legacy receipt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	decode(t, resp, &batch)
	if !snapshotStageMust(t, batch.IssueID, delivery.StageQA).PolicySatisfied {
		t.Fatal("legacy HTTP receipt did not satisfy QA")
	}
}

func TestImplementOnBaselineIssueIsConflictAndOrdinaryIssueStillImplements(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Implement fence", "key": "IMF"}))
	batch := startManualHTTPBatch(t, ts, projectID, "implement-fence-start")
	resp := ts.post(t, "/api/issues/"+fmt.Sprint(batch.IssueID)+"/implement", ts.adminCookie, map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("baseline implement=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	spec := snapshotStageMust(t, batch.IssueID, delivery.StageSpecification)
	if spec.ExecutionNumber != 1 || !spec.PolicySatisfied {
		t.Fatalf("implement rewrote specification: %+v", spec)
	}
	var runs int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE issue_id=?`, batch.IssueID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("implement created %d runs on a baseline issue", runs)
	}
	cancelled := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/control", projectID, batch.ID),
		map[string]any{"action": "cancel"})
	if cancelled.StatusCode != 200 {
		t.Fatalf("cancel=%d %s", cancelled.StatusCode, baselineReadBody(cancelled))
	}
	if resp := ts.post(t, "/api/issues/"+fmt.Sprint(batch.IssueID)+"/implement", ts.adminCookie, map[string]any{}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancelled baseline implement=%d", resp.StatusCode)
	}

	res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,?,?,?)`,
		projectID, 99, "ticket", "Ordinary implement", "backlog")
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := res.LastInsertId()
	ordinary := ts.post(t, "/api/issues/"+fmt.Sprint(issueID)+"/implement", ts.adminCookie, map[string]any{})
	if ordinary.StatusCode != http.StatusCreated {
		t.Fatalf("ordinary implement=%d %s", ordinary.StatusCode, baselineReadBody(ordinary))
	}
}

func TestBaselineBatchBuiltReceiptHTTPRejectsDuplicateFieldsBeforeEffects(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Receipt duplicates", "key": "RCD"}))
	batch := startManualHTTPBatch(t, ts, projectID, "receipt-dup-start")
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID)
	secondCommit := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name  string
		field string
		extra any
	}{
		{name: "commit", field: "commit", extra: secondCommit},
		{name: "cas", field: "expected_implementation_execution", extra: int64(7)},
		{name: "idempotency", field: "idempotency_key", extra: "receipt-dup-other"},
	} {
		body := httpBuiltReceiptJSON("receipt-dup-" + tc.name)
		resp := postBaseline(t, ts, ts.adminCookie, path, json.RawMessage(duplicateHTTPJSONField(t, body, tc.field, tc.extra)))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s duplicate=%d %s", tc.name, resp.StatusCode, baselineReadBody(resp))
		}
	}
	if snapshotStageMust(t, batch.IssueID, delivery.StageImplementation).ExecutionNumber != 0 {
		t.Fatal("duplicate JSON mutated implementation")
	}
	var stolen int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM delivery_evidence WHERE reference_value=?`, secondCommit).Scan(&stolen); err != nil {
		t.Fatal(err)
	}
	if stolen != 0 {
		t.Fatal("duplicate commit last-wins was recorded")
	}
}

func TestBaselineBatchBuiltReceiptHTTPReplayOriginalCAS(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Receipt replay CAS", "key": "RCR"}))
	batch := startManualHTTPBatch(t, ts, projectID, "receipt-replay-http-start")
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID)
	original := httpBuiltReceiptJSON("receipt-http-replay-01")
	if resp := postBaseline(t, ts, ts.adminCookie, path, original); resp.StatusCode != 200 {
		t.Fatalf("first receipt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if resp := postBaseline(t, ts, ts.adminCookie, path, original); resp.StatusCode != 200 {
		t.Fatalf("exact original replay=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	mutated := httpBuiltReceiptJSON("receipt-http-replay-01")
	mutated["expected_implementation_execution"] = 7
	mutated["expected_implementation_authority_epoch"] = 7
	if resp := postBaseline(t, ts, ts.adminCookie, path, mutated); resp.StatusCode != http.StatusConflict {
		t.Fatalf("mutated CAS replay=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	impl := snapshotStageMust(t, batch.IssueID, delivery.StageImplementation)
	if impl.ExecutionNumber != 1 || impl.AuthorityEpoch != 1 || !impl.PolicySatisfied {
		t.Fatalf("mutated CAS changed implementation: %+v", impl)
	}
	if resp := postBaseline(t, ts, ts.adminCookie, path, original); resp.StatusCode != 200 {
		t.Fatalf("exact replay after mutated CAS=%d", resp.StatusCode)
	}
}

func duplicateHTTPJSONField(t *testing.T, body map[string]any, field string, extra any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(body[field])
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte(`"` + field + `":` + string(first))
	dup := []byte(`"` + field + `":` + string(first) + `,"` + field + `":` + string(second))
	if !bytes.Contains(raw, needle) {
		t.Fatalf("missing %s in %s", field, raw)
	}
	return bytes.Replace(raw, needle, dup, 1)
}

func TestImplementOnBaselineLookupFailureRefusesBeforeEffects(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Lookup fence", "key": "ILF"}))
	batch := startManualHTTPBatch(t, ts, projectID, "implement-lookup-start")
	if _, err := db.DB.Exec(`ALTER TABLE baseline_batch_batches RENAME TO baseline_batch_batches_hidden`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DB.Exec(`ALTER TABLE baseline_batch_batches_hidden RENAME TO baseline_batch_batches`)
	})
	failed := ts.post(t, "/api/issues/"+fmt.Sprint(batch.IssueID)+"/implement", ts.adminCookie, map[string]any{})
	if failed.StatusCode != http.StatusInternalServerError {
		t.Fatalf("forced lookup implement=%d %s", failed.StatusCode, baselineReadBody(failed))
	}
	var runs int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM agent_runs WHERE issue_id=?`, batch.IssueID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("lookup failure created %d runs", runs)
	}
	spec := snapshotStageMust(t, batch.IssueID, delivery.StageSpecification)
	if spec.ExecutionNumber != 1 || !spec.PolicySatisfied {
		t.Fatalf("lookup failure rewrote specification: %+v", spec)
	}
	if _, err := db.DB.Exec(`ALTER TABLE baseline_batch_batches_hidden RENAME TO baseline_batch_batches`); err != nil {
		t.Fatal(err)
	}
	owned := ts.post(t, "/api/issues/"+fmt.Sprint(batch.IssueID)+"/implement", ts.adminCookie, map[string]any{})
	if owned.StatusCode != http.StatusConflict {
		t.Fatalf("owned implement after restore=%d %s", owned.StatusCode, baselineReadBody(owned))
	}
	res, err := db.DB.Exec(`INSERT INTO issues(project_id, issue_number, type, title, status) VALUES(?,?,?,?,?)`,
		projectID, 88, "ticket", "Ordinary after lookup failure", "backlog")
	if err != nil {
		t.Fatal(err)
	}
	ordinaryID, _ := res.LastInsertId()
	ordinary := ts.post(t, "/api/issues/"+fmt.Sprint(ordinaryID)+"/implement", ts.adminCookie, map[string]any{})
	if ordinary.StatusCode != http.StatusCreated {
		t.Fatalf("ordinary implement after lookup failure=%d %s", ordinary.StatusCode, baselineReadBody(ordinary))
	}
}

func TestBaselineBatchBuiltReceiptAPIKeyOnAssistedForbidden(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Assisted key", "key": "RAK"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	optIn(t, ts, projectID)
	handover, _ := validHandover(t)
	imported := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/import", projectID),
		map[string]any{"handover": json.RawMessage(handover)})
	var draft baselinebatch.Draft
	decode(t, imported, &draft)
	daemon := newOwnedDaemon(t, projectID, userID)
	worker := daemon.workerSelection("codex")
	if resp := patchBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker}); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	review := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/review", projectID, draft.ID),
		map[string]any{"execution_mode": "assisted", "worker": worker, "selected_requirement_refs": []string{"req.login"}})
	decode(t, review, &draft)
	if resp := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/readiness", projectID, draft.ID), nil); resp.StatusCode != 200 {
		t.Fatal(baselineReadBody(resp))
	}
	if daemon.step(nil) == nil {
		t.Fatal("no readiness")
	}
	started := postBaseline(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/%d/start", projectID, draft.ID), map[string]any{
		"idempotency_key": "assisted-key-start", "review_id": *draft.ReviewID, "draft_revision": draft.Revision,
		"confirm": true, "content_digest": draft.Baseline.ContentDigest, "revision_seal": draft.Baseline.RevisionSeal,
		"execution_mode": "assisted", "selected_requirement_refs": []string{"req.login"}, "worker": worker,
	})
	if started.StatusCode != 200 {
		t.Fatal(baselineReadBody(started))
	}
	var batch baselinebatch.Batch
	decode(t, started, &batch)
	if daemon.step(nil) == nil {
		t.Fatal("no start")
	}
	var keyBody struct {
		Key string `json:"key"`
	}
	decode(t, ts.post(t, "/api/auth/api-keys", ts.adminCookie, map[string]any{"name": "assisted-bot", "scopes": []string{auth.ScopeAgentControlsWrite}}), &keyBody)
	raw, _ := json.Marshal(httpBuiltReceiptJSON("assisted-key-receipt"))
	req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID), strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+keyBody.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("assisted API key receipt=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
}

func TestBaselineBatchBuiltReceiptAutomaticClassOnlyAPIKey(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Class-only receipt", "key": "RCC"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	var memberID int64
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username='member'`).Scan(&memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')
		ON CONFLICT(project_id,user_id) DO UPDATE SET access_level='editor'`, projectID, memberID); err != nil {
		t.Fatal(err)
	}
	daemon := newOwnedClassOnlyClaudeDaemon(t, projectID, userID)
	batch := startAutomaticHTTPBatch(t, ts, projectID, daemon, "class-only-receipt-start")
	if batch.Worker.AccountLabel != "claude_ai_max" || batch.Worker.AccountKey != "" {
		t.Fatalf("invented class-only key: %+v", batch.Worker)
	}
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID)
	starter := mintHTTPAPIKey(t, ts, ts.adminCookie, "class-only-bot", []string{auth.ScopeAgentControlsWrite})
	foreign := mintHTTPAPIKey(t, ts, ts.memberCookie, "class-only-foreign", []string{auth.ScopeAgentControlsWrite})
	narrow := mintHTTPAPIKey(t, ts, ts.adminCookie, "class-only-narrow", []string{auth.ScopeProjectsWrite})

	invented := httpBuiltReceiptJSON("class-only-invented")
	invented["expected_account_key"] = "claude-home"
	invented["expected_runtime_generation"] = batch.Worker.RuntimeGeneration
	if resp := postBaselineBearer(t, ts, starter, path, invented); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("invented key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	wrongGen := httpBuiltReceiptJSON("class-only-wrong-gen")
	wrongGen["expected_runtime_generation"] = "00000000-0000-4000-8000-000000000000"
	if resp := postBaselineBearer(t, ts, starter, path, wrongGen); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong generation=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	missingGen := httpBuiltReceiptJSON("class-only-missing-gen")
	if resp := postBaselineBearer(t, ts, starter, path, missingGen); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing generation=%d %s", resp.StatusCode, baselineReadBody(resp))
	}

	okBody := httpBuiltReceiptJSON("class-only-ok")
	okBody["expected_runtime_generation"] = batch.Worker.RuntimeGeneration
	if resp := postBaselineBearer(t, ts, foreign, path, okBody); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if resp := postBaselineBearer(t, ts, narrow, path, okBody); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("narrow key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if snapshotStageMust(t, batch.IssueID, delivery.StageImplementation).PolicySatisfied {
		t.Fatal("class-only negatives mutated implementation")
	}

	if resp := postBaselineBearer(t, ts, starter, path, okBody); resp.StatusCode != 200 {
		t.Fatalf("class-only empty assertion=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if !snapshotStageMust(t, batch.IssueID, delivery.StageQA).PolicySatisfied {
		t.Fatal("class-only receipt did not satisfy QA")
	}
}

func TestBaselineBatchBuiltReceiptAutomaticNamedAPIKeyRequiresAccount(t *testing.T) {
	ts := newTestServer(t)
	userID := promoteSuperAdmin(t, "admin")
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Named receipt", "key": "RCN"}))
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'codex')`, projectID); err != nil {
		t.Fatal(err)
	}
	daemon := newOwnedDaemon(t, projectID, userID)
	batch := startAutomaticHTTPBatch(t, ts, projectID, daemon, "named-receipt-start")
	if batch.Worker.AccountKey == "" {
		t.Fatal("named automatic stored an empty account key")
	}
	path := fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/built-receipt", projectID, batch.ID)
	starter := mintHTTPAPIKey(t, ts, ts.adminCookie, "named-bot", []string{auth.ScopeAgentControlsWrite})
	omitNamed := httpBuiltReceiptJSON("named-omit-key")
	omitNamed["expected_runtime_generation"] = batch.Worker.RuntimeGeneration
	if resp := postBaselineBearer(t, ts, starter, path, omitNamed); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("omitted named key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	if snapshotStageMust(t, batch.IssueID, delivery.StageImplementation).PolicySatisfied {
		t.Fatal("omitted named key mutated implementation")
	}
	matchNamed := httpBuiltReceiptJSON("named-match-key")
	matchNamed["expected_account_key"] = batch.Worker.AccountKey
	matchNamed["expected_runtime_generation"] = batch.Worker.RuntimeGeneration
	if resp := postBaselineBearer(t, ts, starter, path, matchNamed); resp.StatusCode != 200 {
		t.Fatalf("named matching key=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
}

func snapshotStageMust(t *testing.T, issueID int64, key string) delivery.StageSnapshot {
	t.Helper()
	snap, err := delivery.NewStore(db.DB, delivery.Options{}).SnapshotByIssue(context.Background(), issueID)
	if err != nil {
		t.Fatal(err)
	}
	return stageOf(snap, key)
}
