// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workerlease"
	"github.com/jackc/pgx/v5"
)

const RunCapabilityHeader = workerlease.RunCapabilityHeader
const RunCapabilityPolicyHeader = "X-Aeon-Run-Capability-Policy"
const RunCapabilityPolicy = "run_content_v1"

var ErrRunCapability = errors.New("run capability rejected")
var ErrRunCapabilityEnrollmentRevoked = errors.New("run capability enrollment revoked")

// RunCapabilityPrincipalTx resolves a secret issued by the server, never a
// caller-selected person/run header. The caller holds the tenant/tree fence.
// Only this bounded metadata lookup widens project visibility; the caller's
// current person ceiling is restored before returning, including on failure.
func RunCapabilityPrincipalTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, token string) (tenant.Principal, error) {
	if p.Kind != tenant.Agent || len(token) != 64 {
		return p, ErrRunCapability
	}
	if _, err := hex.DecodeString(token); err != nil {
		return p, ErrRunCapability
	}
	var visibility string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&visibility); err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return p, err
	}
	sum := sha256.Sum256([]byte(token))
	next := p
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.work_order_id::text,r.acting_person_id::text,
 r.content_capability_computer_id::text,r.content_capability_daemon_id,r.content_capability_generation,
 coalesce(n.project_id::text,'')
 FROM agent_runs r
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id AND n.deleted_at IS NULL
 JOIN principals person ON person.tenant_id=r.tenant_id AND person.id=r.acting_person_id
  AND person.kind='person' AND person.status='active' AND person.linked_to IS NULL
 JOIN agent_pairing_computers c ON c.tenant_id=r.tenant_id AND c.id=r.content_capability_computer_id
  AND c.principal_id=r.agent_principal_id AND c.daemon_id=r.content_capability_daemon_id AND c.state IN ('connected','draining')
  AND c.content_daemon_generation=r.content_capability_generation
  AND c.content_capability_epoch=r.content_capability_epoch
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id AND q.state='redeemed'
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id
  AND e.account_id=r.content_capability_account_id AND e.state IN ('connected','draining')
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
  AND a.registered_by_principal_id=r.agent_principal_id AND a.daemon_id=c.daemon_id
  AND a.last_daemon_generation=r.content_capability_generation
 WHERE r.tenant_id=$1 AND r.agent_principal_id=$2 AND r.content_capability_digest=$3
  AND r.content_capability_person_id=r.acting_person_id
  AND r.content_capability_expires_at>clock_timestamp()
  AND (r.daemon_generation IS NULL OR r.daemon_generation=r.content_capability_generation)
  AND (r.daemon_id IS NULL OR r.daemon_id=r.content_capability_daemon_id)`, p.TenantID, p.ID, sum[:]).Scan(
		&next.RunID, &next.RunWorkOrderID, &next.KeyPersonID, &next.RunComputerID, &next.RunDaemonID, &next.RunGeneration, &next.RunProjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Preserve the metadata cleanup signal for a revoked enrollment. This
		// lookup returns no content and accepts only this agent's stored proof.
		var revoked bool
		lookupErr := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_runs r
 JOIN agent_pairing_computers c ON c.tenant_id=r.tenant_id AND c.id=r.content_capability_computer_id
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id AND e.account_id=r.content_capability_account_id
 WHERE r.tenant_id=$1 AND r.agent_principal_id=$2 AND r.content_capability_digest=$3
 AND (c.state='revoked' OR e.state='revoked'))`, p.TenantID, p.ID, sum[:]).Scan(&revoked)
		if lookupErr != nil {
			err = lookupErr
		} else if revoked {
			err = ErrRunCapabilityEnrollmentRevoked
		}
	}
	_, restoreErr := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true)`, visibility)
	if restoreErr != nil {
		return p, restoreErr
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrRunCapability
	}
	if err != nil {
		return p, err
	}
	if p.KeyPersonID != "" && p.KeyPersonID != next.KeyPersonID || p.RunID != "" && p.RunID != next.RunID {
		return p, ErrRunCapability
	}
	return next, nil
}
