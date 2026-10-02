// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/sessionrequest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type SessionRequestPayload struct {
	Pause           bool   `json:"pause,omitempty"`
	DisplayLabel    string `json:"display_label,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	AccountID       string `json:"account_id,omitempty"`
	ModelProfileID  string `json:"model_profile_id,omitempty"`
}

func sessionRequest(kind string) bool { return kind == "rename_request" || kind == "model_request" }

func (m *Module) requestSessionChange(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "session requests require a person")
	}
	var in struct {
		RequestID          string `json:"request_id"`
		ExpectedGeneration string `json:"expected_generation"`
		Kind               string `json:"kind"`
		DisplayLabel       string `json:"display_label"`
		AccountID          string `json:"account_id"`
		ModelProfileID     string `json:"model_profile_id"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || !workorders.UUID(in.ExpectedGeneration) || !sessionRequest(in.Kind) {
		return nil, workorders.Fail(400, "request id, exact generation and request kind required")
	}
	in.RequestID = strings.ToLower(in.RequestID)
	in.ExpectedGeneration = strings.ToLower(in.ExpectedGeneration)
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if in.ExpectedGeneration != s.ID {
		return nil, workorders.Fail(409, "session generation changed")
	}
	raw, _ := json.Marshal(in)
	requestDigest := digest("session-request:"+p.ID+":"+s.ID, string(raw))
	old, err := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1`, in.RequestID))
	if err == nil {
		if old.SessionID == s.ID && subtle.ConstantTimeCompare(old.requestDigest, requestDigest) == 1 {
			if err = expireSessionRequests(ctx, tx, p, s); err != nil {
				return nil, err
			}
			return scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1`, old.ID))
		}
		return nil, workorders.Fail(409, "divergent request replay")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if s.Management != "unmanaged" || s.StoppedAt != nil || s.ArchivedAt != nil {
		return nil, workorders.Fail(409, "requests require a live unmanaged generation")
	}
	payload := SessionRequestPayload{}
	if in.Kind == "rename_request" {
		if !sessionrequest.ValidLabel(in.DisplayLabel) || in.AccountID != "" || in.ModelProfileID != "" {
			return nil, workorders.Fail(400, "label must contain 1-64 ASCII letters, digits, spaces or -_.:()/#")
		}
		payload.DisplayLabel = strings.TrimSpace(in.DisplayLabel)
	} else {
		if in.DisplayLabel != "" || !workorders.UUID(in.AccountID) || !workorders.UUID(in.ModelProfileID) {
			return nil, workorders.Fail(400, "account and model profile from the catalog required")
		}
		// This requests a setting, not account switching or permission to launch.
		// Keep the granted catalog profile immutable in the payload for the session.
		err = tx.QueryRow(ctx, `SELECT m.model,m.effort FROM agent_accounts a JOIN model_profiles m ON m.tenant_id=a.tenant_id AND m.harness=a.harness
   WHERE a.id=$1 AND m.id=$2 AND a.harness=$3 AND m.enabled
   AND (a.allowed_model_profile_ids IS NULL OR m.id=ANY(a.allowed_model_profile_ids))
 AND NOT EXISTS (SELECT 1 FROM model_role_routes r WHERE r.profile_id=m.id AND r.role='build' AND r.state<>'available' AND r.valid_until>clock_timestamp())`, in.AccountID, in.ModelProfileID, s.Harness).Scan(&payload.Model, &payload.ReasoningEffort)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(400, "model profile is not granted to this account and harness")
		}
		if err != nil {
			return nil, err
		}
		if !sessionrequest.ValidModel(s.Harness, payload.Model, payload.ReasoningEffort) {
			return nil, workorders.Fail(400, "model profile has an invalid model or unsupported harness effort")
		}
		payload.AccountID = in.AccountID
		payload.ModelProfileID = in.ModelProfileID
	}
	if err = expireSessionRequests(ctx, tx, p, s); err != nil {
		return nil, err
	}
	var pending int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM harness_controls WHERE session_id=$1 AND kind IN ('rename_request','model_request') AND state<>'completed'`, s.ID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending >= 20 {
		return nil, workorders.Fail(409, "too many outstanding session requests")
	}
	var sequence int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0)+1 FROM harness_controls WHERE session_id=$1`, s.ID).Scan(&sequence); err != nil {
		return nil, err
	}
	data, _ := json.Marshal(payload)
	c, err := scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,id,session_id,kind,sequence,requested_by_principal_id,request_digest,request_payload,expected_generation,expires_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$3,clock_timestamp()+interval '10 minutes') ON CONFLICT (tenant_id,id) DO NOTHING RETURNING `+controlColumns, p.TenantID, in.RequestID, s.ID, in.Kind, sequence, p.ID, requestDigest, string(data)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, workorders.Fail(409, "request id already used")
	}
	if err != nil {
		return nil, err
	}
	return c, record(ctx, tx, p, s, "control_requested", nil, c)
}

func expireSessionRequests(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) error {
	rows, err := tx.Query(ctx, `UPDATE harness_controls SET state='completed',claimed_at=coalesce(claimed_at,clock_timestamp()),outcome='rejected',reason='request_expired',completed_at=clock_timestamp()
 WHERE session_id=$1 AND kind IN ('rename_request','model_request') AND state IN ('pending','claimed') AND expires_at<=clock_timestamp() RETURNING `+controlColumns, s.ID)
	if err != nil {
		return err
	}
	expired := []Control{}
	for rows.Next() {
		c, e := scanControl(rows)
		if e != nil {
			rows.Close()
			return e
		}
		expired = append(expired, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range expired {
		if err = record(ctx, tx, p, s, "control_expired", nil, c); err != nil {
			return err
		}
	}
	return nil
}

func readSessionRequests(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session) ([]Control, error) {
	if err := expireSessionRequests(ctx, tx, p, s); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE session_id=$1 AND kind IN ('rename_request','model_request')
 AND (state<>'completed' OR id IN (SELECT id FROM harness_controls WHERE session_id=$1 AND kind IN ('rename_request','model_request') AND state='completed' ORDER BY sequence DESC LIMIT 20)
 OR id IN (SELECT DISTINCT ON (kind) id FROM harness_controls WHERE session_id=$1 AND kind IN ('rename_request','model_request') AND outcome='applied' ORDER BY kind,sequence DESC)) ORDER BY sequence`, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Control{}
	for rows.Next() {
		c, e := scanControl(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
