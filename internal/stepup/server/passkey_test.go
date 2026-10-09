// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const testOrigin = "https://aeon.example"

type options struct {
	ID  string `json:"challenge_id"`
	Key struct {
		Challenge string `json:"challenge"`
		UV        string `json:"userVerification"`
	} `json:"publicKey"`
}

func rawJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	f.m.Mount(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(string(rawJSON(t, body))))
	r.Header.Set("Origin", testOrigin)
	r = r.WithContext(authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.d.App))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func assertion(t *testing.T, key *ecdsa.PrivateKey, credentialID []byte, c options, uv bool, origin, rp string) json.RawMessage {
	t.Helper()
	client := rawJSON(t, map[string]any{"type": "webauthn.get", "challenge": c.Key.Challenge, "origin": origin, "crossOrigin": false})
	rpHash := sha256.Sum256([]byte(rp))
	auth := append([]byte{}, rpHash[:]...)
	flag := byte(1)
	if uv {
		flag |= 4
	}
	auth = append(auth, flag)
	counter := make([]byte, 4)
	binary.BigEndian.PutUint32(counter, 1)
	auth = append(auth, counter...)
	clientHash := sha256.Sum256(client)
	signed := sha256.Sum256(append(append([]byte{}, auth...), clientHash[:]...))
	signature, err := ecdsa.SignASN1(rand.Reader, key, signed[:])
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	return rawJSON(t, map[string]any{"id": enc(credentialID), "rawId": enc(credentialID), "type": "public-key", "authenticatorAttachment": "platform", "response": map[string]any{"clientDataJSON": enc(client), "authenticatorData": enc(auth), "signature": enc(signature), "userHandle": nil}})
}

// Risk: an unverified, replayed or differently bound assertion applies a change.
func TestStepupPasskeyProofBindingAndHTTPBoundary(t *testing.T) {
	for _, attack := range []string{"no UV", "wrong origin", "wrong RP", "wrong digest", "wrong person", "expired challenge", "OIDC challenge as passkey", "revoked passkey", "agent", "no browser", "valid"} {
		t.Run(attack, func(t *testing.T) {
			f := setup(t)
			f.m.phone = phoneapprovals.New(f.d.App, nil, testOrigin, nil, nil)
			f.m.phone.StepUpReviewTx = f.m.phoneReviewTx
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			credentialID := []byte("stepup-fixture")
			pub, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: key.X.FillBytes(make([]byte, 32)), -3: key.Y.FillBytes(make([]byte, 32))})
			if err != nil {
				t.Fatal(err)
			}
			credential := webauthn.Credential{ID: credentialID, PublicKey: pub, Flags: webauthn.NewCredentialFlags(protocol.FlagUserPresent | protocol.FlagUserVerified), Authenticator: webauthn.Authenticator{Attachment: protocol.Platform}}
			if _, err = f.d.Admin.Exec(t.Context(), `INSERT INTO phone_passkeys(tenant_id,person_id,credential_id,credential)VALUES($1,$2,$3,$4)`, f.person.TenantID, f.person.ID, base64.RawURLEncoding.EncodeToString(credentialID), rawJSON(t, credential)); err != nil {
				t.Fatal(err)
			}
			r := f.create(t)
			phoneMux := http.NewServeMux()
			f.m.phone.Mount(phoneMux)
			phoneRead := func(p tenant.Principal) *httptest.ResponseRecorder {
				req := httptest.NewRequest("GET", "/api/phone-approvals/stepup/"+r.ID, nil)
				req = req.WithContext(authz.BindPool(tenant.WithPrincipal(t.Context(), p), f.d.App))
				response := httptest.NewRecorder()
				phoneMux.ServeHTTP(response, req)
				return response
			}
			view := phoneRead(f.person)
			var review phoneapprovals.Review
			if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &review) != nil || review.Kind != "stepup" || review.ID != r.ID || review.Hash != r.Digest || !review.Pending || len(review.StepUp) == 0 {
				t.Fatalf("phone card %d %s", view.Code, view.Body.String())
			}
			if forbidden := phoneRead(f.agent); forbidden.Code != 403 {
				t.Fatalf("agent phone review %d", forbidden.Code)
			}
			w := f.call(t, f.person, "POST", "/api/stepup-requests/"+r.ID+"/options", input(r))
			if w.Code != 200 {
				t.Fatalf("options %d %s", w.Code, w.Body.String())
			}
			var c options
			if json.Unmarshal(w.Body.Bytes(), &c) != nil || c.Key.UV != "required" {
				t.Fatal("no user verification")
			}
			challenge, err := base64.RawURLEncoding.DecodeString(c.Key.Challenge)
			if err != nil || !strings.Contains(string(challenge), r.ID+":"+r.Digest) {
				t.Fatal("challenge unbound")
			}
			p := f.person
			origin, rp := testOrigin, "aeon.example"
			uv := attack != "no UV"
			if attack == "wrong origin" {
				origin = "https://other.example"
			}
			if attack == "wrong RP" {
				rp = "other.example"
			}
			proof := Approve{Decide: input(r), Proof: phoneapprovals.Proof{ChallengeID: c.ID, Credential: assertion(t, key, credentialID, c, uv, origin, rp)}}
			switch attack {
			case "wrong digest":
				proof.Digest = strings.Repeat("0", 64)
			case "wrong person":
				p = f.other
			case "expired challenge":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE phone_approval_challenges SET expires_at=now()-interval '1 second' WHERE id=$1`, c.ID); err != nil {
					t.Fatal(err)
				}
			case "OIDC challenge as passkey":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE phone_approval_challenges SET session_data=$2 WHERE id=$1`, c.ID, rawJSON(t, reauthSession{f.now})); err != nil {
					t.Fatal(err)
				}
			case "revoked passkey":
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE phone_passkeys SET revoked_at=now() WHERE person_id=$1`, f.person.ID); err != nil {
					t.Fatal(err)
				}
			case "agent":
				p = f.agent
				p.FullAccess = true
				p.OwnerWorkstation = true
			case "no browser":
				p.BrowserSession = false
			}
			w = f.call(t, p, "POST", "/api/stepup-requests/"+r.ID+"/approve", proof)
			if attack == "valid" {
				var out ApprovalRequest
				if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.State != "applied" || out.Method == nil || *out.Method != "passkey_platform" {
					t.Fatalf("valid %d %s", w.Code, w.Body.String())
				}
				ended := phoneRead(f.person)
				if ended.Code != 200 || json.Unmarshal(ended.Body.Bytes(), &review) != nil || review.Pending {
					t.Fatal("decided phone card stayed actionable")
				}
				replay := f.call(t, p, "POST", "/api/stepup-requests/"+r.ID+"/approve", proof)
				if replay.Code != 200 {
					t.Fatal("first decision unavailable")
				}
				if _, err := f.d.Admin.Exec(t.Context(), `UPDATE stepup_requests SET state='pending',revision=1 WHERE id=$1`, r.ID); err != nil {
					t.Fatal(err)
				}
				replay = f.call(t, p, "POST", "/api/stepup-requests/"+r.ID+"/approve", proof)
				if replay.Code != 403 {
					t.Fatalf("consumed proof reused %d %s", replay.Code, replay.Body.String())
				}
			} else {
				expected := 403
				if attack == "wrong digest" {
					expected = 409
				}
				if w.Code != expected {
					t.Fatalf("%s %d %s", attack, w.Code, w.Body.String())
				}
				out, err := f.m.Get(t.Context(), f.agent, r.ID)
				if err != nil || out.State != "pending" {
					t.Fatal("invalid proof mutated target")
				}
			}
		})
	}
}

// Risk: OIDC reuses an old sign-in, another person's or expired/consumed state.
func TestStepupReauthOneUseFreshnessAndBinding(t *testing.T) {
	for _, attack := range []string{"old sign-in", "wrong person", "wrong digest", "expired", "valid"} {
		t.Run(attack, func(t *testing.T) {
			f := setup(t)
			r := f.create(t)
			var start ReauthStart
			err := f.m.transaction(t.Context(), f.person, func(tx pgx.Tx) error {
				var err error
				start, err = f.m.startReauth(t.Context(), tx, f.person, r)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(time.Second)
			authTime := f.now
			person := f.person
			switch attack {
			case "old sign-in":
				authTime = start.StartedAt
			case "wrong person":
				person = f.other
			case "wrong digest":
				start.Digest = strings.Repeat("0", 64)
			case "expired":
				f.now = start.StartedAt.Add(ProofLifetime)
			}
			out, err := f.m.FinishReauth(t.Context(), person, start, authTime)
			if attack == "valid" {
				if err != nil || out.State != "applied" || out.Method == nil || *out.Method != "oidc_reauth" {
					t.Fatalf("fresh outcome %+v %v", out, err)
				}
				var count int
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM phone_approval_challenges WHERE id=$1 AND consumed_at IS NOT NULL`, start.ChallengeID).Scan(&count); err != nil || count != 1 {
					t.Fatal("OIDC proof not consumed")
				}
			} else {
				expected := 403
				if attack == "wrong digest" {
					expected = 409
				}
				requireStatus(t, err, expected)
			}
		})
	}
}
