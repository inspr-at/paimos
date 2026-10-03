// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Control struct {
	Value              string                 `json:"value,omitempty"`
	RequestPayload     *SessionRequestPayload `json:"request_payload,omitempty"`
	ExpectedGeneration *string                `json:"expected_generation,omitempty"`
	ExpiresAt          *time.Time             `json:"expires_at,omitempty"`
	ExpectedOwnership  *ownedprocess.Identity `json:"expected_ownership,omitempty"`
	requestDigest      []byte
	ID                 string     `json:"id"`
	SessionID          string     `json:"session_id"`
	Kind               string     `json:"kind"`
	State              string     `json:"state"`
	Sequence           int64      `json:"sequence"`
	Outcome            *string    `json:"outcome"`
	Reason             *string    `json:"reason"`
	CreatedAt          time.Time  `json:"created_at"`
	ClaimedAt          *time.Time `json:"claimed_at"`
	CompletedAt        *time.Time `json:"completed_at"`
}

const controlColumns = `id::text,session_id::text,kind,state,sequence,outcome,reason,created_at,claimed_at,completed_at,expected_ownership,request_digest,expires_at,coalesce(value,''),request_payload,expected_generation::text`

func scanControl(row pgx.Row) (Control, error) {
	var c Control
	err := row.Scan(&c.ID, &c.SessionID, &c.Kind, &c.State, &c.Sequence, &c.Outcome, &c.Reason, &c.CreatedAt, &c.ClaimedAt, &c.CompletedAt, &c.ExpectedOwnership, &c.requestDigest, &c.ExpiresAt, &c.Value, &c.RequestPayload, &c.ExpectedGeneration)
	return c, err
}
func (m *Module) interrupt(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.requestControl(r, tx, p, "interrupt")
}
func (m *Module) stop(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return m.requestControl(r, tx, p, "stop")
}
func (m *Module) requestControl(r *http.Request, tx pgx.Tx, p tenant.Principal, kind string) (any, error) {
	var in struct{}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "only a person may control a managed session")
	}
	if has(s, managedControlCapability) {
		return nil, workorders.Fail(409, "use managed-controls with request id and exact ownership")
	}
	if s.StoppedAt != nil || s.Management != "managed" || !has(s, kind) {
		return nil, workorders.Fail(409, "owned control unavailable")
	}
	var sequence int64
	err = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0)+1 FROM harness_controls WHERE session_id=$1`, s.ID).Scan(&sequence)
	if err != nil {
		return nil, err
	}
	c, err := scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,session_id,kind,sequence,requested_by_principal_id) VALUES($1,$2,$3,$4,$5) RETURNING `+controlColumns, p.TenantID, s.ID, kind, sequence, p.ID))
	if err != nil {
		return nil, err
	}
	return c, record(ctx, tx, p, s, "control_requested", nil, c)
}
func (m *Module) control(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if s, err = expirePause(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	if err := expireSessionRequests(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	id := r.PathValue("controlId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid control id")
	}
	if err := m.expireControls(r, tx, p, s); err != nil {
		return nil, err
	}
	return scanControl(tx.QueryRow(r.Context(), `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND id=$2`, s.ID, id))
}
func (m *Module) yield(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct{}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	// Yield stays a managed-worker action (AEON-260); unmanaged session requests
	// (AEON-225) complete directly from pending.
	if s.Management != "managed" {
		return nil, workorders.Fail(409, "managed worker required")
	}
	if s, err = expirePause(ctx, tx, p, s); err != nil {
		return nil, err
	}
	if err := expireSessionRequests(ctx, tx, p, s); err != nil {
		return nil, err
	}
	if err := m.expireControls(r, tx, p, s); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND state='pending' AND NOT(kind='stop' AND coalesce(request_payload->>'pause','false')='true') AND (coalesce(request_payload->>'stop_now','false')<>'true' OR $2::jsonb IS NOT NULL) ORDER BY sequence FOR UPDATE`, s.ID, s.ProcessOwnership)
	if err != nil {
		return nil, err
	}
	pending := []Control{}
	for rows.Next() {
		c, e := scanControl(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		pending = append(pending, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	type offered struct {
		Control
		Text        string `json:"text,omitempty"`
		ExpiresInMS int64  `json:"expires_in_ms"`
	}
	claimed := []offered{}
	for _, old := range pending {
		if old.Kind == "tier" && s.Activity != "idle" {
			continue
		}
		c, e := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='claimed',claimed_at=clock_timestamp(),expires_at=CASE WHEN kind='tier' THEN least(expires_at,clock_timestamp()+interval '45 seconds') WHEN request_payload->>'stop_now'='true' OR request_payload->>'level'='pause_quickly' THEN clock_timestamp()+interval '45 seconds' ELSE expires_at END,expected_ownership=CASE WHEN request_payload->>'stop_now'='true' THEN $2::jsonb ELSE expected_ownership END WHERE id=$1 RETURNING `+controlColumns, old.ID, s.ProcessOwnership))
		if e != nil {
			return nil, e
		}
		if e = record(ctx, tx, p, s, "control_claimed", old, c); e != nil {
			return nil, e
		}
		authorized, e := controlRequesterAuthorized(r, tx, p, s, c)
		if e != nil {
			return nil, e
		}
		if !authorized {
			m.controlText.take(relayKey(p.TenantID, s.ID, c.ID))
			c, e = scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='authorization_revoked',completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, c.ID))
			if e != nil {
				return nil, e
			}
			if e = record(ctx, tx, p, s, "control_completed", nil, c); e != nil {
				return nil, e
			}
			continue
		}
		if err := validateSettingCatalog(ctx, tx, s, c.Kind, c.Value); err != nil {
			var invalid *workorders.Error
			if !errors.As(err, &invalid) {
				return nil, err
			}
			c, e = scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='setting_catalog_changed',completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, c.ID))
			if e != nil {
				return nil, e
			}
			if e = record(ctx, tx, p, s, "control_completed", nil, c); e != nil {
				return nil, e
			}
			continue
		}
		text := ""
		if c.Kind == "steer" {
			text = m.controlText.take(relayKey(p.TenantID, s.ID, c.ID))
			if text == "" {
				c, e = scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='transient_input_unavailable',completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, c.ID))
				if e != nil {
					return nil, e
				}
				if e = record(ctx, tx, p, s, "control_completed", nil, c); e != nil {
					return nil, e
				}
				continue
			}
		}
		claimed = append(claimed, offered{Control: c, Text: text})
	}
	if s.Phase != "yielded" {
		before := s
		s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET phase='yielded' WHERE id=$1 RETURNING `+sessionColumns, s.ID))
		if err != nil {
			return nil, err
		}
		if err = record(ctx, tx, p, s, "yielded", before, s); err != nil {
			return nil, err
		}
	}
	// Sample after claiming and recording the batch. The worker subtracts the
	// entire request round trip before using this budget on its monotonic clock.
	now, err := m.ownershipNow(ctx, tx)
	if err != nil {
		return nil, err
	}
	for i := range claimed {
		claimed[i].ExpiresInMS = controlTTL(claimed[i].ExpiresAt, now).Milliseconds()
	}
	return map[string]any{"session": s, "controls": claimed}, nil
}
func (m *Module) completeControl(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Outcome != "applied" && in.Outcome != "rejected" {
		return nil, workorders.Fail(400, "invalid control outcome")
	}
	if len(in.Reason) < 1 || len(in.Reason) > 128 {
		return nil, workorders.Fail(400, "bounded reason required")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if err := expireSessionRequests(r.Context(), tx, p, s); err != nil {
		return nil, err
	}
	id := r.PathValue("controlId")
	if !workorders.UUID(id) {
		return nil, workorders.Fail(400, "invalid control id")
	}
	c, err := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND id=$2 FOR UPDATE`, s.ID, id))
	if err != nil {
		return nil, err
	}
	if c.Kind == "stop" && c.RequestPayload != nil && c.RequestPayload.Pause {
		return nil, workorders.Fail(409, "pause completes only with a planned handover and stop reason paused")
	}
	if sessionRequest(c.Kind) && (c.ExpectedGeneration == nil || *c.ExpectedGeneration != s.ID) {
		return nil, workorders.Fail(409, "wrong request generation")
	}
	if sessionRequest(c.Kind) && c.Reason != nil && *c.Reason == "request_expired" {
		return c, nil
	}
	if c.RequestPayload != nil && c.RequestPayload.StopNow && (c.ExpectedGeneration == nil || *c.ExpectedGeneration != s.ID || c.State != "claimed" && c.State != "completed") {
		return nil, workorders.Fail(409, "stop now requires the claimed exact generation")
	}
	if (c.Kind == "force_stop" || c.RequestPayload != nil && c.RequestPayload.StopNow) && in.Outcome == "applied" && in.Reason != "owned_group_signalled_root_exited" {
		return nil, workorders.Fail(400, "verified owned-process stop result required")
	}
	if c.State == "completed" {
		if c.Outcome != nil && c.Reason != nil && *c.Outcome == in.Outcome && *c.Reason == in.Reason {
			return c, nil
		}
		return nil, workorders.Fail(409, "divergent control completion")
	}
	if c.State != "claimed" && !(sessionRequest(c.Kind) && c.State == "pending") {
		return nil, workorders.Fail(409, "control must be claimed")
	}
	if settingKind(c.Kind) && in.Outcome == "applied" {
		if c.ExpectedOwnership == nil || s.ProcessOwnership == nil || *c.ExpectedOwnership != *s.ProcessOwnership {
			return nil, workorders.Fail(409, "process generation changed; setting outcome is fenced")
		}
		now, err := m.ownershipNow(ctx, tx)
		if err != nil {
			return nil, err
		}
		if c.ExpiresAt == nil || !c.ExpiresAt.After(now) {
			return nil, workorders.Fail(409, "setting authorization expired")
		}
		if c.Kind == "tier" {
			authorized, e := controlRequesterAuthorized(r, tx, p, s, c)
			if e != nil {
				return nil, e
			}
			if !authorized {
				return nil, workorders.Fail(409, "tier authorization revoked")
			}
			if err = validateTier(ctx, tx, s, c.Value); err != nil {
				return nil, err
			}
			old := s
			s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET service_tier=$2,service_tier_revision=service_tier_revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, c.Value))
			if err != nil {
				return nil, err
			}
			if err = record(ctx, tx, p, s, "tier_changed", old, s); err != nil {
				return nil, err
			}
		}
		if c.Kind == "rename" {
			old := s
			s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET display_label=$2 WHERE id=$1 RETURNING `+sessionColumns, s.ID, c.Value))
			if err != nil {
				return nil, err
			}
			if err = recordMetadataChanges(ctx, tx, p.TenantID, old, s); err != nil {
				return nil, err
			}
			if err = record(ctx, tx, p, s, "metadata_changed", old, s); err != nil {
				return nil, err
			}
		}
	}
	before := c
	c, err = scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',claimed_at=coalesce(claimed_at,clock_timestamp()),outcome=CASE WHEN kind IN ('rename_request','model_request') AND expires_at<=statement_timestamp() THEN 'rejected' ELSE $2 END,reason=CASE WHEN kind IN ('rename_request','model_request') AND expires_at<=statement_timestamp() THEN 'request_expired' ELSE $3 END,completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, c.ID, in.Outcome, in.Reason))
	if err != nil {
		return nil, err
	}
	return c, record(ctx, tx, p, s, "control_completed", before, c)
}
