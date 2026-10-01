// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
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
	switch n.State {
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
	case "in_progress", "in progress", "in-progress", "progress", "active":
		key = "progress"
		to = "open"
		since = c.Activity
		reason = "No session, branch or PR activity for %d days."
		if c.Work {
			return nil
		}
		for _, f := range []string{"branch_activity_at", "pr_activity_at"} {
			if t := fieldTime(n, f); t.After(since) {
				since = t
			}
		}
		// A branch or PR without a timestamp is incomplete evidence, not proof
		// of inactivity. Fail closed rather than reopen active work.
		if fieldString(n, "branch") != "" && fieldTime(n, "branch_activity_at").IsZero() || fieldString(n, "pr_url") != "" && fieldTime(n, "pr_activity_at").IsZero() {
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
	if pending(n) {
		if key != "accept" {
			return nil
		}
		d.Skip = true
		d.To = ""
		d.Reason = "Needs a human check: " + strings.TrimSpace(*n.HumanCheck) + ". Automatic acceptance was skipped."
		d.Anchor += "/human-check/" + *n.HumanCheck
	}
	return d
}

// RunTenant executes one UTC calendar day atomically. Receipts and the day
// cursor commit with the events, so concurrent servers and retries are safe.
func (m *Module) RunTenant(ctx context.Context, tenantID string, now time.Time) error {
	day := now.UTC().Truncate(24 * time.Hour)
	return db.InTenant(db.AllProjects(ctx, "daily status autopilot"), m.pool, tenantID, func(tx pgx.Tx) error {
		if err := lock(ctx, tx); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO status_autopilot_days(tenant_id,day) VALUES($1,$2) ON CONFLICT(tenant_id) DO UPDATE SET day=excluded.day WHERE status_autopilot_days.day<excluded.day`, tenantID, day)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		released, err := tx.Query(ctx, `SELECT release_node_id::text FROM journey_releases WHERE state IN ('released','superseded') ORDER BY released_at,release_node_id`)
		if err != nil {
			return err
		}
		var releaseIDs []string
		for released.Next() {
			var id string
			if err = released.Scan(&id); err != nil {
				released.Close()
				return err
			}
			releaseIDs = append(releaseIDs, id)
		}
		err = released.Err()
		released.Close()
		if err != nil {
			return err
		}
		for _, id := range releaseIDs {
			if err = PublishTx(ctx, tx, tenantID, id); err != nil {
				return err
			}
		}
		s, err := Load(ctx, tx)
		if err != nil {
			return err
		}
		p, err := systemactor.Ensure(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE k.slug='ticket' AND n.deleted_at IS NULL AND n.state IN ('new','backlog','blocked','in_progress','in progress','in-progress','progress','active','done','delivered') ORDER BY n.id`)
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
		for _, id := range ids {
			c, _, err := loadCandidate(ctx, tx, id, day)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			effective := s
			if c.Node.ProjectID != nil {
				o, err := Project(ctx, tx, *c.Node.ProjectID, s.Enabled)
				if err != nil {
					return err
				}
				effective.Enabled = o.Effective
			}
			if d := evaluate(c, effective, day); d != nil {
				if err = apply(ctx, tx, p, c.Node, *d); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
func loadCandidate(ctx context.Context, tx pgx.Tx, id string, now time.Time) (candidate, json.RawMessage, error) {
	var c candidate
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT to_jsonb(n) FROM nodes n WHERE n.id=$1 AND n.deleted_at IS NULL FOR UPDATE`, id).Scan(&raw)
	if err != nil {
		return c, nil, err
	}
	if err = json.Unmarshal(raw, &c.Node); err != nil {
		return c, nil, err
	}
	err = tx.QueryRow(ctx, `SELECT
 coalesce((SELECT max(e.at) FROM events e WHERE e.node_id=n.id AND e.tenant_id=n.tenant_id AND e.after->>'state'=n.state AND (e.before->>'state' IS DISTINCT FROM e.after->>'state')),n.created_at),
 greatest(n.created_at,coalesce((SELECT max(e.at) FROM events e WHERE e.node_id=n.id AND e.tenant_id=n.tenant_id AND coalesce(e.metadata->>'job','')<>$2),n.created_at),coalesce((SELECT max(coalesce(h.heartbeat_at,h.created_at)) FROM harness_sessions h WHERE h.ticket_node_id=n.id AND h.tenant_id=n.tenant_id),n.created_at)),
 EXISTS(SELECT 1 FROM harness_sessions h WHERE h.tenant_id=n.tenant_id AND h.ticket_node_id=n.id AND h.phase IN ('starting','working','stopping') AND coalesce(h.heartbeat_at,h.created_at)>=$3),
 EXISTS(SELECT 1 FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id AND p.kind='person' AND (e.type IN ('comment.created','comment.updated') OR EXISTS(SELECT 1 FROM events original WHERE original.tenant_id=e.tenant_id AND original.id=e.undo_of AND original.metadata->>'rule'='accept')) AND e.at>=coalesce((SELECT max(d.at) FROM events d WHERE d.tenant_id=n.tenant_id AND d.node_id=n.id AND d.after->>'state'='delivered' AND d.before->>'state' IS DISTINCT FROM d.after->>'state'),n.created_at))
 FROM nodes n WHERE n.id=$1`, id, Job, now.Add(-15*time.Minute)).Scan(&c.Since, &c.Activity, &c.Work, &c.Objection)
	return c, raw, err
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
	// The audit reason is also a durable comment for stalled, shipped, accepted
	// and skipped-human-check tickets. It names exactly the same policy/evidence.
	if d.To != "" || d.Skip {
		_, err = events.Append(ctx, tx, p, events.Change{NodeID: &n.ID, Type: "comment.created", After: map[string]string{"body_markdown": d.Reason}, Metadata: meta})
	}
	return err
}

// PublishTx is called only after the release is durably published inside its
// caller's tenant transaction. It never approves or publishes a release itself.
func PublishTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) error {
	if err := lock(ctx, tx); err != nil {
		return err
	}
	var project, title, version string
	var published time.Time
	err := tx.QueryRow(ctx, `SELECT r.project_node_id::text,n.title,coalesce(r.version,''),r.released_at FROM journey_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id WHERE r.release_node_id=$1 AND r.state IN ('released','superseded') AND n.deleted_at IS NULL`, releaseID).Scan(&project, &title, &version, &published)
	if err != nil {
		return err
	}
	s, err := Load(ctx, tx)
	if err != nil {
		return err
	}
	o, err := Project(ctx, tx, project, s.Enabled)
	if err != nil || !o.Effective || !s.Rules["publish"].Enabled {
		return err
	}
	p, err := systemactor.Ensure(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT ticket_node_id::text AS id FROM journey_tickets WHERE release_node_id=$1
 UNION
 SELECT n.id::text FROM journey_release_note_snapshots s CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') ticket
 JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id::text=ticket->>'id' AND n.project_id=s.project_node_id
 WHERE s.release_node_id=$1 AND s.project_node_id=$2
 UNION
 SELECT n.id::text FROM release_manifest_note_snapshots s CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') ticket
 JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id::text=ticket->>'id' AND n.project_id=s.project_node_id
 WHERE s.project_node_id=$2 AND s.version=$3
 ORDER BY id`, releaseID, project, version)
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
	for _, id := range ids {
		c, _, err := loadCandidate(ctx, tx, id, published)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		n := c.Node
		// The approved transition is Done → Delivered. A historical release
		// must not ship a reopened ticket or a newer Done episode on replay.
		if n.ProjectID == nil || *n.ProjectID != project || n.State != "done" || c.Since.After(published) {
			continue
		}
		d := decision{Rule: "publish", To: "delivered", Anchor: releaseID, Reason: fmt.Sprintf("Shipped in release %s (%s).", title, version)}
		if pr := fieldString(n, "pr_url"); pr != "" {
			d.Reason += " PR: " + pr + "."
		}
		if merge := fieldString(n, "merge_commit"); merge != "" {
			d.Reason += " Merge: " + merge + "."
		}
		if pending(n) {
			d.Skip = true
			d.To = ""
			d.Anchor += "/human-check/" + *n.HumanCheck
			d.Reason = "Needs a human check: " + strings.TrimSpace(*n.HumanCheck) + ". Delivery in " + title + " (" + version + ") was skipped."
		}
		if err = apply(ctx, tx, p, n, d); err != nil {
			return err
		}
	}
	return nil
}

// Run checks at startup, then at each UTC midnight; the durable cursor ensures
// one deterministic evaluation per tenant/day across restarts and replicas.
func (m *Module) Run(ctx context.Context) {
	for {
		if err := m.runAll(ctx, time.Now().UTC()); err != nil && ctx.Err() == nil {
			slog.Error("status autopilot", "err", err)
		}
		now := time.Now().UTC()
		timer := time.NewTimer(time.Until(now.Truncate(24 * time.Hour).Add(24 * time.Hour)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
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
