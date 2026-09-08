// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/mailer"
	"github.com/inspr-at/paimos/backend/releaseacceptance"
)

type fakeAcceptanceMailer struct {
	mu       sync.Mutex
	messages []mailer.Message
	err      error
}

func (f *fakeAcceptanceMailer) Send(_ context.Context, msg mailer.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.messages = append(f.messages, msg)
	return nil
}

func postAcceptance(t *testing.T, ts *testServer, cookie, path string, body any) *http.Response {
	t.Helper()
	return postAcceptanceHeader(t, ts, cookie, path, body, nil)
}

func postAcceptanceHeader(t *testing.T, ts *testServer, cookie, path string, body any, extra http.Header) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Origin", ts.srv.URL)
		req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
	}
	for key, values := range extra {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func putAcceptance(t *testing.T, ts *testServer, cookie, path string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, ts.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Origin", ts.srv.URL)
	req.Header.Set("X-CSRF-Token", csrfTokenForSessionCookie(t, cookie))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readAcceptance(t *testing.T, resp *http.Response) releaseacceptance.Acceptance {
	t.Helper()
	var acc releaseacceptance.Acceptance
	decode(t, resp, &acc)
	return acc
}

func seedAcceptancePeople(t *testing.T, projectID int64) (memberID, externalID int64) {
	t.Helper()
	memberID = userIDByUsername(t, "member")
	externalID = userIDByUsername(t, "external")
	if _, err := db.DB.Exec(`UPDATE users SET email=? WHERE id=?`, "member@example.test", memberID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET email=? WHERE id=?`, "external@example.test", externalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'viewer')`, projectID, externalID); err != nil {
		t.Fatal(err)
	}
	return memberID, externalID
}

func configureBody(memberID, externalID int64, revision int64, mode string) map[string]any {
	return map[string]any{
		"expected_revision":   revision,
		"operating_mode":      mode,
		"agreement_ref":       "SOW-9",
		"disclosed_gaps":      []map[string]string{{"gap_ref": "gap_backup", "statement": "Backup restore not proven for this target."}},
		"delivery_party_ref":  "party_delivery",
		"operator_party_ref":  "party_customer",
		"required_party_refs": []string{"party_delivery", "party_customer"},
		"parties": []map[string]any{
			{"party_ref": "party_customer", "kind": "linked_user", "user_id": externalID, "email": "external@example.test", "display_name": "Customer", "roles": []string{"acceptance_party", "operator"}},
			{"party_ref": "party_delivery", "kind": "linked_user", "user_id": memberID, "email": "member@example.test", "display_name": "Delivery", "roles": []string{"acceptance_party", "delivery_party"}},
		},
	}
}

func mintFromBuiltReceipt(t *testing.T, ts *testServer, projectID int64) releaseacceptance.Acceptance {
	t.Helper()
	prev := brand.Default.EmailFrom
	brand.Default.EmailFrom = "paimos@example.test"
	t.Cleanup(func() { brand.Default.EmailFrom = prev })
	batch := startManualHTTPBatch(t, ts, projectID, "acc-mint-"+fmt.Sprint(projectID))
	batch = postHTTPBuiltReceipt(t, ts, projectID, batch)
	resp := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/release-record", projectID, batch.ID), map[string]any{})
	if resp.StatusCode != 200 {
		t.Fatalf("mint=%d %s", resp.StatusCode, baselineReadBody(resp))
	}
	acc := readAcceptance(t, resp)
	if acc.Release.ArtifactDigest == "" || acc.Release.BatchID != batch.ID {
		t.Fatalf("mint did not bind built receipt: %+v", acc.Release)
	}
	return acc
}

func TestReleaseAcceptanceHTTPMintConfirmPortalAndSend(t *testing.T) {
	ts := newTestServer(t)
	mail := &fakeAcceptanceMailer{}
	handlers.SetReleaseAcceptanceMailerForTest(mail)
	t.Cleanup(func() { handlers.SetReleaseAcceptanceMailerForTest(nil) })

	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept HTTP", "key": "ACH"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)

	cfg := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeAgencySupported))
	if cfg.StatusCode != 200 {
		t.Fatalf("configure=%d %s", cfg.StatusCode, baselineReadBody(cfg))
	}
	acc = readAcceptance(t, cfg)

	wrong := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_customer"})
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong party=%d %s", wrong.StatusCode, baselineReadBody(wrong))
	}
	wrong.Body.Close()

	internalExternal := postAcceptance(t, ts, ts.externalCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_customer"})
	if internalExternal.StatusCode != http.StatusForbidden {
		t.Fatalf("external internal confirm=%d %s", internalExternal.StatusCode, baselineReadBody(internalExternal))
	}
	internalExternal.Body.Close()

	own := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_delivery"})
	if own.StatusCode != 200 {
		t.Fatalf("member confirm=%d %s", own.StatusCode, baselineReadBody(own))
	}
	own.Body.Close()

	portal := postAcceptance(t, ts, ts.externalCookie, fmt.Sprintf("/api/portal/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_customer"})
	if portal.StatusCode != 200 {
		t.Fatalf("portal viewer confirm=%d %s", portal.StatusCode, baselineReadBody(portal))
	}
	acc = readAcceptance(t, portal)
	if acc.Status == releaseacceptance.StatusAccepted {
		t.Fatal("confirmed without sent mail")
	}

	preview := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/preview", projectID, acc.Release.ID),
		map[string]any{"subject": "Accept ACH", "body": "Please accept this built release among the involved parties."})
	if preview.StatusCode != 200 {
		t.Fatalf("preview=%d %s", preview.StatusCode, baselineReadBody(preview))
	}
	acc = readAcceptance(t, preview)
	send := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/authorize-send", projectID, acc.Release.ID),
		map[string]any{
			"request_key": "send-http-1", "recipient_party_refs": []string{"party_customer", "party_delivery"},
			"preview_revision": acc.PreviewRevision, "confirm_send": true,
		})
	if send.StatusCode != 200 {
		t.Fatalf("send=%d %s", send.StatusCode, baselineReadBody(send))
	}
	acc = readAcceptance(t, send)
	if acc.Status != releaseacceptance.StatusAccepted || acc.Release.State != releaseacceptance.StateAccepted {
		t.Fatalf("expected accepted got %+v missing=%+v", acc.Status, acc.Missing)
	}
	if len(mail.messages) != 1 {
		t.Fatalf("sent=%d", len(mail.messages))
	}
	html := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/evidence?format=html", projectID, acc.Release.ID), ts.adminCookie)
	body, _ := io.ReadAll(html.Body)
	html.Body.Close()
	if html.StatusCode != 200 || strings.Contains(string(body), "<script>") {
		t.Fatalf("evidence html=%d %s", html.StatusCode, body)
	}
}

func TestReleaseAcceptanceHTTPRejectsKeysImpersonationRevokedAndCrossProject(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept deny", "key": "ACD"}))
	otherID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Other", "key": "ACO"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)
	cfg := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, 0, releaseacceptance.ModeAgencySupported))
	if cfg.StatusCode != 200 {
		t.Fatal(baselineReadBody(cfg))
	}
	cfg.Body.Close()

	agent := postAcceptanceHeader(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_delivery"}, http.Header{"X-Paimos-Agent-Name": []string{"codex"}})
	if agent.StatusCode != http.StatusForbidden {
		t.Fatalf("agent confirm=%d %s", agent.StatusCode, baselineReadBody(agent))
	}
	agent.Body.Close()

	key := mintHTTPAPIKey(t, ts, ts.adminCookie, "acc-key", []string{"*"})
	keyResp := postBaselineBearer(t, ts, key, fmt.Sprintf("/api/projects/%d/baseline-batches/batches/%d/release-record", projectID, acc.Release.BatchID), map[string]any{})
	if keyResp.StatusCode != http.StatusForbidden {
		t.Fatalf("api key mint=%d %s", keyResp.StatusCode, baselineReadBody(keyResp))
	}
	keyResp.Body.Close()

	promoteToSuperAdmin(t, "admin")
	start := postAcceptance(t, ts, ts.adminCookie, "/api/auth/impersonation/start", map[string]any{"user_id": memberID})
	if start.StatusCode != 200 {
		t.Fatalf("impersonate=%d %s", start.StatusCode, baselineReadBody(start))
	}
	start.Body.Close()
	imp := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_delivery"})
	if imp.StatusCode != http.StatusForbidden {
		t.Fatalf("impersonated confirm=%d %s", imp.StatusCode, baselineReadBody(imp))
	}
	imp.Body.Close()
	end := postAcceptance(t, ts, ts.adminCookie, "/api/auth/impersonation/end", map[string]any{})
	end.Body.Close()

	cross := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", otherID, acc.Release.ID), ts.adminCookie)
	if cross.StatusCode != http.StatusNotFound {
		t.Fatalf("cross project=%d %s", cross.StatusCode, baselineReadBody(cross))
	}
	cross.Body.Close()

	sid := strings.TrimPrefix(ts.memberCookie, "session=")
	if _, err := db.DB.Exec(`UPDATE sessions SET expires_at=datetime('now','-1 hour') WHERE id=?`, sid); err != nil {
		t.Fatal(err)
	}
	revoked := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID),
		map[string]any{"party_ref": "party_delivery"})
	if revoked.StatusCode != http.StatusForbidden && revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session=%d %s", revoked.StatusCode, baselineReadBody(revoked))
	}
	revoked.Body.Close()
}

func TestReleaseAcceptanceHTTPStaleReplayConcurrentAndFailedMail(t *testing.T) {
	ts := newTestServer(t)
	handlers.SetReleaseAcceptanceMailerForTest(mailer.Unconfigured{})
	t.Cleanup(func() { handlers.SetReleaseAcceptanceMailerForTest(nil) })
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept stale", "key": "ACS"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)
	first := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, 0, releaseacceptance.ModeAgencySupported))
	acc = readAcceptance(t, first)
	next := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeCustomerOperated))
	if next.StatusCode != 200 {
		t.Fatal(baselineReadBody(next))
	}
	acc = readAcceptance(t, next)
	oldRev := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, 1, releaseacceptance.ModeAgencySupported))
	if oldRev.StatusCode != http.StatusConflict {
		t.Fatalf("stale revision=%d %s", oldRev.StatusCode, baselineReadBody(oldRev))
	}
	oldRev.Body.Close()

	preview := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/preview", projectID, acc.Release.ID),
		map[string]any{"subject": "Accept", "body": "reviewed message"})
	acc = readAcceptance(t, preview)
	oldPreview := acc.PreviewRevision
	preview2 := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/preview", projectID, acc.Release.ID),
		map[string]any{"subject": "Accept 2", "body": "reviewed message changed"})
	acc = readAcceptance(t, preview2)
	staleSend := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/authorize-send", projectID, acc.Release.ID),
		map[string]any{"request_key": "stale-send", "recipient_party_refs": []string{"party_customer"}, "preview_revision": oldPreview, "confirm_send": true})
	if staleSend.StatusCode != http.StatusConflict {
		t.Fatalf("stale preview send=%d %s", staleSend.StatusCode, baselineReadBody(staleSend))
	}
	staleSend.Body.Close()

	send := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/authorize-send", projectID, acc.Release.ID),
		map[string]any{"request_key": "fail-send", "recipient_party_refs": []string{"party_customer", "party_delivery"}, "preview_revision": acc.PreviewRevision, "confirm_send": true})
	acc = readAcceptance(t, send)
	if acc.Status == releaseacceptance.StatusAccepted {
		t.Fatal("unconfigured SMTP finalized")
	}
	if len(acc.EmailEvidence) != 1 || acc.EmailEvidence[0].State == releaseacceptance.MailSent {
		t.Fatalf("failed mail evidence=%+v", acc.EmailEvidence)
	}
	if acc.MailRecovery == "" {
		t.Fatal("missing recovery")
	}
	replay := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/authorize-send", projectID, acc.Release.ID),
		map[string]any{"request_key": "fail-send", "recipient_party_refs": []string{"party_customer", "party_delivery"}, "preview_revision": acc.PreviewRevision, "confirm_send": true})
	replayAcc := readAcceptance(t, replay)
	if len(replayAcc.EmailEvidence) != 1 {
		t.Fatalf("replay duplicated evidence: %d", len(replayAcc.EmailEvidence))
	}

	path := fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/confirm", projectID, acc.Release.ID)
	raw, _ := json.Marshal(map[string]any{"party_ref": "party_delivery"})
	csrf := csrfTokenForSessionCookie(t, ts.memberCookie)
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, ts.srv.URL+path, bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Cookie", ts.memberCookie)
			req.Header.Set("Origin", ts.srv.URL)
			req.Header.Set("X-CSRF-Token", csrf)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()
	got := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, got)
	n := 0
	for _, c := range acc.Confirmations {
		if c.PartyRef == "party_delivery" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("concurrent confirmations=%d", n)
	}

	hiddenMember := userIDByUsername(t, "member")
	if _, err := db.DB.Exec(`INSERT INTO project_members(user_id,project_id,access_level) VALUES(?,?,'none')
		ON CONFLICT(user_id,project_id) DO UPDATE SET access_level='none'`, hiddenMember, projectID); err != nil {
		t.Fatal(err)
	}
	priv := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/evidence?format=json", projectID, acc.Release.ID), ts.memberCookie)
	if priv.StatusCode != http.StatusNotFound && priv.StatusCode != http.StatusForbidden {
		t.Fatalf("evidence privacy=%d %s", priv.StatusCode, baselineReadBody(priv))
	}
	priv.Body.Close()
}

func insertHTTPProjectEnv(t *testing.T, projectID int64, name string) int64 {
	t.Helper()
	res, err := db.DB.Exec(`INSERT INTO project_environments(project_id, name, url, host_alias, host_ip, sort_order) VALUES(?,?,'','','',0)`, projectID, name)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestReleaseAcceptanceHTTPRecordExternalAndStandingPolicy(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept policy HTTP", "key": "ACP"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)
	cfg := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeCustomerOperated))
	if cfg.StatusCode != 200 {
		t.Fatalf("configure=%d %s", cfg.StatusCode, baselineReadBody(cfg))
	}
	acc = readAcceptance(t, cfg)

	missingAttest := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/record-external", projectID, acc.Release.ID),
		map[string]any{
			"request_key": "http-ext-missing-confirm", "recipient_party_refs": []string{"party_customer"},
			"raw_message":         "From: a@example.test\r\nSubject: Accept\r\n\r\nWe accept.\r\n",
			"attestation":         "I recorded this email. Recipients are not consent.",
			"attested_party_refs": []string{"party_customer"},
		})
	if missingAttest.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing confirm_attest=%d %s", missingAttest.StatusCode, baselineReadBody(missingAttest))
	}
	missingAttest.Body.Close()

	noAttest := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/record-external", projectID, acc.Release.ID),
		map[string]any{
			"request_key": "http-ext-recipients-only", "recipient_party_refs": []string{"party_customer"},
			"raw_message":         "From: a@example.test\r\nSubject: Accept\r\n\r\nWe accept.\r\n",
			"attestation":         "I recorded this email. Recipients are not consent.",
			"attested_party_refs": []string{},
			"confirm_attest":      false,
		})
	if noAttest.StatusCode != 200 {
		t.Fatalf("recipients-only record=%d %s", noAttest.StatusCode, baselineReadBody(noAttest))
	}
	acc = readAcceptance(t, noAttest)
	if len(acc.Confirmations) != 0 || acc.Status == releaseacceptance.StatusAccepted {
		t.Fatalf("recipients auto-attested over HTTP: status=%s confirmations=%+v", acc.Status, acc.Confirmations)
	}

	withAttest := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/record-external", projectID, acc.Release.ID),
		map[string]any{
			"request_key": "http-ext-attest", "recipient_party_refs": []string{"party_customer"},
			"raw_message":         "From: a@example.test\r\nSubject: Accept\r\n\r\nWe accept again.\r\n",
			"attestation":         "I attest, as the recording human, that the selected party accepted this same release revision.",
			"attested_party_refs": []string{"party_customer"},
			"confirm_attest":      true,
		})
	if withAttest.StatusCode != 200 {
		t.Fatalf("attested record=%d %s", withAttest.StatusCode, baselineReadBody(withAttest))
	}
	acc = readAcceptance(t, withAttest)
	if len(acc.Confirmations) != 1 || acc.Confirmations[0].PartyRef != "party_customer" || acc.Confirmations[0].Source != releaseacceptance.SourceExternalEmail {
		t.Fatalf("attested confirmations=%+v", acc.Confirmations)
	}

	unknownPolicy := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/acceptance-standing-policies", projectID), map[string]any{
		"policy_ref": "policy_customer", "content_digest": acc.Release.ContentDigest, "revision_seal": acc.Release.RevisionSeal,
		"parties": []string{"party_delivery", "party_customer"}, "agreement_ref": "SOW-9",
		"gaps":        []map[string]string{{"gap_ref": "gap_backup", "statement": "Backup restore not proven for this target."}},
		"bounded_use": "Same approved baseline implementation updates only.", "expires_at": "2026-12-01T00:00:00Z",
		"release_channel": acc.Release.ReleaseChannel, "artifact_digest": acc.Release.ArtifactDigest,
		"target_ref": "production", "model_ref": "customer_operated",
	})
	if unknownPolicy.StatusCode != http.StatusBadRequest {
		t.Fatalf("approve name without bind=%d %s", unknownPolicy.StatusCode, baselineReadBody(unknownPolicy))
	}
	unknownPolicy.Body.Close()

	envID := insertHTTPProjectEnv(t, projectID, "production")
	listed := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, listed)
	if acc.DeploymentTarget != "" {
		t.Fatalf("sole environment auto-bound over HTTP: %q", acc.DeploymentTarget)
	}
	if len(acc.TargetCandidates) != 1 || acc.TargetCandidates[0].EnvironmentID != envID {
		t.Fatalf("candidates=%+v", acc.TargetCandidates)
	}

	badPharos := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/deployment-target", projectID, acc.Release.ID),
		map[string]any{"kind": "pharos_owner", "registration_id": 999})
	if badPharos.StatusCode != http.StatusBadRequest {
		t.Fatalf("fake pharos bind=%d %s", badPharos.StatusCode, baselineReadBody(badPharos))
	}
	badPharos.Body.Close()

	bound := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/deployment-target", projectID, acc.Release.ID),
		map[string]any{"kind": "project_environment", "environment_id": envID})
	if bound.StatusCode != 200 {
		t.Fatalf("bind=%d %s", bound.StatusCode, baselineReadBody(bound))
	}
	acc = readAcceptance(t, bound)
	if acc.DeploymentTarget == "" || acc.DeploymentTarget == "production" || acc.DeploymentTargetKind != releaseacceptance.TargetKindProjectEnv {
		t.Fatalf("HTTP bind identity=%+v", acc)
	}
	boundRef := acc.DeploymentTarget

	uiBody := map[string]any{
		"policy_ref": "policy_customer_bound", "content_digest": acc.Release.ContentDigest, "revision_seal": acc.Release.RevisionSeal,
		"parties": []string{"party_delivery", "party_customer"}, "agreement_ref": "SOW-9",
		"gaps":        []map[string]string{{"gap_ref": "gap_backup", "statement": "Backup restore not proven for this target."}},
		"bounded_use": "Same approved baseline implementation updates only.", "expires_at": "2026-12-01T00:00:00Z",
		"release_channel": acc.Release.ReleaseChannel, "artifact_digest": acc.Release.ArtifactDigest,
		"target_ref": acc.DeploymentTarget, "model_ref": "customer_operated",
	}
	approved := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/acceptance-standing-policies", projectID), uiBody)
	if approved.StatusCode != 200 {
		t.Fatalf("approve UI body=%d %s", approved.StatusCode, baselineReadBody(approved))
	}
	var policy releaseacceptance.StandingPolicy
	decode(t, approved, &policy)
	if policy.TargetRef != boundRef || policy.ModelRef != releaseacceptance.ModeCustomerOperated {
		t.Fatalf("UI policy scope=%+v", policy)
	}

	driftCfg := configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeCustomerOperated)
	driftCfg["agreement_ref"] = "SOW-CHANGED"
	drift := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), driftCfg)
	if drift.StatusCode != 200 {
		t.Fatalf("drift configure=%d %s", drift.StatusCode, baselineReadBody(drift))
	}
	acc = readAcceptance(t, drift)
	stale := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("apply drift=%d %s", stale.StatusCode, baselineReadBody(stale))
	}
	stale.Body.Close()

	restore := configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeCustomerOperated)
	restored := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), restore)
	if restored.StatusCode != 200 {
		t.Fatalf("restore configure=%d %s", restored.StatusCode, baselineReadBody(restored))
	}
	acc = readAcceptance(t, restored)
	applied := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if applied.StatusCode != 200 {
		t.Fatalf("apply=%d %s", applied.StatusCode, baselineReadBody(applied))
	}
	acc = readAcceptance(t, applied)
	found := false
	for _, c := range acc.Confirmations {
		if c.PartyRef == "party_delivery" && c.Source == releaseacceptance.SourceStandingPolicy {
			found = true
		}
	}
	if !found {
		t.Fatalf("standing policy confirmation missing: %+v", acc.Confirmations)
	}

	if _, err := db.DB.Exec(`DELETE FROM project_environments WHERE id=?`, envID); err != nil {
		t.Fatal(err)
	}
	replacement := insertHTTPProjectEnv(t, projectID, "production")
	afterReplace := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, afterReplace)
	if acc.DeploymentTarget != "" {
		t.Fatalf("same-name retarget kept HTTP consent: %q", acc.DeploymentTarget)
	}
	invalidated := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if invalidated.StatusCode != http.StatusForbidden {
		t.Fatalf("apply after retarget=%d %s", invalidated.StatusCode, baselineReadBody(invalidated))
	}
	invalidated.Body.Close()

	rebind := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/deployment-target", projectID, acc.Release.ID),
		map[string]any{"kind": "project_environment", "environment_id": replacement})
	if rebind.StatusCode != 200 {
		t.Fatalf("rebind=%d %s", rebind.StatusCode, baselineReadBody(rebind))
	}
	acc = readAcceptance(t, rebind)
	if acc.DeploymentTarget == boundRef {
		t.Fatal("replacement environment reused prior HTTP identity")
	}

	revoked := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/acceptance-standing-policies/%d/revoke", projectID, policy.ID), map[string]any{})
	if revoked.StatusCode != 200 {
		t.Fatalf("revoke=%d %s", revoked.StatusCode, baselineReadBody(revoked))
	}
	revoked.Body.Close()
	afterRevoke := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if afterRevoke.StatusCode != http.StatusForbidden {
		t.Fatalf("apply revoked=%d %s", afterRevoke.StatusCode, baselineReadBody(afterRevoke))
	}
	afterRevoke.Body.Close()
}
