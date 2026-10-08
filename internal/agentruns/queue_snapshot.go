// SPDX-License-Identifier: AGPL-3.0-only
package agentruns

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/inspr-at/paimos/internal/workqueue"
	"github.com/jackc/pgx/v5"
)

const snapshotNodes = 256
const snapshotLeaves = 100
const snapshotDepth = 32

type snapshotNode struct {
	ID       string    `json:"id"`
	Parent   *string   `json:"parent_id"`
	Revision time.Time `json:"revision"`
}
type snapshotItem struct {
	NodeID   string         `json:"node_id"`
	Revision time.Time      `json:"revision"`
	Outcome  string         `json:"outcome"`
	RunID    string         `json:"run_id,omitempty"`
	Path     []snapshotNode `json:"path,omitempty"`
}

// A depth-first keyset cursor stays bounded by snapshotDepth, even across pages.
// Positions remain exact decimal strings; UUID breaks ties at equal positions.
type snapshotCursor struct {
	Path          []snapshotNode `json:"path"`
	AfterPosition string         `json:"after_position,omitempty"`
	AfterID       string         `json:"after_id,omitempty"`
}

type parentQueueSnapshot struct {
	ID                    string           `json:"id"`
	ParentID              string           `json:"parent_id"`
	ParentRevision        time.Time        `json:"parent_revision"`
	TreeRevision          string           `json:"tree_revision"`
	State                 string           `json:"state"`
	Truncated             bool             `json:"truncated"`
	Partial               bool             `json:"partial"`
	TreeChanged           bool             `json:"tree_changed"`
	Items                 []snapshotItem   `json:"items"`
	ContinuationAvailable bool             `json:"continuation_available"`
	Start                 []snapshotCursor `json:"_start,omitempty"`
	Next                  []snapshotCursor `json:"_next,omitempty"`
}

// Walk only the visible work tree. Each page visits at most snapshotNodes
// nodes, with keyset queries bounded before eligibility work. RLS removes hidden
// branches before LIMIT. The server holds the cursor, never the caller.
func captureQueueTree(ctx context.Context, tx pgx.Tx, parent string, start []snapshotCursor) (parentQueueSnapshot, error) {
	s := parentQueueSnapshot{ParentID: parent, State: "pending", Items: []snapshotItem{}}
	var root snapshotNode
	var kind string
	err := tx.QueryRow(ctx, `SELECT n.id::text,n.parent_id::text,n.updated_at,k.slug FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`, parent).Scan(&root.ID, &root.Parent, &root.Revision, &kind)
	if err != nil {
		return s, err
	}
	if kind != "work" && kind != "project" {
		return s, workorders.Fail(409, "select a work parent or project")
	}
	s.ParentRevision = root.Revision
	if len(start) == 0 {
		start = []snapshotCursor{{Path: []snapshotNode{root}}}
	}
	s.Start = append([]snapshotCursor{}, start...)
	pending := append([]snapshotCursor{}, start...)
	topology := []snapshotNode{root}
	for len(pending) > 0 {
		index := len(pending) - 1
		b := pending[index]
		if len(topology) == snapshotNodes || len(s.Items) == snapshotLeaves {
			// Probe only for another visible child, without evaluating eligibility
			// or traversing beyond the page budget. Empty frames can be discarded.
			var more bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.parent_id=$1 AND n.deleted_at IS NULL AND k.slug='work'
 AND ($2::numeric IS NULL OR (n.position,n.id)>($2::numeric,$3::uuid)))`, b.Path[len(b.Path)-1].ID, nullableCursor(b.AfterPosition), nullableCursor(b.AfterID)).Scan(&more)
			if err != nil {
				return s, err
			}
			if !more {
				pending = pending[:index]
				continue
			}
			s.Truncated = true
			break
		}
		var n snapshotNode
		var state, position string
		var leaf bool
		// The inner LIMIT bounds node selection before the canonical leaf/pending
		// predicates. Empty cursors select the first child; all later queries advance.
		err = tx.QueryRow(ctx, `SELECT n.id::text,n.parent_id::text,n.updated_at,n.state,n.position::text,aeon_work_leaf(n.id) AND aeon_work_pending(n.id) IS NULL
 FROM (SELECT n.* FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.parent_id=$1 AND n.deleted_at IS NULL AND k.slug='work'
 AND ($2::numeric IS NULL OR (n.position,n.id)>($2::numeric,$3::uuid))
 ORDER BY n.position,n.id LIMIT 1) n`, b.Path[len(b.Path)-1].ID, nullableCursor(b.AfterPosition), nullableCursor(b.AfterID)).Scan(&n.ID, &n.Parent, &n.Revision, &state, &position, &leaf)
		if errors.Is(err, pgx.ErrNoRows) {
			pending = pending[:index]
			continue
		}
		if err != nil {
			return s, err
		}
		pending[index].AfterPosition = position
		pending[index].AfterID = n.ID
		topology = append(topology, n)
		path := append(append([]snapshotNode{}, b.Path...), n)
		if leaf {
			switch workqueue.State(state) {
			case "new", "open", "backlog", "blocked":
				s.Items = append(s.Items, snapshotItem{NodeID: n.ID, Revision: n.Revision, Outcome: "pending", Path: path})
			}
		} else if len(path) >= snapshotDepth {
			s.Truncated = true
		} else {
			pending = append(pending, snapshotCursor{Path: path})
		}
	}
	s.Next = pending
	s.ContinuationAvailable = len(pending) > 0
	raw, err := json.Marshal(topology)
	if err != nil {
		return s, err
	}
	sum := sha256.Sum256(raw)
	s.TreeRevision = hex.EncodeToString(sum[:])
	s.Partial = s.Truncated
	return s, nil
}

func nullableCursor(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (m *module) queueSnapshotCapture(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Revision       time.Time `json:"expected_revision"`
		ContinuationOf string    `json:"continuation_of"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision.IsZero() {
		return nil, workorders.Fail(400, "expected_revision required")
	}
	if in.ContinuationOf != "" && !workorders.UUID(in.ContinuationOf) {
		return nil, workorders.Fail(400, "invalid continuation snapshot id")
	}
	ctx := r.Context()
	t, err := queueLoadTicket(ctx, tx, r.PathValue("nodeId"), true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, true); err != nil {
		return nil, err
	}
	var start []snapshotCursor
	if in.ContinuationOf != "" {
		previous, err := loadQueueSnapshot(ctx, tx, p, in.ContinuationOf, false)
		if err != nil {
			return nil, err
		}
		if previous.ParentID != t.ID || previous.State == "cancelled" || len(previous.Next) == 0 {
			return nil, workorders.Fail(409, "continuation unavailable for this parent")
		}
		// Check the complete visible frontier in the final capture transaction.
		// Moving or hiding a branch cannot redirect the old cursor into another tree.
		for _, cursor := range previous.Next {
			current, err := snapshotPathCurrent(ctx, tx, cursor.Path)
			if err != nil {
				return nil, err
			}
			if !current {
				return nil, workorders.Fail(409, "continuation ancestry changed; capture a fresh snapshot")
			}
		}
		start = previous.Next
	}
	s, err := captureQueueTree(ctx, tx, t.ID, start)
	if err != nil {
		return nil, err
	}
	if !s.ParentRevision.Equal(in.Revision) {
		return nil, workorders.Fail(409, "parent changed")
	}
	if t.Kind != "parent" && t.Kind != "project" {
		return nil, workorders.Fail(409, "select a work parent or project")
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO parent_queue_snapshots(tenant_id,owner_id,parent_id,payload) VALUES($1,$2,$3,$4) RETURNING id::text`, p.TenantID, p.ID, s.ParentID, raw).Scan(&s.ID)
	if err != nil {
		return nil, err
	}
	if err = saveQueueSnapshot(ctx, tx, s); err != nil {
		return nil, err
	}
	if err = recordQueueSnapshot(ctx, tx, p, s, "queue.snapshot_captured"); err != nil {
		return nil, err
	}
	return snapshotVisible(ctx, tx, p, s)
}

// Audit only the operation receipt on the parent, never captured leaf identities
// or ancestry that may later belong to a project the reader cannot access.
func recordQueueSnapshot(ctx context.Context, tx pgx.Tx, p tenant.Principal, s parentQueueSnapshot, kind string) error {
	return workorders.Record(ctx, tx, p, s.ParentID, kind, nil, map[string]any{
		"snapshot_id": s.ID, "state": s.State, "truncated": s.Truncated,
		"partial": s.Partial, "tree_changed": s.TreeChanged,
	})
}

func loadQueueSnapshot(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, lock bool) (parentQueueSnapshot, error) {
	var s parentQueueSnapshot
	var raw []byte
	q := `SELECT payload FROM parent_queue_snapshots WHERE id=$1 AND owner_id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	if err := tx.QueryRow(ctx, q, id, p.ID).Scan(&raw); err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	t, err := queueLoadTicket(ctx, tx, s.ParentID, false)
	if err != nil {
		return s, err
	}
	if err = queuePermission(ctx, tx, p, t.ProjectID, false); err != nil {
		return s, err
	}
	return s, nil
}
func saveQueueSnapshot(ctx context.Context, tx pgx.Tx, s parentQueueSnapshot) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE parent_queue_snapshots SET payload=$2 WHERE id=$1`, s.ID, raw)
	return err
}

// Stored identities never substitute for current visibility, even on retry.
func snapshotVisible(ctx context.Context, tx pgx.Tx, p tenant.Principal, s parentQueueSnapshot) (parentQueueSnapshot, error) {
	out := s
	out.Start = nil
	out.Next = nil
	out.Items = []snapshotItem{}
	// Capture bounds the stored membership at snapshotLeaves. Re-read current
	// visibility through RLS in one query; full ticket fields and blockers are
	// unnecessary here and formerly cost one round trip for every leaf.
	if len(s.Items) > snapshotLeaves {
		return out, workorders.Fail(409, "snapshot exceeds item bound")
	}
	ids := make([]string, len(s.Items))
	for i, item := range s.Items {
		ids[i] = item.NodeID
	}
	rows, err := tx.Query(ctx, `SELECT id::text,project_id::text FROM nodes WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL`, ids)
	if err != nil {
		return out, err
	}
	projects := make(map[string]*string, len(ids))
	for rows.Next() {
		var id string
		var project *string
		if err = rows.Scan(&id, &project); err != nil {
			rows.Close()
			return out, err
		}
		projects[id] = project
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	// These are read permissions for this projection only, scoped to the current
	// project returned above. Mutation checks still run for every target inside
	// queueSnapshotApply/queuePrepare under the tenant/tree/access fences.
	type readScope struct {
		project string
		runs    bool
	}
	permissions := make(map[readScope]error)
	permission := func(project *string, runs bool) error {
		key := readScope{runs: runs}
		if project != nil {
			key.project = *project
		}
		if checked, ok := permissions[key]; ok {
			return checked
		}
		var err error
		if runs {
			err = authz.RequireTx(ctx, tx, p, "run.read", authz.Scope{ProjectID: key.project})
		} else {
			err = queuePermission(ctx, tx, p, project, false)
		}
		permissions[key] = err
		return err
	}
	for _, item := range s.Items {
		project, visible := projects[item.NodeID]
		if !visible {
			out.Partial = true
			continue
		}
		err := permission(project, false)
		if errors.Is(err, authz.ErrForbidden) {
			out.Partial = true
			continue
		}
		if err != nil {
			return out, err
		}
		if item.RunID != "" && p.Kind != tenant.Agent {
			err = permission(project, true)
			if errors.Is(err, authz.ErrForbidden) {
				item.RunID = ""
			} else if err != nil {
				return out, err
			}
		}
		item.Path = nil
		out.Items = append(out.Items, item)
	}
	return out, nil
}
func (m *module) queueSnapshotGet(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := loadQueueSnapshot(r.Context(), tx, p, r.PathValue("snapshotId"), false)
	if err != nil {
		return nil, err
	}
	return snapshotVisible(r.Context(), tx, p, s)
}
func (m *module) queueSnapshotCancel(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	s, err := loadQueueSnapshot(ctx, tx, p, r.PathValue("snapshotId"), true)
	if err != nil {
		return nil, err
	}
	if s.State == "applied" {
		return nil, workorders.Fail(409, "snapshot already applied; remove individual queued leaves")
	}
	if s.State == "cancelled" {
		return snapshotVisible(ctx, tx, p, s)
	}
	s.State = "cancelled"
	if err = saveQueueSnapshot(ctx, tx, s); err != nil {
		return nil, err
	}
	if err = recordQueueSnapshot(ctx, tx, p, s, "queue.snapshot_cancelled"); err != nil {
		return nil, err
	}
	return snapshotVisible(ctx, tx, p, s)
}

// Check every captured edge, including intermediate ancestors. Moving an entire
// branch cannot smuggle its unchanged leaf revision into the old snapshot.
func snapshotPathCurrent(ctx context.Context, tx pgx.Tx, path []snapshotNode) (bool, error) {
	ids := make([]string, len(path))
	for i, n := range path {
		ids[i] = n.ID
	}
	rows, err := tx.Query(ctx, `SELECT id::text,parent_id::text FROM nodes WHERE id=ANY($1::uuid[]) AND deleted_at IS NULL`, ids)
	if err != nil {
		return false, err
	}
	parents := make(map[string]*string, len(path))
	for rows.Next() {
		var id string
		var parent *string
		if err = rows.Scan(&id, &parent); err != nil {
			rows.Close()
			return false, err
		}
		parents[id] = parent
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	for _, n := range path {
		parent, found := parents[n.ID]
		if !found || (parent == nil) != (n.Parent == nil) || parent != nil && *parent != *n.Parent {
			return false, nil
		}
	}
	return true, nil
}

func (m *module) queueSnapshotApply(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	s, err := loadQueueSnapshot(ctx, tx, p, r.PathValue("snapshotId"), true)
	if err != nil {
		return nil, err
	}
	if s.State == "cancelled" {
		return nil, workorders.Fail(409, "snapshot cancelled")
	}
	if s.State == "applied" {
		return snapshotVisible(ctx, tx, p, s)
	}
	parent, err := queueLoadTicket(ctx, tx, s.ParentID, true)
	if err != nil {
		return nil, err
	}
	if err = queuePermission(ctx, tx, p, parent.ProjectID, true); err != nil {
		return nil, err
	}
	current, err := captureQueueTree(ctx, tx, s.ParentID, s.Start)
	if err != nil {
		return nil, err
	}
	s.TreeChanged = current.TreeRevision != s.TreeRevision || current.Truncated != s.Truncated
	changes := []events.Change{}
	for i := range s.Items {
		item := &s.Items[i]
		t, err := queueLoadTicket(ctx, tx, item.NodeID, true)
		if errors.Is(err, pgx.ErrNoRows) {
			item.Outcome = "unavailable"
			s.Partial = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = queuePermission(ctx, tx, p, t.ProjectID, true); errors.Is(err, authz.ErrForbidden) {
			item.Outcome = "unavailable"
			s.Partial = true
			continue
		} else if err != nil {
			return nil, err
		}
		pathOK, err := snapshotPathCurrent(ctx, tx, item.Path)
		if err != nil {
			return nil, err
		}
		var revision time.Time
		if err = tx.QueryRow(ctx, `SELECT updated_at FROM nodes WHERE id=$1`, t.ID).Scan(&revision); err != nil {
			return nil, err
		}
		if !pathOK || t.Kind != "work" {
			if !pathOK {
				s.TreeChanged = true
			}
			item.Outcome = "changed"
			s.Partial = true
			continue
		}
		existing, loadErr := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM agent_runs WHERE queue_node_id=$1 AND status IN ('queued','starting','running','waiting')`, t.ID))
		if loadErr == nil {
			if existing.Status == "queued" && sameTarget(existing, queueTarget{}) {
				item.Outcome = "already_queued"
				item.RunID = existing.ID
			} else {
				item.Outcome = "active"
				s.Partial = true
			}
			continue
		}
		if !errors.Is(loadErr, pgx.ErrNoRows) {
			return nil, loadErr
		}
		// Shared membership replay above remains idempotent. Every other live
		// binding (session, child-order run, or running order) uses the canonical
		// predicate under the pairing/tree/access fences before creating work.
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_busy($1)`, t.ID).Scan(&busy); err != nil {
			return nil, err
		}
		if busy {
			item.Outcome = "active"
			s.Partial = true
			continue
		}
		if !revision.Equal(item.Revision) {
			item.Outcome = "changed"
			s.Partial = true
			continue
		}
		prepared, err := m.queuePrepare(ctx, tx, p, t, queueTarget{})
		if err != nil {
			var qe *queueError
			if errors.As(err, &qe) && qe.Code == "queue_not_ready" {
				item.Outcome = "not_ready"
				s.Partial = true
				continue
			}
			return nil, err
		}

		item.RunID = prepared.Run.ID
		item.Outcome = "queued"
		if len(prepared.Changes) == 0 {
			item.Outcome = "already_queued"
		}
		changes = append(changes, prepared.Changes...)
	}
	s.State = "applied"
	if err = saveQueueSnapshot(ctx, tx, s); err != nil {
		return nil, err
	}
	// No resource locks after this point. Entire composite write rolls back on
	// unexpected errors or cancellation; the snapshot remains pending for retry.
	for _, change := range changes {
		if _, err = events.Append(ctx, tx, p, change); err != nil {
			return nil, err
		}
	}
	if err = recordQueueSnapshot(ctx, tx, p, s, "queue.snapshot_applied"); err != nil {
		return nil, err
	}
	return snapshotVisible(ctx, tx, p, s)
}
