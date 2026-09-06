// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package managedharness

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/models"
)

var (
	ErrBrowserInvalid     = errors.New("harness_browser_invalid")
	ErrBrowserUnavailable = errors.New("harness_browser_unavailable")
	ErrBrowserConflict    = errors.New("harness_browser_conflict")
	ErrBrowserStorage     = errors.New("harness_browser_storage_unavailable")
)

type BrowserControlRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	RequestKey       string `json:"request_key"`
}
type BrowserControlResponse struct {
	SchemaVersion int                   `json:"schema_version"`
	Control       models.HarnessControl `json:"control"`
	State         string                `json:"state"`
}
type AssignmentEvent struct {
	Revision     int64   `json:"revision"`
	CreatedAt    string  `json:"created_at"`
	BeforeParent *string `json:"before_parent_harness_session_id"`
	AfterParent  *string `json:"after_parent_harness_session_id"`
	BeforeTicket *int64  `json:"before_ticket_id"`
	AfterTicket  *int64  `json:"after_ticket_id"`
	BeforeShape  *string `json:"before_work_shape"`
	AfterShape   *string `json:"after_work_shape"`
}
type AssignmentHistory struct {
	SchemaVersion     int               `json:"schema_version"`
	SessionID         string            `json:"session_id"`
	Events            []AssignmentEvent `json:"events"`
	NextAfterRevision *int64            `json:"next_after_revision"`
}

func browserAuthority(ctx context.Context, tx *sql.Tx, p auth.Principal, project int64, edit bool) error {
	u, current, err := auth.ReauthorizePrincipalTx(ctx, tx, p, time.Now())
	if err != nil || u == nil || current.Kind() != auth.PrincipalSession || current.Impersonated() {
		return ErrBrowserUnavailable
	}
	var level string
	err = tx.QueryRowContext(ctx, auth.AgentModeAuthorizationCTE+`SELECT access_level FROM agent_mode_projects WHERE project_id=?`, current.UserID(), project).Scan(&level)
	if err != nil || (edit && level != "editor") {
		return ErrBrowserUnavailable
	}
	return nil
}
func browserControl(c models.HarnessControl) BrowserControlResponse {
	states := map[string]string{"pending": "requested", "claimed": "claimed", "applied": "completed", "rejected": "failed"}
	return BrowserControlResponse{SchemaVersion: 1, Control: c, State: states[c.State]}
}

// RequestControlCAS keeps the old endpoint untouched while performing browser
// reauthorization, selected revision check and existing control insertion under
// one SQLite write transaction. A request UUID binds a deterministic public
// control ID; even terminal retries cannot create another control.
func (s *Service) RequestControlCAS(ctx context.Context, p auth.Principal, project int64, sessionID, kind string, request BrowserControlRequest) (BrowserControlResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BrowserControlResponse{}, ErrBrowserStorage
	}
	defer tx.Rollback()
	if err = browserAuthority(ctx, tx, p, project, true); err != nil {
		return BrowserControlResponse{}, err
	}
	if request.ExpectedRevision < 1 || uuid.Validate(request.RequestKey) != nil || (kind != ControlStop && kind != ControlInterrupt) {
		return BrowserControlResponse{}, ErrBrowserInvalid
	}
	current, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE id=? AND project_id=?`, sessionID, project))
	if err != nil || current.ManagementMode != ManagementManaged || current.Phase == PhaseStopped || (kind == ControlStop && !current.Capabilities.Stop) || (kind == ControlInterrupt && !current.Capabilities.Interrupt) {
		return BrowserControlResponse{}, ErrBrowserUnavailable
	}
	if current.Revision != request.ExpectedRevision {
		return BrowserControlResponse{}, ErrBrowserConflict
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("paimos-browser-control-v1:%d:%d:%s:%s:%d:%s", p.UserID(), project, sessionID, kind, request.ExpectedRevision, request.RequestKey)))
	digest[6] = (digest[6] & 0x0f) | 0x40
	digest[8] = (digest[8] & 0x3f) | 0x80
	value, _ := uuid.FromBytes(digest[:16])
	id := value.String()
	prior, err := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=? OR (harness_session_id=? AND kind=? AND state IN ('pending','claimed')) ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, id, sessionID, kind, id))
	if err == nil {
		if tx.Commit() != nil {
			return BrowserControlResponse{}, ErrBrowserStorage
		}
		return browserControl(prior), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return BrowserControlResponse{}, ErrBrowserStorage
	}
	var sequence int64
	if tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM harness_session_controls WHERE harness_session_id=?`, sessionID).Scan(&sequence) != nil {
		return BrowserControlResponse{}, ErrBrowserStorage
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO harness_session_controls(id,harness_session_id,sequence,kind,requested_by_user_id) VALUES(?,?,?,?,?)`, id, sessionID, sequence, kind, p.UserID()); err != nil {
		return BrowserControlResponse{}, ErrBrowserStorage
	}
	out, err := scanControl(tx.QueryRowContext(ctx, `SELECT `+controlColumns+` FROM harness_session_controls WHERE id=?`, id))
	if err != nil || tx.Commit() != nil {
		return BrowserControlResponse{}, ErrBrowserStorage
	}
	return browserControl(out), nil
}
func (s *Service) AssignmentHistory(ctx context.Context, p auth.Principal, project int64, id string, after int64, limit int) (AssignmentHistory, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssignmentHistory{}, ErrBrowserStorage
	}
	defer tx.Rollback()
	if err = browserAuthority(ctx, tx, p, project, false); err != nil {
		return AssignmentHistory{}, err
	}
	if after < 0 || limit < 1 || limit > 100 {
		return AssignmentHistory{}, ErrBrowserInvalid
	}
	var exists int
	if tx.QueryRowContext(ctx, `SELECT 1 FROM harness_sessions WHERE project_id=? AND id=?`, project, id).Scan(&exists) != nil {
		return AssignmentHistory{}, ErrBrowserUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT event_sequence,created_at,before_parent_harness_session_id,after_parent_harness_session_id,before_ticket_id,after_ticket_id,before_work_shape,after_work_shape FROM harness_session_events WHERE harness_session_id=? AND operation='binding_changed' AND event_sequence>? ORDER BY event_sequence LIMIT ?`, id, after, limit)
	if err != nil {
		return AssignmentHistory{}, ErrBrowserStorage
	}
	out := AssignmentHistory{SchemaVersion: 1, SessionID: id, Events: []AssignmentEvent{}}
	for rows.Next() {
		var e AssignmentEvent
		if rows.Scan(&e.Revision, &e.CreatedAt, &e.BeforeParent, &e.AfterParent, &e.BeforeTicket, &e.AfterTicket, &e.BeforeShape, &e.AfterShape) != nil {
			rows.Close()
			return AssignmentHistory{}, ErrBrowserStorage
		}
		out.Events = append(out.Events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || tx.Commit() != nil {
		return AssignmentHistory{}, ErrBrowserStorage
	}
	if len(out.Events) > 0 {
		v := out.Events[len(out.Events)-1].Revision
		out.NextAfterRevision = &v
	}
	return out, nil
}
