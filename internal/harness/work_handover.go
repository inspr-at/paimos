// SPDX-License-Identifier: AGPL-3.0-only
package harness

import (
	"context"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

// PrepareWorkHandover requests cooperative AEON-479 handovers. The caller holds
// the work tree fence and appends the returned event flush LAST in its write.
// Calling again retries expired delivery and reuses active controls. No process
// signal, lease release or fabricated stopped_at is permitted here.
func PrepareWorkHandover(ctx context.Context, tx pgx.Tx, p tenant.Principal, nodes []string, action string) (func() error, error) {
	if p.Kind != tenant.Person || len(nodes) > 100 {
		return nil, workorders.Fail(403, "person required for work handover")
	}
	rows, err := tx.Query(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE stopped_at IS NULL AND (
 ticket_node_id=ANY($1::uuid[]) OR ticket_node_id IN (SELECT id FROM nodes WHERE parent_id=ANY($1::uuid[])) OR work_order_id IN (SELECT id FROM nodes WHERE parent_id=ANY($1::uuid[]))
 OR run_id IN (SELECT r.id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.queue_node_id=ANY($1::uuid[]) OR o.parent_id=ANY($1::uuid[]))) ORDER BY id LIMIT 21 FOR UPDATE`, nodes)
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
	if len(sessions) > 20 {
		return nil, workorders.Fail(400, "handover scope exceeds 20 sessions")
	}
	batches := make([]*controlEventBatch, 0, len(sessions))
	for _, s := range sessions {
		batch := &controlEventBatch{}
		batches = append(batches, batch)
		sessionCtx := context.WithValue(ctx, controlEventKey{}, batch)
		if s.Pause != nil && (s.Pause.StopRequested || (s.Pause.State == "requested" || s.Pause.State == "planned") && !workHandover(s.Pause)) {
			return nil, workorders.Fail(409, "session already has a different stop request")
		}

		scopeRequest, _ := http.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
		scopeRequest.SetPathValue("projectId", s.ProjectID)
		owner, admin, parent, e := pauseController(scopeRequest, tx, p, "")
		if e != nil {
			return nil, e
		}
		if !pauseAllowed(s, owner, admin, parent) {
			return nil, workorders.Fail(403, "session owner required for work handover")
		}
		if !cooperativePause(s) {
			return nil, workorders.Fail(409, "running session cannot receive a graceful handover")
		}
		if _, e = requestPause(sessionCtx, tx, p, s, pauseRequest{Level: "wrap_up", Reason: "work_lifecycle:" + action, Note: "Finish or roll back the current step, commit WIP, write a handover and mark this session stopped. Work waits for confirmed stop.", DeadlineMinutes: 10}); e != nil {
			return nil, e
		}
	}
	return func() error {
		for _, batch := range batches {
			for _, e := range batch.events {
				if err := workorders.Record(ctx, tx, e.principal, e.project, "harness."+e.kind, e.before, e.after); err != nil {
					return err
				}
			}
		}
		return nil
	}, nil
}

func workHandover(p *Pause) bool { return p != nil && strings.HasPrefix(p.Reason, "work_lifecycle:") }

// RequireHandoverDelivery rejects runs with no live reporting generation rather
// than guessing that silence or a lost heartbeat proves process exit.
func RequireHandoverDelivery(ctx context.Context, tx pgx.Tx, ids []string) error {
	var missing bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions s
 WHERE NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason) AND s.stopped_at IS NOT NULL
 AND (s.ticket_node_id=ANY($1::uuid[]) OR s.ticket_node_id IN(SELECT id FROM nodes WHERE parent_id=ANY($1::uuid[]))
 OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=ANY($1::uuid[]))
 OR s.run_id IN(SELECT r.id FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE r.queue_node_id=ANY($1::uuid[]) OR o.parent_id=ANY($1::uuid[]))))
 OR EXISTS(SELECT 1 FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id
 WHERE (r.queue_node_id=ANY($1::uuid[]) OR o.parent_id=ANY($1::uuid[])) AND r.status IN ('starting','running','waiting')
 AND NOT EXISTS(SELECT 1 FROM harness_sessions s WHERE s.run_id=r.id AND s.stopped_at IS NULL))
 OR EXISTS(SELECT 1 FROM work_orders w JOIN nodes o ON o.id=w.node_id WHERE o.parent_id=ANY($1::uuid[]) AND o.deleted_at IS NULL AND w.status='running'
 AND NOT EXISTS(SELECT 1 FROM agent_runs r WHERE r.work_order_id=w.node_id)
 AND NOT EXISTS(SELECT 1 FROM harness_sessions s WHERE (s.work_order_id=w.node_id OR s.ticket_node_id=o.parent_id OR s.ticket_node_id=o.id) AND s.stopped_at IS NULL))`, ids).Scan(&missing)
	if err != nil {
		return err
	}
	if missing {
		return workorders.Fail(409, "running work must publish a live session for graceful handover")
	}
	return nil
}
