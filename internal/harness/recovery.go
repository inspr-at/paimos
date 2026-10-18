// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/ownedprocess"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// RecoveryPreview describes record recovery only. No supplied PID, label or
// stale heartbeat is accepted as proof of process ownership or process exit.
type RecoveryPreview struct {
	ProcessOwnership         *ownedprocess.Identity `json:"process_ownership,omitempty"`
	ForceConfirmation        string                 `json:"force_confirmation"`
	SessionID                string                 `json:"session_id"`
	Host                     string                 `json:"host"`
	DisplayLabel             *string                `json:"display_label"`
	ObservedRevision         string                 `json:"observed_revision"`
	Confirmation             string                 `json:"confirmation"`
	ProcessState             string                 `json:"process_state"`
	ProcessScope             string                 `json:"process_scope"`
	CanArchive               bool                   `json:"can_archive"`
	ArchiveUnavailableReason string                 `json:"archive_unavailable_reason,omitempty"`
	ForceStopAvailable       bool                   `json:"force_stop_available"`
	ForceStopReason          string                 `json:"force_stop_reason"`
}

func recoveryRevision(s Session) string {
	// Bind the displayed identity, ownership, binding revision and closure.
	// Routine activity, usage, provenance and heartbeat updates must not
	// invalidate a person's confirmation of this exact process generation.
	snapshot := struct {
		ID, ProjectID, AgentID, Host, Management string
		RunID, DisplayLabel                      *string
		Revision                                 int64
		StoppedAt, ArchivedAt                    *time.Time
		Ownership                                *ownedprocess.Identity
	}{s.ID, s.ProjectID, s.AgentPrincipalID, s.Host, s.Management, s.RunID, s.DisplayLabel, s.Revision, s.StoppedAt, s.ArchivedAt, s.ProcessOwnership}
	raw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func archiveConfirmation(s Session) string { return "archive " + s.ID + " on " + s.Host }

func archiveUnavailable(s Session) string {
	if s.ArchivedAt != nil {
		return "This registration is already archived."
	}
	// A legacy managed daemon treats every worker API error as a reason to
	// terminate its child. Recovery-aware daemon identity reports certify that
	// it understands the explicit archived response and can detach safely.
	if s.StoppedAt == nil && s.Management == "managed" && s.ProcessOwnership == nil {
		return "This managed daemon has not reported support for safe recovery. Stop its session before archiving."
	}
	return ""
}

func recoveryAuthorized(r *http.Request, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person && !authz.OwnerWorkstation(p) {
		return workorders.Fail(403, "a person with harness.recover permission is required")
	}
	err := authz.RequireTx(r.Context(), tx, p, "harness.recover", authz.Scope{ProjectID: r.PathValue("projectId")})
	if errors.Is(err, authz.ErrForbidden) {
		return workorders.Fail(403, "harness.recover permission required")
	}
	return err
}

func (m *Module) recovery(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if p.Kind != tenant.Person && !authz.OwnerWorkstation(p) {
		return nil, workorders.Fail(403, "human recovery permission required")
	}
	scope := authz.Scope{ProjectID: r.PathValue("projectId")}
	archiveErr := authz.RequireTx(r.Context(), tx, p, "harness.recover", scope)
	forceErr := authz.RequireTx(r.Context(), tx, p, "harness.force_stop", scope)
	if archiveErr != nil && !errors.Is(archiveErr, authz.ErrForbidden) {
		return nil, archiveErr
	}
	if forceErr != nil && !errors.Is(forceErr, authz.ErrForbidden) {
		return nil, forceErr
	}
	if archiveErr != nil && forceErr != nil {
		return nil, workorders.Fail(403, "human recovery permission required")
	}
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	now, err := m.ownershipNow(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	return RecoveryPreview{SessionID: s.ID, Host: s.Host, DisplayLabel: s.DisplayLabel,
		ObservedRevision: recoveryRevision(s), Confirmation: archiveConfirmation(s),
		ProcessState: "unknown", ProcessScope: "No process will be signalled. This action archives this registration only; other sessions and child processes are unaffected.",
		CanArchive:               archiveUnavailable(s) == "" && archiveErr == nil,
		ArchiveUnavailableReason: archiveUnavailable(s),
		ForceStopAvailable:       forceErr == nil && forceAvailable(s, now),
		ProcessOwnership:         s.ProcessOwnership, ForceConfirmation: forceConfirmation(s),
		ForceStopReason: "Force stop requires a live daemon with verified ownership of this exact process generation."}, nil
}

func (m *Module) archive(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := recoveryAuthorized(r, tx, p); err != nil {
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
	reason, err := cleanText(in.Reason, 240, "recovery reason")
	if err != nil {
		return nil, err
	}
	if reason == "" || reason != strings.TrimSpace(in.Reason) || !workorders.UUID(in.RequestID) || len(in.ExpectedRevision) != 64 || len(in.Confirmation) > 256 {
		return nil, workorders.Fail(400, "exact observation, request id and a bounded recovery reason are required")
	}
	in.Reason = reason
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(in)
	requestDigest := digest("archive:"+p.ID, string(payload))
	if s.ArchivedAt != nil {
		var requestID string
		var saved []byte
		if err = tx.QueryRow(ctx, `SELECT recovery_request_id::text,recovery_request_digest FROM harness_sessions WHERE id=$1`, s.ID).Scan(&requestID, &saved); err != nil {
			return nil, err
		}
		if requestID == in.RequestID && subtle.ConstantTimeCompare(saved, requestDigest) == 1 {
			return s, nil
		}
		return nil, workorders.Fail(409, "session already archived; divergent recovery retry")
	}
	if in.ExpectedRevision != recoveryRevision(s) {
		return nil, workorders.Fail(409, "session changed; refresh recovery details and confirm again")
	}
	if why := archiveUnavailable(s); why != "" {
		return nil, workorders.Fail(409, why)
	}
	if in.Confirmation != archiveConfirmation(s) {
		return nil, workorders.Fail(400, "exact session and host confirmation required")
	}
	// A daemon may already hold a claimed force command. Archive cannot revoke
	// an in-flight signal, so wait for its outcome or expiry before closing the
	// generation. The daemon independently rejects expired authorizations.
	var forceInFlight bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_controls WHERE session_id=$1 AND kind='force_stop' AND state IN ('pending','claimed') AND expires_at>clock_timestamp())`, s.ID).Scan(&forceInFlight); err != nil {
		return nil, err
	}
	if forceInFlight {
		return nil, workorders.Fail(409, "force stop authorization is still active; wait for its result or expiry, then refresh")
	}
	before := s
	s, err = closeGeneration(ctx, tx, p, s, "archived_process_unknown")
	if err != nil {
		return nil, err
	}
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET archived_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=$2,recovery_request_digest=$3,recovery_actor_id=$4,recovery_reason=$5,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.RequestID, requestDigest, p.ID, in.Reason))
	if err != nil {
		return nil, err
	}
	return s, record(ctx, tx, p, s, "archived", before, map[string]any{"session": s, "request_id": in.RequestID, "reason": in.Reason, "process_state": "unknown", "processes_signalled": false})
}
