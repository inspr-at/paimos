// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

type workTarget struct {
	ID        string    `json:"id"`
	UpdatedAt time.Time `json:"updated_at"`
}
type splitChild struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}
type workAction struct {
	ID           string   `json:"id"`
	NodeID       string   `json:"node_id"`
	Kind         string   `json:"kind"`
	State        string   `json:"state"`
	TargetCount  int      `json:"target_count"`
	WaitingCount int      `json:"waiting_count"`
	Result       []string `json:"result"`
	targets      []workTarget
	children     []splitChild
	requester    string
}
type workPreview struct {
	IsLeaf        bool        `json:"is_leaf"`
	Busy          bool        `json:"busy"`
	OpenLeaves    int         `json:"open_leaves"`
	UpdatedAt     time.Time   `json:"updated_at"`
	ScopeRevision string      `json:"scope_revision"`
	Pending       *workAction `json:"pending"`
}
type workRequest struct {
	RequestID     string       `json:"request_id"`
	Kind          string       `json:"kind"`
	UpdatedAt     time.Time    `json:"expected_updated_at"`
	OpenLeaves    int          `json:"expected_open_leaves"`
	ScopeRevision string       `json:"expected_scope_revision"`
	Children      []splitChild `json:"children"`
}
type deferredWorkEvents struct{ events []Event }

func (b *deferredWorkEvents) WriteEvent(_ context.Context, _ pgx.Tx, e Event) error {
	b.events = append(b.events, e)
	return nil
}

func workPermission(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, permission string) (nodeJSON, error) {
	n, err := loadNode(ctx, tx, id, true)
	if err != nil {
		return n, err
	}
	var project, slug string
	err = tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,''),k.slug FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL`, id).Scan(&project, &slug)
	if err != nil {
		return n, err
	}
	if slug != "work" {
		return n, badRequest("work node required")
	}
	if err = authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
		return n, err
	}
	return n, nil
}
func openWorkTargets(ctx context.Context, tx pgx.Tx, id string) ([]workTarget, error) {
	var complete bool
	if err := tx.QueryRow(ctx, `SELECT aeon_work_scope_complete($1::uuid)`, id).Scan(&complete); err != nil {
		return nil, err
	}
	if !complete {
		return nil, authz.ErrForbidden
	}
	rows, err := tx.Query(ctx, `SELECT s.id::text,n.updated_at FROM aeon_work_scope(ARRAY[$1::uuid]) s JOIN nodes n ON n.id=s.id WHERE s.is_leaf AND s.bucket IN ('open','in_progress','blocked') ORDER BY s.id LIMIT 101`, id)
	if err != nil {
		return nil, err
	}
	targets := []workTarget{}
	for rows.Next() {
		var t workTarget
		if err = rows.Scan(&t.ID, &t.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(targets) > 100 {
		return nil, badRequest("work action scope exceeds 100 open leaves")
	}
	return targets, nil
}
func scopeRevision(targets []workTarget) string {
	raw, _ := json.Marshal(targets)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func loadWorkAction(ctx context.Context, tx pgx.Tx, node, id string) (*workAction, error) {
	a := &workAction{Result: []string{}}
	var targets, children, result []byte
	err := tx.QueryRow(ctx, `SELECT id::text,node_id::text,requested_by::text,kind,state,targets,children,result FROM work_lifecycle_actions WHERE node_id=$1 AND id=$2 FOR UPDATE`, node, id).Scan(&a.ID, &a.NodeID, &a.requester, &a.Kind, &a.State, &targets, &children, &result)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(targets, &a.targets); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(children, &a.children); err != nil {
		return nil, err
	}
	if err = json.Unmarshal(result, &a.Result); err != nil {
		return nil, err
	}
	a.TargetCount = len(a.targets)
	if a.State == "waiting" {
		a.WaitingCount = a.TargetCount - len(a.Result)
	}
	return a, nil
}
func (m *Module) handleWorkLifecycle(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	if p.Kind != tenant.Person {
		writeError(w, 403, "only a person may split or cancel work")
		return
	}
	id, ok := pathUUID(w, r.PathValue("nodeId"), "invalid node id")
	if !ok {
		return
	}
	var in workRequest
	if r.Method == http.MethodPost && r.PathValue("actionId") == "" {
		if err := workorders.BufferBody(w, r); err != nil {
			workorders.WriteError(w, err)
			return
		}
		raw, ok := readBody(w, r)
		if !ok {
			return
		}
		if err := decodeJSON(raw, &in); err != nil {
			writeErr(w, err)
			return
		}
		if !workorders.UUID(in.RequestID) || in.UpdatedAt.IsZero() || len(in.ScopeRevision) != 64 || in.OpenLeaves < 0 || in.OpenLeaves > 100 || (in.Kind != "split" && in.Kind != "cancel") || len(in.Children) > 20 || (in.Kind == "split" && len(in.Children) < 1) || (in.Kind == "cancel" && len(in.Children) != 0) {
			writeErr(w, badRequest("valid work action and preview required"))
			return
		}
		for _, c := range in.Children {
			if strings.TrimSpace(c.Title) == "" || len(c.Title) > 500 || len(c.Body) > 16000 || !utf8.ValidString(c.Title+c.Body) || strings.ContainsRune(c.Title+c.Body, 0) {
				writeErr(w, badRequest("bounded child title and body required"))
				return
			}
		}
	}
	var out any
	err := db.InTransaction(r.Context(), m.pool, func(ctx context.Context) error {
		return m.tx(ctx, p.TenantID, func(ctx context.Context, tx pgx.Tx) error {
			if err := db.LockWorkTreeTx(ctx, tx); err != nil {
				return err
			}
			permission := "nodes.write"
			if r.Method == http.MethodGet {
				permission = "nodes.read"
			}
			n, err := workPermission(ctx, tx, p, id, permission)
			if err != nil {
				return err
			}
			if r.Method == http.MethodGet {
				targets, err := openWorkTargets(ctx, tx, id)
				if err != nil {
					return err
				}
				preview := workPreview{UpdatedAt: n.UpdatedAt, OpenLeaves: len(targets), ScopeRevision: scopeRevision(targets)}
				if err = tx.QueryRow(ctx, `SELECT aeon_work_leaf($1::uuid),aeon_work_busy($1::uuid)`, id).Scan(&preview.IsLeaf, &preview.Busy); err != nil {
					return err
				}
				var pending string
				err = tx.QueryRow(ctx, `SELECT id::text FROM work_lifecycle_actions WHERE node_id=$1 AND requested_by=$2 AND state='waiting'`, id, p.ID).Scan(&pending)
				if err == nil {
					preview.Pending, err = loadWorkAction(ctx, tx, id, pending)
				}
				if err != nil && !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				out = preview
				return nil
			}
			batch := &deferredWorkEvents{}
			copyModule := *m
			copyModule.events = batch
			actionID := r.PathValue("actionId")
			if actionID == "" {
				actionID = in.RequestID
			}
			if !workorders.UUID(actionID) {
				return badRequest("invalid action id")
			}
			a, err := loadWorkAction(ctx, tx, id, actionID)
			var handoverFlush func() error
			fresh := false
			if errors.Is(err, pgx.ErrNoRows) && r.PathValue("actionId") == "" {
				fresh = true
				targets, e := openWorkTargets(ctx, tx, id)
				if e != nil {
					return e
				}
				if !n.UpdatedAt.Equal(in.UpdatedAt) || len(targets) != in.OpenLeaves || scopeRevision(targets) != in.ScopeRevision {
					return conflict("work changed since the preview; review it again")
				}
				var leaf bool
				if e = tx.QueryRow(ctx, `SELECT aeon_work_leaf($1::uuid)`, id).Scan(&leaf); e != nil {
					return e
				}
				if in.Kind == "split" && !leaf {
					return conflict("only a leaf can be split")
				}
				if in.Kind == "split" {
					targets = []workTarget{{id, n.UpdatedAt}}
				}
				ids := []string{}
				for _, t := range targets {
					ids = append(ids, t.ID)
				}
				if e = harness.RequireHandoverDelivery(ctx, tx, ids); e != nil {
					return e
				}
				rawTargets, _ := json.Marshal(targets)
				rawChildren, _ := json.Marshal(in.Children)
				if len(in.Children) == 0 {
					rawChildren = []byte("[]")
				}
				_, e = tx.Exec(ctx, `INSERT INTO work_lifecycle_actions(tenant_id,id,node_id,requested_by,kind,targets,children) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, actionID, id, p.ID, in.Kind, string(rawTargets), string(rawChildren))
				if e != nil {
					return dbErr("request work action", e)
				}
				a, err = loadWorkAction(ctx, tx, id, actionID)
				if err != nil {
					return err
				}
				handoverFlush, err = harness.PrepareWorkHandover(ctx, tx, p, ids, actionID)
				if err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
			if a.requester != p.ID {
				return authz.ErrForbidden
			}
			if r.PathValue("actionId") == "" && (a.Kind != in.Kind || !sameSplitChildren(a.children, in.Children)) {
				return conflict("request id already used for a different work action")
			}
			if a.State == "completed" || a.State == "abandoned" {
				out = a
				return nil
			}
			if r.Method == http.MethodDelete {
				_, err = tx.Exec(ctx, `UPDATE work_lifecycle_actions SET state='abandoned',completed_at=clock_timestamp() WHERE id=$1`, a.ID)
				if err != nil {
					return err
				}
				a.State = "abandoned"
				out = a
				return m.record(ctx, tx, p.ID, &id, "work.lifecycle_abandoned", nil, a)
			}
			if !fresh {
				// A continuation renews expired cooperative delivery. The shared
				// busy predicate still waits for every original confirmed stop.
				ids := make([]string, 0, len(a.targets))
				for _, target := range a.targets {
					ids = append(ids, target.ID)
				}
				handoverFlush, err = harness.PrepareWorkHandover(ctx, tx, p, ids, a.ID)
				if err != nil {
					return err
				}
			}
			if _, err = tx.Exec(ctx, `SELECT set_config('aeon.work_lifecycle_action',$1,true)`, a.ID); err != nil {
				return err
			}
			beforeResultCount := len(a.Result)
			if err = copyModule.finishWorkAction(ctx, tx, p, n, a); err != nil {
				return err
			}
			for _, event := range batch.events {
				if err = m.events.WriteEvent(ctx, tx, event); err != nil {
					return err
				}
			}
			if handoverFlush != nil {
				if err = handoverFlush(); err != nil {
					return err
				}
			}
			// Persist an intent event even while waiting; a request never claims success.
			event := "work.lifecycle_waiting"
			if a.State == "completed" {
				event = "work.lifecycle_completed"
			}
			if fresh || len(a.Result) != beforeResultCount || a.State == "completed" {
				if err = m.record(ctx, tx, p.ID, &id, event, nil, a); err != nil {
					return err
				}
			}
			out = a
			return nil
		})
	})
	if err != nil {
		mapped := dbErr("work lifecycle", err)
		var he *httpError
		if errors.As(mapped, &he) {
			writeErr(w, mapped)
		} else {
			workorders.WriteError(w, mapped)
		}
		return
	}
	writeJSON(w, 200, out)
}
func sameSplitChildren(a, b []splitChild) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *Module) finishWorkAction(ctx context.Context, tx pgx.Tx, p tenant.Principal, n nodeJSON, a *workAction) error {
	completed := map[string]bool{}
	for _, id := range a.Result {
		completed[id] = true
	}
	a.WaitingCount = 0
	for _, target := range a.targets {
		if completed[target.ID] {
			continue
		}
		current, err := workPermission(ctx, tx, p, target.ID, "nodes.write")
		if err != nil {
			return err
		}
		var leaf bool
		if err = tx.QueryRow(ctx, `SELECT aeon_work_leaf($1::uuid)`, target.ID).Scan(&leaf); err != nil {
			return err
		}
		if !leaf {
			return conflict("work action target is no longer a leaf")
		}
		// Cancel unclaimed reservations under the same fence, never an active run.
		rows, err := tx.Query(ctx, `SELECT r.id::text FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE (r.queue_node_id=$1 OR o.parent_id=$1) AND r.status='queued' ORDER BY r.id LIMIT 101 FOR UPDATE OF r`, target.ID)
		if err != nil {
			return err
		}
		runIDs := []string{}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			runIDs = append(runIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(runIDs) > 100 {
			return badRequest("queued run scope exceeds 100")
		}
		if len(runIDs) > 0 {
			var project string
			if err = tx.QueryRow(ctx, `SELECT coalesce(project_id::text,'') FROM nodes WHERE id=$1`, target.ID).Scan(&project); err != nil {
				return err
			}
			if err = authz.RequireTx(ctx, tx, p, "run.create", authz.Scope{ProjectID: project}); err != nil {
				return err
			}
			for _, id := range runIDs {
				var before json.RawMessage
				if err = tx.QueryRow(ctx, `SELECT to_jsonb(r)-'tenant_id' FROM agent_runs r WHERE id=$1`, id).Scan(&before); err != nil {
					return err
				}
				if _, err = agentpairing.CancelQueuedRun(ctx, tx, id); err != nil {
					return err
				}
				var after json.RawMessage
				if err = tx.QueryRow(ctx, `SELECT to_jsonb(r)-'tenant_id' FROM agent_runs r WHERE id=$1`, id).Scan(&after); err != nil {
					return err
				}
				if err = m.record(ctx, tx, p.ID, &target.ID, "run.cancelled", before, after); err != nil {
					return err
				}
			}
		}
		var busy bool
		// An order marked running can be settled only after every real run reports
		// terminal and every bound generation reports stopped. Heartbeat loss alone
		// is not process exit and aeon_work_busy deliberately keeps that fence.
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM harness_sessions s WHERE (s.ticket_node_id=$1 OR s.ticket_node_id IN(SELECT id FROM nodes WHERE parent_id=$1) OR s.work_order_id IN(SELECT id FROM nodes WHERE parent_id=$1) OR s.run_id IN(SELECT id FROM agent_runs WHERE queue_node_id=$1 OR work_order_id IN(SELECT id FROM nodes WHERE parent_id=$1))) AND NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason))
   OR EXISTS(SELECT 1 FROM agent_runs r JOIN nodes o ON o.id=r.work_order_id WHERE (r.queue_node_id=$1 OR o.parent_id=$1) AND r.status IN ('queued','starting','running','waiting'))`, target.ID).Scan(&busy); err != nil {
			return err
		}
		if busy {
			a.WaitingCount++
			continue
		}
		orders, err := tx.Query(ctx, `SELECT w.node_id::text,to_jsonb(w)-'tenant_id',coalesce(n.project_id::text,'') FROM work_orders w JOIN nodes n ON n.id=w.node_id WHERE n.parent_id=$1 AND n.deleted_at IS NULL AND w.status NOT IN('done','cancelled') ORDER BY w.node_id LIMIT 101 FOR UPDATE OF w`, target.ID)
		if err != nil {
			return err
		}
		type orderStop struct {
			id, project string
			before      json.RawMessage
		}
		stops := []orderStop{}
		for orders.Next() {
			var stop orderStop
			if err = orders.Scan(&stop.id, &stop.before, &stop.project); err != nil {
				orders.Close()
				return err
			}
			stops = append(stops, stop)
		}
		err = orders.Err()
		orders.Close()
		if err != nil {
			return err
		}
		if len(stops) > 100 {
			return badRequest("work-order scope exceeds 100 per leaf")
		}
		for _, stop := range stops {
			if err = authz.RequireTx(ctx, tx, p, "work_orders.write", authz.Scope{ProjectID: stop.project}); err != nil {
				return err
			}
			var after json.RawMessage
			if err = tx.QueryRow(ctx, `UPDATE work_orders w SET status='cancelled',revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1 RETURNING to_jsonb(w)-'tenant_id'`, stop.id).Scan(&after); err != nil {
				return err
			}
			if err = m.record(ctx, tx, p.ID, &stop.id, "work_order.cancelled", stop.before, after); err != nil {
				return err
			}
		}
		if a.Kind == "split" {
			if !current.UpdatedAt.Equal(target.UpdatedAt) {
				return conflict("leaf changed during handover; abandon this split and review again")
			}
			for _, child := range a.children {
				parent := n.ID
				body := child.Body
				made, err := m.createNode(ctx, p, nodeCreate{KindID: n.KindID, Title: child.Title, Body: &body, ParentID: &parent})
				if err != nil {
					return err
				}
				a.Result = append(a.Result, made.ID)
			}
		} else {
			var bucket string
			if err = tx.QueryRow(ctx, `SELECT aeon_work_status_category(n.state,k.field_schema) FROM nodes n JOIN node_kinds k ON k.id=n.kind_id WHERE n.id=$1`, target.ID).Scan(&bucket); err != nil {
				return err
			}
			if bucket == "open" || bucket == "in_progress" || bucket == "blocked" {
				state := json.RawMessage(`"cancelled"`)
				if _, err = m.updateNode(ctx, p, target.ID, map[string]json.RawMessage{"state": state}, &current.UpdatedAt); err != nil {
					return err
				}
			}
			a.Result = append(a.Result, target.ID)
		}
	}
	if a.WaitingCount == 0 {
		a.State = "completed"
	}
	result, _ := json.Marshal(a.Result)
	_, err := tx.Exec(ctx, `UPDATE work_lifecycle_actions SET state=$2,result=$3,completed_at=CASE WHEN $2='completed' THEN clock_timestamp() ELSE NULL END WHERE id=$1`, a.ID, a.State, string(result))
	return err
}
