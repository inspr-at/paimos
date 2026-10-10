// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func hostCapacity(ctx context.Context, tx pgx.Tx, computer string) (hostcapacity.View, error) {
	v := hostcapacity.View{History: []hostcapacity.Point{}}
	var policy, signals, history []byte
	var platform, state string
	var now time.Time
	// Legacy pairing records may omit platform; unknown evidence must remain
	// advisory rather than failing capacity reads and lifecycle mutations.
	err := tx.QueryRow(ctx, `SELECT capacity_policy,capacity_signals,capacity_reported_at,capacity_history,
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND r.status IN ('starting','running','waiting')),
 (SELECT count(*) FROM agent_runs r WHERE r.agent_principal_id=c.principal_id AND r.status='queued'),clock_timestamp(),coalesce(q.details->>'platform',''),c.state
 FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1`, computer).Scan(&policy, &signals, &v.ReportedAt, &history, &v.Running, &v.Queued, &now, &platform, &state)
	if err != nil {
		return v, notFound(err)
	}
	if err = json.Unmarshal(policy, &v.Policy); err != nil {
		return v, err
	}
	if len(signals) > 0 {
		if err = json.Unmarshal(signals, &v.Signals); err != nil {
			return v, err
		}
	}
	if err = json.Unmarshal(history, &v.History); err != nil {
		return v, err
	}
	recent := v.History[:0]
	for _, point := range v.History {
		if !point.At.Before(now.Add(-time.Hour)) && !point.At.After(now) {
			recent = append(recent, point)
		}
	}
	v.History = recent
	v.Reason, v.LoadLimit = hostcapacity.Evaluate(v.Policy, v.Signals, v.ReportedAt, v.Running, now)
	var unattended *hostcapacity.UnattendedSignals
	if v.Signals != nil {
		unattended = v.Signals.Unattended
	}
	readiness := hostcapacity.EvaluateUnattended(platform, state, unattended, v.ReportedAt, now)
	v.Unattended = &readiness
	return v, nil
}

// UnattendedForPrincipal is the report-only integration seam for routine
// routing/claim. Call with the pairing mutation fence held in the final claim
// transaction, alongside existing owner/account/generation/capability gates.
// It grants no authority, takes no new lock and never changes legacy runs.
// Missing, revoked and other-tenant computers cannot become available.
func UnattendedForPrincipal(ctx context.Context, tx pgx.Tx, principal string) (hostcapacity.UnattendedView, error) {
	var computer string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent_pairing_computers WHERE principal_id=$1`, principal).Scan(&computer)
	if errors.Is(err, pgx.ErrNoRows) {
		return hostcapacity.UnattendedView{Status: "wait", Reason: "unattended_computer_unknown", Message: "No paired computer is available for this principal.", AfterRebootReason: "unattended_reboot_unknown", AfterRebootMessage: "Unattended recovery after reboot is unconfirmed."}, nil
	}
	if err != nil {
		return hostcapacity.UnattendedView{}, err
	}
	v, err := hostCapacity(ctx, tx, computer)
	if err != nil {
		return hostcapacity.UnattendedView{}, err
	}
	return *v.Unattended, nil
}

func (m *Module) saveHostCapacity(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		ExpectedRevision int64               `json:"expected_revision"`
		Policy           hostcapacity.Policy `json:"policy"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	if in.ExpectedRevision < 1 || in.Policy.Validate() != nil {
		WriteError(w, fail(400, "invalid_request", "valid reviewed capacity policy required"))
		return
	}
	var out hostcapacity.View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "account management required")
		}
		var revision int64
		var state, owner string
		if err := tx.QueryRow(r.Context(), `SELECT c.revision,c.state,q.approved_by::text FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1 FOR NO KEY UPDATE OF c`, r.PathValue("computerId")).Scan(&revision, &state, &owner); err != nil {
			return notFound(err)
		}
		if owner != p.ID {
			return fail(403, "forbidden", "only the computer owner may change its capacity")
		}
		if revision != in.ExpectedRevision || state != "connected" {
			return fail(409, "conflict", "computer changed; review settings again")
		}
		raw, err := json.Marshal(in.Policy)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE agent_pairing_computers SET capacity_policy=$2,revision=revision+1,
    capacity_signals=CASE WHEN $3 THEN capacity_signals ELSE capacity_signals-'input_active' END WHERE id=$1`, r.PathValue("computerId"), raw, in.Policy.Mode == "smart" && in.Policy.ConsiderActivity); err != nil {
			return err
		}
		out, err = hostCapacity(r.Context(), tx, r.PathValue("computerId"))
		if err != nil {
			return err
		}
		return audit(r.Context(), tx, p, "agent_pairing.capacity_changed", map[string]any{"computer_id": r.PathValue("computerId"), "policy": in.Policy})
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

func (m *Module) reportHostCapacity(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.PrincipalFrom(r.Context())
	if !ok || p.Kind != tenant.Agent {
		WriteError(w, fail(403, "forbidden", "runtime required"))
		return
	}
	var signals hostcapacity.Signals
	if err := decode(w, r, &signals); err != nil {
		WriteError(w, err)
		return
	}
	if signals.Validate() != nil {
		WriteError(w, fail(400, "invalid_request", "bounded host signals required"))
		return
	}
	var out hostcapacity.View
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "run.claim", authz.Scope{}) != nil {
			return fail(403, "forbidden", "runtime permission required")
		}
		var computer, state string
		var raw []byte
		err := tx.QueryRow(r.Context(), `SELECT id::text,state,capacity_policy FROM agent_pairing_computers WHERE principal_id=$1 FOR NO KEY UPDATE`, p.ID).Scan(&computer, &state, &raw)
		if errors.Is(err, pgx.ErrNoRows) {
			out = hostcapacity.View{Policy: hostcapacity.Default(), History: []hostcapacity.Point{}}
			return nil
		}
		if err != nil {
			return err
		}
		if state != "connected" {
			return fail(409, "enrollment_draining", "computer is not accepting new work")
		}
		var policy hostcapacity.Policy
		if err = json.Unmarshal(raw, &policy); err != nil {
			return err
		}
		if policy.Mode != "smart" || !policy.ConsiderActivity {
			signals.InputActive = nil
		}
		data, err := json.Marshal(signals)
		if err != nil {
			return err
		}
		// At most one point per minute, one hour of history, and 60 points. Receipt
		// time is authoritative: a daemon cannot extend freshness with its clock.
		if _, err = tx.Exec(r.Context(), `UPDATE agent_pairing_computers c SET capacity_signals=$2,capacity_reported_at=clock_timestamp(),
   capacity_history=CASE WHEN $3::float8 IS NULL THEN capacity_history
    WHEN EXISTS(SELECT 1 FROM jsonb_array_elements(capacity_history) p WHERE (p->>'at')::timestamptz>clock_timestamp()-interval '1 minute') THEN capacity_history
    ELSE (SELECT coalesce(jsonb_agg(p ORDER BY p->>'at'),'[]'::jsonb) FROM
     (SELECT p FROM jsonb_array_elements(capacity_history || jsonb_build_array(jsonb_build_object('at',clock_timestamp(),'load',$3::float8))) p
      WHERE (p->>'at')::timestamptz>clock_timestamp()-interval '1 hour' ORDER BY p->>'at' DESC LIMIT 60) points) END WHERE id=$1`, computer, data, signals.Load); err != nil {
			return err
		}
		out, err = hostCapacity(r.Context(), tx, computer)
		return err
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, out)
}

// Called while the pairing access fence is held, before the final claim write.
// All starts on a computer count together, across accounts and harnesses.
func hostStartFence(ctx context.Context, tx pgx.Tx, account string) error {
	var computer string
	err := tx.QueryRow(ctx, `SELECT computer_id::text FROM agent_pairing_enrollments WHERE account_id=$1`, account).Scan(&computer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	v, err := hostCapacity(ctx, tx, computer)
	if err != nil {
		return err
	}
	if v.Reason != "" {
		return fail(409, "host_capacity_wait", "waiting for host capacity: "+v.Reason)
	}
	return nil
}

// HostCapacityForPrincipal is a read-only advisory for queued managed runs.
func HostCapacityForPrincipal(ctx context.Context, tx pgx.Tx, principal string) (*hostcapacity.View, error) {
	var computer string
	err := tx.QueryRow(ctx, `SELECT id::text FROM agent_pairing_computers WHERE principal_id=$1 AND state='connected'`, principal).Scan(&computer)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := hostCapacity(ctx, tx, computer)
	return &v, err
}

func (m *Module) renameComputer(w http.ResponseWriter, r *http.Request, p tenant.Principal) {
	var in struct {
		Name             string `json:"name"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := decode(w, r, &in); err != nil {
		WriteError(w, err)
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !safeText(in.Name, 128) || in.Name == "" || in.ExpectedRevision < 1 {
		WriteError(w, fail(400, "invalid_request", "reviewed computer name required"))
		return
	}
	err := m.in(r.Context(), p.TenantID, func(tx pgx.Tx) error {
		if authz.RequireTx(r.Context(), tx, p, "account.manage", authz.Scope{}) != nil {
			return fail(403, "forbidden", "account management required")
		}
		var revision int64
		var owner, state string
		if err := tx.QueryRow(r.Context(), `SELECT c.revision,q.approved_by::text,c.state FROM agent_pairing_computers c JOIN agent_pairing_requests q ON q.tenant_id=c.tenant_id AND q.id=c.request_id WHERE c.id=$1 FOR NO KEY UPDATE OF c`, r.PathValue("computerId")).Scan(&revision, &owner, &state); err != nil {
			return notFound(err)
		}
		if owner != p.ID {
			return fail(403, "forbidden", "computer owner required")
		}
		if revision != in.ExpectedRevision || state != "connected" {
			return fail(409, "conflict", "computer changed; review name again")
		}
		if _, err := tx.Exec(r.Context(), `UPDATE agent_pairing_computers SET display_name=$2,revision=revision+1 WHERE id=$1`, r.PathValue("computerId"), in.Name); err != nil {
			return err
		}
		return audit(r.Context(), tx, p, "agent_pairing.renamed", map[string]any{"computer_id": r.PathValue("computerId"), "name": in.Name})
	})
	if err != nil {
		WriteError(w, err)
		return
	}
	reply(w, map[string]string{"name": in.Name})
}
