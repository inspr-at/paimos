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

// ExternalRegistrationTx validates an existing live external inbox registration.
// Callers hold their access/role/thread fences before asking for a session lock.
func ExternalRegistrationTx(ctx context.Context, tx pgx.Tx, sessionID string, lock bool) (ExternalRegistration, error) {
	var out ExternalRegistration
	q := `SELECT id::text,project_id::text,agent_principal_id::text,owner_principal_id::text,role,harness,ref_digest,vendor_ref_digest FROM harness_sessions
 WHERE id=$1::uuid AND management='unmanaged' AND harness IN ('claude','codex','pi','cursor','grok')
 AND capabilities @> ARRAY['inbox']::text[] AND owner_principal_id IS NOT NULL
 AND stopped_at IS NULL AND archived_at IS NULL AND phase NOT IN ('stopping','stopped')
 AND coalesce(heartbeat_at,created_at)>clock_timestamp()-interval '2 minutes'`
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
	lease := r.Header.Get("X-Aeon-Worker-Lease")
	if p.Kind != tenant.Agent || len(lease) < 32 || len(lease) > 4096 {
		return ExternalRegistration{}, workorders.Fail(404, "chat binding unavailable")
	}
	out, err := ExternalRegistrationTx(ctx, tx, sessionID, lock)
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
