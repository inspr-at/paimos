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

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/brand"
	"github.com/inspr-at/paimos/backend/contracts"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/externalstage"
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

func TestReleaseAcceptanceHTTPExportKeepsMaliciousInputInert(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept XSS", "key": "ACX"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)

	body := configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeAgencySupported)
	body["agreement_ref"] = "SOW <script>alert(1)</script>"
	body["disclosed_gaps"] = []map[string]string{{
		"gap_ref": "gap_backup", "statement": `<img src=x onerror=alert(1)> restore gap`,
	}}
	parties := body["parties"].([]map[string]any)
	parties[0]["display_name"] = `Customer <img src="https://evil.example" onerror="alert(1)">`
	cfg := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), body)
	if cfg.StatusCode != 200 {
		t.Fatalf("configure=%d %s", cfg.StatusCode, baselineReadBody(cfg))
	}
	cfg.Body.Close()

	preview := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/email/preview", projectID, acc.Release.ID),
		map[string]any{"subject": `Accept <script>alert(1)</script>`, "body": `<img src="https://evil.example" onerror="alert(1)"> and text`})
	if preview.StatusCode != 200 {
		t.Fatalf("preview=%d %s", preview.StatusCode, baselineReadBody(preview))
	}
	preview.Body.Close()

	htmlResp := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/evidence?format=html", projectID, acc.Release.ID), ts.adminCookie)
	htmlBody := readEvidenceExport(t, htmlResp, "text/html")
	if htmlResp.Header.Get("Content-Security-Policy") != "default-src 'none'" {
		t.Fatalf("html csp=%q", htmlResp.Header.Get("Content-Security-Policy"))
	}
	if strings.Contains(htmlBody, "<script>") || strings.Contains(htmlBody, "<img") ||
		strings.Contains(htmlBody, `src="https://evil.example"`) {
		t.Fatalf("html still contains raw markup: %s", htmlBody)
	}
	if !strings.Contains(htmlBody, "&lt;script&gt;") || !strings.Contains(htmlBody, "&lt;img") {
		t.Fatalf("html missing escapes: %s", htmlBody)
	}

	jsonResp := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/evidence?format=json", projectID, acc.Release.ID), ts.adminCookie)
	jsonBody := readEvidenceExport(t, jsonResp, "application/json")
	if strings.Contains(jsonBody, "&lt;script&gt;") {
		t.Fatalf("json HTML-escaped stored text: %s", jsonBody)
	}
	var exported releaseacceptance.Acceptance
	if err := json.Unmarshal([]byte(jsonBody), &exported); err != nil {
		t.Fatalf("json unmarshal: %v %s", err, jsonBody)
	}
	if exported.AgreementRef != "SOW <script>alert(1)</script>" {
		t.Fatalf("json agreement=%q", exported.AgreementRef)
	}
	if exported.PreviewSubject != "Accept <script>alert(1)</script>" {
		t.Fatalf("json subject=%q", exported.PreviewSubject)
	}

	eml := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/evidence?format=eml", projectID, acc.Release.ID), ts.adminCookie)
	_ = readEvidenceExport(t, eml, "message/rfc822")
}

func readEvidenceExport(t *testing.T, resp *http.Response, wantType string) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export %s status=%d %s", wantType, resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, wantType) {
		t.Fatalf("export %s content-type=%q", wantType, got)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("export %s missing nosniff", wantType)
	}
	if resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("export %s cache-control=%q", wantType, resp.Header.Get("Cache-Control"))
	}
	return string(raw)
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

	sid := cookieSessionID(ts.memberCookie)
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
	res, err := db.DB.Exec(`INSERT INTO project_environments(project_id, name, url, host_alias, host_ip, sort_order)
		VALUES(?,?,?,?,?,0)`, projectID, name, "https://old-target.example.invalid", "qa-old-target", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func adminSessionCredential(t *testing.T) string {
	t.Helper()
	var cred string
	if err := db.DB.QueryRow(`SELECT credential_id FROM sessions WHERE user_id=? AND expires_at>datetime('now') ORDER BY created_at DESC LIMIT 1`,
		userIDByUsername(t, "admin")).Scan(&cred); err != nil {
		t.Fatal(err)
	}
	return cred
}

func seedHTTPPharosOwner(t *testing.T, projectID, batchID int64, environment string) (registrationID, apiKeyID, userID int64, deliveryKey string) {
	t.Helper()
	var deliveryID int64
	if err := db.DB.QueryRow(`SELECT delivery_id FROM baseline_batch_batches WHERE id=? AND project_id=?`, batchID, projectID).Scan(&deliveryID); err != nil || deliveryID <= 0 {
		t.Fatalf("batch delivery missing: %v %d", err, deliveryID)
	}
	if err := db.DB.QueryRow(`SELECT delivery_key FROM deliveries WHERE id=?`, deliveryID).Scan(&deliveryKey); err != nil {
		t.Fatal(err)
	}
	return seedHTTPPharosOwnerOnKey(t, projectID, deliveryKey, environment)
}

func seedHTTPPharosOnNewDelivery(t *testing.T, projectID int64) int64 {
	t.Helper()
	var next int64
	if err := db.DB.QueryRow(`SELECT COALESCE(MAX(issue_number),0)+1 FROM issues WHERE project_id=?`, projectID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	issue, err := db.DB.Exec(`INSERT INTO issues(project_id,issue_number,type,title) VALUES(?,?,'ticket','other-pharos')`, projectID, next)
	if err != nil {
		t.Fatal(err)
	}
	issueID, _ := issue.LastInsertId()
	deliveryKey := fmt.Sprintf("issue:%d", issueID)
	delivery, err := db.DB.Exec(`INSERT INTO deliveries(issue_id,delivery_key,project_id_hint,created_at,updated_at)
		VALUES(?,?,?,strftime('%Y-%m-%dT%H:%M:%fZ','now'),strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, issueID, deliveryKey, projectID)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = delivery.LastInsertId()
	regID, _, _, _ := seedHTTPPharosOwnerOnKey(t, projectID, deliveryKey, "staging")
	return regID
}

func seedHTTPPharosOwnerOnKey(t *testing.T, projectID int64, deliveryKey, environment string) (registrationID, apiKeyID, userID int64, key string) {
	t.Helper()
	user, err := db.DB.Exec(`INSERT INTO users(username,password,role,status,email) VALUES(?,?,?,'active',?)`,
		"pharos-http-"+uuid.NewString()[:8], "x", "member", "pharos-http-"+uuid.NewString()[:8]+"@example.test")
	if err != nil {
		t.Fatal(err)
	}
	userID, _ = user.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_members(project_id,user_id,access_level) VALUES(?,?,'editor')`, projectID, userID); err != nil {
		t.Fatal(err)
	}
	keyRow, err := db.DB.Exec(`INSERT INTO api_keys(user_id,name,key_hash,key_prefix,scopes) VALUES(?,?,?,?,?)`,
		userID, "pharos-other", strings.ReplaceAll(uuid.NewString()+uuid.NewString(), "-", "")[:64], "paimos_pharos", "*")
	if err != nil {
		t.Fatal(err)
	}
	apiKeyID, _ = keyRow.LastInsertId()
	stage, err := externalstage.NewService(db.DB, externalstage.Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest()})
	if err != nil {
		t.Fatal(err)
	}
	adminID := userIDByUsername(t, "admin")
	reg, err := stage.RegisterReporter(context.Background(), externalstage.Principal{
		UserID: adminID, Kind: "session", SessionCredentialID: adminSessionCredential(t),
	}, deliveryKey, "register-other-pharos-"+uuid.NewString(), externalstage.RegisterReporterRequest{
		APIKeyID: apiKeyID, ReporterClass: externalstage.ReporterClassPharos,
		ReporterRole: externalstage.ReporterRoleOwner, Workflow: "deploy-" + environment, Environment: environment,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reg.RegistrationID, apiKeyID, userID, deliveryKey
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

	moved := ts.put(t, fmt.Sprintf("/api/projects/%d/environments/%d", projectID, envID), ts.adminCookie, map[string]any{
		"name": "production", "url": "https://new-target.example.invalid",
		"host_alias": "qa-new-target", "host_ip": "192.0.2.20", "sort_order": 0,
	})
	if moved.StatusCode != 200 {
		t.Fatalf("PUT environment=%d %s", moved.StatusCode, baselineReadBody(moved))
	}
	moved.Body.Close()
	afterMove := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, afterMove)
	if acc.DeploymentTarget != "" {
		t.Fatalf("HTTP destination PUT kept digest: %q", acc.DeploymentTarget)
	}
	blockedMove := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if blockedMove.StatusCode != http.StatusForbidden {
		t.Fatalf("apply after destination PUT=%d %s", blockedMove.StatusCode, baselineReadBody(blockedMove))
	}
	blockedMove.Body.Close()

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

func TestReleaseAcceptanceHTTPPharosTargetAuthority(t *testing.T) {
	ts := newTestServer(t)
	projectID := responseID(t, ts.post(t, "/api/projects", ts.adminCookie, map[string]string{"name": "Accept pharos HTTP", "key": "APH"}))
	memberID, externalID := seedAcceptancePeople(t, projectID)
	acc := mintFromBuiltReceipt(t, ts, projectID)
	cfg := putAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID),
		configureBody(memberID, externalID, acc.Revision, releaseacceptance.ModeCustomerOperated))
	if cfg.StatusCode != 200 {
		t.Fatalf("configure=%d %s", cfg.StatusCode, baselineReadBody(cfg))
	}
	acc = readAcceptance(t, cfg)
	_ = insertHTTPProjectEnv(t, projectID, "production")
	regID, apiKeyID, userID, deliveryKey := seedHTTPPharosOwner(t, projectID, acc.Release.BatchID, "production")

	listed := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, listed)
	if acc.DeploymentTarget != "" {
		t.Fatalf("pharos existence auto-bound over HTTP: %q", acc.DeploymentTarget)
	}
	found := false
	for _, c := range acc.TargetCandidates {
		if c.Kind == releaseacceptance.TargetKindPharosOwner && c.RegistrationID == regID {
			found = true
		}
	}
	if !found {
		t.Fatalf("HTTP candidates missing live pharos owner: %+v", acc.TargetCandidates)
	}

	wrongID := seedHTTPPharosOnNewDelivery(t, projectID)
	wrong := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/deployment-target", projectID, acc.Release.ID),
		map[string]any{"kind": "pharos_owner", "registration_id": wrongID})
	if wrong.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong delivery bind=%d %s", wrong.StatusCode, baselineReadBody(wrong))
	}
	wrong.Body.Close()

	bound := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/deployment-target", projectID, acc.Release.ID),
		map[string]any{"kind": "pharos_owner", "registration_id": regID})
	if bound.StatusCode != 200 {
		t.Fatalf("pharos bind=%d %s", bound.StatusCode, baselineReadBody(bound))
	}
	acc = readAcceptance(t, bound)
	if acc.DeploymentTarget == "" || acc.DeploymentTargetKind != releaseacceptance.TargetKindPharosOwner {
		t.Fatalf("HTTP pharos bind identity=%+v", acc)
	}
	boundRef := acc.DeploymentTarget

	approved := postAcceptance(t, ts, ts.adminCookie, fmt.Sprintf("/api/projects/%d/acceptance-standing-policies", projectID), map[string]any{
		"policy_ref": "policy_pharos_http", "content_digest": acc.Release.ContentDigest, "revision_seal": acc.Release.RevisionSeal,
		"parties": []string{"party_delivery", "party_customer"}, "agreement_ref": "SOW-9",
		"gaps":        []map[string]string{{"gap_ref": "gap_backup", "statement": "Backup restore not proven for this target."}},
		"bounded_use": "Same approved baseline implementation updates only.", "expires_at": "2026-12-01T00:00:00Z",
		"release_channel": acc.Release.ReleaseChannel, "artifact_digest": acc.Release.ArtifactDigest,
		"target_ref": boundRef, "model_ref": "customer_operated",
	})
	if approved.StatusCode != 200 {
		t.Fatalf("approve pharos=%d %s", approved.StatusCode, baselineReadBody(approved))
	}
	var policy releaseacceptance.StandingPolicy
	decode(t, approved, &policy)

	applied := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if applied.StatusCode != 200 {
		t.Fatalf("apply pharos=%d %s", applied.StatusCode, baselineReadBody(applied))
	}
	applied.Body.Close()

	if _, err := db.DB.Exec(`UPDATE api_keys SET disabled_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`, apiKeyID); err != nil {
		t.Fatal(err)
	}
	afterDisable := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, afterDisable)
	if acc.DeploymentTarget != "" {
		t.Fatalf("disabled pharos key kept HTTP bind: %q", acc.DeploymentTarget)
	}
	blockedKey := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if blockedKey.StatusCode != http.StatusForbidden {
		t.Fatalf("apply after disabled key=%d %s", blockedKey.StatusCode, baselineReadBody(blockedKey))
	}
	blockedKey.Body.Close()

	stage, err := externalstage.NewService(db.DB, externalstage.Options{FixtureDigest: contracts.ExternalStageV1FixtureDigest()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stage.RevokeReporter(context.Background(), externalstage.Principal{
		UserID: userIDByUsername(t, "admin"), Kind: "session", SessionCredentialID: adminSessionCredential(t),
	}, deliveryKey, "revoke-http-pharos", regID); err != nil {
		t.Fatal(err)
	}
	afterRevoke := ts.get(t, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance", projectID, acc.Release.ID), ts.adminCookie)
	acc = readAcceptance(t, afterRevoke)
	if acc.DeploymentTarget != "" {
		t.Fatalf("revoked pharos fell back over HTTP: %q", acc.DeploymentTarget)
	}
	blocked := postAcceptance(t, ts, ts.memberCookie, fmt.Sprintf("/api/projects/%d/release-records/%d/acceptance/apply-policy", projectID, acc.Release.ID),
		map[string]any{"policy_id": policy.ID})
	if blocked.StatusCode != http.StatusForbidden {
		t.Fatalf("apply after pharos revoke=%d %s", blocked.StatusCode, baselineReadBody(blocked))
	}
	blocked.Body.Close()
	_ = userID
}
