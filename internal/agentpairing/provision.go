// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentcompat"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/hookcap"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/version"
	"github.com/jackc/pgx/v5"
)

type approval struct {
	Digest       string   `json:"request_digest"`
	Verification string   `json:"verification"`
	Selected     []string `json:"selected_account_keys"`
}

func (m *Module) approve(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in approval
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if !hashRE.MatchString(in.Digest) || (in.Verification != "one_per_harness" && in.Verification != "connect_only") || len(in.Selected) < 1 || len(in.Selected) > maxComputerAccounts {
		WriteError(w, fail(400, "invalid_request", "review digest, selected accounts and verification choice required"))
		return
	}
	slices.Sort(in.Selected)
	if len(slices.Compact(slices.Clone(in.Selected))) != len(in.Selected) {
		WriteError(w, fail(400, "invalid_request", "duplicate selection"))
		return
	}
	var out View
	err := db.InTenant(r.Context(), m.pool, p.TenantID, func(tx pgx.Tx) error {
		ctx := r.Context()
		// Retiring a replaced principal invokes the last-owner tenant fence.
		// Approval must acquire it before the pairing advisory and record rows.
		if _, err := tx.Exec(ctx, `SELECT id FROM tenants WHERE id=$1::uuid FOR NO KEY UPDATE`, p.TenantID); err != nil {
			return err
		}
		if err := Lock(ctx, tx); err != nil {
			return err
		}
		rec, err := load(ctx, tx, r.PathValue("requestId"))
		if err != nil {
			return notFound(err)
		}
		if rec.Digest != in.Digest {
			return fail(409, "conflict", "review details changed")
		}
		chosen := []Choice{}
		for _, a := range rec.Details.Accounts {
			if slices.Contains(in.Selected, a.AccountKey) {
				chosen = append(chosen, a)
			}
		}
		if len(chosen) != len(in.Selected) {
			return fail(400, "invalid_request", "selection must match reviewed accounts")
		}
		if err = authz.RequireTx(ctx, tx, p, "account.manage", authz.Scope{}); err != nil {
			return fail(403, "forbidden", "person account management required")
		}
		if err = expire(ctx, tx, &rec); err != nil {
			return err
		}
		if rec.State == "approved" || rec.State == "redeemed" {
			var selected []string
			if err = tx.QueryRow(ctx, `SELECT selected_account_keys FROM agent_pairing_requests WHERE id=$1`, rec.ID).Scan(&selected); err != nil {
				return err
			}
			if rec.Mode != nil && *rec.Mode == "one_per_harness" && in.Verification == "connect_only" && slices.Equal(selected, in.Selected) {
				// A person may withdraw pending verification, never replenish it.
				if err = supersedeVerifications(ctx, tx, *rec.ComputerID, ""); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `UPDATE agent_pairing_requests SET verification='connect_only' WHERE id=$1`, rec.ID); err != nil {
					return err
				}
				rec.Mode = &in.Verification
				if err = audit(ctx, tx, p, "agent_pairing.verification_withdrawn", map[string]any{"request_id": rec.ID}); err != nil {
					return err
				}
			} else if rec.Mode == nil || *rec.Mode != in.Verification || !slices.Equal(selected, in.Selected) {
				return fail(409, "conflict", "approval is immutable")
			}
			out, err = view(ctx, tx, rec, false)
			return err
		}
		if rec.State != "pending" {
			out, err = view(ctx, tx, rec, false)
			return err
		}
		ledgerMode, err := LedgerMode(ctx, tx)
		if err != nil {
			return err
		}
		if ledgerMode && !slices.Contains(rec.Details.Capabilities, LedgerCapability) {
			return fail(409, "ledger_enrollment_required", "ledger-v1 capable helper required for approval")
		}
		if in.Verification == "one_per_harness" {
			for _, a := range chosen {
				if capability := verificationCapabilities(rec.Details.Platform, rec.Details.Arch)[a.Harness]; !capability.Supported {
					return fail(409, "verification_unavailable", capability.Reason+" Choose Connect only or exclude this account explicitly.")
				}
			}
		}
		for _, scope := range RuntimePermissions {
			if err = authz.RequireTx(ctx, tx, p, scope, authz.Scope{}); err != nil {
				return fail(403, "forbidden", "approver cannot delegate required runtime permission: "+scope)
			}
		}
		// Revalidate profile scope under the same lock as issuance. A submitted UUID
		// or local account label is never a request to adopt another identity.
		for _, a := range chosen {
			var ok bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE id=$1 AND harness=$2 AND enabled AND ($3='' OR (starts_with(model,$3||'/') AND length(model)>length($3)+1)))`, a.ProfileID, a.Harness, a.Provider).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return fail(409, "conflict", "reviewed model profile is no longer available")
			}
		}
		computer, principal, daemon := "", "", ""
		if rec.Details.ExistingComputerID != "" {
			computer = rec.Details.ExistingComputerID
			var state string
			if err = tx.QueryRow(ctx, `SELECT principal_id::text,daemon_id,state FROM agent_pairing_computers WHERE id=$1 FOR UPDATE`, computer).Scan(&principal, &daemon, &state); err != nil {
				return notFound(err)
			}
			if state != "connected" {
				return fail(409, "pairing_revoked", "computer is disconnecting or revoked; pair afresh")
			}
			// A changed local installation must re-import before any new dispatch.
			// Approval/retry is fenced; identical completed approvals never clear it.
			if ledgerMode {
				if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET ledger_generation=NULL,ledger_enrolled_at=NULL WHERE id=$1`, computer); err != nil {
					return err
				}
			}
			if err = validateAdditionalAccounts(ctx, tx, computer, chosen); err != nil {
				return err
			}
			// Do not expand the original creator ceiling during Add harness.
			var creator string
			if err = tx.QueryRow(ctx, `SELECT q.approved_by::text FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1`, computer).Scan(&creator); err != nil {
				return err
			}
			original := tenant.Principal{ID: creator, TenantID: p.TenantID, Kind: tenant.Person}
			for _, scope := range RuntimePermissions {
				if authz.RequireTx(ctx, tx, original, scope, authz.Scope{}) != nil {
					return fail(403, "forbidden", "original runtime delegation is no longer valid")
				}
			}
		} else {
			computer = rec.ID
			daemon = "paired-" + computer
			if err = tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'agent',$2,'{}') RETURNING id::text`, p.TenantID, rec.Details.ComputerName).Scan(&principal); err != nil {
				return err
			}
			var role string
			if err = tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,$2,'Paired computer runtime') RETURNING id::text`, p.TenantID, "paired_"+strings.ReplaceAll(principal, "-", "")).Scan(&role); err != nil {
				return err
			}
			for _, scope := range RuntimePermissions {
				if _, err = tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,$3)`, p.TenantID, role, scope); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, p.TenantID, principal, role); err != nil {
				return err
			}
			suffix, err := randomHex(8)
			if err != nil {
				return err
			}
			prefix := strings.ReplaceAll(p.TenantID, "-", "") + suffix
			var key string
			if err = tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_owner_required) VALUES($1,$2,$3,$4,$5,$6,$7,true) RETURNING id::text`, p.TenantID, principal, rec.Details.ComputerName, prefix, rec.RuntimeHash, RuntimePermissions, p.ID).Scan(&key); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,local_auth_public_key) VALUES($1,$2,$2,$3,$4,$5,$6,$7)`, p.TenantID, computer, principal, key, daemon, rec.LifecycleHash, rec.Details.LocalAuthPublicKey); err != nil {
				return err
			}
			if err = retireReplaced(ctx, tx, p, rec.Details, principal); err != nil {
				return err
			}
		}
		if rec.Details.ExistingComputerID != "" {
			if err = supersedeVerifications(ctx, tx, computer, rec.ID); err != nil {
				return err
			}
		}
		for _, a := range chosen {
			// A revoked account key remains reserved forever in this daemon. Re-pair
			// must choose a new local opaque key; stale setup cannot revive a tombstone.
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_accounts WHERE daemon_id=$1 AND harness=$2 AND account_key=$3)`, daemon, a.Harness, a.AccountKey).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return fail(409, "conflict", "account key was already enrolled; use a fresh local enrollment key")
			}
			var account string
			if err = tx.QueryRow(ctx, `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,max_parallel_runs,host_label,allowed_model_profile_ids) VALUES($1,$2,$3,$4,$5,$6,1,$7,NULL) RETURNING id::text`, p.TenantID, a.AccountKey, a.Harness, daemon, principal, a.Label, rec.Details.ComputerName).Scan(&account); err != nil {
				return err
			}
			if a.Harness == "pi" {
				if _, err = tx.Exec(ctx, `UPDATE agent_accounts a SET provider=split_part(p.model,'/',1), model=substring(p.model from position('/' in p.model)+1), model_data_note=(p.model LIKE 'openrouter/stealth/%' OR p.model LIKE '%:free') FROM model_profiles p WHERE a.id=$1 AND p.id=$2 AND p.tenant_id=a.tenant_id AND position('/' in p.model)>1`, account, a.ProfileID); err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, account, computer, rec.ID, a.ProfileID, rec.VerificationExpiresAt); err != nil {
				return err
			}
			if in.Verification == "one_per_harness" {
				if err = verificationJob(ctx, tx, p, computer, principal, account, a, rec.VerificationExpiresAt); err != nil {
					return err
				}
			}
		}
		rec.State = "approved"
		rec.Mode = &in.Verification
		rec.ComputerID = &computer
		rec.ApprovedBy = &p.ID
		if _, err = tx.Exec(ctx, `UPDATE agent_pairing_requests SET state='approved',verification=$2,approved_by=$3,computer_id=$4,selected_account_keys=$5 WHERE id=$1`, rec.ID, in.Verification, p.ID, computer, in.Selected); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE agent_pairing_computers SET revision=revision+1 WHERE id=$1`, computer); err != nil {
			return err
		}
		out, err = view(ctx, tx, rec, false)
		if err != nil {
			return err
		}
		return audit(ctx, tx, p, "agent_pairing.approved", out)
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

// Check the complete live enrollment, not just this Add harness request. An
// isolated request cannot evade the config-home isolation or computer-size
// bound. Legacy Add harness reenrollment retains its existing protocol.
// Immutable reviewed details retain the opaque home binding without a migration.
func validateAdditionalAccounts(ctx context.Context, tx pgx.Tx, computer string, chosen []Choice) error {
	rows, err := tx.Query(ctx, `SELECT q.details,a.account_key FROM agent_pairing_enrollments e
 JOIN agent_pairing_requests q ON q.tenant_id=e.tenant_id AND q.id=e.request_id
 JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id
 WHERE e.computer_id=$1 AND e.state<>'revoked' ORDER BY e.account_id LIMIT 33`, computer)
	if err != nil {
		return err
	}
	defer rows.Close()
	all := slices.Clone(chosen)
	for rows.Next() {
		var raw []byte
		var key string
		if err := rows.Scan(&raw, &key); err != nil {
			return err
		}
		var details Details
		if err := json.Unmarshal(raw, &details); err != nil {
			return err
		}
		for _, account := range details.Accounts {
			if account.AccountKey == key {
				all = append(all, account)
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(all) > maxComputerAccounts {
		return fail(400, "invalid_request", "at most 32 accounts per computer")
	}
	if !slices.ContainsFunc(all, func(a Choice) bool { return a.ConfigHomeID != "" }) {
		return nil
	}
	return validateAccountChoices(all)
}

// retireReplaced deactivates the runtime identities a fresh pairing replaces, so
// re-pairing a laptop leaves one identity behind, not one more each time. A
// computer counts as replaced only when the same person who approved it approves
// its successor, the machine describes itself the same way (name, platform,
// architecture), and no pinned local-auth key contradicts it; the old computer
// must also be revoked and its identity still active. A name alone proves
// nothing: two people may each call their laptop "Laptop". A computer that is
// still connected is never touched.
func retireReplaced(ctx context.Context, tx pgx.Tx, p tenant.Principal, d Details, replacedBy string) error {
	rows, err := tx.Query(ctx, `SELECT pr.id::text FROM agent_pairing_computers c
		JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id
		JOIN principals pr ON pr.tenant_id=c.tenant_id AND pr.id=c.principal_id
		WHERE c.tenant_id=$1 AND c.state='revoked' AND pr.kind='agent' AND pr.status='active' AND pr.name=$2 AND pr.id<>$3
		  AND q.approved_by=$4 AND q.details->>'platform'=$5 AND q.details->>'arch'=$6
		  AND (c.local_auth_public_key='' OR $7='' OR c.local_auth_public_key=$7)
		ORDER BY c.created_at,c.id FOR UPDATE OF pr`, p.TenantID, d.ComputerName, replacedBy, p.ID, d.Platform, d.Arch, d.LocalAuthPublicKey)
	if err != nil {
		return err
	}
	var old []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		old = append(old, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range old {
		if _, err = authz.RetireAgentTx(ctx, tx, p, id, map[string]any{"replaced_by": replacedBy}); err != nil {
			return err
		}
	}
	return nil
}
func verificationJob(ctx context.Context, tx pgx.Tx, p tenant.Principal, computer, principal, account string, a Choice, expires time.Time) error {
	out, err := createVerificationJob(ctx, tx, p, computer, principal, account, a, expires)
	if err != nil {
		return err
	}
	return audit(ctx, tx, p, "agent_pairing.verification_created", out.audit())
}

func createVerificationJob(ctx context.Context, tx pgx.Tx, p tenant.Principal, computer, principal, account string, a Choice, expires time.Time) (verificationRetry, error) {
	var project *string
	if err := tx.QueryRow(ctx, `SELECT verification_project_id::text FROM agent_pairing_computers WHERE id=$1`, computer).Scan(&project); err != nil {
		return verificationRetry{}, err
	}
	if project == nil {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,'Computer connection checks' FROM node_kinds k WHERE k.slug='project' RETURNING id::text`, p.TenantID).Scan(&id); err != nil {
			return verificationRetry{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_pairing_computers SET verification_project_id=$2 WHERE id=$1`, computer, id); err != nil {
			return verificationRetry{}, err
		}
		project = &id
	}
	var order, run string
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,body,parent_id) SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,$2,$3,$4 FROM node_kinds k WHERE k.slug='work_order' RETURNING id::text`, p.TenantID, "Verify "+a.Harness+" connection", VerificationTask, project).Scan(&order)
	if err != nil {
		return verificationRetry{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id,status,max_duration_seconds) VALUES($1,$2,$3,$4,'ready',$5)`, p.TenantID, order, p.ID, principal, VerificationSeconds); err != nil {
		return verificationRetry{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_criteria(tenant_id,work_order_id,position,description) VALUES($1,$2,0,'Return AEON_VERIFIED in enforced read-only mode without privileged actions')`, p.TenantID, order); err != nil {
		return verificationRetry{}, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,model_profile_id,requested_model,requested_account_id,purpose) SELECT $1,$2,$3,m.id,m.model,$4,'pairing_verification' FROM model_profiles m WHERE m.id=$5 RETURNING id::text`, p.TenantID, order, principal, account, a.ProfileID).Scan(&run); err != nil {
		return verificationRetry{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET verification_run_id=$2 WHERE account_id=$1`, account, run); err != nil {
		return verificationRetry{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,burst_ratio,pairing_verification) VALUES($1,$2,clock_timestamp(),$3,'requests',1,'unrestricted',0,true)`, p.TenantID, account, expires); err != nil {
		return verificationRetry{}, err
	}
	return verificationRetry{AccountID: account, RunID: run, ExpiresAt: expires, WorkOrderID: order}, nil
}
func audit(ctx context.Context, tx pgx.Tx, p tenant.Principal, kind string, after any) error {
	return appendEvent(ctx, tx, p, events.Change{Type: kind, After: after})
}
func (m *Module) deny(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rec, err := load(r.Context(), tx, r.PathValue("requestId"))
		if err != nil {
			return notFound(err)
		}
		if err = authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}); err != nil {
			return err
		}
		if err = expire(r.Context(), tx, &rec); err != nil {
			return err
		}
		if rec.State == "approved" || rec.State == "redeemed" {
			return fail(409, "conflict", "approved pairing must be disconnected")
		}
		if rec.State == "pending" {
			rec.State = "denied"
			if _, err = tx.Exec(r.Context(), `UPDATE agent_pairing_requests SET state='denied' WHERE id=$1`, rec.ID); err != nil {
				return err
			}
			if err = audit(r.Context(), tx, p, "agent_pairing.denied", map[string]string{"request_id": rec.ID}); err != nil {
				return err
			}
		}
		out, err = view(r.Context(), tx, rec, false)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
func expire(ctx context.Context, tx pgx.Tx, rec *record) error {
	var expired bool
	if err := tx.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() OR attempts>=10 FROM agent_pairing_requests WHERE id=$1`, rec.ID).Scan(&expired); err != nil {
		return err
	}
	if (rec.State != "pending" && rec.State != "approved") || !expired {
		return nil
	}
	if rec.ComputerID != nil {
		// An expired Add harness grant affects only its new enrollments.
		if err := disconnectRequest(ctx, tx, *rec); err != nil {
			return err
		}
	}
	rec.State = "expired"
	_, err := tx.Exec(ctx, `UPDATE agent_pairing_requests SET state='expired' WHERE id=$1`, rec.ID)
	return err
}
func view(ctx context.Context, tx pgx.Tx, rec record, prefix bool) (View, error) {
	if err := ExpireUnclaimedVerifications(ctx, tx); err != nil {
		return View{}, err
	}
	v := View{AgentCompatibility: agentcompat.Result{Status: "unknown"}, RequestID: rec.ID, TenantID: rec.TenantID, State: rec.State, Digest: rec.Digest, ExpiresAt: rec.ExpiresAt, ComputerName: rec.Details.ComputerName, Platform: rec.Details.Platform, Arch: rec.Details.Arch, Workspace: rec.Details.Workspace, Capabilities: rec.Details.Capabilities, Requested: rec.Details.Accounts, ComputerID: rec.ComputerID, Cleanup: "pending", Processes: "unconfirmed", Enrollments: []Enrollment{}, Verification: Verification{"read_only", rec.Mode, 1, 1, VerificationSeconds, 1, "requests", rec.VerificationExpiresAt, VerificationTask}}
	if err := tx.QueryRow(ctx, `SELECT name FROM tenants WHERE id=$1`, rec.TenantID).Scan(&v.TenantName); err != nil {
		return v, err
	}
	v.VerificationCapabilities = verificationCapabilities(rec.Details.Platform, rec.Details.Arch)
	v.VerificationHelperVersion = version.Version
	v.ServerCapabilities = []string{LedgerCapability}
	var err error
	v.LedgerMode, err = LedgerMode(ctx, tx)
	if err != nil {
		return v, err
	}
	v.ExistingComputerID = rec.Details.ExistingComputerID
	v.SetupState = "not_started"
	v.Connectivity = "unknown"
	v.AccountingState = "settled"
	if rec.ComputerID == nil {
		return v, nil
	}
	if err := finalizeDrain(ctx, tx, *rec.ComputerID); err != nil {
		return v, err
	}
	if err := tx.QueryRow(ctx, `SELECT hook_capabilities FROM agent_pairing_computers WHERE id=$1`, *rec.ComputerID).Scan(&v.HookCapabilities); err != nil {
		return v, err
	}
	for i, c := range v.HookCapabilities {
		v.HookCapabilities[i] = hookcap.Project(c)
	}
	var keyPrefix string
	err = tx.QueryRow(ctx, `SELECT c.state,c.principal_id::text,c.daemon_id,c.local_cleanup,c.local_processes,c.revision,k.prefix,c.setup_state,c.setup_error,c.harness_statuses,c.harness_details,c.last_seen_at,
 CASE WHEN EXISTS(SELECT 1 FROM agent_accounts a JOIN agent_pairing_enrollments e ON e.tenant_id=a.tenant_id AND e.account_id=a.id WHERE e.computer_id=c.id AND e.state='connected' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes') THEN 'online' WHEN c.last_seen_at IS NULL THEN 'unknown' ELSE 'offline' END,c.archived_at,c.agent_protocol,c.agent_version,c.agent_version_scheme,c.local_auth_public_key<>'',coalesce(nullif(c.display_name,''),$2),c.ledger_generation,c.ledger_enrolled_at FROM agent_pairing_computers c JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id WHERE c.id=$1`, *rec.ComputerID, rec.Details.ComputerName).Scan(&v.ComputerState, &v.PrincipalID, &v.DaemonID, &v.Cleanup, &v.Processes, &v.Revision, &keyPrefix, &v.SetupState, &v.SetupError, &v.HarnessStatuses, &v.HarnessDetails, &v.LastSeenAt, &v.Connectivity, &v.ArchivedAt, &v.AgentRelease.Protocol, &v.AgentRelease.Version, &v.AgentRelease.VersionScheme, &v.LocalAuthPinned, &v.ComputerName, &v.LedgerGeneration, &v.LedgerEnrolledAt)
	if err != nil {
		return v, err
	}
	capacity, err := hostCapacity(ctx, tx, *rec.ComputerID)
	if err != nil {
		return v, err
	}
	v.HostCapacity = &capacity
	v.AgentCompatibility = agentcompat.Supported().Check(v.AgentRelease)
	if *v.ComputerState == "revoked" {
		v.State = "revoked"
	} else if prefix && *v.ComputerState == "connected" {
		v.RuntimePrefix = keyPrefix
	}
	actorID := ""
	if actor, ok := tenant.PrincipalFrom(ctx); ok && actor.Kind == tenant.Person {
		actorID = actor.ID
	}
	rows, err := tx.Query(ctx, `SELECT e.account_id::text,a.account_key,a.harness,a.label,e.model_profile_id::text,e.state,e.local_cleanup,e.verification_run_id::text,
  ARRAY(SELECT r.id::text FROM agent_runs r WHERE r.account_id=e.account_id AND r.status IN ('starting','running','waiting') ORDER BY r.id),
 CASE WHEN e.verification_run_id IS NULL THEN 'not_selected' WHEN e.verification_expired_at IS NOT NULL THEN 'expired' WHEN e.verification_expires_at<=clock_timestamp() AND (SELECT status FROM agent_runs WHERE id=e.verification_run_id)='queued' THEN 'expired' ELSE (SELECT status FROM agent_runs WHERE id=e.verification_run_id) END,
 coalesce((SELECT error_code FROM run_telemetry WHERE run_id=e.verification_run_id AND error_code IS NOT NULL ORDER BY sequence DESC LIMIT 1),''),
 coalesce((SELECT verification_unavailable_reason FROM agent_runs WHERE id=e.verification_run_id),''),
 coalesce(e.verification_claimed_at+interval '120 seconds'<=clock_timestamp() AND (SELECT status FROM agent_runs WHERE id=e.verification_run_id) IN ('starting','running','waiting'),false),
 coalesce(e.state='connected' AND a.last_probe_ok AND a.last_probe_at>clock_timestamp()-interval '2 minutes',false),
 coalesce(coalesce(a.owner_person_id,(SELECT approved_by FROM agent_pairing_requests WHERE id=e.request_id))=nullif($2,'')::uuid,false),
 (SELECT ended_at FROM agent_runs WHERE id=e.verification_run_id AND status='completed'),e.verification_expires_at,
 (SELECT max(started_at) FROM agent_runs WHERE account_id=e.account_id AND purpose='managed')
  FROM agent_pairing_enrollments e JOIN agent_accounts a ON a.tenant_id=e.tenant_id AND a.id=e.account_id WHERE e.computer_id=$1 ORDER BY a.created_at,a.id`, *rec.ComputerID, actorID)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var e Enrollment
		if err = rows.Scan(&e.AccountID, &e.AccountKey, &e.Harness, &e.Label, &e.ProfileID, &e.State, &e.Cleanup, &e.VerificationRunID, &e.ActiveRunIDs, &e.VerificationState, &e.VerificationError, &e.VerificationReason, &e.VerificationStalled, &e.VerificationExpiredReady, &e.CanVerify, &e.VerifiedAt, &e.VerificationExpiresAt, &e.LastUsedAt); err != nil {
			return v, err
		}
		if e.VerificationStalled && e.VerificationError == "" {
			e.VerificationError = "verification_timeout"
		}
		e.VerificationExpiredReady = e.VerificationState == "expired" && e.VerificationExpiredReady
		if e.VerificationReason != "" {
			e.VerificationError = "verification_unavailable"
		}
		if e.VerificationState == "queued" && !v.VerificationCapabilities[e.Harness].Supported {
			e.VerificationState = "unavailable"
			e.VerificationError = "verification_unavailable"
		}
		e.LocalProcesses = "unconfirmed"
		if e.Cleanup == "confirmed" {
			e.LocalProcesses = "drained"
		}
		e.AccountingState = "settled"
		if len(e.ActiveRunIDs) > 0 {
			e.AccountingState = "unconfirmed"
			v.AccountingState = "unconfirmed"
		}
		v.Enrollments = append(v.Enrollments, e)
	}
	return v, rows.Err()
}
func (m *Module) list(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	out := []View{}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `SELECT request_id::text FROM agent_pairing_computers WHERE archived_at IS NULL ORDER BY created_at DESC,id LIMIT 100`)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			rec, err := load(r.Context(), tx, id)
			if err != nil {
				return err
			}
			v, err := view(r.Context(), tx, rec, false)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, map[string]any{"computers": out})
}
func computerRecord(ctx context.Context, tx pgx.Tx, id string) (record, error) {
	if !uuidRE.MatchString(id) {
		return record{}, fail(404, "not_found", "computer not found")
	}
	var request string
	if err := tx.QueryRow(ctx, `SELECT request_id::text FROM agent_pairing_computers WHERE id=$1`, id).Scan(&request); err != nil {
		return record{}, notFound(err)
	}
	rec, err := load(ctx, tx, request)
	return rec, notFound(err)
}
func (m *Module) get(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var out View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		rec, err := computerRecord(r.Context(), tx, r.PathValue("computerId"))
		if err != nil {
			return err
		}
		if err = expire(r.Context(), tx, &rec); err != nil {
			return err
		}
		out, err = view(r.Context(), tx, rec, false)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}
