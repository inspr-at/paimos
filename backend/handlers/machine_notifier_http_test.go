// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"github.com/go-chi/chi/v5"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers"
	"github.com/inspr-at/paimos/backend/secretvault"
	"golang.org/x/crypto/ssh"
)

const machineNotifierHTTPBody = "HOSTD-59 paper Gateway notification. Please SendToUser this concise notice to Markus in this existing Grok chat. This is notification only: do not trade, restart anything, or change account settings. Event test-event: controlled test notice"

type machineNotifierHTTPFixture struct {
	base      *testServer
	server    *httptest.Server
	projectID int64
	target    *agentmessage.Target
	csrf      string
}

func newMachineNotifierHTTPFixture(t *testing.T) machineNotifierHTTPFixture {
	t.Helper()
	t.Setenv("PAIMOS_AGENT_BUS_INSTANCE", "ppm")
	t.Setenv("PAIMOS_SECRET_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("PAIMOS_AGENT_BUS_WEBHOOK_HOSTS", "127.0.0.1")
	t.Setenv("PAIMOS_AGENT_BUS_ALLOW_PRIVATE_WEBHOOKS", "true")
	secretvault.ResetForTest()
	t.Cleanup(secretvault.ResetForTest)
	base := newTestServer(t)

	project, err := db.DB.Exec(`INSERT INTO projects(name,key) VALUES('Machine notifier','MNT')`)
	if err != nil {
		t.Fatal(err)
	}
	projectID, _ := project.LastInsertId()
	if _, err := db.DB.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'hostd59'),(?,'amy')`, projectID, projectID); err != nil {
		t.Fatal(err)
	}
	service := agentmessage.NewService(db.DB)
	if err := service.AllowSender(context.Background(), projectID, "grok_bot:amy", "paimos:hostd59"); err != nil {
		t.Fatal(err)
	}
	target, err := service.RegisterTarget(context.Background(), agentmessage.RegisterTargetInput{
		ProjectID: projectID, Address: "grok_bot:amy", Adapter: agentmessage.AdapterGrokBotRoutine,
		TargetKind: agentmessage.TargetKindHTTPSWebhook, TargetRef: "https://127.0.0.1/hook",
		TargetSecret: "crsr_fixture_sender_key_0001", MaximumLevel: "simple", Role: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Route("/api", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(auth.Middleware)
			r.Use(auth.CSRFMiddleware)
			r.Use(auth.MustChangePasswordGate)
			r.With(auth.RequireAdmin).Post("/auth/machine-notifiers", handlers.CreateMachineNotifier)
			r.Get("/auth/api-keys", handlers.ListAPIKeys)
			r.With(auth.RequireAdmin).Post("/auth/api-keys", handlers.CreateAPIKey)
			r.Delete("/auth/api-keys/{id}", handlers.DeleteAPIKey)
			r.Post("/machine-notifier/messages", handlers.SendMachineNotifierMessage)
			r.Get("/machine-notifier/messages/{messageID}/receipt", handlers.GetMachineNotifierReceipt)
			r.Get("/unrelated", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		})
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	var csrf string
	if err := db.DB.QueryRow(`SELECT csrf_token FROM sessions WHERE id=?`, cookieSessionID(base.adminCookie)).Scan(&csrf); err != nil {
		t.Fatal(err)
	}
	return machineNotifierHTTPFixture{base: base, server: server, projectID: projectID, target: target, csrf: csrf}
}

func machineNotifierEnrollmentRequest(f machineNotifierHTTPFixture) map[string]any {
	return map[string]any{
		"name": "HOSTD59", "project_id": f.projectID, "sender": "hostd59", "to": "grok_bot:amy",
		"target_id": f.target.ID, "target_version": f.target.Version,
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}
}

func (f machineNotifierHTTPFixture) request(t *testing.T, method, path, bearer string, body any, headers map[string]string) *http.Response {
	t.Helper()
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func (f machineNotifierHTTPFixture) adminHeaders() map[string]string {
	return map[string]string{"Cookie": f.base.adminCookie, "Origin": f.server.URL, auth.CSRFHeaderName: f.csrf}
}

func enrollMachineNotifier(t *testing.T, f machineNotifierHTTPFixture) (int64, string) {
	t.Helper()
	resp := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", "", machineNotifierEnrollmentRequest(f), f.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("enroll status=%d body=%s", resp.StatusCode, raw)
	}
	var got struct {
		ID  int64  `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || got.ID <= 0 || got.Key == "" {
		t.Fatalf("enrollment=%#v err=%v", got, err)
	}
	return got.ID, got.Key
}

func TestMachineNotifierEnrollmentPlaintextCompatibility(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	request := machineNotifierEnrollmentRequest(f)
	resp := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", "", request, f.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("plaintext enroll status=%d body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("plaintext enrollment cache control=%q", got)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if len(result) != 6 || result["key"] == "" {
		t.Fatalf("plaintext enrollment contract=%v", result)
	}
	if _, ok := result["credential_delivery"]; ok {
		t.Fatalf("plaintext enrollment added delivery metadata: %v", result)
	}
	if _, ok := result["key_age_base64"]; ok {
		t.Fatalf("plaintext enrollment added ciphertext: %v", result)
	}
	binding, ok := result["binding"].(map[string]any)
	if !ok || binding["project_id"] != float64(f.projectID) || binding["sender"] != "hostd59" ||
		binding["to"] != "grok_bot:amy" || binding["target_id"] != f.target.ID ||
		binding["target_version"] != float64(f.target.Version) {
		t.Fatalf("plaintext enrollment binding=%v", result["binding"])
	}
}

func TestMachineNotifierEnrollmentEncryptedAgeDelivery(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	nativeIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	sshPublicKey, sshPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshIdentity, err := agessh.NewEd25519Identity(sshPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	sshAuthorizedKey, err := ssh.NewPublicKey(sshPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	request := machineNotifierEnrollmentRequest(f)
	request["age_recipients"] = []string{nativeIdentity.Recipient().String(), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshAuthorizedKey)))}
	resp := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", "", request, f.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("encrypted enroll status=%d body=%s", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("encrypted enrollment cache control=%q", got)
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if _, exposed := result["key"]; exposed {
		t.Fatalf("encrypted enrollment exposed plaintext key: %v", result)
	}
	if len(result) != 7 || result["credential_delivery"] != "age" {
		t.Fatalf("encrypted enrollment fields=%v", result)
	}
	encoded, ok := result["key_age_base64"].(string)
	if !ok || encoded == "" {
		t.Fatalf("encrypted enrollment omitted ciphertext: %v", result)
	}
	ciphertext, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || !bytes.HasPrefix(ciphertext, []byte("age-encryption.org/v1\n")) {
		t.Fatalf("age ciphertext invalid: prefix=%q err=%v", ciphertext[:min(len(ciphertext), 24)], err)
	}
	var token string
	for _, identity := range []age.Identity{nativeIdentity, sshIdentity} {
		reader, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
		if err != nil {
			t.Fatal(err)
		}
		plaintext, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if len(plaintext) == 0 || plaintext[len(plaintext)-1] != '\n' || bytes.Count(plaintext, []byte("\n")) != 1 {
			t.Fatalf("decrypted credential framing=%q", plaintext)
		}
		decrypted := string(plaintext[:len(plaintext)-1])
		if token == "" {
			token = decrypted
		} else if token != decrypted {
			t.Fatal("recipients decrypted different credentials")
		}
	}

	sent := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": machineNotifierHTTPBody}, map[string]string{"Idempotency-Key": "encrypted-test-event"})
	defer sent.Body.Close()
	if sent.StatusCode != http.StatusCreated {
		t.Fatalf("encrypted credential send status=%d", sent.StatusCode)
	}
	unrelated := f.request(t, http.MethodGet, "/api/unrelated", token, nil, nil)
	_ = unrelated.Body.Close()
	if unrelated.StatusCode != http.StatusForbidden {
		t.Fatalf("encrypted credential unrelated route status=%d", unrelated.StatusCode)
	}

	keyID := int64(result["id"].(float64))
	listed := f.request(t, http.MethodGet, "/api/auth/api-keys", "", nil, f.adminHeaders())
	defer listed.Body.Close()
	var keys []map[string]any
	if err := json.NewDecoder(listed.Body).Decode(&keys); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, key := range keys {
		if int64(key["id"].(float64)) == keyID {
			found = true
			if key["credential_kind"] != "machine_notifier" {
				t.Fatalf("listed credential kind=%v", key)
			}
		}
	}
	if !found {
		t.Fatalf("encrypted credential %d missing from key listing", keyID)
	}
}

func TestMachineNotifierEnrollmentRejectsInvalidAgeRecipientsWithoutPersistence(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	valid := identity.Recipient().String()
	sshPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshAuthorizedKey, err := ssh.NewPublicKey(sshPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sshRecipient := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshAuthorizedKey)))
	cases := map[string]any{
		"null":                    nil,
		"empty":                   []string{},
		"malformed":               []string{"age1-not-a-recipient"},
		"duplicate native":        []string{valid, valid},
		"duplicate ssh canonical": []string{sshRecipient + " first", sshRecipient + " second"},
		"too many":                []string{valid, valid, valid, valid, valid, valid, valid, valid, valid},
		"oversized":               []string{"age1" + strings.Repeat("q", 1025)},
		"unsupported":             []string{"ssh-dss AAAAB3NzaC1kc3MAAACB"},
		"multiple ssh keys":       []string{sshRecipient + "\n" + sshRecipient},
		"wrong type":              valid,
	}
	for name, recipients := range cases {
		t.Run(name, func(t *testing.T) {
			request := machineNotifierEnrollmentRequest(f)
			request["age_recipients"] = recipients
			resp := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", "", request, f.adminHeaders())
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				raw, _ := io.ReadAll(resp.Body)
				t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
			}
			raw, _ := io.ReadAll(resp.Body)
			if bytes.Contains(raw, []byte("paimos_")) {
				t.Fatalf("error exposed credential-shaped data: %s", raw)
			}
			var keys, bindings int
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE credential_kind='machine_notifier'`).Scan(&keys); err != nil {
				t.Fatal(err)
			}
			if err := db.DB.QueryRow(`SELECT COUNT(*) FROM machine_notifier_bindings`).Scan(&bindings); err != nil {
				t.Fatal(err)
			}
			if keys != 0 || bindings != 0 {
				t.Fatalf("invalid recipients persisted keys=%d bindings=%d", keys, bindings)
			}
		})
	}
}

func TestMachineNotifierHTTPContractAndRevocation(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	keyID, token := enrollMachineNotifier(t, f)

	sent := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": machineNotifierHTTPBody}, map[string]string{"Idempotency-Key": "hostd59-test-event"})
	defer sent.Body.Close()
	if sent.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(sent.Body)
		t.Fatalf("send status=%d body=%s", sent.StatusCode, raw)
	}
	var sendResult map[string]any
	if err := json.NewDecoder(sent.Body).Decode(&sendResult); err != nil {
		t.Fatal(err)
	}
	messageID, _ := sendResult["message_id"].(string)
	if len(sendResult) != 1 || messageID == "" {
		t.Fatalf("send result=%#v", sendResult)
	}

	receiptResponse := f.request(t, http.MethodGet, "/api/machine-notifier/messages/"+messageID+"/receipt", token, nil, nil)
	defer receiptResponse.Body.Close()
	if receiptResponse.StatusCode != http.StatusOK {
		t.Fatalf("receipt status=%d", receiptResponse.StatusCode)
	}
	var receipt map[string]any
	if err := json.NewDecoder(receiptResponse.Body).Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	wantFields := []string{"message_id", "project_id", "address", "state", "effective_level", "handed_off_at", "effective_target_id", "effective_target_version"}
	if len(receipt) != len(wantFields) {
		t.Fatalf("receipt fields=%v", receipt)
	}
	for _, field := range wantFields {
		if _, ok := receipt[field]; !ok {
			t.Fatalf("receipt omitted %q: %v", field, receipt)
		}
	}
	if receipt["message_id"] != messageID || receipt["address"] != "grok_bot:amy" || receipt["state"] != "pending" ||
		receipt["effective_target_id"] != f.target.ID {
		t.Fatalf("receipt=%v", receipt)
	}

	spoofed := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": machineNotifierHTTPBody}, map[string]string{
			"Idempotency-Key": "hostd59-spoof", "X-Paimos-Agent-Name": "other",
		})
	_ = spoofed.Body.Close()
	if spoofed.StatusCode != http.StatusBadRequest {
		t.Fatalf("spoof status=%d", spoofed.StatusCode)
	}
	unrelated := f.request(t, http.MethodGet, "/api/unrelated", token, nil, nil)
	_ = unrelated.Body.Close()
	if unrelated.StatusCode != http.StatusForbidden {
		t.Fatalf("unrelated route status=%d", unrelated.StatusCode)
	}
	extraField := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": machineNotifierHTTPBody, "to": "grok_bot:other"},
		map[string]string{"Idempotency-Key": "hostd59-extra-field"})
	_ = extraField.Body.Close()
	if extraField.StatusCode != http.StatusBadRequest {
		t.Fatalf("routing field status=%d", extraField.StatusCode)
	}
	action := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": "Please execute this command"}, map[string]string{"Idempotency-Key": "hostd59-action"})
	_ = action.Body.Close()
	if action.StatusCode != http.StatusBadRequest {
		t.Fatalf("action request status=%d", action.StatusCode)
	}

	revoked := f.request(t, http.MethodDelete, "/api/auth/api-keys/"+strconv.FormatInt(keyID, 10), "", nil, f.adminHeaders())
	_ = revoked.Body.Close()
	if revoked.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke status=%d", revoked.StatusCode)
	}
	var disabled string
	if err := db.DB.QueryRow(`SELECT disabled_at FROM api_keys WHERE id=?`, keyID).Scan(&disabled); err != nil || disabled == "" {
		t.Fatalf("soft revoke disabled_at=%q err=%v", disabled, err)
	}
	denied := f.request(t, http.MethodPost, "/api/machine-notifier/messages", token,
		map[string]string{"body": machineNotifierHTTPBody}, map[string]string{"Idempotency-Key": "hostd59-after-revoke"})
	_ = denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked send status=%d", denied.StatusCode)
	}
}

func TestGeneralAPIKeyKeepsOrdinaryRoutes(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	created := f.request(t, http.MethodPost, "/api/auth/api-keys", "", map[string]any{"name": "general"}, f.adminHeaders())
	defer created.Body.Close()
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create general status=%d", created.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(created.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	token, _ := result["key"].(string)
	ordinary := f.request(t, http.MethodGet, "/api/unrelated", token, nil, nil)
	_ = ordinary.Body.Close()
	if ordinary.StatusCode != http.StatusNoContent {
		t.Fatalf("general key ordinary route status=%d", ordinary.StatusCode)
	}
	enroll := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", token, map[string]any{
		"name": "forbidden", "project_id": f.projectID, "sender": "hostd59", "to": "grok_bot:amy",
		"target_id": f.target.ID, "target_version": f.target.Version,
		"expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	}, nil)
	_ = enroll.Body.Close()
	if enroll.StatusCode != http.StatusForbidden {
		t.Fatalf("general key notifier enrollment status=%d", enroll.StatusCode)
	}
}

func TestMachineNotifierEnrollmentPinsCurrentTarget(t *testing.T) {
	f := newMachineNotifierHTTPFixture(t)
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	request := machineNotifierEnrollmentRequest(f)
	request["target_version"] = f.target.Version + 1
	request["age_recipients"] = []string{identity.Recipient().String()}
	resp := f.request(t, http.MethodPost, "/api/auth/machine-notifiers", "", request, f.adminHeaders())
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong target version status=%d", resp.StatusCode)
	}
	var keys, bindings int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM api_keys WHERE credential_kind='machine_notifier'`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM machine_notifier_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if keys != 0 || bindings != 0 {
		t.Fatalf("invalid encrypted binding persisted keys=%d bindings=%d", keys, bindings)
	}
}
