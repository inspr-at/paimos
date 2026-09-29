// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type removeResult struct {
	Session            Session `json:"session"`
	Message            string  `json:"message"`
	ProcessesSignalled bool    `json:"processes_signalled"`
	ProcessState       string  `json:"process_state"`
	// EventID is the harness.removed event that POST /api/events/{id}/undo
	// reverses (AEON-291). An idempotent retry of an earlier removal omits it.
	EventID int64 `json:"event_id,omitempty"`
	// Undoable says whether the caller may undo that event (events.undo in
	// the session's project). Present only with event_id.
	Undoable *bool `json:"undoable,omitempty"`
}

func removalAuthorized(r *http.Request, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Person {
		return workorders.Fail(403, "human project access required")
	}
	if err := project(r.Context(), tx, r.PathValue("projectId")); err != nil {
		return err
	}
	err := authz.RequireTx(r.Context(), tx, p, "harness.read", authz.Scope{ProjectID: r.PathValue("projectId")})
	if errors.Is(err, authz.ErrForbidden) {
		return workorders.Fail(403, "harness.read project permission required")
	}
	return err
}

func removalReason(r *http.Request) (string, error) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return "", err
	}
	reason, err := cleanText(in.Reason, 240, "removal reason")
	if err != nil {
		return "", err
	}
	if reason == "" {
		return "", workorders.Fail(400, "removal reason required")
	}
	return reason, nil
}

func (m *Module) remove(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := removalAuthorized(r, tx, p); err != nil {
		return nil, err
	}
	reason, err := removalReason(r)
	if err != nil {
		return nil, err
	}
	// Load the latest revision under the same lock used by all worker mutations.
	// No client observation, daemon availability or pending control can veto removal.
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	out, err := removeRegistration(r.Context(), tx, p, s, reason, nil)
	if err != nil || out.EventID == 0 {
		return out, err
	}
	undo := authz.RequireTx(r.Context(), tx, p, "events.undo", authz.Scope{ProjectID: s.ProjectID})
	if undo != nil && !errors.Is(undo, authz.ErrForbidden) {
		return nil, undo
	}
	allowed := undo == nil
	out.Undoable = &allowed
	return out, nil
}

// removeStaleBatch bounds one batch request's row locks and response. The
// client repeats while the response says more eligible records remain.
var removeStaleBatch = 200

// removeRegistration archives one record. audit adds request-level facts (the
// batch id and cutoff) to the removal event.
func removeRegistration(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, reason string, audit map[string]any) (removeResult, error) {
	result := func(s Session) removeResult {
		return removeResult{Session: s, Message: "Record removed; process not stopped by removal.", ProcessState: "unknown"}
	}
	if s.ArchivedAt != nil {
		return result(s), nil
	}
	before := s
	s, err := closeGeneration(ctx, tx, p, s, "removed_process_unknown")
	if err != nil {
		return removeResult{}, err
	}
	// Retain the private lease/reference digests as tombstones: proof() returns
	// 410 and registration replay cannot bring this generation back. Outstanding
	// force authorizations are revoked, but an already delivered signal cannot be
	// recalled. Legacy daemons may react to revocation; never claim remote exit.
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET archived_at=clock_timestamp(),recovery_process_state='unknown',recovery_request_id=gen_random_uuid(),recovery_request_digest=$2,recovery_actor_id=$3,recovery_reason=$4,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, digest("remove:"+p.ID, s.ID), p.ID, reason))
	if err != nil {
		return removeResult{}, err
	}
	after := map[string]any{"session": s, "reason": reason, "process_state": "unknown", "processes_signalled": false}
	for k, v := range audit {
		after[k] = v
	}
	removed, err := events.Append(ctx, tx, p, events.Change{NodeID: &s.ProjectID, Type: "harness.removed", Before: before, After: after})
	if err != nil {
		return removeResult{}, err
	}
	out := result(s)
	out.EventID = removed.ID
	return out, nil
}

func (m *Module) removeStale(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := removalAuthorized(r, tx, p); err != nil {
		return nil, err
	}
	reason, err := removalReason(r)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	var cutoff time.Time
	var batchID string
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()-interval '15 minutes',gen_random_uuid()::text`).Scan(&cutoff, &batchID); err != nil {
		return nil, err
	}
	// PostgreSQL rechecks the predicate after a concurrent heartbeat releases its
	// row lock. Deterministic ordering also serializes overlapping batch requests.
	// One request locks and removes at most removeStaleBatch rows.
	const eligible = ` FROM harness_sessions WHERE project_id=$1 AND archived_at IS NULL AND coalesce(heartbeat_at,created_at)<$2`
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+eligible+` ORDER BY id LIMIT $3 FOR UPDATE`, r.PathValue("projectId"), cutoff, removeStaleBatch)
	if err != nil {
		return nil, err
	}
	sessions := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		sessions = append(sessions, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	audit := map[string]any{"batch_id": batchID, "cutoff": cutoff}
	items := []removeResult{}
	for _, s := range sessions {
		item, e := removeRegistration(ctx, tx, p, s, reason, audit)
		if e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	// Eligible rows beyond this batch stay unlocked; the client repeats the request.
	var more bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1`+eligible+`)`, r.PathValue("projectId"), cutoff).Scan(&more); err != nil {
		return nil, err
	}
	return struct {
		Items  []removeResult `json:"items"`
		Cutoff time.Time      `json:"cutoff"`
		More   bool           `json:"more"`
	}{items, cutoff, more}, nil
}
