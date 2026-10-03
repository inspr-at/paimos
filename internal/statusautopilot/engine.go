// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const Job = "status-autopilot"
const Changed = "status_autopilot.changed"

type node struct {
	ID         string                     `json:"id"`
	ProjectID  *string                    `json:"project_id"`
	State      string                     `json:"state"`
	HumanCheck *string                    `json:"human_check"`
	Fields     map[string]json.RawMessage `json:"fields"`
	Marks      map[string]bool            `json:"status_autopilot"`
	Updated    time.Time                  `json:"updated_at"`
}
type candidate struct {
	Node            node
	Since, Activity time.Time
	WorkActivity    time.Time
	Work, Objection bool
}
type decision struct {
	Rule, To, Flag, Reason, Anchor string
	Skip                           bool
}

func pending(n node) bool { return n.HumanCheck != nil && strings.TrimSpace(*n.HumanCheck) != "" }
func fieldTime(n node, key string) time.Time {
	var s string
	_ = json.Unmarshal(n.Fields[key], &s)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
func fieldString(n node, key string) string {
	var s string
	_ = json.Unmarshal(n.Fields[key], &s)
	return strings.TrimSpace(s)
}
func evaluate(c candidate, s Settings, now time.Time) *decision {
	n := c.Node
	var key, to, flag, reason string
	since := c.Since
	switch normaliseState(n.State) {
	case "new":
		key = "new"
		flag = "triage_list"
		reason = "Untriaged for %d days; listed for triage."
	case "backlog":
		key = "backlog"
		flag = "cancel_suggested"
		since = c.Activity
		reason = "Untouched for %d days; Cancelled is suggested."
	case "blocked":
		key = "blocked"
		flag = "blocked_reminder"
		reason = "Blocked for %d days; reminder to check the named blocker."
	case "in_progress", "inprogress", "progress", "active":
		key = "progress"
		to = "open"
		since = c.WorkActivity
		if since.Before(c.Since) {
			since = c.Since
		}
		reason = "No session, branch or PR activity for %d days."
		if c.Work {
			return nil
		}
	case "done":
		key = "done"
		flag = "missed_release"
		reason = "Merged but not released for %d days; flagged as missed release."
		if t := fieldTime(n, "merged_at"); !t.IsZero() {
			since = t
		}
	case "delivered":
		key = "accept"
		to = "accepted"
		reason = "Delivered for %d days without objection; accepted under this workspace’s status policy."
		if c.Objection || fieldString(n, "delivery_objection") != "" {
			return nil
		}
	default:
		return nil
	}
	r := s.Rules[key]
	if !r.Enabled || since.IsZero() || now.Before(since.Add(time.Duration(r.Days)*24*time.Hour)) {
		return nil
	}
	// Off keeps suggestions visible, but does not send reminders or move states.
	if !s.Enabled && key != "new" && key != "backlog" {
		return nil
	}
	if flag != "" && n.Marks[flag] {
		return nil
	}
	d := &decision{Rule: key, To: to, Flag: flag, Reason: fmt.Sprintf(reason, r.Days), Anchor: since.UTC().Format(time.RFC3339Nano)}
	if pending(n) && key == "accept" {
		d.Skip = true
		d.To = ""
		d.Reason = "Needs a human check: " + strings.TrimSpace(*n.HumanCheck) + ". Automatic acceptance was skipped."
		d.Anchor += "/human-check/" + *n.HumanCheck
	}
	return d
}

// Keep the SQL candidate index and this normalisation aligned with nodes.normaliseWorkState.
var stateSeparator = regexp.MustCompile(`[[:space:]-]+`)

func normaliseState(state string) string {
	return stateSeparator.ReplaceAllString(strings.ToLower(strings.TrimSpace(state)), "_")
}

const candidateStateSQL = `regexp_replace(lower(btrim(n.state)), '[[:space:]-]+', '_', 'g')`

func loadCandidates(ctx context.Context, tx pgx.Tx, ids []string, now time.Time) ([]candidate, error) {
	rows, err := tx.Query(ctx, `SELECT to_jsonb(n),
 coalesce(episode.at,n.created_at),
 greatest(n.created_at,coalesce(activity.at,n.created_at),coalesce(work.at,n.created_at),coalesce(review.at,n.created_at)),
 greatest(coalesce(episode.at,n.created_at),coalesce(work.at,n.created_at),coalesce(review.at,n.created_at)),
 coalesce(work.live,false),
 EXISTS(SELECT 1 FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id AND p.kind='person' AND (e.type IN ('comment.created','comment.updated') OR EXISTS(SELECT 1 FROM events original WHERE original.tenant_id=e.tenant_id AND original.id=e.undo_of AND original.metadata->>'rule'='accept')) AND e.at>=coalesce(episode.at,n.created_at))
 FROM nodes n
 LEFT JOIN LATERAL (SELECT max(e.at) at FROM events e WHERE e.node_id=n.id AND e.tenant_id=n.tenant_id AND e.after->>'state'=n.state AND e.before->>'state' IS DISTINCT FROM e.after->>'state') episode ON true
 LEFT JOIN LATERAL (SELECT max(e.at) at FROM events e WHERE e.node_id=n.id AND e.tenant_id=n.tenant_id AND coalesce(e.metadata->>'job','')<>$2) activity ON true
 LEFT JOIN LATERAL (SELECT max(greatest(h.created_at,h.heartbeat_at,h.stopped_at)) at,
 bool_or(h.archived_at IS NULL AND (
 (h.phase IN ('starting','working','stopping') AND h.stopped_at IS NULL AND coalesce(h.heartbeat_at,h.created_at)>=$3)
 OR (h.stop_reason='paused' AND h.pause_record->>'state' IN ('paused','resume_requested')))) live
 FROM harness_sessions h WHERE h.ticket_node_id=n.id AND h.tenant_id=n.tenant_id) work ON true
 LEFT JOIN LATERAL (SELECT max(r.created_at) at FROM work_order_reviews r WHERE r.ticket_node_id=n.id AND r.tenant_id=n.tenant_id AND r.pull_request IS NOT NULL) review ON true
 WHERE n.id=ANY($1::uuid[]) AND n.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM project_releases r WHERE r.tenant_id=n.tenant_id AND r.release_node_id=n.id) ORDER BY n.id FOR UPDATE OF n`, ids, Job, now.Add(-15*time.Minute))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		var raw []byte
		if err = rows.Scan(&raw, &c.Since, &c.Activity, &c.WorkActivity, &c.Work, &c.Objection); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c.Node); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func loadCandidate(ctx context.Context, tx pgx.Tx, id string, now time.Time) (candidate, json.RawMessage, error) {
	candidates, err := loadCandidates(ctx, tx, []string{id}, now)
	if err != nil {
		return candidate{}, nil, err
	}
	if len(candidates) == 0 {
		return candidate{}, nil, pgx.ErrNoRows
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, id).Scan(&raw)
	return candidates[0], raw, err
}

func apply(ctx context.Context, tx pgx.Tx, p tenant.Principal, n node, d decision) error {
	var seen bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM status_autopilot_receipts WHERE node_id=$1 AND rule=$2 AND anchor=$3)`, n.ID, d.Rule, d.Anchor).Scan(&seen); err != nil || seen {
		return err
	}
	var before, after []byte
	if err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE id=$1`, n.ID).Scan(&before); err != nil {
		return err
	}
	typ := Changed
	if d.Skip {
		typ = "status_autopilot.skipped"
		after = before
	} else {
		if n.Marks == nil {
			n.Marks = map[string]bool{}
		}
		if d.Flag != "" {
			n.Marks[d.Flag] = true
		}
		state := n.State
		if d.To != "" {
			state = d.To
			n.Marks = map[string]bool{}
		}
		marks, err := json.Marshal(n.Marks)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `UPDATE nodes SET state=$2,status_autopilot=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes)`, n.ID, state, marks).Scan(&after); err != nil {
			return err
		}
	}
	meta, _ := json.Marshal(map[string]any{"job": Job, "rule": d.Rule, "reason": d.Reason, "flag": d.Flag})
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &n.ID, Type: typ, Before: json.RawMessage(before), After: json.RawMessage(after), Metadata: meta})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO status_autopilot_receipts(tenant_id,node_id,rule,anchor,event_id) VALUES($1,$2,$3,$4,$5)`, p.TenantID, n.ID, d.Rule, d.Anchor, ev.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE status_autopilot_proposals SET status='applied' WHERE node_id=$1 AND rule=$2 AND anchor=$3 AND status='pending'`, n.ID, d.Rule, d.Anchor); err != nil {
		return err
	}
	// The audit reason is also a durable comment for stalled, shipped, accepted
	// and skipped-human-check tickets. It names exactly the same policy/evidence.
	if d.To != "" || d.Skip {
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &n.ID, Type: "comment.created", After: map[string]string{"body_markdown": d.Reason}, Metadata: meta})
	}
	return err
}

// Run drains release work every minute; the durable day cursor evaluates daily
// rules only once per UTC day, and preserves progress through process restarts.
func (m *Module) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := m.runAll(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Error("status autopilot", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (m *Module) runAll(ctx context.Context, now time.Time) error {
	rows, err := m.pool.Query(ctx, `SELECT id::text FROM tenants ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err = m.RunTenant(ctx, id, now); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func UndoHandlers() map[string]events.UndoFunc { return map[string]events.UndoFunc{Changed: undo} }
func undo(ctx context.Context, tx pgx.Tx, p tenant.Principal, e events.Event) (events.Change, error) {
	var before, after node
	if e.NodeID == nil || json.Unmarshal(e.Before, &before) != nil || json.Unmarshal(e.After, &after) != nil || before.ID != *e.NodeID || after.ID != *e.NodeID {
		return events.Change{}, events.ErrConflict
	}
	if err := lock(ctx, tx); err != nil {
		return events.Change{}, err
	}
	c, current, err := loadCandidate(ctx, tx, *e.NodeID, time.Now().UTC())
	if err != nil {
		return events.Change{}, events.ErrConflict
	}
	scope := authz.Scope{}
	if c.Node.ProjectID != nil {
		scope.ProjectID = *c.Node.ProjectID
	}
	if authz.RequireTx(ctx, tx, p, "nodes.write", scope) != nil {
		return events.Change{}, events.ErrForbidden
	}
	if !c.Node.Updated.Equal(after.Updated) || c.Node.State != after.State {
		return events.Change{}, events.ErrConflict
	}
	marks, err := json.Marshal(before.Marks)
	if err != nil {
		return events.Change{}, err
	}
	var restored []byte
	err = tx.QueryRow(ctx, `UPDATE nodes SET state=$2,status_autopilot=$3,updated_at=clock_timestamp() WHERE id=$1 RETURNING to_jsonb(nodes)`, before.ID, before.State, marks).Scan(&restored)
	return events.Change{NodeID: e.NodeID, Type: "status_autopilot.undone", Before: current, After: json.RawMessage(restored)}, err
}
