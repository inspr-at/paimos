// SPDX-License-Identifier: AGPL-3.0-only

// Package harness exposes classic-compatible public harness generations on top
// of Aeon's tenant-scoped agent runs, work orders and principal inbox. New
// returns an httpapi.Module; the coordinator mounts it behind auth middleware.
// B7 adds GET /api/harness-sessions with state, harness, agent, project and
// ticket filters, limit 1..200 and a tenant/principal/filter-bound cursor.
// It returns items/next_cursor in descending (created_at,id) order. Each item
// extends Session with project and nullable ticket {id,key,title} summaries.
// State is stopped after closure, otherwise phase. Historical node bindings
// retain their summaries after soft deletion. Existing Plugin() supplies the
// compiled manifest; no new manifest registration or cmd wiring is needed.
// AC4 adds nullable display_label (migration 0864), supplied by the CLI's
// harness register --label. It is public metadata, never principal identity.
// Exact replay includes the normalized label. Existing harness.registered,
// harness.bound and harness.stopped events are emitted transactionally through
// events.Append and the aeon_events notification trigger; /api/events/stream
// replays them with tenant/project visibility. Clients treat them as read hints.
// AEON-184 adds GET /api/harness-sessions/live: the agents actively working
// in each visible project right now, for the Projects page (live.go).
// AEON-192 adds activity_note on heartbeat. New(pool) remains the coordinator's
// httpapi.Module constructor and Plugin() remains its compiled manifest.
package harness

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/plugins"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type Module struct{ pool *pgxpool.Pool }

var _ httpapi.Module = (*Module)(nil)

func New(pool *pgxpool.Pool) httpapi.Module { return &Module{pool: pool} }

func (m *Module) Mount(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern, scope string
		agent          bool
		status         int
		fn             func(*http.Request, pgx.Tx, tenant.Principal) (any, error)
	}{
		{"POST /api/projects/{projectId}/harness-sessions", "harness.write", false, 201, m.register},
		{"GET /api/harness-sessions", "harness.read", false, 200, m.listAll},
		{"GET /api/harness-sessions/live", "harness.read", false, 200, m.live},
		{"GET /api/projects/{projectId}/harness-sessions", "harness.read", false, 200, m.list},
		{"GET /api/projects/{projectId}/harness-sessions/orchestrator", "harness.read", false, 200, m.orchestrator},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}", "harness.read", false, 200, m.status},
		{"PATCH /api/projects/{projectId}/harness-sessions/{sessionId}/binding", "harness.write", false, 200, m.bind},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/heartbeat", "harness.worker", true, 200, m.heartbeat},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/yield", "harness.worker", true, 200, m.yield},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/drain", "harness.worker", true, 200, m.drain},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/complete-delivery", "harness.worker", true, 200, m.completeDelivery},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/interrupt", "harness.control", false, 201, m.interrupt},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/stop", "harness.control", false, 201, m.stop},
		{"GET /api/projects/{projectId}/harness-sessions/{sessionId}/controls/{controlId}", "harness.read", false, 200, m.control},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/controls/{controlId}/complete", "harness.worker", true, 200, m.completeControl},
		{"POST /api/projects/{projectId}/harness-sessions/{sessionId}/stop", "harness.worker", true, 200, m.markStopped},
	} {
		mux.HandleFunc(route.pattern, workorders.Endpoint(m.pool, route.scope, route.agent, route.status, route.fn))
	}
}

type Session struct {
	ID                     string         `json:"id"`
	ProjectID              string         `json:"project_id"`
	AgentPrincipalID       string         `json:"agent_principal_id"`
	RunID                  *string        `json:"run_id"`
	TicketNodeID           *string        `json:"ticket_node_id"`
	WorkOrderID            *string        `json:"work_order_id"`
	ParentID               *string        `json:"parent_harness_session_id"`
	Harness                string         `json:"harness"`
	Host                   string         `json:"host"`
	DisplayLabel           *string        `json:"display_label"`
	ActivityNote           *string        `json:"activity_note"`
	ActivityHistory        []ActivityNote `json:"activity_history,omitempty"`
	Management             string         `json:"management_mode"`
	Role                   string         `json:"role"`
	WorkShape              string         `json:"work_shape"`
	Capabilities           []string       `json:"advertised_capabilities"`
	Phase                  string         `json:"phase"`
	Activity               string         `json:"activity"`
	ActivitySequence       int64          `json:"activity_sequence"`
	Revision               int64          `json:"revision"`
	HeartbeatAt            *time.Time     `json:"heartbeat_at"`
	StoppedAt              *time.Time     `json:"stopped_at"`
	StopReason             *string        `json:"stop_reason"`
	CreatedAt              time.Time      `json:"created_at"`
	refDigest, leaseDigest []byte
}

type ActivityNote struct {
	Note string    `json:"note"`
	At   time.Time `json:"at"`
}

func normalizeActivityNote(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	clean := strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw))
	return clean, clean != "" && utf8.RuneCountInString(clean) <= 120
}

const sessionColumns = `id::text,project_id::text,agent_principal_id::text,run_id::text,ticket_node_id::text,work_order_id::text,parent_id::text,harness,host,management,role,work_shape,capabilities,phase,activity,activity_sequence,revision,heartbeat_at,stopped_at,stop_reason,created_at,ref_digest,lease_digest,display_label,activity_note`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.ProjectID, &s.AgentPrincipalID, &s.RunID, &s.TicketNodeID, &s.WorkOrderID, &s.ParentID, &s.Harness, &s.Host, &s.Management, &s.Role, &s.WorkShape, &s.Capabilities, &s.Phase, &s.Activity, &s.ActivitySequence, &s.Revision, &s.HeartbeatAt, &s.StoppedAt, &s.StopReason, &s.CreatedAt, &s.refDigest, &s.leaseDigest, &s.DisplayLabel, &s.ActivityNote)
	return s, err
}
func project(ctx context.Context, tx pgx.Tx, id string) error {
	if !workorders.UUID(id) {
		return workorders.Fail(400, "invalid project id")
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.deleted_at IS NULL AND k.slug='project')`, id).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return workorders.Fail(404, "project not found")
	}
	return nil
}
func load(ctx context.Context, tx pgx.Tx, projectID, id string, lock bool) (Session, error) {
	if !workorders.UUID(id) {
		return Session{}, workorders.Fail(400, "invalid session id")
	}
	q := `SELECT ` + sessionColumns + ` FROM harness_sessions WHERE project_id=$1 AND id=$2`
	if lock {
		q += ` FOR UPDATE`
	}
	return scanSession(tx.QueryRow(ctx, q, projectID, id))
}
func digest(domain, value string) []byte {
	sum := sha256.Sum256([]byte("aeon.harness." + domain + "\x00" + value))
	return sum[:]
}
func proof(s Session, r *http.Request, p tenant.Principal) error {
	lease := r.Header.Get("X-Aeon-Worker-Lease")
	if p.Kind != tenant.Agent || p.ID != s.AgentPrincipalID || len(lease) < 32 || subtle.ConstantTimeCompare(digest("lease", lease), s.leaseDigest) != 1 || s.StoppedAt != nil {
		return workorders.Fail(403, "harness worker proof rejected")
	}
	return nil
}
func worker(ctx context.Context, tx pgx.Tx, r *http.Request, p tenant.Principal) (Session, error) {
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil { // No existence oracle on a worker path.
		if errors.Is(err, pgx.ErrNoRows) {
			return s, workorders.Fail(403, "harness worker proof rejected")
		}
		return s, err
	}
	return s, proof(s, r, p)
}
func record(ctx context.Context, tx pgx.Tx, p tenant.Principal, s Session, kind string, before, after any) error {
	return workorders.Record(ctx, tx, p, s.ProjectID, "harness."+kind, before, after)
}
func validHarness(v string) bool {
	switch v {
	case "codex", "claude", "pi", "cursor", "grok":
		return true
	}
	return false
}
func validShape(v string) bool { return v == "ship" || v == "scout" }
func validPhase(v string) bool {
	switch v {
	case "starting", "working", "yielded", "stopping":
		return true
	}
	return false
}
func has(s Session, cap string) bool {
	for _, v := range s.Capabilities {
		if v == cap {
			return true
		}
	}
	return false
}
func normalizeCaps(in []string, management string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range in {
		for _, v := range strings.Split(group, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			switch v {
			case "inbox", "status", "steer", "interrupt", "stop":
			default:
				return nil, workorders.Fail(400, "invalid capability")
			}
			if seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	if management == "unmanaged" && (seen["interrupt"] || seen["stop"]) {
		return nil, workorders.Fail(400, "unmanaged session cannot own controls")
	}
	sort.Strings(out)
	return out, nil
}
func validateTicket(ctx context.Context, tx pgx.Tx, projectID, ticketID string) error {
	if !workorders.UUID(ticketID) {
		return workorders.Fail(400, "invalid ticket id")
	}
	var ok bool
	err := tx.QueryRow(ctx, `WITH RECURSIVE chain AS (SELECT id,parent_id,kind_id,deleted_at FROM nodes WHERE id=$1 UNION ALL SELECT n.id,n.parent_id,n.kind_id,n.deleted_at FROM nodes n JOIN chain c ON n.id=c.parent_id) SELECT EXISTS(SELECT 1 FROM chain c JOIN node_kinds k ON k.id=c.kind_id WHERE c.id=$1 AND c.deleted_at IS NULL AND k.slug IN ('ticket','task','work_order')) AND EXISTS(SELECT 1 FROM chain WHERE id=$2 AND deleted_at IS NULL)`, ticketID, projectID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return workorders.Fail(400, "ticket must be a live node under project")
	}
	return nil
}
func validateParent(ctx context.Context, tx pgx.Tx, projectID, parentID, childID string) error {
	if parentID == "" {
		return nil
	}
	if !workorders.UUID(parentID) || parentID == childID {
		return workorders.Fail(400, "invalid parent session")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text,0))`, projectID); err != nil {
		return err
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT stopped_at IS NULL FROM harness_sessions WHERE project_id=$1 AND id=$2`, projectID, parentID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || !active {
		return workorders.Fail(409, "active same-project parent required")
	}
	if err != nil {
		return err
	}
	var depth int
	err = tx.QueryRow(ctx, `WITH RECURSIVE ancestors(id,parent_id,depth) AS (SELECT id,parent_id,1 FROM harness_sessions WHERE id=$1 UNION ALL SELECT p.id,p.parent_id,a.depth+1 FROM harness_sessions p JOIN ancestors a ON p.id=a.parent_id WHERE a.depth<17) SELECT coalesce(max(depth),0) FROM ancestors`, parentID).Scan(&depth)
	if err != nil {
		return err
	}
	if depth >= 16 {
		return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
	}
	if childID != "" {
		var cycle bool
		var descendantDepth int
		err = tx.QueryRow(ctx, `WITH RECURSIVE descendants(id,depth) AS (SELECT id,1 FROM harness_sessions WHERE id=$1 UNION ALL SELECT c.id,d.depth+1 FROM harness_sessions c JOIN descendants d ON c.parent_id=d.id WHERE d.depth<17) SELECT coalesce(max(depth),0),coalesce(bool_or(id=$2),false) FROM descendants`, childID, parentID).Scan(&descendantDepth, &cycle)
		if err != nil {
			return err
		}
		if cycle {
			return workorders.Fail(409, "harness hierarchy cycle")
		}
		if depth+descendantDepth > 16 {
			return workorders.Fail(409, "harness hierarchy exceeds 16 ancestors")
		}
	}
	return nil
}

type registration struct {
	AgentPrincipalID string   `json:"agent_principal_id"`
	RunID            *string  `json:"run_id"`
	TicketNodeID     *string  `json:"ticket_node_id"`
	WorkOrderID      *string  `json:"work_order_id"`
	ParentID         *string  `json:"parent_harness_session_id"`
	Harness          string   `json:"harness"`
	Host             string   `json:"host"`
	DisplayLabel     *string  `json:"display_label"`
	Management       string   `json:"management_mode"`
	Role             string   `json:"role"`
	WorkShape        string   `json:"work_shape"`
	Capabilities     []string `json:"advertised_capabilities"`
	SessionRef       string   `json:"harness_session_ref"`
	WorkerLease      string   `json:"worker_lease"`
}

func (m *Module) register(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	projectID := r.PathValue("projectId")
	if err := project(ctx, tx, projectID); err != nil {
		return nil, err
	}
	var in registration
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !workorders.UUID(in.AgentPrincipalID) || !validHarness(in.Harness) || len(in.Host) < 1 || len(in.Host) > 128 || strings.TrimSpace(in.Host) != in.Host || len(in.SessionRef) < 16 || len(in.SessionRef) > 4096 || len(in.WorkerLease) < 32 || len(in.WorkerLease) > 256 || strings.ContainsAny(in.SessionRef+in.WorkerLease, "\r\n") || in.SessionRef == in.WorkerLease {
		return nil, workorders.Fail(400, "invalid harness registration")
	}
	if in.DisplayLabel != nil {
		label := strings.TrimSpace(*in.DisplayLabel)
		if !utf8.ValidString(label) || utf8.RuneCountInString(label) > 128 || strings.ContainsFunc(*in.DisplayLabel, unicode.IsControl) {
			return nil, workorders.Fail(400, "display label must be at most 128 characters without control characters")
		}
		in.DisplayLabel = nil
		if label != "" {
			in.DisplayLabel = &label
		}
	}
	if p.Kind == tenant.Agent && p.ID != in.AgentPrincipalID {
		return nil, workorders.Fail(403, "agent may register only itself")
	}
	if in.Management != "managed" && in.Management != "unmanaged" {
		return nil, workorders.Fail(400, "invalid management mode")
	}
	if in.Role != "worker" && in.Role != "coordinator" {
		return nil, workorders.Fail(400, "invalid hierarchy role")
	}
	caps, err := normalizeCaps(in.Capabilities, in.Management)
	if err != nil {
		return nil, err
	}
	if (in.TicketNodeID == nil) != (in.WorkShape == "" || in.WorkShape == "unknown") {
		return nil, workorders.Fail(400, "ticket and work shape must be bound together")
	}
	if in.TicketNodeID == nil {
		in.WorkShape = "unknown"
	} else {
		if !validShape(in.WorkShape) {
			return nil, workorders.Fail(400, "invalid work shape")
		}
		if err = validateTicket(ctx, tx, projectID, *in.TicketNodeID); err != nil {
			return nil, err
		}
	}
	var agentKind string
	err = tx.QueryRow(ctx, `SELECT kind FROM principals WHERE id=$1`, in.AgentPrincipalID).Scan(&agentKind)
	if err != nil {
		return nil, err
	}
	if agentKind != "agent" {
		return nil, workorders.Fail(400, "agent principal required")
	}
	if in.RunID != nil {
		var agentID, orderID string
		err = tx.QueryRow(ctx, `SELECT agent_principal_id::text,work_order_id::text FROM agent_runs WHERE id=$1`, *in.RunID).Scan(&agentID, &orderID)
		if err != nil {
			return nil, err
		}
		if agentID != in.AgentPrincipalID || in.WorkOrderID == nil || *in.WorkOrderID != orderID {
			return nil, workorders.Fail(409, "run and work order binding conflict")
		}
	}
	if in.WorkOrderID != nil {
		var orderNode string
		err = tx.QueryRow(ctx, `SELECT node_id::text FROM work_orders WHERE node_id=$1`, *in.WorkOrderID).Scan(&orderNode)
		if err != nil {
			return nil, err
		}
		if err = validateTicket(ctx, tx, projectID, orderNode); err != nil {
			return nil, err
		}
	}
	ref, lease := digest("ref", in.SessionRef), digest("lease", in.WorkerLease)
	existing, err := scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND ref_digest=$2 AND stopped_at IS NULL FOR UPDATE`, projectID, ref))
	if err == nil {
		if subtle.ConstantTimeCompare(existing.leaseDigest, lease) != 1 || existing.AgentPrincipalID != in.AgentPrincipalID || existing.Harness != in.Harness || existing.Host != in.Host || existing.Management != in.Management || existing.Role != in.Role || existing.WorkShape != in.WorkShape || !same(existing.DisplayLabel, in.DisplayLabel) || !same(existing.ParentID, in.ParentID) || !same(existing.TicketNodeID, in.TicketNodeID) || !same(existing.RunID, in.RunID) || !same(existing.WorkOrderID, in.WorkOrderID) || !sameCaps(existing.Capabilities, caps) {
			return nil, workorders.Fail(409, "active generation conflicts with registration")
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if in.ParentID != nil {
		if err = validateParent(ctx, tx, projectID, *in.ParentID, ""); err != nil {
			return nil, err
		}
	}
	s, err := scanSession(tx.QueryRow(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,run_id,ticket_node_id,work_order_id,parent_id,harness,host,management,role,work_shape,capabilities,ref_digest,lease_digest,display_label) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING `+sessionColumns, p.TenantID, projectID, in.AgentPrincipalID, in.RunID, in.TicketNodeID, in.WorkOrderID, in.ParentID, in.Harness, in.Host, in.Management, in.Role, in.WorkShape, caps, ref, lease, in.DisplayLabel))
	if err != nil {
		return nil, err
	}
	return s, record(ctx, tx, p, s, "registered", nil, s)
}
func same(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func sameCaps(a, b []string) bool {
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
func (m *Module) list(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("projectId")
	if err := project(r.Context(), tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 ORDER BY created_at DESC,id LIMIT 200`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (m *Module) status(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	s, err := load(r.Context(), tx, r.PathValue("projectId"), r.PathValue("sessionId"), false)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT note,created_at FROM harness_activity_notes WHERE session_id=$1 ORDER BY id DESC LIMIT 20`, s.ID)
	if err != nil {
		return nil, err
	}
	s.ActivityHistory = []ActivityNote{}
	for rows.Next() {
		var item ActivityNote
		if err = rows.Scan(&item.Note, &item.At); err != nil {
			rows.Close()
			return nil, err
		}
		s.ActivityHistory = append(s.ActivityHistory, item)
	}
	err = rows.Err()
	rows.Close()
	return s, err
}
func (m *Module) orchestrator(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	id := r.PathValue("projectId")
	if err := project(r.Context(), tx, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions WHERE project_id=$1 AND role='coordinator' AND management='managed' AND phase IN ('working','yielded') AND activity IN ('busy','idle') AND heartbeat_at>clock_timestamp()-interval '2 minutes' AND stopped_at IS NULL`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := []Session{}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			return nil, e
		}
		found = append(found, s)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(found) == 1 {
		return map[string]any{"state": "resolved", "session": found[0]}, nil
	}
	if len(found) > 1 {
		return map[string]any{"state": "ambiguous"}, nil
	}
	return map[string]any{"state": "unset"}, nil
}
func (m *Module) bind(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		ExpectedRevision int64   `json:"expected_revision"`
		ParentID         *string `json:"parent_harness_session_id"`
		TicketNodeID     *string `json:"ticket_node_id"`
		WorkShape        string  `json:"work_shape"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.ExpectedRevision < 1 {
		return nil, workorders.Fail(400, "expected revision required")
	}
	ctx := r.Context()
	s, err := load(ctx, tx, r.PathValue("projectId"), r.PathValue("sessionId"), true)
	if err != nil {
		return nil, err
	}
	if p.Kind == tenant.Agent && p.ID != s.AgentPrincipalID {
		return nil, workorders.Fail(403, "agent may bind only its own session")
	}
	if s.StoppedAt != nil || s.Revision != in.ExpectedRevision {
		return nil, workorders.Fail(409, "session revision conflict")
	}
	if in.TicketNodeID == nil {
		if in.WorkShape != "unknown" {
			return nil, workorders.Fail(400, "detached ticket needs unknown shape")
		}
	} else {
		if !validShape(in.WorkShape) {
			return nil, workorders.Fail(400, "invalid work shape")
		}
		if err = validateTicket(ctx, tx, s.ProjectID, *in.TicketNodeID); err != nil {
			return nil, err
		}
	}
	if in.ParentID != nil {
		if err = validateParent(ctx, tx, s.ProjectID, *in.ParentID, s.ID); err != nil {
			return nil, err
		}
	}
	before := s
	err = tx.QueryRow(ctx, `UPDATE harness_sessions SET parent_id=$2,ticket_node_id=$3,work_shape=$4,revision=revision+1 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.ParentID, in.TicketNodeID, in.WorkShape).Scan(&s.ID, &s.ProjectID, &s.AgentPrincipalID, &s.RunID, &s.TicketNodeID, &s.WorkOrderID, &s.ParentID, &s.Harness, &s.Host, &s.Management, &s.Role, &s.WorkShape, &s.Capabilities, &s.Phase, &s.Activity, &s.ActivitySequence, &s.Revision, &s.HeartbeatAt, &s.StoppedAt, &s.StopReason, &s.CreatedAt, &s.refDigest, &s.leaseDigest, &s.DisplayLabel, &s.ActivityNote)
	if err != nil {
		return nil, err
	}
	return s, record(ctx, tx, p, s, "bound", before, s)
}
func (m *Module) heartbeat(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Phase            string  `json:"phase"`
		Activity         string  `json:"activity"`
		ActivitySequence int64   `json:"activity_sequence"`
		ActivityNote     *string `json:"activity_note"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if !validPhase(in.Phase) || in.ActivitySequence < 0 {
		return nil, workorders.Fail(400, "invalid heartbeat")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if in.Activity == "" {
		in.Activity = s.Activity
	}
	if in.Activity != "unknown" && in.Activity != "busy" && in.Activity != "idle" {
		return nil, workorders.Fail(400, "invalid activity")
	}
	if in.ActivitySequence < s.ActivitySequence {
		return nil, workorders.Fail(409, "stale activity sequence")
	}
	if in.ActivitySequence == s.ActivitySequence && in.Activity != s.Activity {
		return nil, workorders.Fail(409, "divergent activity replay")
	}
	if in.ActivityNote != nil {
		note, valid := normalizeActivityNote(*in.ActivityNote)
		if !valid {
			return nil, workorders.Fail(400, "activity note must be at most 120 characters")
		}
		in.ActivityNote = &note
	}
	note := s.ActivityNote
	changed := in.ActivityNote != nil && (note == nil || *note != *in.ActivityNote)
	if in.ActivityNote != nil {
		note = in.ActivityNote
	}
	before := s
	err = tx.QueryRow(ctx, `UPDATE harness_sessions SET phase=$2,activity=$3,activity_sequence=$4,heartbeat_at=clock_timestamp(),activity_note=$5 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Phase, in.Activity, in.ActivitySequence, note).Scan(&s.ID, &s.ProjectID, &s.AgentPrincipalID, &s.RunID, &s.TicketNodeID, &s.WorkOrderID, &s.ParentID, &s.Harness, &s.Host, &s.Management, &s.Role, &s.WorkShape, &s.Capabilities, &s.Phase, &s.Activity, &s.ActivitySequence, &s.Revision, &s.HeartbeatAt, &s.StoppedAt, &s.StopReason, &s.CreatedAt, &s.refDigest, &s.leaseDigest, &s.DisplayLabel, &s.ActivityNote)
	if err != nil {
		return nil, err
	}
	if changed && note != nil {
		if _, err = tx.Exec(ctx, `INSERT INTO harness_activity_notes(tenant_id,session_id,note) VALUES($1,$2,$3)`, p.TenantID, s.ID, *note); err != nil {
			return nil, err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM harness_activity_notes WHERE tenant_id=$1 AND session_id=$2 AND id NOT IN (SELECT id FROM harness_activity_notes WHERE tenant_id=$1 AND session_id=$2 ORDER BY id DESC LIMIT 20)`, p.TenantID, s.ID); err != nil {
			return nil, err
		}
	}
	if err = record(ctx, tx, p, s, "heartbeat", before, s); err != nil {
		return nil, err
	}
	return s, nil
}
func (m *Module) markStopped(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	switch in.Reason {
	case "stopped", "process_exited", "process_failed", "ownership_lost":
	default:
		return nil, workorders.Fail(400, "invalid stop reason")
	}
	ctx := r.Context()
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	before := s
	rows, err := tx.Query(ctx, `SELECT id::text FROM harness_controls WHERE session_id=$1 AND state<>'completed' ORDER BY sequence FOR UPDATE`, s.ID)
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
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		beforeControl, e := scanControl(tx.QueryRow(ctx, `SELECT `+controlColumns+` FROM harness_controls WHERE id=$1`, id))
		if e != nil {
			return nil, e
		}
		afterControl, e := scanControl(tx.QueryRow(ctx, `UPDATE harness_controls SET state='completed',outcome='rejected',reason='ownership_lost',claimed_at=coalesce(claimed_at,clock_timestamp()),completed_at=clock_timestamp() WHERE id=$1 RETURNING `+controlColumns, id))
		if e != nil {
			return nil, e
		}
		if e = record(ctx, tx, p, s, "control_completed", beforeControl, afterControl); e != nil {
			return nil, e
		}
	}
	leaseRows, err := tx.Query(ctx, `SELECT id::text,message_id::text,cursor FROM harness_deliveries WHERE session_id=$1 AND completed_at IS NULL AND released_at IS NULL FOR UPDATE`, s.ID)
	if err != nil {
		return nil, err
	}
	type leaseRef struct {
		id, message string
		cursor      int64
	}
	leases := []leaseRef{}
	for leaseRows.Next() {
		var v leaseRef
		if err = leaseRows.Scan(&v.id, &v.message, &v.cursor); err != nil {
			leaseRows.Close()
			return nil, err
		}
		leases = append(leases, v)
	}
	err = leaseRows.Err()
	leaseRows.Close()
	if err != nil {
		return nil, err
	}
	for _, v := range leases {
		if _, err = tx.Exec(ctx, `UPDATE harness_deliveries SET released_at=clock_timestamp() WHERE id=$1`, v.id); err != nil {
			return nil, err
		}
		if err = record(ctx, tx, p, s, "delivery_released", map[string]any{"delivery_id": v.id, "message_id": v.message, "cursor": v.cursor}, map[string]any{"delivery_id": v.id, "message_id": v.message, "cursor": v.cursor, "released": true}); err != nil {
			return nil, err
		}
	}
	s, err = scanSession(tx.QueryRow(ctx, `UPDATE harness_sessions SET phase='stopped',stopped_at=clock_timestamp(),stop_reason=$2 WHERE id=$1 RETURNING `+sessionColumns, s.ID, in.Reason))
	if err != nil {
		return nil, err
	}
	return s, record(ctx, tx, p, s, "stopped", before, s)
}

// Plugin is the compiled registry declaration. Harness HTTP scopes are enforced
// by the R2 key system, independently of plugin execution permissions.
func Plugin() (plugins.Plugin, error) {
	p := plugins.Plugin{Manifest: plugins.Manifest{ID: "harness", Version: "1", Owner: "aeon"}}
	digest, err := plugins.Digest(p)
	if err != nil {
		return p, err
	}
	p.Manifest.DigestSHA256 = digest
	return p, nil
}
