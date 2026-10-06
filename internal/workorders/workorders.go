// SPDX-License-Identifier: AGPL-3.0-only

package workorders

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
)

type Criterion struct {
	ID          string     `json:"id"`
	Position    int        `json:"position"`
	Description string     `json:"description"`
	CheckedAt   *time.Time `json:"checked_at"`
	CheckedBy   *string    `json:"checked_by_principal_id"`
}
type Order struct {
	Kind        string              `json:"kind"`
	Review      *reviewgate.Binding `json:"review,omitempty"`
	NodeID      string              `json:"node_id"`
	Status      string              `json:"status"`
	Revision    int64               `json:"revision"`
	RequestedBy string              `json:"requested_by_principal_id"`
	Assignee    *string             `json:"assignee_principal_id"`
	MaxCost     *int64              `json:"max_cost_micros"`
	MaxDuration *int64              `json:"max_duration_seconds"`
	Criteria    []Criterion         `json:"criteria"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}
type CreateInput struct {
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Parent      *string  `json:"parent_id"`
	Assignee    *string  `json:"assignee_principal_id"`
	MaxCost     *int64   `json:"max_cost_micros"`
	MaxDuration *int64   `json:"max_duration_seconds"`
	Criteria    []string `json:"criteria"`
}
type patch struct {
	Revision    int64           `json:"expected_revision"`
	Status      *string         `json:"status"`
	Assignee    json.RawMessage `json:"assignee_principal_id"`
	MaxCost     json.RawMessage `json:"max_cost_micros"`
	MaxDuration json.RawMessage `json:"max_duration_seconds"`
}
type Evidence struct {
	ID          string    `json:"id"`
	OrderID     string    `json:"work_order_id"`
	SubmittedBy string    `json:"submitted_by_principal_id"`
	Kind        string    `json:"kind"`
	Reference   string    `json:"reference"`
	CriterionID *string   `json:"criterion_id,omitempty"`
	RunID       *string   `json:"run_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}
type module struct{ pool *pgxpool.Pool }

// New returns the module for /api/work-orders (excluding /runs). Mount alongside
// agentruns.New behind auth.Middleware. No cmd/aeon wiring is performed here.
func New(pool *pgxpool.Pool) httpapi.Module { return &module{pool} }
func (m *module) Mount(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern, scope string
		status         int
		fn             func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"GET /api/work-orders", "work_orders.read", 200, m.list},
		{"POST /api/work-orders", "work_orders.write", 201, m.create},
		{"GET /api/work-orders/{workOrderId}", "work_orders.read", 200, m.get},
		{"PATCH /api/work-orders/{workOrderId}", "work_orders.write", 200, m.patch},
		{"POST /api/work-orders/{workOrderId}/criteria/{criterionId}/check", "work_orders.write", 200, m.check},
		{"POST /api/work-orders/{workOrderId}/evidence", "work_orders.write", 201, m.evidence},
	} {
		mux.HandleFunc(route.pattern, Endpoint(m.pool, route.scope, false, route.status, route.fn))
	}
}

// Load reads a live order in the caller's db.InTenant transaction. Mutators
// request a row lock; all run mutations lock the order before locking a run.
func Load(ctx context.Context, tx pgx.Tx, id string, lock bool) (Order, error) {
	q := `SELECT w.kind,w.node_id::text,w.status,w.revision,w.requested_by_principal_id::text,w.assignee_principal_id::text,
	 w.max_cost_micros,w.max_duration_seconds,w.created_at,w.updated_at FROM work_orders w
	 JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id WHERE w.node_id=$1 AND n.deleted_at IS NULL`
	if lock {
		q += ` FOR UPDATE OF w`
	}
	var o Order
	err := tx.QueryRow(ctx, q, id).Scan(&o.Kind, &o.NodeID, &o.Status, &o.Revision, &o.RequestedBy, &o.Assignee, &o.MaxCost, &o.MaxDuration, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return o, err
	}
	if o.Kind == "review" {
		o.Review, err = reviewgate.Load(ctx, tx, id)
		if err != nil {
			return o, err
		}
	}
	o.Criteria = []Criterion{}
	rows, err := tx.Query(ctx, `SELECT id::text,position,description,checked_at,checked_by_principal_id::text FROM work_criteria WHERE work_order_id=$1 ORDER BY position`, id)
	if err != nil {
		return o, err
	}
	defer rows.Close()
	for rows.Next() {
		var c Criterion
		if err = rows.Scan(&c.ID, &c.Position, &c.Description, &c.CheckedAt, &c.CheckedBy); err != nil {
			return o, err
		}
		o.Criteria = append(o.Criteria, c)
	}
	return o, rows.Err()
}

// CanEdit limits agents to orders they requested or were assigned. People can
// manage tenant work orders; API key scopes remain a separate outer ceiling.
func CanEdit(p tenant.Principal, o Order) error {
	if p.Kind == tenant.Person || p.ID == o.RequestedBy || (o.Assignee != nil && p.ID == *o.Assignee) {
		return nil
	}
	return Fail(403, "work order is assigned to another principal")
}

// Exhausted includes every run, including elapsed server time for live runs.
// NUMERIC aggregates avoid overflowing when multiple BIGINT totals are summed.
func Exhausted(ctx context.Context, tx pgx.Tx, o Order) (bool, error) {
	var exhausted bool
	err := tx.QueryRow(ctx, `SELECT ($2::bigint IS NOT NULL AND coalesce(sum(cost_micros),0)>=$2::bigint)
	 OR ($3::bigint IS NOT NULL AND coalesce(sum(greatest(0,extract(epoch FROM (coalesce(ended_at,clock_timestamp())-started_at)))),0)>=$3::bigint)
	 FROM agent_runs WHERE work_order_id=$1`, o.NodeID, o.MaxCost, o.MaxDuration).Scan(&exhausted)
	return exhausted, err
}

// Record appends an R1 event in the mutation transaction.
func Record(ctx context.Context, tx pgx.Tx, p tenant.Principal, id, kind string, before, after any) error {
	_, err := events.Append(ctx, tx, p, events.Change{NodeID: &id, Type: kind, Before: before, After: after})
	return err
}

// BlockBudget prevents new dispatch without discarding usage that already
// happened. The caller holds the order lock and commits this with run telemetry.
func BlockBudget(ctx context.Context, tx pgx.Tx, p tenant.Principal, o Order) error {
	full, err := Exhausted(ctx, tx, o)
	if err != nil || !full || o.Status == "blocked" || o.Status == "done" || o.Status == "cancelled" {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE work_orders SET status='blocked',revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, o.NodeID); err != nil {
		return err
	}
	after, err := Load(ctx, tx, o.NodeID, false)
	if err != nil {
		return err
	}
	return Record(ctx, tx, p, o.NodeID, "work_order.budget_exhausted", o, after)
}

func (m *module) get(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	return Load(r.Context(), tx, r.PathValue("workOrderId"), false)
}
func (m *module) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	limit, err := Limit(r)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeCursor(p.TenantID, r.URL.Query().Get("cursor"))
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT w.node_id::text FROM work_orders w JOIN nodes n ON n.tenant_id=w.tenant_id AND n.id=w.node_id
	 WHERE n.deleted_at IS NULL AND ($1::uuid IS NULL OR w.node_id>$1::uuid) ORDER BY w.node_id LIMIT $2`, nilString(cursor), limit+1)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = encodeCursor(p.TenantID, ids[len(ids)-1])
	}
	result := []Order{}
	for _, id := range ids {
		o, err := Load(r.Context(), tx, id, false)
		if err != nil {
			return nil, err
		}
		result = append(result, o)
	}
	return orderPage{result, next}, nil
}
func nilString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func validBudget(cost, duration *int64) bool {
	return (cost == nil || *cost >= 0) && (duration == nil || *duration > 0)
}
func validID(id *string) bool { return id == nil || UUID(*id) }

func (m *module) create(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in CreateInput
	if err := Decode(r, &in); err != nil {
		return nil, err
	}
	return Create(r.Context(), tx, p, in)
}

// Create preserves the ordinary node, criteria and event path for typed orders.
// The caller owns authorization and the tenant transaction.
func Create(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CreateInput) (Order, error) {
	o, changes, err := CreateDeferred(ctx, tx, p, in)
	if err != nil {
		return Order{}, err
	}
	for _, change := range changes {
		if _, err := events.Append(ctx, tx, p, change); err != nil {
			return Order{}, err
		}
	}
	return o, nil
}

// CreateDeferred prepares the ordinary order and node snapshots without taking
// the event counter. Composite writers append the returned changes only after
// all node, queue and run locks have been acquired. The caller owns the tenant
// transaction, authorization and tree lock; rollback discards the entire unit.
func CreateDeferred(ctx context.Context, tx pgx.Tx, p tenant.Principal, in CreateInput) (Order, []events.Change, error) {
	if strings.TrimSpace(in.Title) == "" || len(in.Criteria) == 0 || !validBudget(in.MaxCost, in.MaxDuration) || !validID(in.Parent) || !validID(in.Assignee) {
		return Order{}, nil, Fail(400, "title, criteria, valid budget and IDs required")
	}
	for _, c := range in.Criteria {
		if strings.TrimSpace(c) == "" {
			return Order{}, nil, Fail(400, "empty criterion")
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_setting('aeon.tenant_id',true),0))`); err != nil {
		return Order{}, nil, err
	}
	var id string
	err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,key,kind_id,title,body,parent_id,position)
	 SELECT $1,aeon_next_node_key($1,k.short_prefix),k.id,$2,$3,$4,
	 (SELECT coalesce(max(position),0)+1024 FROM nodes WHERE parent_id IS NOT DISTINCT FROM $4::uuid AND deleted_at IS NULL)
	 FROM node_kinds k WHERE k.slug='work_order' RETURNING id::text`, p.TenantID, in.Title, in.Body, in.Parent).Scan(&id)
	if err != nil {
		return Order{}, nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,assignee_principal_id,max_cost_micros,max_duration_seconds) VALUES($1,$2,$3,$4,$5,$6)`, p.TenantID, id, p.ID, in.Assignee, in.MaxCost, in.MaxDuration); err != nil {
		return Order{}, nil, err
	}
	for i, c := range in.Criteria {
		if _, err = tx.Exec(ctx, `INSERT INTO work_criteria(tenant_id,work_order_id,position,description) VALUES($1,$2,$3,$4)`, p.TenantID, id, i, c); err != nil {
			return Order{}, nil, err
		}
	}
	o, err := Load(ctx, tx, id, false)
	if err != nil {
		return Order{}, nil, err
	}
	// Preserve the created node snapshot as well as its typed work-order detail.
	var node json.RawMessage
	if err = tx.QueryRow(ctx, `SELECT to_jsonb(n)-'tenant_id' FROM nodes n WHERE id=$1`, id).Scan(&node); err != nil {
		return Order{}, nil, err
	}
	return o, []events.Change{
		{NodeID: &id, Type: "node.created", After: node},
		{NodeID: &id, Type: "work_order.created", After: o},
	}, nil
}

func (m *module) patch(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in patch
	if err := Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Revision < 1 {
		return nil, Fail(400, "expected_revision required")
	}
	ctx := r.Context()
	o, err := Load(ctx, tx, r.PathValue("workOrderId"), true)
	if err != nil {
		return nil, err
	}
	if err = CanEdit(p, o); err != nil {
		return nil, err
	}
	if o.Revision != in.Revision {
		return nil, Fail(409, "revision conflict")
	}
	before := o
	if o.Kind == "review" && len(in.Assignee) > 0 {
		return nil, Fail(409, "review assignment is pinned; request a new review")
	}
	if in.Status != nil {
		switch *in.Status {
		case "draft", "ready", "running", "blocked", "done", "cancelled":
			o.Status = *in.Status
		default:
			return nil, Fail(400, "invalid status")
		}
	}
	for _, v := range []struct {
		raw json.RawMessage
		dst any
	}{{in.Assignee, &o.Assignee}, {in.MaxCost, &o.MaxCost}, {in.MaxDuration, &o.MaxDuration}} {
		if len(v.raw) > 0 {
			if err = json.Unmarshal(v.raw, v.dst); err != nil {
				return nil, Fail(400, "invalid patch")
			}
		}
	}
	if !validID(o.Assignee) || !validBudget(o.MaxCost, o.MaxDuration) {
		return nil, Fail(400, "invalid budget or assignee")
	}
	if o.Status == "done" {
		for _, c := range o.Criteria {
			if c.CheckedAt == nil {
				return nil, Fail(409, "all acceptance criteria must be checked")
			}
		}
		var evidence, live bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM work_evidence WHERE work_order_id=$1),EXISTS(SELECT 1 FROM agent_runs WHERE work_order_id=$1 AND status IN ('queued','starting','running','waiting'))`, o.NodeID).Scan(&evidence, &live); err != nil {
			return nil, err
		}
		if len(o.Criteria) == 0 || !evidence || live {
			return nil, Fail(409, "done requires criteria, evidence and no active runs")
		}
	}
	if o.Status == "ready" || o.Status == "running" {
		full, err := Exhausted(ctx, tx, o)
		if err != nil {
			return nil, err
		}
		if full {
			return nil, Fail(409, "work-order budget exhausted")
		}
	}
	_, err = tx.Exec(ctx, `UPDATE work_orders SET status=$2,assignee_principal_id=$3,max_cost_micros=$4,max_duration_seconds=$5,revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, o.NodeID, o.Status, o.Assignee, o.MaxCost, o.MaxDuration)
	if err != nil {
		return nil, err
	}
	o, err = Load(ctx, tx, o.NodeID, false)
	if err != nil {
		return nil, err
	}
	return o, Record(ctx, tx, p, o.NodeID, "work_order.updated", before, o)
}

func (m *module) check(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Checked *bool `json:"checked"`
	}
	if err := Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Checked == nil {
		return nil, Fail(400, "checked required")
	}
	ctx := r.Context()
	o, err := Load(ctx, tx, r.PathValue("workOrderId"), true)
	if err != nil {
		return nil, err
	}
	if err = CanEdit(p, o); err != nil {
		return nil, err
	}
	if o.Status == "done" && !*in.Checked {
		return nil, Fail(409, "reopen order before unchecking criteria")
	}
	for _, c := range o.Criteria {
		if c.ID == r.PathValue("criterionId") {
			if (c.CheckedAt != nil) == *in.Checked {
				return c, nil
			}
			before := c
			err = tx.QueryRow(ctx, `UPDATE work_criteria SET checked_at=CASE WHEN $2 THEN clock_timestamp() END,checked_by_principal_id=CASE WHEN $2 THEN $3::uuid END WHERE id=$1 RETURNING checked_at,checked_by_principal_id::text`, c.ID, *in.Checked, p.ID).Scan(&c.CheckedAt, &c.CheckedBy)
			if err != nil {
				return nil, err
			}
			if err = bump(ctx, tx, o.NodeID); err != nil {
				return nil, err
			}
			return c, Record(ctx, tx, p, o.NodeID, "work_order.criterion_checked", before, c)
		}
	}
	return nil, pgx.ErrNoRows
}
func bump(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `UPDATE work_orders SET revision=revision+1,updated_at=clock_timestamp() WHERE node_id=$1`, id)
	return err
}
func (m *module) evidence(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Kind        string  `json:"kind"`
		Reference   string  `json:"reference"`
		CriterionID *string `json:"criterion_id"`
		RunID       *string `json:"run_id"`
	}
	if err := Decode(r, &in); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Reference) == "" || !validID(in.CriterionID) || !validID(in.RunID) {
		return nil, Fail(400, "invalid evidence")
	}
	ctx := r.Context()
	o, err := Load(ctx, tx, r.PathValue("workOrderId"), true)
	if err != nil {
		return nil, err
	}
	if err = CanEdit(p, o); err != nil {
		return nil, err
	}
	switch in.Kind {
	case "url":
		u, e := url.Parse(in.Reference)
		if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return nil, Fail(400, "invalid evidence URL")
		}
	case "node":
		if !UUID(in.Reference) {
			return nil, Fail(400, "invalid evidence node")
		}
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1 AND deleted_at IS NULL)`, in.Reference).Scan(&exists); err != nil {
			return nil, err
		}
		if !exists {
			return nil, Fail(400, "evidence node not found")
		}
	case "text":
	default:
		return nil, Fail(400, "invalid evidence kind")
	}
	e := Evidence{OrderID: o.NodeID, SubmittedBy: p.ID, Kind: in.Kind, Reference: in.Reference, CriterionID: in.CriterionID, RunID: in.RunID}
	if o.Kind == "review" && (len(in.Reference) > reviewgate.MaxOutput || reviewgate.SensitiveText(in.Reference)) {
		return nil, Fail(400, "review evidence exceeds its bound or contains credential material")
	}
	err = tx.QueryRow(ctx, `INSERT INTO work_evidence(tenant_id,work_order_id,submitted_by_principal_id,kind,reference,criterion_id,run_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,created_at`, p.TenantID, o.NodeID, p.ID, in.Kind, in.Reference, in.CriterionID, in.RunID).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	if o.Kind == "review" {
		if in.RunID == nil {
			return nil, Fail(403, "assigned reviewer run required")
		}
		if err = reviewgate.RecordEvidence(ctx, tx, o.NodeID, e.ID, p.ID, *in.RunID, in.Kind, in.Reference, r.Header.Get("X-Aeon-Daemon-ID"), r.Header.Get("X-Aeon-Daemon-Generation")); err != nil {
			if err == reviewgate.ErrEvidence {
				return nil, Fail(403, err.Error())
			}
			return nil, err
		}
		if err = Record(ctx, tx, p, o.Review.TicketID, "review.result_recorded", nil, map[string]any{"work_order_id": o.NodeID, "run_id": *in.RunID, "repository": o.Review.Repository, "base_sha": o.Review.BaseSHA, "head_sha": o.Review.HeadSHA, "result": reviewgate.Parse(in.Reference)}); err != nil {
			return nil, err
		}
	}
	if err = bump(ctx, tx, o.NodeID); err != nil {
		return nil, err
	}
	return e, Record(ctx, tx, p, o.NodeID, "work_order.evidence_added", nil, e)
}

// BriefDigest binds the content used for launch, without persisting the prompt.
// Callers pass ordered criterion descriptions from the same order revision.
func BriefDigest(title, body string, criteria []string) (string, error) {
	size := len(title) + len(body)
	if len(criteria) > 200 {
		return "", Fail(400, "work brief exceeds bound")
	}
	for _, c := range criteria {
		size += len(c)
	}
	if size > 256<<10 {
		return "", Fail(400, "work brief exceeds bound")
	}
	raw, err := json.Marshal(struct {
		Title    string   `json:"title"`
		Body     string   `json:"body"`
		Criteria []string `json:"criteria"`
	}{title, body, criteria})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("aeon.worker.brief.v1\x00"), raw...))
	return fmt.Sprintf("%x", sum), nil
}
