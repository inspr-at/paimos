// SPDX-License-Identifier: AGPL-3.0-only
package stepup

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/phoneapprovals"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type reauthSession struct {
	StartedAt time.Time `json:"started_at"`
}

func (m *Module) startReauth(ctx context.Context, tx pgx.Tx, p tenant.Principal, r ApprovalRequest) (ReauthStart, error) {
	out := ReauthStart{RequestID: r.ID, Digest: r.Digest, StartedAt: m.now().UTC()}
	raw, err := json.Marshal(reauthSession{out.StartedAt})
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM phone_approval_challenges WHERE person_id=$1`, p.ID); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO phone_approval_challenges(tenant_id,person_id,kind,request_id,binding,session_data,expires_at) VALUES($1,$2,'stepup',$3,$4,$5,$6) RETURNING id::text`, p.TenantID, p.ID, r.ID, phoneapprovals.StepUpBinding(p, r.ID, r.Digest), raw, out.StartedAt.Add(ProofLifetime)).Scan(&out.ChallengeID)
	return out, err
}

// FinishReauth is called only after auth verifies the provider's signature,
// audience, issuer, nonce, state, PKCE, current browser person and subject.
// No HTTP endpoint accepts a claimed auth_time, method or ID token as proof.
func (m *Module) FinishReauth(ctx context.Context, p tenant.Principal, start ReauthStart, authTime time.Time) (ApprovalRequest, error) {
	return m.decide(ctx, p, start.RequestID, Decide{Digest: start.Digest, Revision: 1}, "approve", func(tx pgx.Tx, r ApprovalRequest) (string, time.Time, error) {
		var raw []byte
		err := tx.QueryRow(ctx, `UPDATE phone_approval_challenges SET consumed_at=$5 WHERE id=$1 AND person_id=$2 AND kind='stepup' AND request_id=$3 AND binding=$4 AND consumed_at IS NULL AND expires_at>$5 AND session_data ? 'started_at' RETURNING session_data`, start.ChallengeID, p.ID, r.ID, phoneapprovals.StepUpBinding(p, r.ID, r.Digest), m.now()).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", time.Time{}, fault(403, "fresh sign-in expired or already used")
		}
		if err != nil {
			return "", time.Time{}, err
		}
		var session reauthSession
		if json.Unmarshal(raw, &session) != nil || session.StartedAt.IsZero() || authTime.Unix() <= session.StartedAt.Unix() || authTime.After(m.now().Add(5*time.Second)) {
			return "", time.Time{}, fault(403, "fresh sign-in required after Approve")
		}
		return "oidc_reauth", authTime, nil
	})
}
