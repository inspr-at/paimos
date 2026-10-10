// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// ExternalRegistration is a private authorization input, never a response DTO.
// Native identities are server-stored digests, never raw references or DTOs.
// It deliberately omits account, vendor reference, path and model metadata.
type ExternalRegistration struct {
	ID, ProjectID, AgentPrincipalID, OwnerPersonID, Role string
	Harness                                              string
	RefDigest, VendorRefDigest                           []byte
}

// ChatCapability is advertised by an owned (agentd-managed) registration whose
// harness supplies the normalized S1 chat stream. It grants no other control.
const ChatCapability = "chat"

const registrationColumns = `SELECT id::text,project_id::text,agent_principal_id::text,owner_principal_id::text,role,harness,ref_digest,vendor_ref_digest FROM harness_sessions
 WHERE id=$1::uuid AND harness IN ('claude','codex','pi','cursor','grok') AND owner_principal_id IS NOT NULL
 AND stopped_at IS NULL AND archived_at IS NULL AND phase NOT IN ('stopping','stopped')
 AND coalesce(heartbeat_at,created_at)>clock_timestamp()-interval '2 minutes'`

// ExternalRegistrationTx validates an existing live external inbox registration.
// Callers hold their access/role/thread fences before asking for a session lock.
func ExternalRegistrationTx(ctx context.Context, tx pgx.Tx, sessionID string, lock bool) (ExternalRegistration, error) {
	return registrationTx(ctx, tx, sessionID, lock, ` AND management='unmanaged' AND capabilities @> ARRAY['inbox']::text[]`)
}

// ChatRegistrationTx is the chat-only boundary. It also admits an owned
// agentd run that advertises the chat stream; every other liveness, owner and
// harness condition is identical to ExternalRegistrationTx.
func ChatRegistrationTx(ctx context.Context, tx pgx.Tx, sessionID string, lock bool) (ExternalRegistration, error) {
	return registrationTx(ctx, tx, sessionID, lock, ` AND (management='unmanaged' AND capabilities @> ARRAY['inbox']::text[]
 OR management='managed' AND capabilities @> ARRAY['`+ChatCapability+`']::text[])`)
}

func registrationTx(ctx context.Context, tx pgx.Tx, sessionID string, lock bool, boundary string) (ExternalRegistration, error) {
	var out ExternalRegistration
	q := registrationColumns + boundary
	if lock {
		q += ` FOR NO KEY UPDATE`
	}
	err := tx.QueryRow(ctx, q, sessionID).Scan(&out.ID, &out.ProjectID, &out.AgentPrincipalID, &out.OwnerPersonID, &out.Role, &out.Harness, &out.RefDigest, &out.VendorRefDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, workorders.Fail(404, "chat binding unavailable")
	}
	return out, err
}

// VerifyExternalLeaseTx uses the exact existing registration lease digest,
// rather than a public session ID/reference or principal-wide inbox authority.
func VerifyExternalLeaseTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, sessionID string, lock bool) (ExternalRegistration, error) {
	return verifyLeaseTx(ctx, tx, r, p, sessionID, lock, ExternalRegistrationTx)
}

// VerifyChatLeaseTx applies the same agent principal and private worker lease
// proof to the chat-only registration boundary.
func VerifyChatLeaseTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, sessionID string, lock bool) (ExternalRegistration, error) {
	return verifyLeaseTx(ctx, tx, r, p, sessionID, lock, ChatRegistrationTx)
}

func verifyLeaseTx(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal, sessionID string, lock bool, registration func(context.Context, pgx.Tx, string, bool) (ExternalRegistration, error)) (ExternalRegistration, error) {
	lease := r.Header.Get("X-Aeon-Worker-Lease")
	if p.Kind != tenant.Agent || len(lease) < 32 || len(lease) > 4096 {
		return ExternalRegistration{}, workorders.Fail(404, "chat binding unavailable")
	}
	out, err := registration(ctx, tx, sessionID, lock)
	if err != nil {
		return out, err
	}
	var stored []byte
	err = tx.QueryRow(ctx, `SELECT lease_digest FROM harness_sessions WHERE id=$1::uuid`, sessionID).Scan(&stored)
	if err != nil {
		return ExternalRegistration{}, err
	}
	if out.AgentPrincipalID != p.ID || subtle.ConstantTimeCompare(digest("lease", lease), stored) != 1 {
		return ExternalRegistration{}, workorders.Fail(404, "chat binding unavailable")
	}
	return out, nil
}
