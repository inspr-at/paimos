// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const recoveryCapability = "session_recovery_v1"

type AgentDiagnosis struct {
	SessionID        string `json:"session_id"`
	ObservedRevision string `json:"observed_revision"`
	Cause            string `json:"cause"`
	Detail           string `json:"detail"`
	Action           string `json:"action"`
}

// Evidence describes what was observed, not an inferred 401 or remote exit.
func diagnoseAgent(s Session, now time.Time, paired, keyLive, hostFresh bool, runStatus string) AgentDiagnosis {
	out := AgentDiagnosis{SessionID: s.ID, ObservedRevision: recoveryRevision(s), Cause: "unknown", Detail: "Reporting stopped; the cause has not been verified."}
	switch {
	case s.ArchivedAt != nil || s.HandedOverToID != nil:
		out.Cause, out.Detail = "registration_closed", "This registration has ended."
	case !paired:
		out.Cause, out.Detail = "host_unpaired", "No paired daemon is bound to this registration."
	case !keyLive:
		out.Cause, out.Detail = "credential_rejected", "The paired daemon credential is revoked or expired. Credential delivery is required before recovery."
	case !hostFresh:
		out.Cause, out.Detail = "host_not_reporting", "The paired host has not reported recently; its process state is unknown."
	case s.Management != "managed" && (!has(s, "attached_reconnect_v1") || s.ProcessOwnership == nil || s.RunID != nil):
		out.Cause, out.Detail = "hook_binding_unavailable", "The attached session has no verified daemon-owned heartbeat and inbox hook binding."
	case s.Management == "unmanaged" && s.StoppedAt != nil:
		out.Cause, out.Detail = "hook_binding_unavailable", "The attached hook registration has ended; reconnect cannot revive it."
	case s.Management == "unmanaged" && (s.HeartbeatAt == nil || now.Sub(*s.HeartbeatAt) >= 2*time.Minute):
		out.Cause, out.Detail, out.Action = "heartbeat_overdue", "The paired host is reporting but the attached hook heartbeat is overdue.", "reconnect"
	case s.Management == "unmanaged" && (s.InboxSeenAt == nil || now.Sub(*s.InboxSeenAt) >= 2*time.Minute):
		out.Cause, out.Detail, out.Action = "inbox_not_listening", "The attached hook is reporting but its inbox is not listening.", "reconnect"
	case s.Management == "unmanaged":
		out.Cause, out.Detail = "reporting", "Attached heartbeat and inbox reporting are current."
	case !has(s, recoveryCapability) || s.ProcessOwnership == nil || s.RunID == nil:
		out.Cause, out.Detail = "adapter_unavailable", "This daemon has not reported support for recovery of this exact process generation."
	case runStatus == "failed" || runStatus == "cancelled" || runStatus == "completed":
		out.Cause, out.Detail, out.Action = "run_ended", "The run ended; the daemon must verify process exit before restarting.", "restart"
	case runStatus != "running" && runStatus != "waiting":
		out.Cause, out.Detail = "ownership_unconfirmed", "Run ownership is unconfirmed; recovery cannot launch another process."
	case s.StoppedAt != nil:
		out.Cause, out.Detail = "ownership_unconfirmed", "The registration ended while the run still reports activity."
	case s.HeartbeatAt == nil || now.Sub(*s.HeartbeatAt) >= 2*time.Minute:
		out.Cause, out.Detail, out.Action = "heartbeat_overdue", "The host is reporting but this session's heartbeat is overdue.", "restart"
	case s.InboxSeenAt == nil || now.Sub(*s.InboxSeenAt) >= 2*time.Minute:
		out.Cause, out.Detail, out.Action = "inbox_not_listening", "The session is reporting but its inbox is not listening.", "reconnect"
	default:
		out.Cause, out.Detail = "reporting", "Heartbeat and inbox reporting are current."
	}
	return out
}

func (m *Module) agentDiagnosis(ctx context.Context, tx pgx.Tx, s Session) (AgentDiagnosis, error) {
	now, err := m.ownershipNow(ctx, tx)
	if err != nil {
		return AgentDiagnosis{}, err
	}
	var paired, keyLive, hostFresh, continued bool
	var runStatus string
	daemon := ""
	if s.ProcessOwnership != nil {
		daemon = s.ProcessOwnership.DaemonID
	}
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers c WHERE c.principal_id=$1 AND c.daemon_id=$2 AND c.state='connected'),
 EXISTS(SELECT 1 FROM agent_pairing_computers c JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id WHERE c.principal_id=$1 AND c.daemon_id=$2 AND c.state='connected' AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>$3)),
 EXISTS(SELECT 1 FROM agent_pairing_computers c WHERE c.principal_id=$1 AND c.daemon_id=$2 AND c.state='connected' AND c.last_seen_at BETWEEN $3-interval '2 minutes' AND $3),
 coalesce((SELECT status FROM agent_runs WHERE id=$4),''),EXISTS(SELECT 1 FROM harness_recoveries WHERE session_id=$5 AND next_run_id IS NOT NULL)`, s.AgentPrincipalID, daemon, now, s.RunID, s.ID).Scan(&paired, &keyLive, &hostFresh, &runStatus, &continued)
	if err != nil {
		return AgentDiagnosis{}, err
	}
	out := diagnoseAgent(s, now, paired, keyLive, hostFresh, runStatus)
	if continued {
		out.Cause, out.Detail, out.Action = "continuation_queued", "A continuation already exists for this session; it follows normal dispatch.", ""
	}
	return out, nil
}

func recoveryPerson(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, action string) error {
	if p.Kind != tenant.Person {
		return workorders.Fail(403, "person required for agent recovery")
	}
	if err := authz.RequireTx(ctx, tx, p, "harness.control", authz.Scope{ProjectID: project}); err != nil {
		return err
	}
	if action == "restart" {
		return authz.RequireTx(ctx, tx, p, "run.create", authz.Scope{ProjectID: project})
	}
	return nil
}

func (m *Module) diagnoseRecovery(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := recoveryPerson(r.Context(), tx, p, r.PathValue("projectId"), ""); err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	out, err := m.agentDiagnosis(r.Context(), tx, s)
	if err == nil && out.Action == "restart" && authz.RequireTx(r.Context(), tx, p, "run.create", authz.Scope{ProjectID: s.ProjectID}) != nil {
		out.Action = ""
	}
	return out, err
}

type AgentRecovery struct {
	ID              string                `json:"id"`
	SessionID       string                `json:"session_id"`
	Action          string                `json:"action"`
	State           string                `json:"state"`
	Outcome         *string               `json:"outcome"`
	NextRunID       *string               `json:"next_run_id"`
	ExpiresAt       time.Time             `json:"expires_at"`
	ClaimedAt       *time.Time            `json:"claimed_at"`
	Ownership       ownedprocess.Identity `json:"expected_ownership"`
	ProjectID       string                `json:"project_id"`
	RunID           *string               `json:"run_id"`
	ExpiresInMS     int64                 `json:"expires_in_ms,omitempty"`
	actor, revision string
	requestDigest   []byte
	contextDigest   []byte
	completion      *string
}

const agentRecoveryColumns = `q.id::text,q.session_id::text,q.action,q.state,q.outcome,q.next_run_id::text,q.expires_at,q.claimed_at,q.expected_ownership,s.project_id::text,s.run_id::text,q.requested_by::text,q.observed_revision,q.request_digest,q.completion,q.context_digest`

func scanAgentRecovery(row pgx.Row) (AgentRecovery, error) {
	var out AgentRecovery
	err := row.Scan(&out.ID, &out.SessionID, &out.Action, &out.State, &out.Outcome, &out.NextRunID, &out.ExpiresAt, &out.ClaimedAt, &out.Ownership, &out.ProjectID, &out.RunID, &out.actor, &out.revision, &out.requestDigest, &out.completion, &out.contextDigest)
	return out, err
}
func agentRecoveryLoad(ctx context.Context, tx pgx.Tx, id string) (AgentRecovery, error) {
	return scanAgentRecovery(tx.QueryRow(ctx, `SELECT `+agentRecoveryColumns+` FROM harness_recoveries q JOIN harness_sessions s ON s.tenant_id=q.tenant_id AND s.id=q.session_id WHERE q.id=$1 FOR UPDATE OF q`, id))
}
func (m *Module) expireAgentRecoveries(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) error {
	rows, err := tx.Query(ctx, `UPDATE harness_recoveries SET state='expired',outcome=CASE WHEN state='claimed' THEN 'unconfirmed' ELSE 'rejected' END,completed_at=clock_timestamp() WHERE session_id=$1 AND state IN ('pending','claimed') AND expires_at<=clock_timestamp() RETURNING id::text,outcome`, s.ID)
	if err != nil {
		return err
	}
	var expired []map[string]string
	for rows.Next() {
		var id, outcome string
		if err = rows.Scan(&id, &outcome); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, map[string]string{"id": id, "outcome": outcome})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, q := range expired {
		if err = record(ctx, tx, p, s, "agent_recovery_expired", nil, q); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) requestAgentRecovery(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in struct {
		RequestID string `json:"request_id"`
		Revision  string `json:"expected_revision"`
		Action    string `json:"action"`
	}
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || len(in.Revision) != 64 || (in.Action != "restart" && in.Action != "reconnect") {
		return nil, workorders.Fail(400, "exact observation, request id and typed recovery action required")
	}
	if err = agentpairing.LockMutation(r.Context(), tx); err != nil {
		return nil, err
	}
	if err = recoveryPerson(r.Context(), tx, p, r.PathValue("projectId"), in.Action); err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(in)
	dg := digest("agent-recovery:"+p.ID, string(raw))
	prior, err := agentRecoveryLoad(r.Context(), tx, in.RequestID)
	if err == nil {
		if prior.SessionID == s.ID && subtle.ConstantTimeCompare(dg, prior.requestDigest) == 1 {
			return prior, nil
		}
		return nil, workorders.Fail(409, "divergent recovery retry")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	d, err := m.agentDiagnosis(r.Context(), tx, s)
	if err != nil {
		return nil, err
	}
	if d.ObservedRevision != in.Revision || d.Action != in.Action {
		return nil, workorders.Fail(409, "recovery observation changed or paired action unavailable")
	}
	if err = m.expireAgentRecoveries(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	var live bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_recoveries WHERE session_id=$1 AND state IN ('pending','claimed'))`, s.ID).Scan(&live); err != nil {
		return nil, err
	}
	if live {
		return nil, workorders.Fail(409, "a recovery request is still pending")
	}
	ownership, _ := json.Marshal(s.ProcessOwnership)
	_, err = tx.Exec(r.Context(), `INSERT INTO harness_recoveries(tenant_id,id,session_id,requested_by,action,observed_revision,expected_ownership,request_digest,context_digest) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, in.RequestID, s.ID, p.ID, in.Action, in.Revision, string(ownership), dg, recoveryContextDigest(s))
	if err != nil {
		return nil, err
	}
	q, err := agentRecoveryLoad(r.Context(), tx, in.RequestID)
	if err != nil {
		return nil, err
	}
	return q, record(r.Context(), tx, p, s, "agent_recovery_requested", nil, q)
}

func (m *Module) readAgentRecovery(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	if err = agentpairing.LockMutation(r.Context(), tx); err != nil {
		return nil, err
	}
	if err = authz.RequireTx(r.Context(), tx, p, "harness.read", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if !workorders.UUID(r.PathValue("requestId")) {
		return nil, workorders.Fail(400, "invalid recovery id")
	}
	if err = m.expireAgentRecoveries(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	q, err := agentRecoveryLoad(r.Context(), tx, r.PathValue("requestId"))
	if err != nil {
		return nil, err
	}
	if q.SessionID != s.ID {
		return nil, workorders.Fail(404, "recovery request not found")
	}
	return q, nil
}

type daemonRecoveryInput struct {
	DaemonID   string `json:"daemon_id"`
	Generation string `json:"generation"`
	Outcome    string `json:"outcome,omitempty"`
}

func recoveryDaemon(ctx context.Context, tx pgx.Tx, p tenant.Principal, in daemonRecoveryInput) error {
	if p.Kind != tenant.Agent || !validIdentity(in.Generation) || len(in.DaemonID) > 128 || in.DaemonID == "" {
		return workorders.Fail(403, "paired exact daemon generation required")
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agent_pairing_computers WHERE principal_id=$1 AND daemon_id=$2 AND state='connected')`, p.ID, in.DaemonID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return workorders.Fail(403, "active paired daemon required")
	}
	return nil
}
func recoveryActor(ctx context.Context, tx pgx.Tx, p tenant.Principal, q AgentRecovery) (tenant.Principal, error) {
	actor := tenant.Principal{ID: q.actor, TenantID: p.TenantID}
	if err := tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, actor.ID).Scan(&actor.Kind); err != nil {
		return actor, err
	}
	return actor, recoveryPerson(ctx, tx, actor, q.ProjectID, q.Action)
}

func (m *Module) claimAgentRecoveries(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in daemonRecoveryInput
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Outcome != "" {
		return nil, workorders.Fail(400, "claim cannot supply an outcome")
	}
	if err = agentpairing.LockMutation(r.Context(), tx); err != nil {
		return nil, err
	}
	if err = recoveryDaemon(r.Context(), tx, p, in); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT q.id::text FROM harness_recoveries q JOIN harness_sessions s ON s.tenant_id=q.tenant_id AND s.id=q.session_id WHERE s.agent_principal_id=$1 AND q.state='pending' AND q.expires_at>clock_timestamp() AND q.expected_ownership->>'daemon_id'=$2 AND q.expected_ownership->>'generation'=$3 ORDER BY q.created_at,q.id LIMIT 10`, p.ID, in.DaemonID, in.Generation)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []AgentRecovery{}
	for _, id := range ids {
		q, err := agentRecoveryLoad(r.Context(), tx, id)
		if err != nil {
			return nil, err
		}
		s, err := load(r.Context(), tx, q.ProjectID, q.SessionID, true)
		if err != nil {
			return nil, err
		}
		if err = authz.RequireTx(r.Context(), tx, p, "harness.worker", authz.Scope{ProjectID: s.ProjectID}); err != nil {
			return nil, err
		}
		if _, err = recoveryActor(r.Context(), tx, p, q); err != nil {
			if !errors.Is(err, authz.ErrForbidden) {
				return nil, err
			}
			if _, err = tx.Exec(r.Context(), `UPDATE harness_recoveries SET state='completed',outcome='rejected',completed_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				return nil, err
			}
			if err = record(r.Context(), tx, p, s, "agent_recovery_rejected", nil, map[string]string{"id": id, "reason": "authorization_revoked"}); err != nil {
				return nil, err
			}
			continue
		}
		if s.ArchivedAt != nil || s.HandedOverToID != nil || q.revision != recoveryRevision(s) {
			if _, err = tx.Exec(r.Context(), `UPDATE harness_recoveries SET state='completed',outcome='rejected',completed_at=clock_timestamp() WHERE id=$1`, id); err != nil {
				return nil, err
			}
			if err = record(r.Context(), tx, p, s, "agent_recovery_rejected", nil, map[string]string{"id": id, "reason": "observation_changed"}); err != nil {
				return nil, err
			}
			continue
		}
		if _, err = tx.Exec(r.Context(), `UPDATE harness_recoveries SET state='claimed',claimed_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return nil, err
		}
		q, err = agentRecoveryLoad(r.Context(), tx, id)
		if err != nil {
			return nil, err
		}
		now, err := m.ownershipNow(r.Context(), tx)
		if err != nil {
			return nil, err
		}
		q.ExpiresInMS = controlTTL(&q.ExpiresAt, now).Milliseconds()
		out = append(out, q)
		if err = record(r.Context(), tx, p, s, "agent_recovery_claimed", nil, q); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Module) completeAgentRecovery(r *http.Request, tx pgx.Tx, p tenant.Principal) (result any, err error) {
	r, flush := deferControlEvents(r, tx)
	defer flush(&err)
	var in daemonRecoveryInput
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(r.PathValue("requestId")) || (in.Outcome != "reconnected" && in.Outcome != "exited" && in.Outcome != "rejected") {
		return nil, workorders.Fail(400, "typed completion required")
	}
	if err = agentpairing.LockMutation(r.Context(), tx); err != nil {
		return nil, err
	}
	if err = recoveryDaemon(r.Context(), tx, p, in); err != nil {
		return nil, err
	}
	q, err := agentRecoveryLoad(r.Context(), tx, r.PathValue("requestId"))
	if err != nil {
		return nil, err
	}
	s, err := load(r.Context(), tx, q.ProjectID, q.SessionID, true)
	if err != nil {
		return nil, err
	}
	if err = authz.RequireTx(r.Context(), tx, p, "harness.worker", authz.Scope{ProjectID: s.ProjectID}); err != nil {
		return nil, err
	}
	if s.AgentPrincipalID != p.ID || q.Ownership.DaemonID != in.DaemonID || q.Ownership.Generation != in.Generation {
		return nil, workorders.Fail(403, "recovery daemon binding rejected")
	}
	if q.State == "completed" && q.completion != nil {
		if *q.completion == in.Outcome {
			return q, nil
		}
		return nil, workorders.Fail(409, "divergent recovery completion")
	}
	now, err := m.ownershipNow(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if q.State != "claimed" || !q.ExpiresAt.After(now) {
		return nil, workorders.Fail(409, "recovery claim expired; outcome unconfirmed")
	}
	actor, err := recoveryActor(r.Context(), tx, p, q)
	if err != nil {
		return nil, err
	}
	if s.ArchivedAt != nil || s.HandedOverToID != nil || s.ProcessOwnership == nil || *s.ProcessOwnership != q.Ownership || subtle.ConstantTimeCompare(q.contextDigest, recoveryContextDigest(s)) != 1 {
		return nil, workorders.Fail(409, "session process binding changed")
	}
	// Bound the recorded handover before admission. A later failure would roll
	// back a Decision Desk transition that recovery must commit.
	handover, _ := json.Marshal(recoveryHandover(s))
	if len(handover) > 16000 {
		return nil, workorders.Fail(409, "recorded handover exceeds recovery bound")
	}
	outcome := "rejected"
	var next *string
	var event func() error
	var rejection *workorders.Error
	if in.Outcome == "reconnected" {
		if q.Action != "reconnect" || q.revision != recoveryRevision(s) || s.HeartbeatAt == nil || now.Sub(*s.HeartbeatAt) > ownershipWindow || s.InboxSeenAt == nil || now.Sub(*s.InboxSeenAt) > ownershipWindow {
			return nil, workorders.Fail(409, "fresh heartbeat and inbox evidence required")
		}
		outcome = "reconnected"
	} else if in.Outcome == "exited" {
		if q.Action != "restart" || s.RunID == nil || s.StoppedAt == nil || s.StopReason == nil || (*s.StopReason != "stopped" && *s.StopReason != "process_exited" && *s.StopReason != "process_failed") {
			return nil, workorders.Fail(409, "verified old process exit required")
		}
		model, effort := "", ""
		if s.Model != nil {
			model = *s.Model
		}
		if s.ReasoningEffort != nil {
			effort = *s.ReasoningEffort
		}
		if m.sessionRecovery == nil {
			return nil, workorders.Fail(409, "run recovery admission unavailable")
		}
		runID, flushRun, refused, e := m.sessionRecovery(r.Context(), tx, actor, s.ProjectID, *s.RunID, model, effort)
		if e != nil {
			return nil, e
		}
		if refused != nil {
			rejection = refused
		} else {
			next = &runID
			event = flushRun
			outcome = "continuation_queued"
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE harness_recoveries SET state='completed',completion=$2,outcome=$3,next_run_id=$4,completed_at=clock_timestamp(),handover=$5::jsonb,service_tier=$6,display_label=$7 WHERE id=$1`, q.ID, in.Outcome, outcome, next, string(handover), s.ServiceTier, s.DisplayLabel); err != nil {
		return nil, err
	}
	q, err = agentRecoveryLoad(r.Context(), tx, q.ID)
	if err != nil {
		return nil, err
	}
	if event != nil {
		if err = event(); err != nil {
			return nil, err
		}
	}
	if err = record(r.Context(), tx, p, s, "agent_recovery_completed", nil, q); err != nil {
		return nil, err
	}
	// The endpoint commits a *workorders.Error result, then writes it. Returning
	// the refusal as an error would undo the decision and leave recovery claimed.
	if rejection != nil {
		return rejection, nil
	}
	return q, nil
}

func recoveryHandover(s Session) *Handover {
	if s.Pause != nil && s.Pause.Handover != nil {
		return s.Pause.Handover
	}
	if s.Continuation != nil {
		return &s.Continuation.Handover
	}
	return nil
}
func recoveryContextDigest(s Session) []byte {
	handover := recoveryHandover(s)
	raw, _ := json.Marshal(struct {
		Revision                                                                 int64
		Run, Ticket, Order, Model, Effort, Tier, Label, Parent, Worktree, Branch *string
		Handover                                                                 *Handover
	}{s.Revision, s.RunID, s.TicketNodeID, s.WorkOrderID, s.Model, s.ReasoningEffort, s.ServiceTier, s.DisplayLabel, s.ParentID, s.Worktree, s.Branch, handover})
	return digest("recovery-context", string(raw))
}

// One bounded query for a list page; no per-row requests or host-name joins.
func readAgentRecoveryEvidence(ctx context.Context, tx pgx.Tx, ids []string, out map[string]StateEvidence, allowed func(string, string) bool) error {
	rows, err := tx.Query(ctx, `SELECT s.id::text,s.project_id::text,s.agent_principal_id::text,s.host,s.management,s.run_id::text,s.display_label,s.revision,s.stopped_at,s.archived_at,s.process_ownership,s.handed_over_to_id::text,s.heartbeat_at,s.inbox_seen_at,s.capabilities,clock_timestamp(),
 coalesce(c.state='connected',false),coalesce(c.state='connected' AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at>clock_timestamp()),false),coalesce(c.last_seen_at BETWEEN clock_timestamp()-interval '2 minutes' AND clock_timestamp(),false),EXISTS(SELECT 1 FROM harness_recoveries q WHERE q.session_id=s.id AND q.next_run_id IS NOT NULL)
 FROM harness_sessions s LEFT JOIN agent_pairing_computers c ON c.tenant_id=s.tenant_id AND c.principal_id=s.agent_principal_id AND c.daemon_id=s.process_ownership->>'daemon_id'
 LEFT JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id WHERE s.id=ANY($1::uuid[])`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var s Session
		var now time.Time
		var paired, keyLive, hostFresh, continued bool
		if err = rows.Scan(&s.ID, &s.ProjectID, &s.AgentPrincipalID, &s.Host, &s.Management, &s.RunID, &s.DisplayLabel, &s.Revision, &s.StoppedAt, &s.ArchivedAt, &s.ProcessOwnership, &s.HandedOverToID, &s.HeartbeatAt, &s.InboxSeenAt, &s.Capabilities, &now, &paired, &keyLive, &hostFresh, &continued); err != nil {
			return err
		}
		if !allowed("harness.control", s.ProjectID) {
			continue
		}
		evidence := out[s.ID]
		status := ""
		if evidence.RunStatus != nil {
			status = *evidence.RunStatus
		}
		diagnosis := diagnoseAgent(s, now, paired, keyLive, hostFresh, status)
		if continued {
			diagnosis.Cause, diagnosis.Detail, diagnosis.Action = "continuation_queued", "A continuation already exists for this session; it follows normal dispatch.", ""
		}
		if diagnosis.Action == "restart" && !allowed("run.create", s.ProjectID) {
			diagnosis.Action = ""
		}
		evidence.AgentRecovery = &diagnosis
		out[s.ID] = evidence
	}
	return rows.Err()
}
