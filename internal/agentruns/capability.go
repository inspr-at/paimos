// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

func (m *module) capability(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Daemon     string `json:"daemon_id"`
		Generation string `json:"daemon_generation"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if r.Header.Get(db.RunCapabilityPolicyHeader) != db.RunCapabilityPolicy || !identifier(in.Daemon) || !identifier(in.Generation) {
		return nil, workorders.Fail(409, "run_capability_upgrade_required")
	}
	ctx := r.Context()
	v, _, err := lockRun(ctx, tx, r.PathValue("runId"))
	if err != nil {
		return nil, err
	}
	if v.AgentID != p.ID || v.ActingPersonID == nil {
		return nil, workorders.Fail(403, "run_person_required")
	}
	if v.Status != "queued" && v.Status != "starting" && v.Status != "running" && v.Status != "waiting" {
		return nil, workorders.Fail(409, "run_not_executable")
	}
	if err := workqueue.RequireRunPersonTx(ctx, tx, p, v.ID); err != nil {
		return nil, err
	}
	bound, err := db.RunPrincipalTx(ctx, tx, p, v.ID)
	if err != nil {
		return nil, workorders.Fail(403, "run_person_rejected")
	}
	if err := authz.RequireTx(ctx, tx, bound, "run.read", authz.Scope{AnyProject: true}); err != nil {
		return nil, err
	}
	var computer, account string
	err = tx.QueryRow(ctx, `SELECT c.id::text,a.id::text FROM agent_pairing_computers c
 JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id AND q.state='redeemed'
 JOIN agent_pairing_enrollments e ON e.tenant_id=c.tenant_id AND e.computer_id=c.id AND e.state='connected'
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
 JOIN model_profiles m ON m.id=$5 AND m.harness=a.harness
 WHERE c.tenant_id=$1 AND c.principal_id=$2 AND c.state='connected' AND c.daemon_id=$3
 AND a.registered_by_principal_id=$2 AND a.daemon_id=$3 AND a.last_daemon_generation=$4
 AND ($6::uuid IS NULL OR a.id=$6) AND ($7::uuid IS NULL OR a.id=$7)
 ORDER BY a.id LIMIT 1`, p.TenantID, p.ID, in.Daemon, in.Generation, v.ProfileID, v.AccountID, v.RequestedAccountID).Scan(&computer, &account)
	if err == pgx.ErrNoRows {
		return nil, workorders.Fail(403, "run_generation_rejected")
	}
	if err != nil {
		return nil, err
	}
	if v.DaemonID != nil && *v.DaemonID != in.Daemon || v.Generation != nil && *v.Generation != in.Generation {
		return nil, workorders.Fail(403, "run_generation_rejected")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(random[:])
	digest := sha256.Sum256([]byte(token))
	var expires time.Time
	err = tx.QueryRow(ctx, `UPDATE agent_runs SET content_capability_digest=$2,content_capability_person_id=acting_person_id,
 content_capability_computer_id=$3,content_capability_account_id=$4,content_capability_daemon_id=$5,
 content_capability_generation=$6,content_capability_expires_at=clock_timestamp()+interval '24 hours'
 WHERE id=$1 RETURNING content_capability_expires_at`, v.ID, digest[:], computer, account, in.Daemon, in.Generation).Scan(&expires)
	if err != nil {
		return nil, err
	}
	return struct {
		Capability string    `json:"capability"`
		RunID      string    `json:"run_id"`
		PersonID   string    `json:"acting_person_id"`
		ComputerID string    `json:"computer_id"`
		DaemonID   string    `json:"daemon_id"`
		Generation string    `json:"daemon_generation"`
		Expires    time.Time `json:"expires_at"`
	}{token, v.ID, *v.ActingPersonID, computer, in.Daemon, in.Generation, expires}, nil
}
