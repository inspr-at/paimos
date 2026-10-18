// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// Status and heartbeat are pinned reporter surfaces. The Agents tree reads
// lineage and caller-specific move rights from lists and hierarchy mutations.
func reporterSession(s Session) Session {
	s.CanReparent, s.HandedOverToID, s.AdoptedFromID = nil, nil, nil
	return s
}

// Every hierarchy writer takes this before row locks, including registrations
// without a parent. Concurrent restarts cannot split or duplicate adoption.
func lockHierarchy(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, projectID)
	return err
}

func heartbeatExpired(ctx context.Context, tx pgx.Tx, s Session) (bool, error) {
	mins, err := heartbeatLostMinutes(ctx, tx)
	if err != nil {
		return false, err
	}
	var expired bool
	err = tx.QueryRow(ctx, `SELECT coalesce(heartbeat_at,created_at)<clock_timestamp()-make_interval(mins=>$2) FROM harness_sessions WHERE id=$1`, s.ID, mins).Scan(&expired)
	return expired, err
}

func handoverPredecessor(ctx context.Context, tx pgx.Tx, projectID string, in registration, ref []byte) (*Session, error) {
	if in.SucceedsID != nil {
		s, err := load(ctx, tx, projectID, *in.SucceedsID, true)
		if err != nil {
			return nil, err
		}
		if s.Pause != nil && s.StopReason != nil && *s.StopReason == "paused" {
			if err = pausedPredecessor(s, in); err != nil {
				return nil, err
			}
			return &s, nil
		}
		if in.Role != "coordinator" {
			return nil, workorders.Fail(409, "worker predecessor must be paused")
		}
	}
	if in.Role != "coordinator" {
		return nil, nil
	}
	var s Session
	var err error
	if in.SucceedsID != nil {
		s, err = load(ctx, tx, projectID, *in.SucceedsID, true)
	} else {
		s, err = scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND agent_principal_id=$2 AND harness=$3 AND ref_digest=$4 ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, projectID, in.AgentPrincipalID, in.Harness, ref))
	}
	if errors.Is(err, pgx.ErrNoRows) && in.SucceedsID == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if s.StopReason != nil && *s.StopReason == "paused" {
		return nil, workorders.Fail(409, "paused generations require an explicit succeeds_session_id and resume request")
	}
	if s.AgentPrincipalID != in.AgentPrincipalID || s.Role != "coordinator" || s.ArchivedAt != nil || s.HandedOverToID != nil {
		if in.SucceedsID == nil {
			return nil, nil
		}
		return nil, workorders.Fail(409, "same-principal available predecessor required")
	}
	if s.StoppedAt == nil {
		stale, e := heartbeatExpired(ctx, tx, s)
		if e != nil {
			return nil, e
		}
		if !stale {
			return nil, workorders.Fail(409, "predecessor still heartbeating")
		}
	}
	return &s, nil
}

func adoptChildren(ctx context.Context, tx pgx.Tx, p tenant.Principal, old, next Session) error {
	if old.StoppedAt == nil {
		var err error
		old, err = closeGeneration(ctx, tx, p, old, StopReasonHeartbeatLost)
		if err != nil {
			return err
		}
	}
	// Resume adopts paused children as well as live ones. Bound their combined
	// scope before materializing snapshots or changing any child; hierarchy
	// writers already share the project lock, so this set cannot grow mid-write.
	includePaused := old.Pause != nil && old.Pause.State == "resume_requested"
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND parent_id=$2 AND archived_at IS NULL
 AND (stopped_at IS NULL OR ($3 AND stop_reason='paused' AND pause_record->>'state' IN ('paused','resume_requested')))
 ORDER BY id LIMIT $4 FOR UPDATE`, old.ProjectID, old.ID, includePaused, maxAdoptedChildren+1)
	if err != nil {
		return err
	}
	children := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			rows.Close()
			return e
		}
		children = append(children, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	if len(children) > maxAdoptedChildren {
		return workorders.Fail(409, "handover exceeds 1000 direct children")
	}
	for _, child := range children {
		if err = validateParent(ctx, tx, old.ProjectID, next.ID, child.ID); err != nil {
			return err
		}
		adopted, e := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,adopted_from_id=$3,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, child.ID, next.ID, old.ID))
		if e != nil {
			return e
		}
		// A dedicated lineage note cannot be overwritten by the worker's next beat.
		if err = record(ctx, tx, p, adopted, "adopted", child, adopted); err != nil {
			return err
		}
	}
	handed, err := scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET handed_over_to_id=$2,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, old.ID, next.ID))
	if err != nil {
		return err
	}
	return record(ctx, tx, p, handed, "handed_over", old, handed)
}
