// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func upgradedWatchFixture(t *testing.T) (*fixture, string, attachwatch.DeviceRequest, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(key.Curve, key.X, key.Y))
	f, bearer, in := watchFixtureWithKey(t, public)
	return f, bearer, in, key
}
func signWatchConsent(t *testing.T, key *ecdsa.PrivateKey, digest, nonce string) string {
	t.Helper()
	raw, err := ecdsa.SignASN1(rand.Reader, key, attachwatch.LocalConsentHash(digest, nonce))
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestEnclaveConsentRejectsWrongKeyNonceReplayAndExpiredApproval(t *testing.T) {
	f, bearer, in, signer := upgradedWatchFixture(t)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	in.Snapshot.Platform = "darwin"
	in.Digest = in.Snapshot.Digest()
	v := requestWatch(t, f, bearer, in)
	var approved attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200), &approved)
	if !attachwatch.LocalAuthNonceValid(approved.LocalAuthNonce) {
		t.Fatal("server challenge missing")
	}
	in.Operation = "poll"
	in.Sequence = 1
	in.ConsentDigest = approved.ConsentDigest
	in.LocalAuthNonce = approved.LocalAuthNonce
	wrong, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	in.LocalAuthSignature = signWatchConsent(t, wrong, in.ConsentDigest, in.LocalAuthNonce)
	f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 403)
	var sessions int
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT count(*) FROM harness_sessions WHERE agent_principal_id=(SELECT principal_id FROM agent_pairing_computers WHERE id=$1)`, in.ComputerID).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("forged signature created session")
	}
	in.LocalAuthNonce = strings.Repeat("f", 64)
	in.LocalAuthSignature = signWatchConsent(t, signer, in.ConsentDigest, in.LocalAuthNonce)
	f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 403)
	in.LocalAuthNonce = approved.LocalAuthNonce
	in.LocalAuthSignature = signWatchConsent(t, signer, in.ConsentDigest, in.LocalAuthNonce)
	var active attachwatch.View
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 200), &active)
	if active.State != "active" || active.LocalAuthNonce != "" || active.SessionID == nil {
		t.Fatal("valid signature did not consume challenge and activate")
	}
	in.Sequence++
	f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 403)
	// The same signature cannot transfer to another request with the same process.
	in.Operation = "request"
	in.RequestID = uuid(t, f.db)
	in.LocalAuthSignature = ""
	in.LocalAuthNonce = ""
	in.ConsentDigest = ""
	in.Sequence = 0
	v = requestWatch(t, f, bearer, in)
	decodeResult(t, f.call("POST", "/api/agent-pairing/attach/"+in.RequestID+"/approve", map[string]string{"request_digest": v.Digest, "consent_digest": v.ConsentDigest}, true, "", 200), &approved)
	in.Operation = "poll"
	in.Sequence = 1
	in.ConsentDigest = approved.ConsentDigest
	in.LocalAuthNonce = approved.LocalAuthNonce
	in.LocalAuthSignature = signWatchConsent(t, signer, active.ConsentDigest, in.LocalAuthNonce)
	f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 403)
	in.LocalAuthSignature = signWatchConsent(t, signer, in.ConsentDigest, in.LocalAuthNonce)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	f.call("POST", "/api/agent-pairing/attach", in, false, bearer, 410)
}

func TestLegacyPairingUsesAeonApprovalDespiteCapabilityReport(t *testing.T) {
	f, bearer, in := watchFixture(t)
	setWatchMode(f, attachwatch.ConsentLocalAuth)
	f.call("POST", "/api/agent-pairing/attach", attachwatch.DeviceRequest{Operation: "register", AttachProtocol: attachwatch.Protocol, ComputerID: in.ComputerID, DeviceProof: in.DeviceProof, PollKey: in.PollKey, LocalAuthCapability: attachwatch.LocalAuthAvailable}, false, bearer, 200)
	v := activateWatch(t, f, bearer, &in)
	if v.ConsentMode != attachwatch.ConsentAeon {
		t.Fatal("legacy computer gained local confirmation authority")
	}
	// Registration cannot add or replace a pairing key, even with lifecycle proof.
	f.call("POST", "/api/agent-pairing/attach", map[string]any{"operation": "register", "computer_id": in.ComputerID, "device_proof": in.DeviceProof, "poll_key": in.PollKey, "local_auth_public_key": "pretend"}, false, bearer, 400)
}

func TestPairingKeyRejectsMalformedKeysLinuxAndChangesAfterReview(t *testing.T) {
	f := newFixture(t)
	p := f.propose("codex")
	p.request["local_auth_public_key"] = "not-a-P256-key"
	f.call("POST", "/api/agent-pairing/device", p.request, false, "", 400)
	signer, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(signer.Curve, signer.X, signer.Y))
	p.request["local_auth_public_key"] = public
	f.call("POST", "/api/agent-pairing/device", p.request, false, "", 409)
	p.request["platform"] = "linux"
	f.call("POST", "/api/agent-pairing/device", p.request, false, "", 400)
}

func TestAddHarnessPreservesPairingKeyAndCannotReplaceIt(t *testing.T) {
	f := newFixture(t)
	signer, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	public := base64.StdEncoding.EncodeToString(elliptic.Marshal(signer.Curve, signer.X, signer.Y))
	p := f.proposePlatformKey("darwin", "arm64", public, "codex")
	f.approve(p, "connect_only")
	v := f.redeem(p)
	q := &proposal{id: uuid(t, f.db), device: nonce(), runtime: p.runtime, lifecycle: p.lifecycle}
	q.request = map[string]any{}
	for field, value := range p.request {
		q.request[field] = value
	}
	q.request["request_id"] = q.id
	q.request["device_hash"] = hash(q.device)
	q.request["existing_computer_id"] = *v.ComputerID
	q.request["existing_lifecycle_secret"] = p.lifecycle
	q.request["accounts"] = []map[string]string{{"account_key": "claude-add", "harness": "claude", "label": "Chosen Claude account", "model_profile_id": f.profiles["claude"]}}
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	q.request["local_auth_public_key"] = base64.StdEncoding.EncodeToString(elliptic.Marshal(other.Curve, other.X, other.Y))
	f.call("POST", "/api/agent-pairing/device", q.request, false, "", 409)
	q.request["local_auth_public_key"] = public
	f.submit(q)
	f.approve(q, "connect_only")
	added := f.redeem(q)
	var pinned string
	if *added.ComputerID != *v.ComputerID || f.db.Admin.QueryRow(t.Context(), `SELECT local_auth_public_key FROM agent_pairing_computers WHERE id=$1`, *v.ComputerID).Scan(&pinned) != nil || pinned != public {
		t.Fatal("Add harness replaced the browser-pinned key")
	}
}

// Reapply the upgrade to populated old-schema fixtures as the non-bypass app
// owner. This catches a migration silently updating zero rows under FORCE RLS.
func TestEnclaveMigrationEndsLegacyStrictWatchesUnderForcedRLS(t *testing.T) {
	f, bearer, in := watchFixture(t)
	strict := activateWatch(t, f, bearer, &in)
	preserved := in
	preserved.RequestID = uuid(t, f.db)
	preserved.Snapshot.Process.PID++
	preserved.Digest = preserved.Snapshot.Digest()
	preserved.Operation = "request"
	preserved.Sequence = 0
	preserved.ConsentDigest = ""
	aeon := activateWatch(t, f, bearer, &preserved)
	if _, err := f.db.Admin.Exec(t.Context(), `UPDATE harness_attach_requests SET consent_mode='local_auth' WHERE id=$1`, strict.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Admin.Exec(t.Context(), `ALTER TABLE agent_pairing_computers DROP COLUMN local_auth_public_key; ALTER TABLE harness_attach_requests DROP COLUMN local_auth_nonce`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../db/migrations/1046_enclave_consent.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.App.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err := tx.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state, phase string
	var stopped bool
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT a.state,s.phase,s.stopped_at IS NOT NULL FROM harness_attach_requests a JOIN harness_sessions s ON s.tenant_id=a.tenant_id AND s.id=a.session_id WHERE a.id=$1`, strict.RequestID).Scan(&state, &phase, &stopped); err != nil || state != "detached" || phase != "stopped" || !stopped {
		t.Fatal("old strict watch survived migration")
	}
	if err := f.db.Admin.QueryRow(t.Context(), `SELECT state FROM harness_attach_requests WHERE id=$1`, aeon.RequestID).Scan(&state); err != nil || state != "active" {
		t.Fatal("migration ended an Aeon watch")
	}
}
