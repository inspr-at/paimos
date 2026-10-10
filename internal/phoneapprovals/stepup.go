// SPDX-License-Identifier: AGPL-3.0-only
package phoneapprovals

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// StepUpOptionsTx reuses phone passkeys and the bound one-use two-minute
// challenge. The caller holds the tenant/target fences and checks authority.
// absent=true selects the existing OIDC sign-in; it never asks for a new key.
func (m *Module) StepUpOptionsTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, hash string) (options any, absent bool, err error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" {
		return nil, false, fail(403, "person required")
	}
	u, err := loadUser(ctx, tx, p)
	if err != nil {
		return nil, false, err
	}
	if len(u.creds) == 0 {
		return nil, true, nil
	}
	if m.wa == nil {
		return nil, false, fail(503, "passkeys unavailable")
	}
	bind := binding(p, "stepup", id, hash, "approve", "")
	challenge, err := boundChallenge(id, hash, bind)
	if err != nil {
		return nil, false, err
	}
	option, session, err := m.wa.BeginLogin(u, webauthn.WithChallenge(challenge), webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return nil, false, fail(403, "passkey options unavailable")
	}
	sid, err := storeSession(ctx, tx, p, "stepup", id, bind, session)
	return map[string]any{"method": "passkey", "challenge_id": sid, "publicKey": option.Response}, false, err
}

// VerifyStepUpTx does not append an event: the native mutation and its outcome
// must finish before acquiring the event counter. Assertions are never stored.
func (m *Module) VerifyStepUpTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, hash string, proof Proof) (string, time.Time, error) {
	if p.Kind != tenant.Person || p.KeyCreatorID != "" || m.wa == nil || len(proof.Credential) == 0 || len(proof.Credential) > 64<<10 {
		return "", time.Time{}, fail(403, "fresh passkey verification required")
	}
	u, err := loadUser(ctx, tx, p)
	if err != nil {
		return "", time.Time{}, err
	}
	session, err := consume(ctx, tx, p, proof.ChallengeID, "stepup", id, binding(p, "stepup", id, hash, "approve", ""))
	if err != nil {
		return "", time.Time{}, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(proof.Credential)
	if err != nil {
		return "", time.Time{}, fail(403, "passkey verification rejected")
	}
	c, err := m.wa.ValidateLogin(u, session, parsed)
	if err != nil || c == nil || c.Authenticator.CloneWarning {
		return "", time.Time{}, fail(403, "passkey verification rejected")
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return "", time.Time{}, err
	}
	tag, err := tx.Exec(ctx, `UPDATE phone_passkeys SET credential=$3 WHERE person_id=$1 AND credential_id=$2 AND revoked_at IS NULL`, p.ID, base64.RawURLEncoding.EncodeToString(c.ID), raw)
	if err != nil {
		return "", time.Time{}, err
	}
	if tag.RowsAffected() != 1 {
		return "", time.Time{}, fail(403, "passkey revoked")
	}
	method := "passkey"
	if c.Authenticator.Attachment == protocol.Platform {
		method = "passkey_platform"
	}
	return method, time.Now().UTC(), nil
}

// StepUpBinding is also used for OIDC state stored in the same challenge table.
func StepUpBinding(p tenant.Principal, id, hash string) string {
	return binding(p, "stepup", id, hash, "approve", "")
}

// StepUpFailure exposes only our constant error classification to the native
// HTTP boundary, never provider/credential data or a database diagnostic.
func StepUpFailure(err error) (int, string, bool) {
	var p *problem
	if errors.As(err, &p) {
		return p.status, p.message, true
	}
	return 0, "", false
}
