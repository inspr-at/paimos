// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const ownershipWindow = 45 * time.Second

func validIdentity(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16
}
func reportOwnership(ctx context.Context, tx pgx.Tx, s Session, identity ownedprocess.Identity) error {
	daemon, err := cleanText(identity.DaemonID, 128, "daemon id")
	if err != nil {
		return err
	}
	if s.Management != "managed" || s.RunID == nil || daemon == "" || daemon != identity.DaemonID || !validIdentity(identity.Generation) || !validIdentity(identity.ProcessID) || identity.RootPID < 2 || identity.GroupID != identity.RootPID || identity.StartedAt.IsZero() || identity.StartedAt.After(time.Now().Add(time.Minute)) {
		return workorders.Fail(400, "verified managed process identity required")
	}
	if s.ProcessOwnership != nil && *s.ProcessOwnership != identity {
		return workorders.Fail(409, "process identity changed; register a new generation")
	}
	raw, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE harness_sessions SET process_ownership=$2::jsonb,process_observed_at=clock_timestamp() WHERE id=$1`, s.ID, string(raw))
	return err
}
func forceAvailable(s Session, now time.Time) bool {
	return s.ArchivedAt == nil && s.StoppedAt == nil && s.Management == "managed" && s.RunID != nil && has(s, "stop") && s.ProcessOwnership != nil && s.ProcessObservedAt != nil && now.Sub(*s.ProcessObservedAt) >= 0 && now.Sub(*s.ProcessObservedAt) <= ownershipWindow
}
func forceConfirmation(s Session) string {
	if s.ProcessOwnership == nil {
		return ""
	}
	return fmt.Sprintf("force stop %s on %s group %d", s.ID, s.Host, s.ProcessOwnership.GroupID)
}
func (m *Module) forceStop(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person {
		return nil, workorders.Fail(403, "human force-stop confirmation required")
	}
	if err := authz.RequireTx(r.Context(), tx, p, "harness.force_stop", authz.Scope{ProjectID: r.PathValue("projectId")}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return nil, workorders.Fail(403, "harness.force_stop permission required")
		}
		return nil, err
	}
	var in struct {
		ExpectedRevision string `json:"expected_revision"`
		Confirmation     string `json:"confirmation"`
		RequestID        string `json:"request_id"`
		Reason           string `json:"reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	reason, err := cleanText(in.Reason, 240, "force stop reason")
	if err != nil {
		return nil, err
	}
	if !workorders.UUID(in.RequestID) || len(in.ExpectedRevision) != 64 || len(in.Confirmation) > 300 || reason == "" {
		return nil, workorders.Fail(400, "bounded reason, exact observation and request id required")
	}
	in.Reason = reason
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(in)
	requestDigest := digest("force:"+p.ID, string(raw))
	c, err := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1 AND session_id=$2`, in.RequestID, s.ID))
	if err == nil {
		if c.Kind == "force_stop" && subtle.ConstantTimeCompare(c.requestDigest, requestDigest) == 1 {
			return c, nil
		}
		return nil, workorders.Fail(409, "divergent force-stop retry")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if !forceAvailable(s, time.Now()) {
		return nil, workorders.Fail(409, "live owned process unavailable; no termination was requested")
	}
	if in.ExpectedRevision != recoveryRevision(s) || in.Confirmation != forceConfirmation(s) {
		return nil, workorders.Fail(409, "session or process identity changed; refresh and confirm again")
	}
	identity, _ := json.Marshal(s.ProcessOwnership)
	var sequence int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(max(sequence),0)+1 FROM harness_controls WHERE session_id=$1`, s.ID).Scan(&sequence); err != nil {
		return nil, err
	}
	c, err = scanControl(tx.QueryRow(ctx, `INSERT INTO harness_controls(tenant_id,id,session_id,kind,sequence,requested_by_principal_id,expected_ownership,request_digest,expires_at) VALUES($1,$2,$3,'force_stop',$4,$5,$6::jsonb,$7,clock_timestamp()+interval '45 seconds') RETURNING `+controlColumns, p.TenantID, in.RequestID, s.ID, sequence, p.ID, string(identity), requestDigest))
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE harness_sessions SET revision=revision+1 WHERE id=$1`, s.ID); err != nil {
		return nil, err
	}
	return c, record(ctx, tx, p, s, "force_stop_requested", nil, map[string]any{"control": c, "reason": reason, "scope": "owned_process_group", "root_exit_verified": false})
}
