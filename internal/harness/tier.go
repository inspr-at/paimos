// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/servicetier"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type TierRequest struct {
	ID          string     `json:"id"`
	SessionID   string     `json:"session_id"`
	Tier        string     `json:"tier"`
	Reason      string     `json:"reason"`
	RequestedBy string     `json:"requested_by_principal_id"`
	State       string     `json:"state"`
	DecidedBy   *string    `json:"decided_by_principal_id"`
	ControlID   *string    `json:"control_id"`
	CreatedAt   time.Time  `json:"created_at"`
	DecidedAt   *time.Time `json:"decided_at"`
}

const tierRequestColumns = `id::text,session_id::text,tier,reason,requested_by_principal_id::text,state,decided_by_principal_id::text,control_id::text,created_at,decided_at`

func scanTierRequest(row pgx.Row) (TierRequest, error) {
	var q TierRequest
	err := row.Scan(&q.ID, &q.SessionID, &q.Tier, &q.Reason, &q.RequestedBy, &q.State, &q.DecidedBy, &q.ControlID, &q.CreatedAt, &q.DecidedAt)
	return q, err
}

type TierState struct {
	History          []TierHistory        `json:"history"`
	HistoryTruncated bool                 `json:"history_truncated"`
	RunCost          *TierRunCost         `json:"run_cost"`
	Estimates        []TierEstimate       `json:"estimates"`
	SessionID        string               `json:"session_id"`
	Revision         int64                `json:"revision"`
	Active           *string              `json:"active_tier"`
	Pending          *Control             `json:"pending"`
	LastChange       *Control             `json:"last_change"`
	ReadOnly         bool                 `json:"read_only"`
	ReadOnlyReason   string               `json:"read_only_reason,omitempty"`
	Reports          []servicetier.Report `json:"reports"`
	Requests         []TierRequest        `json:"requests"`
}

func sessionTierReport(s Session, model string) servicetier.Report {
	for _, r := range s.ServiceTierReports {
		if r.Model == model && r.Harness == s.Harness {
			return r
		}
	}
	version := "unknown"
	if s.HarnessVersion != nil {
		version = *s.HarnessVersion
	}
	return servicetier.Advertised(s.Harness, model, version)
}
func tierModel(s Session) string {
	if s.Model != nil {
		return *s.Model
	}
	return "unknown"
}
func validateTier(ctx context.Context, tx pgx.Tx, s Session, tier string) error {
	if !servicetier.Valid(tier) {
		return workorders.Fail(400, "invalid service tier")
	}
	if _, ok := sessionTierReport(s, tierModel(s)).Find(tier); !ok {
		return workorders.Fail(400, "tier not offered with a published price for this model")
	}
	return nil
}
func tierReadOnly(s Session) string {
	if s.StoppedAt != nil || s.ArchivedAt != nil || s.Phase == "stopping" {
		return "This session has ended or is stopping"
	}
	if s.Management != "managed" {
		return "Reported by the harness; " + sessionTierReport(s, tierModel(s)).ChangeInstructions
	}
	if !has(s, servicetier.Capability) {
		return "This daemon does not support confirmed tier changes"
	}
	return ""
}
func tierPerson(r *http.Request, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return workorders.Fail(403, "only a person may change or decide a service tier")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.control", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return workorders.Fail(403, "harness.control permission required")
		}
		return err
	}
	return nil
}
func (m *Module) tierState(r *http.Request, tx pgx.Tx, p tenant.Principal, s Session) (TierState, error) {
	out := TierState{SessionID: s.ID, Revision: s.ServiceTierRevision, Active: s.ServiceTier, Reports: s.ServiceTierReports, Requests: []TierRequest{}}
	if out.Reports == nil || len(out.Reports) == 0 {
		out.Reports = []servicetier.Report{sessionTierReport(s, tierModel(s))}
	}
	out.ReadOnlyReason = tierReadOnly(s)
	out.ReadOnly = out.ReadOnlyReason != ""
	c, err := scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND kind='tier' AND state<>'completed' ORDER BY sequence LIMIT 1`, s.ID))
	if err == nil {
		out.Pending = &c
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	last, err := scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND kind='tier' AND state='completed' ORDER BY sequence DESC LIMIT 1`, s.ID))
	if err == nil {
		out.LastChange = &last
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+tierRequestColumns+` FROM harness_tier_requests WHERE session_id=$1 ORDER BY created_at DESC,id DESC LIMIT 50`, s.ID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		q, err := scanTierRequest(rows)
		if err != nil {
			return out, err
		}
		out.Requests = append(out.Requests, q)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	rows.Close()
	err = m.tierEvidence(r, tx, s, &out)
	return out, err
}
func (m *Module) getTier(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if err = m.expireControls(r, tx, p, s); err != nil {
		return nil, err
	}
	return m.tierState(r, tx, p, s)
}
func (m *Module) reportTiers(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := worker(r.Context(), tx, r, p)
	if err != nil {
		return nil, err
	}
	var in struct {
		Reports []servicetier.Report `json:"reports"`
		Active  *string              `json:"active_tier"`
	}
	r.Body = http.MaxBytesReader(nil, r.Body, 65536)
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if len(in.Reports) < 1 || len(in.Reports) > 32 || (in.Active != nil && !servicetier.Valid(*in.Active)) {
		return nil, workorders.Fail(400, "invalid tier reports")
	}
	seen := map[string]bool{}
	now, err := m.ownershipNow(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	for _, report := range in.Reports {
		if report.Harness != s.Harness || !validUsageModel(report.Model) || seen[report.Model] || report.CheckedAt.After(now.Add(5*time.Minute)) {
			return nil, workorders.Fail(400, "invalid report binding or checked-at")
		}
		seen[report.Model] = true
		if err = report.Validate(); err != nil {
			return nil, workorders.Fail(400, err.Error())
		}
	}
	if s.Management == "managed" && in.Active != nil && !(s.ServiceTier == nil && *in.Active == "default") {
		return nil, workorders.Fail(409, "managed tiers require a person control and daemon confirmation")
	}
	var pending bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_controls WHERE session_id=$1 AND kind='tier' AND state<>'completed')`, s.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, workorders.Fail(409, "tier report is frozen while a person change is pending")
	}
	encoded, err := json.Marshal(in.Reports)
	if err != nil {
		return nil, err
	}
	old := s
	s, err = scanSession(tx.QueryRow(r.Context(), `UPDATE harness_sessions SET service_tier_reports=$2,service_tier=coalesce($3,service_tier),service_tier_revision=service_tier_revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, encoded, in.Active))
	if err != nil {
		return nil, err
	}
	return s, record(r.Context(), tx, p, s, "tier_reported", old, s)
}

type tierChange struct {
	UndoOf    string                `json:"undo_of_control_id,omitempty"`
	RequestID string                `json:"request_id"`
	Tier      string                `json:"tier"`
	Revision  *int64                `json:"expected_revision"`
	Ownership ownedprocess.Identity `json:"expected_ownership"`
	Decision  string                `json:"decision,omitempty"`
}

func (m *Module) setTier(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := tierPerson(r, tx, p); err != nil {
		return nil, err
	}
	var in tierChange
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Decision != "" {
		return nil, workorders.Fail(400, "use the decision endpoint")
	}
	return m.changeTier(r, tx, p, in, "")
}
func (m *Module) changeTier(r *http.Request, tx pgx.Tx, p tenant.Principal, in tierChange, requestID string) (any, error) {
	if !workorders.UUID(in.RequestID) || !servicetier.Valid(in.Tier) || in.Revision == nil || *in.Revision < 0 {
		return nil, workorders.Fail(400, "request id, tier and expected revision required")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if in.UndoOf != "" && !workorders.UUID(in.UndoOf) {
		return nil, workorders.Fail(400, "invalid undo control")
	}
	// Idempotent replay is bound to the actor and the entire original change.
	raw, _ := json.Marshal(in)
	dg := digest("tier-change:"+p.ID+":"+requestID, string(raw))
	var prior []byte
	err = tx.QueryRow(r.Context(), `SELECT request_digest FROM harness_controls WHERE session_id=$1 AND id=$2 AND kind='tier'`, s.ID, in.RequestID).Scan(&prior)
	if err == nil {
		if !bytes.Equal(prior, dg) {
			return nil, workorders.Fail(409, "request id already used")
		}
		return m.tierState(r, tx, p, s)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if in.UndoOf != "" {
		var valid bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_controls c JOIN harness_tier_history h ON h.tenant_id=c.tenant_id AND h.control_id=c.id
   WHERE c.session_id=$1 AND c.id=$2 AND h.action IN ('switch_requested','approved') AND h.from_tier=$3
   AND ((c.state='pending' AND $3=service_tier) OR (c.state='completed' AND c.outcome='applied' AND c.value=service_tier))
   AND c.sequence=(SELECT max(sequence) FROM harness_controls WHERE session_id=$1 AND kind='tier')) FROM harness_sessions WHERE id=$1`, s.ID, in.UndoOf, in.Tier).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, workorders.Fail(409, "tier control is no longer undoable")
		}
	}
	if reason := tierReadOnly(s); reason != "" {
		return nil, workorders.Fail(409, reason)
	}
	if *in.Revision != s.ServiceTierRevision {
		return nil, workorders.Fail(409, "service tier changed; refresh")
	}
	now, err := m.ownershipNow(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if !forceAvailable(s, now) || s.ProcessOwnership == nil || *s.ProcessOwnership != in.Ownership {
		return nil, workorders.Fail(409, "live exact daemon/process ownership required")
	}
	if err = validateTier(r.Context(), tx, s, in.Tier); err != nil {
		return nil, err
	}
	if err = m.expireControls(r, tx, p, s); err != nil {
		return nil, err
	}
	c, err := scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND kind='tier' AND state<>'completed'`, s.ID))
	if err == nil {
		if c.State != "pending" || s.ServiceTier == nil || *s.ServiceTier != in.Tier {
			return nil, workorders.Fail(409, "a tier change is already in flight")
		}
		// Undo before claim never races an adapter application. Claimed controls
		// must settle; their undo is a new fenced control afterwards.
		if _, err = tx.Exec(r.Context(), `UPDATE harness_controls SET state='completed',outcome='rejected',reason='tier_cancelled',claimed_at=clock_timestamp(),completed_at=clock_timestamp() WHERE id=$1`, c.ID); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE harness_tier_requests SET state='pending',decided_at=NULL,decided_by_principal_id=NULL,control_id=NULL WHERE session_id=$1 AND control_id=$2`, s.ID, c.ID); err != nil {
			return nil, err
		}
		// Persist the undo operation too, so a lost response can be replayed.
		if err = persistTierReceipt(r.Context(), tx, p, s, in, dg, "tier_cancelled_pending"); err != nil {
			return nil, err
		}
		var asked *string
		err = tx.QueryRow(r.Context(), `SELECT request_id::text FROM harness_tier_history WHERE session_id=$1 AND control_id=$2 AND action IN ('approved','switch_requested') ORDER BY id DESC LIMIT 1`, s.ID, c.ID).Scan(&asked)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		originalRequest := ""
		if asked != nil {
			originalRequest = *asked
		}
		if err = appendTierHistory(r.Context(), tx, p, s, "cancelled", c.Value, originalRequest, in.RequestID, c.ID); err != nil {
			return nil, err
		}
		if err = record(r.Context(), tx, p, s, "tier_cancelled", c, map[string]any{"control_id": c.ID}); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	} else {
		if s.ServiceTier != nil && *s.ServiceTier == in.Tier {
			if err = persistTierReceipt(r.Context(), tx, p, s, in, dg, "tier_already_active"); err != nil {
				return nil, err
			}
			return m.tierState(r, tx, p, s)
		}
		body, _ := json.Marshal(map[string]any{"request_id": in.RequestID, "kind": "tier", "value": in.Tier, "expected_ownership": in.Ownership})
		controlRequest := r.Clone(r.Context())
		controlRequest.Body = io.NopCloser(bytes.NewReader(body))
		result, err := m.queueManagedControl(controlRequest, tx, p, true)
		if err != nil {
			return nil, err
		}
		c = result.(Control)
		if _, err = tx.Exec(r.Context(), `UPDATE harness_controls SET request_digest=$2 WHERE id=$1`, c.ID, dg); err != nil {
			return nil, err
		}
		// Stepping to exactly what the agent asked for is approval, too.
		var approved string
		err = tx.QueryRow(r.Context(), `UPDATE harness_tier_requests SET state='approved',decided_at=clock_timestamp(),decided_by_principal_id=$3,control_id=$4 WHERE session_id=$1 AND state='pending' AND tier=$2 AND ($5='' OR id=nullif($5,'')::uuid) RETURNING id::text`, s.ID, in.Tier, p.ID, c.ID, requestID).Scan(&approved)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		action := "switch_requested"
		if approved != "" {
			action = "approved"
		}
		if in.UndoOf != "" {
			action = "undo_requested"
		}
		if err = appendTierHistory(r.Context(), tx, p, s, action, in.Tier, approved, c.ID, in.UndoOf); err != nil {
			return nil, err
		}
	}
	s, err = scanSession(tx.QueryRow(r.Context(), `UPDATE harness_sessions SET service_tier_revision=service_tier_revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID))
	if err != nil {
		return nil, err
	}
	return m.tierState(r, tx, p, s)
}

// Completed receipts bind successful operations that need no daemon work.
// The caller holds the session lock, which also serializes control sequences.
func persistTierReceipt(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, in tierChange, dg []byte, reason string) error {
	identity, err := json.Marshal(in.Ownership)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO harness_controls(tenant_id,id,session_id,kind,sequence,requested_by_principal_id,expected_ownership,request_digest,expires_at,value,state,outcome,reason,claimed_at,completed_at) SELECT $1,$2,$3,'tier',coalesce(max(sequence),0)+1,$4,$5::jsonb,$6,clock_timestamp(),$7,'completed','applied',$8,clock_timestamp(),clock_timestamp() FROM harness_controls WHERE session_id=$3`, p.TenantID, in.RequestID, s.ID, p.ID, string(identity), dg, in.Tier, reason)
	return err
}
func (m *Module) askTier(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if p.Kind != tenant.Agent || p.ID != s.AgentPrincipalID {
		return nil, workorders.Fail(403, "only this session's agent may ask")
	}
	var in struct {
		ID     string `json:"request_id"`
		Tier   string `json:"tier"`
		Reason string `json:"reason"`
	}
	if err = workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.ID) || !servicetier.Valid(in.Tier) || !utf8.ValidString(in.Reason) || strings.TrimSpace(in.Reason) == "" || utf8.RuneCountInString(in.Reason) > 500 {
		return nil, workorders.Fail(400, "request id, tier and bounded reason required")
	}
	q, err := scanTierRequest(tx.QueryRow(r.Context(), `SELECT `+tierRequestColumns+` FROM harness_tier_requests WHERE session_id=$1 AND id=$2`, s.ID, in.ID))
	if err == nil {
		if q.Tier != in.Tier || q.Reason != in.Reason || q.RequestedBy != p.ID {
			return nil, workorders.Fail(409, "request id already used")
		}
		return q, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if reason := tierReadOnly(s); reason != "" {
		return nil, workorders.Fail(409, reason)
	}
	if err = validateTier(r.Context(), tx, s, in.Tier); err != nil {
		return nil, err
	}
	if s.ServiceTier != nil && *s.ServiceTier == in.Tier {
		return nil, workorders.Fail(409, "this tier is already active")
	}
	var pending bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM harness_tier_requests WHERE session_id=$1 AND state='pending') OR EXISTS(SELECT 1 FROM harness_controls WHERE session_id=$1 AND kind='tier' AND state<>'completed')`, s.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending {
		return nil, workorders.Fail(409, "a tier request or change is pending")
	}
	q, err = scanTierRequest(tx.QueryRow(r.Context(), `INSERT INTO harness_tier_requests(tenant_id,id,session_id,tier,reason,requested_by_principal_id) VALUES($1,$2,$3,$4,$5,$6) RETURNING `+tierRequestColumns, p.TenantID, in.ID, s.ID, in.Tier, in.Reason, p.ID))
	if err != nil {
		return nil, err
	}
	if err = appendTierHistory(r.Context(), tx, p, s, "requested", in.Tier, q.ID, "", ""); err != nil {
		return nil, err
	}
	return q, record(r.Context(), tx, p, s, "tier_requested", nil, q)
}
func (m *Module) decideTier(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := tierPerson(r, tx, p); err != nil {
		return nil, err
	}
	var in tierChange
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Decision != "approve" && in.Decision != "decline" {
		return nil, workorders.Fail(400, "approve or decline required")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	id := r.PathValue("requestId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid request id")
	}
	q, err := scanTierRequest(tx.QueryRow(r.Context(), `SELECT `+tierRequestColumns+` FROM harness_tier_requests WHERE session_id=$1 AND id=$2 FOR UPDATE`, s.ID, id))
	if err != nil {
		return nil, err
	}
	if q.State != "pending" {
		if in.Decision == "approve" && q.State == "approved" && q.ControlID != nil && *q.ControlID == in.RequestID {
			return m.changeTier(r, tx, p, in, id)
		}
		if in.Decision == "decline" && q.State == "declined" && q.DecidedBy != nil && *q.DecidedBy == p.ID && q.Tier == in.Tier {
			return m.tierState(r, tx, p, s)
		}
		return nil, workorders.Fail(409, "request already decided")
	}
	if q.Tier != in.Tier {
		return nil, workorders.Fail(400, "decision tier differs from request")
	}
	if in.Decision == "approve" {
		return m.changeTier(r, tx, p, in, id)
	}
	if _, err = tx.Exec(r.Context(), `UPDATE harness_tier_requests SET state='declined',decided_at=clock_timestamp(),decided_by_principal_id=$2 WHERE id=$1`, id, p.ID); err != nil {
		return nil, err
	}
	if err = appendTierHistory(r.Context(), tx, p, s, "declined", q.Tier, q.ID, "", ""); err != nil {
		return nil, err
	}
	if err = record(r.Context(), tx, p, s, "tier_declined", q, map[string]any{"request_id": id, "state": "declined"}); err != nil {
		return nil, err
	}
	return m.tierState(r, tx, p, s)
}
