// SPDX-License-Identifier: AGPL-3.0-only
package phoneapprovals

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const testOrigin = "https://aeon.example"

type fixture struct {
	db              *dbtest.DB
	m               *Module
	mux             *http.ServeMux
	p, other, agent tenant.Principal
	key             *ecdsa.PrivateKey
	credentialID    []byte
	keyID           string
	counter         uint32
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func fixtureFor(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{db: dbtest.Open(t), mux: http.NewServeMux(), credentialID: []byte("test-phone-credential")}
	var tenantID string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('phone-test','Phone test') RETURNING id::text`).Scan(&tenantID); err != nil {
		t.Fatal(err)
	}
	person := func(name string, kind tenant.PrincipalKind) tenant.Principal {
		p := tenant.Principal{TenantID: tenantID, Kind: kind, Name: name, Roles: []string{"admin"}}
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,$2,$3,$4) RETURNING id::text`, tenantID, string(kind), name, p.Roles).Scan(&p.ID); err != nil {
			t.Fatal(err)
		}
		dbtest.BindRole(t, f.db, tenantID, p.ID, "admin")
		return p
	}
	f.p, f.other, f.agent = person("Owner", tenant.Person), person("Other", tenant.Person), person("Agent", tenant.Agent)
	var err error
	f.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.m = New(f.db.App, nil, testOrigin, make([]byte, 32), &webpush.Options{VAPIDPublicKey: "test-public"})
	f.m.Mount(f.mux)
	f.keyID = f.addKey(t, f.p)
	return f
}
func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Admin.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) publicKey(t *testing.T) []byte {
	t.Helper()
	b, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: f.key.X.FillBytes(make([]byte, 32)), -3: f.key.Y.FillBytes(make([]byte, 32))})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (f *fixture) addKey(t *testing.T, p tenant.Principal) string {
	t.Helper()
	credentialID := f.credentialID
	if p.ID != f.p.ID {
		credentialID = []byte(p.ID)
	}
	c := webauthn.Credential{ID: credentialID, PublicKey: f.publicKey(t), Flags: webauthn.NewCredentialFlags(protocol.FlagUserPresent | protocol.FlagUserVerified), Authenticator: webauthn.Authenticator{Attachment: protocol.Platform}}
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO phone_passkeys(tenant_id,person_id,credential_id,credential) VALUES($1,$2,$3,$4) RETURNING id::text`, p.TenantID, p.ID, base64.RawURLEncoding.EncodeToString(c.ID), mustJSON(t, c)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *fixture) request(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,rationale,expires_at) VALUES($1,$2,$2,'nodes.read','tenant','Read workspace nodes',now()+interval '2 hours') RETURNING id::text`, f.p.TenantID, f.agent.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, origin string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(mustJSON(t, body)))
	}
	r := httptest.NewRequest(method, path, reader)
	r.Header.Set("Origin", origin)
	r = r.WithContext(authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.db.App))
	w := httptest.NewRecorder()
	f.mux.ServeHTTP(w, r)
	return w
}
func status(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body.String())
	}
}

type ceremony struct {
	ID  string `json:"challenge_id"`
	Key struct {
		Challenge string `json:"challenge"`
		UV        string `json:"userVerification"`
	} `json:"publicKey"`
}

func (f *fixture) options(t *testing.T, id string) (Decision, ceremony) {
	t.Helper()
	w := f.call(t, f.p, "GET", "/api/phone-approvals/approval/"+id, nil, testOrigin)
	status(t, w, 200)
	var review Review
	if err := json.Unmarshal(w.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	d := Decision{Decision: "approved", Hash: review.Hash, Reason: "Reviewed on phone"}
	w = f.call(t, f.p, "POST", "/api/phone-approvals/approval/"+id+"/options", d, testOrigin)
	status(t, w, 200)
	var c ceremony
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if c.Key.UV != "required" {
		t.Fatal("user verification not required")
	}
	challenge, err := base64.RawURLEncoding.DecodeString(c.Key.Challenge)
	if err != nil || !strings.Contains(string(challenge), id+":"+d.Hash) {
		t.Fatal("challenge does not contain request ID and hash")
	}
	return d, c
}
func (f *fixture) assertion(t *testing.T, c ceremony, uv bool, origin, rp string) json.RawMessage {
	t.Helper()
	client := mustJSON(t, map[string]any{"type": "webauthn.get", "challenge": c.Key.Challenge, "origin": origin, "crossOrigin": false})
	rpHash := sha256.Sum256([]byte(rp))
	auth := append([]byte{}, rpHash[:]...)
	flag := byte(1)
	if uv {
		flag |= 4
	}
	f.counter++
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, f.counter)
	auth = append(auth, flag)
	auth = append(auth, count...)
	clientHash := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, f.key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return mustJSON(t, map[string]any{"id": enc(f.credentialID), "rawId": enc(f.credentialID), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": enc(client), "authenticatorData": enc(auth), "signature": enc(signature), "userHandle": nil}})
}
func TestPhoneDecisionCryptographyAndBinding(t *testing.T) {
	f := fixtureFor(t)
	for _, name := range []string{"missing proof", "no user verification", "wrong origin", "wrong RP", "changed reason", "changed decision", "wrong request", "wrong person", "expired", "revoked", "permission lost"} {
		t.Run(name, func(t *testing.T) {
			f.exec(t, `DELETE FROM phone_approval_limits`)
			id := f.request(t)
			d, c := f.options(t, id)
			proof := DecisionProof{Decision: d, Proof: Proof{ChallengeID: c.ID, Credential: f.assertion(t, c, true, testOrigin, "aeon.example")}}
			p := f.p
			target := id
			switch name {
			case "missing proof":
				proof.Proof = Proof{}
			case "no user verification":
				proof.Credential = f.assertion(t, c, false, testOrigin, "aeon.example")
			case "wrong origin":
				proof.Credential = f.assertion(t, c, true, "https://other.example", "aeon.example")
			case "wrong RP":
				proof.Credential = f.assertion(t, c, true, testOrigin, "other.example")
			case "changed reason":
				proof.Reason = "Different reason"
			case "changed decision":
				proof.Decision.Decision = "denied"
			case "wrong request":
				target = f.request(t)
			case "wrong person":
				p = f.other
			case "expired":
				f.exec(t, `UPDATE phone_approval_challenges SET expires_at=now()-interval '1 second' WHERE id=$1`, c.ID)
			case "revoked":
				f.exec(t, `UPDATE phone_passkeys SET revoked_at=now() WHERE id=$1`, f.keyID)
			case "permission lost":
				dbtest.BindRole(t, f.db, f.p.TenantID, f.p.ID, "member")
			}
			w := f.call(t, p, "POST", "/api/phone-approvals/approval/"+target+"/decision", proof, testOrigin)
			if w.Code != 403 && w.Code != 409 {
				t.Fatalf("bad proof accepted: %d %s", w.Code, w.Body.String())
			}
			var n int
			if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM approval_decisions`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid proof changed decisions: %d %v", n, err)
			}
			f.exec(t, `UPDATE phone_passkeys SET revoked_at=NULL WHERE id=$1`, f.keyID)
			dbtest.BindRole(t, f.db, f.p.TenantID, f.p.ID, "admin")
		})
	}
	f.exec(t, `DELETE FROM phone_approval_limits`)
	id := f.request(t)
	d, c := f.options(t, id)
	proof := DecisionProof{Decision: d, Proof: Proof{ChallengeID: c.ID, Credential: f.assertion(t, c, true, testOrigin, "aeon.example")}}
	status(t, f.call(t, f.p, "POST", "/api/phone-approvals/approval/"+id+"/decision", proof, testOrigin), 200)
	status(t, f.call(t, f.p, "POST", "/api/phone-approvals/approval/"+id+"/decision", proof, testOrigin), 409)
	var grants, verified int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM agent_permission_grants WHERE approval_request_id=$1),(SELECT count(*) FROM events WHERE type='phone_approval.verified' AND after->>'request_id'=$1::text)`, id).Scan(&grants, &verified); err != nil || grants != 1 || verified != 1 {
		t.Fatalf("grant/audit not atomic: %d %d %v", grants, verified, err)
	}
	deniedID := f.request(t)
	d, c = f.options(t, deniedID)
	d.Decision = "denied"
	w := f.call(t, f.p, "POST", "/api/phone-approvals/approval/"+deniedID+"/options", d, testOrigin)
	status(t, w, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	proof = DecisionProof{Decision: d, Proof: Proof{ChallengeID: c.ID, Credential: f.assertion(t, c, true, testOrigin, "aeon.example")}}
	status(t, f.call(t, f.p, "POST", "/api/phone-approvals/approval/"+deniedID+"/decision", proof, testOrigin), 200)
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM agent_permission_grants WHERE approval_request_id=$1`, deniedID).Scan(&grants); err != nil || grants != 0 {
		t.Fatal("denial granted permission", err)
	}
}
func TestPhonePersonTenantOriginAndRevocation(t *testing.T) {
	f := fixtureFor(t)
	id := f.request(t)
	for _, path := range []string{"/api/me/phone-approvals", "/api/phone-approvals/approval/" + id} {
		status(t, f.call(t, f.agent, "GET", path, nil, testOrigin), 403)
	}
	status(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/passkeys/options", nil, "https://attacker.example"), 403)
	foreign := f.p
	if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('foreign','Foreign') RETURNING id::text`).Scan(&foreign.TenantID); err != nil {
		t.Fatal(err)
	}
	status(t, f.call(t, foreign, "GET", "/api/phone-approvals/approval/"+id, nil, testOrigin), 403)
	var count int
	if err := db.InTenant(t.Context(), f.db.App, foreign.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM phone_passkeys`).Scan(&count)
	}); err != nil || count != 0 {
		t.Fatal("RLS leaked passkeys", err)
	}
	status(t, f.call(t, f.other, "DELETE", "/api/me/phone-approvals/passkeys/"+f.keyID, nil, testOrigin), 404)
	_, c := f.options(t, id)
	status(t, f.call(t, f.p, "DELETE", "/api/me/phone-approvals/passkeys/"+f.keyID, nil, testOrigin), 204)
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM phone_approval_challenges WHERE id=$1 AND consumed_at IS NULL`, c.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("revocation left a usable challenge", err)
	}
	f.exec(t, `UPDATE phone_approval_limits SET attempts=30 WHERE person_id=$1`, f.p.ID)
	w := f.call(t, f.p, "POST", "/api/me/phone-approvals/passkeys/options", nil, testOrigin)
	status(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("no retry hint")
	}
}
func TestPhoneQuietHoursAndSubscriptionValidation(t *testing.T) {
	date := time.Date(2026, 10, 1, 22, 0, 0, 0, time.UTC)
	p := Preferences{Zone: "UTC", Start: 22 * 60, End: 7 * 60, Escalation: 15}
	if !quiet(p, date) || quiet(p, date.Add(9*time.Hour)) || !quiet(p, date.Add(8*time.Hour)) {
		t.Fatal("overnight quiet hours")
	}
	p.Start, p.End = 10*60, 12*60
	if !quiet(p, date.Add(-11*time.Hour)) || quiet(p, date.Add(-10*time.Hour)) {
		t.Fatal("daytime quiet hours")
	}
	p.Start = p.End
	if quiet(p, date) {
		t.Fatal("equal endpoints should disable quiet hours")
	}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sub := webpush.Subscription{Endpoint: "https://web.push.apple.com/test", Keys: webpush.Keys{P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}}
	if !validSubscription(sub) {
		t.Fatal("valid subscription refused")
	}
	for _, endpoint := range []string{"http://web.push.apple.com/test", "https://127.0.0.1/test", "https://web.push.apple.com.attacker.example/test", "https://web.push.apple.com:443/test", "https://a@web.push.apple.com/test"} {
		sub.Endpoint = endpoint
		if validSubscription(sub) {
			t.Fatal("unsafe push endpoint accepted")
		}
	}
}

func TestPhoneOriginAndVAPIDContact(t *testing.T) {
	options := &webpush.Options{Subscriber: "mailto:ops@example.test"}
	m := New(nil, nil, testOrigin, make([]byte, 32), options)
	if m.wa == nil || m.vapid == nil || m.vapid.Subscriber != "ops@example.test" || options.Subscriber != "mailto:ops@example.test" {
		t.Fatal("VAPID contact normalization changed configuration or ceremony availability")
	}
	if New(nil, nil, "http://remote.example", make([]byte, 32), options).wa != nil {
		t.Fatal("insecure remote origin enabled passkeys")
	}
	if New(nil, nil, testOrigin, nil, options).vapid != nil {
		t.Fatal("push enabled without a vault key")
	}
	if err := validateDecision(Decision{Decision: "approved", Hash: strings.Repeat("g", 64)}); err == nil {
		t.Fatal("nonhex content hash accepted")
	}
	if err := validateDecision(Decision{Decision: "approved", Hash: strings.Repeat("a", 64), Reason: strings.Repeat("a", 4001)}); err == nil {
		t.Fatal("overlong reason accepted")
	}
	aad := subscriptionAAD("tenant-a", "person-a")
	ciphertext, err := seal(m.vault, []byte("test subscription"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := open(m.vault, ciphertext, aad); err != nil || string(plain) != "test subscription" {
		t.Fatal("subscription encryption did not round-trip")
	}
	for _, wrong := range [][]byte{subscriptionAAD("tenant-b", "person-a"), subscriptionAAD("tenant-a", "person-b")} {
		if _, err := open(m.vault, ciphertext, wrong); err == nil {
			t.Fatal("subscription ciphertext crossed a tenant or person")
		}
	}
}

func TestPhoneRegistrationRequiresVerifiedPlatformCeremony(t *testing.T) {
	f := fixtureFor(t)
	f.exec(t, `DELETE FROM phone_passkeys`)
	for _, uv := range []bool{false, true} {
		w := f.call(t, f.p, "POST", "/api/me/phone-approvals/passkeys/options", nil, testOrigin)
		status(t, w, 200)
		var c ceremony
		if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		client := mustJSON(t, map[string]any{"type": "webauthn.create", "challenge": c.Key.Challenge, "origin": testOrigin})
		hash := sha256.Sum256([]byte("aeon.example"))
		flags := byte(0x41)
		if uv {
			flags |= 4
		}
		auth := append([]byte{}, hash[:]...)
		auth = append(auth, flags, 0, 0, 0, 0)
		auth = append(auth, make([]byte, 16)...)
		size := make([]byte, 2)
		binary.BigEndian.PutUint16(size, uint16(len(f.credentialID)))
		auth = append(auth, size...)
		auth = append(auth, f.credentialID...)
		auth = append(auth, f.publicKey(t)...)
		attestation, err := cbor.Marshal(map[string]any{"fmt": "none", "authData": auth, "attStmt": map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		enc := base64.RawURLEncoding.EncodeToString
		credential := mustJSON(t, map[string]any{"id": enc(f.credentialID), "rawId": enc(f.credentialID), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": enc(client), "attestationObject": enc(attestation), "transports": []string{"internal"}}})
		proof := Proof{ChallengeID: c.ID, Credential: credential}
		w = f.call(t, f.p, "POST", "/api/me/phone-approvals/passkeys", proof, testOrigin)
		if !uv {
			status(t, w, 403)
		} else {
			status(t, w, 200)
			status(t, f.call(t, f.p, "POST", "/api/me/phone-approvals/passkeys", proof, testOrigin), 403)
		}
	}
}

func TestPhonePushPointerQuietEscalationAndGone(t *testing.T) {
	f := fixtureFor(t)
	f.addKey(t, f.other)
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []tenant.Principal{f.p, f.other} {
		sub := webpush.Subscription{Endpoint: "https://web.push.apple.com/test/" + p.ID, Keys: webpush.Keys{P256dh: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}}
		w := f.call(t, p, "POST", "/api/me/phone-approvals/subscriptions", sub, testOrigin)
		status(t, w, 200)
		status(t, f.call(t, p, "PUT", "/api/me/phone-approvals/settings", Preferences{Enabled: true, Zone: "UTC", Escalation: 5}, testOrigin), 200)
		var encrypted []byte
		if err := f.db.Admin.QueryRow(t.Context(), `SELECT subscription FROM phone_push_subscriptions WHERE person_id=$1`, p.ID).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encrypted), sub.Endpoint) {
			t.Fatal("plaintext subscription storage")
		}
		status(t, f.call(t, p, "POST", "/api/me/phone-approvals/subscriptions", sub, testOrigin), 200)
		other := f.other
		if p.ID == f.other.ID {
			other = f.p
		}
		status(t, f.call(t, other, "POST", "/api/me/phone-approvals/subscriptions", sub, testOrigin), 409)
	}
	newAged := func() string {
		var id string
		if err := f.db.Admin.QueryRow(t.Context(), `INSERT INTO approval_requests(tenant_id,proposed_by_principal_id,agent_principal_id,scope,resource_kind,rationale,proposed_at,expires_at) VALUES($1,$2,$2,'nodes.read','tenant','Review required',now()-interval '10 minutes',now()+interval '1 hour') RETURNING id::text`, f.p.TenantID, f.agent.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := newAged()
	sent := 0
	code := 201
	f.m.send = func(_ context.Context, b []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var pointer map[string]string
		if err := json.Unmarshal(b, &pointer); err != nil {
			t.Fatal(err)
		}
		if len(pointer) != 1 || !strings.HasPrefix(pointer["url"], "/phone-approvals/approval/") {
			t.Fatal("push carried more than a review pointer")
		}
		sent++
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err := f.m.dispatch(t.Context()); err != nil || sent != 2 {
		t.Fatalf("escalation: sent=%d %v", sent, err)
	}
	if err := f.m.dispatch(t.Context()); err != nil || sent != 2 {
		t.Fatalf("duplicate send: sent=%d %v", sent, err)
	}
	var count int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM approval_decisions WHERE request_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("push approved a request", err)
	}
	// A quiet first recipient still permits the next person's timed escalation.
	first := f.p
	if f.other.ID < first.ID {
		first = f.other
	}
	f.exec(t, `UPDATE phone_approval_preferences SET quiet_start=0,quiet_end=1439 WHERE person_id=$1`, first.ID)
	newAged()
	if err := f.m.dispatch(t.Context()); err != nil || sent != 3 {
		t.Fatalf("quiet escalation: sent=%d %v", sent, err)
	}
	code = 410
	newAged()
	if err := f.m.dispatch(t.Context()); err != nil || sent != 4 {
		t.Fatalf("expired subscription: sent=%d %v", sent, err)
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM phone_push_subscriptions WHERE revoked_at IS NOT NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatal("gone endpoint not revoked", err)
	}
	// A person who loses decision permission gets no notification even when subscribed.
	f.exec(t, `UPDATE phone_approval_preferences SET quiet_start=0,quiet_end=0`)
	dbtest.BindRole(t, f.db, first.TenantID, first.ID, "member")
	newAged()
	if err := f.m.dispatch(t.Context()); err != nil || sent != 4 {
		t.Fatalf("notification leaked after permission loss: sent=%d %v", sent, err)
	}
}
