// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/reviewgate"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

// A decision is evidence on the existing project node, not a new execution
// object. The node event namespace already has project-member visibility.
// Nothing here launches work, supplies permissions or evaluates review gates.
const leadDecisionEvent = "node.lead_decision_recorded"

type LeadDecisionGate struct {
	Kind       string     `json:"kind"`
	State      string     `json:"state"`
	ObservedAt *time.Time `json:"observed_at"`
}

type LeadDecisionLink struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	HeadSHA string `json:"head_sha,omitempty"`
}

type LeadDecisionWrite struct {
	RequestID       string             `json:"request_id,omitempty"`
	TicketNodeID    string             `json:"ticket_node_id,omitempty"`
	Stage           string             `json:"stage"`
	Outcome         string             `json:"outcome"`
	ReasonCodes     []string           `json:"reason_codes"`
	PolicySource    string             `json:"policy_source"`
	PolicyRevision  int64              `json:"policy_revision"`
	Attempt         int                `json:"attempt"`
	PreviousEventID *int64             `json:"previous_event_id,omitempty"`
	Gates           []LeadDecisionGate `json:"gates"`
	Results         []LeadDecisionLink `json:"results"`
}

type LeadDecisionFreshness struct {
	Kind      string `json:"kind"`
	Freshness string `json:"freshness"`
}

type LeadDecisionResult struct {
	LeadDecisionLink
	BaseSHA        string  `json:"base_sha,omitempty"`
	AuthorRunID    *string `json:"author_run_id,omitempty"`
	ReviewRunID    *string `json:"review_run_id,omitempty"`
	AuthorFamily   string  `json:"author_family,omitempty"`
	ReviewerFamily *string `json:"reviewer_family,omitempty"`
}

type LeadDecision struct {
	EventID          int64                   `json:"event_id,omitempty"`
	ProjectID        string                  `json:"project_id"`
	SessionID        string                  `json:"session_id"`
	RecordedAt       time.Time               `json:"recorded_at"`
	Evidence         string                  `json:"evidence"`
	AuthorityGranted bool                    `json:"authority_granted"`
	Request          LeadDecisionWrite       `json:"request"`
	Outcome          string                  `json:"outcome"`
	ReasonCodes      []string                `json:"reason_codes"`
	GateFreshness    []LeadDecisionFreshness `json:"gate_freshness"`
	Results          []LeadDecisionResult    `json:"results"`
}

type LeadDecisionRecorded struct {
	Decision LeadDecision `json:"decision"`
	Replayed bool         `json:"replayed"`
}

type LeadDecisionPage struct {
	Items     []LeadDecision `json:"items"`
	NextAfter *int64         `json:"next_after"`
}

// NormalizeLeadDecision validates a closed vocabulary before database work.
// Numeric revisions and enums keep prompts, host/account identifiers, arbitrary
// URLs and free-form failure messages out of every public replay surface.
func NormalizeLeadDecision(in LeadDecisionWrite) (LeadDecisionWrite, error) {
	bad := func() (LeadDecisionWrite, error) {
		return in, workorders.Fail(400, "invalid lead decision evidence")
	}
	canonicalUUID := func(id string) bool { return workorders.UUID(id) && strings.ToLower(id) == id }
	if !canonicalUUID(in.RequestID) || (in.TicketNodeID != "" && !canonicalUUID(in.TicketNodeID)) ||
		(in.PolicySource != "workspace" && in.PolicySource != "project") || in.PolicyRevision < 1 || in.PolicyRevision > 2147483647 ||
		in.Attempt < 1 || in.Attempt > 32 || in.PreviousEventID != nil && *in.PreviousEventID < 1 || len(in.ReasonCodes) < 1 || len(in.ReasonCodes) > 8 || len(in.Gates) > 4 || len(in.Results) > 8 {
		return bad()
	}
	switch in.Stage {
	case "queue":
		if in.Outcome != "selected" && in.Outcome != "wait" && in.Outcome != "partial" {
			return bad()
		}
	case "admission":
		if in.Outcome != "selected" && in.Outcome != "wait" || len(in.Gates) != 4 {
			return bad()
		}
	case "review":
		if in.Outcome != "requested" && in.Outcome != "passed" && in.Outcome != "failed" && in.Outcome != "wait" && in.Outcome != "partial" {
			return bad()
		}
	case "release_handoff":
		if in.Outcome != "handoff" && in.Outcome != "wait" && in.Outcome != "partial" {
			return bad()
		}
	default:
		return bad()
	}
	seen := map[string]bool{}
	for _, code := range in.ReasonCodes {
		if seen[code] || !leadReason(code) {
			return bad()
		}
		seen[code] = true
	}
	if in.Outcome == "partial" && !seen["partial_result"] {
		return bad()
	}
	if seen["partial_result"] && in.Outcome != "partial" && in.Outcome != "wait" {
		return bad()
	}
	seen = map[string]bool{}
	for _, gate := range in.Gates {
		if seen[gate.Kind] || !leadGateKind(gate.Kind) || (gate.State != "ready" && gate.State != "full" && gate.State != "unreadable") {
			return bad()
		}
		seen[gate.Kind] = true
	}
	seen = map[string]bool{}
	for _, link := range in.Results {
		if !canonicalUUID(link.ID) || seen[link.Kind+":"+link.ID] {
			return bad()
		}
		seen[link.Kind+":"+link.ID] = true
		switch link.Kind {
		case "run", "release":
			if link.HeadSHA != "" {
				return bad()
			}
		case "review":
			if !reviewgate.ValidSHA(link.HeadSHA) {
				return bad()
			}
		default:
			return bad()
		}
	}
	// Copies keep caller-owned slices unchanged and make replay independent of
	// JSON object/array order without accepting duplicate evidence.
	in.ReasonCodes = append([]string{}, in.ReasonCodes...)
	in.Gates = append([]LeadDecisionGate{}, in.Gates...)
	in.Results = append([]LeadDecisionLink{}, in.Results...)
	sort.Strings(in.ReasonCodes)
	sort.Slice(in.Gates, func(i, j int) bool { return in.Gates[i].Kind < in.Gates[j].Kind })
	sort.Slice(in.Results, func(i, j int) bool { return in.Results[i].Kind+in.Results[i].ID < in.Results[j].Kind+in.Results[j].ID })
	for i := range in.Gates {
		if in.Gates[i].ObservedAt != nil {
			at := in.Gates[i].ObservedAt.UTC()
			in.Gates[i].ObservedAt = &at
		}
	}
	return in, nil
}

func leadGateKind(kind string) bool {
	return kind == "dial" || kind == "harness" || kind == "account_room" || kind == "host_load"
}

func leadReason(code string) bool {
	switch code {
	case "oldest_eligible", "manual_order", "dependency_wait", "criteria_wait", "no_work", "policy_wait", "gates_ready",
		"review_requested", "review_passed", "review_failed", "attempt_limit", "release_handoff", "person_gate_required",
		"forecast_unavailable", "model_escalated", "login_selected", "connection_broken", "process_broken", "capacity_unavailable", "capacity_full", "partial_result":
		return true
	}
	for _, kind := range []string{"dial", "harness", "account_room", "host_load"} {
		for _, state := range []string{"full", "unreadable", "stale"} {
			if code == kind+"_"+state {
				return true
			}
		}
	}
	return false
}

func freezeLeadDecision(in LeadDecisionWrite, projectID, sessionID string, now time.Time) LeadDecision {
	out := LeadDecision{ProjectID: projectID, SessionID: sessionID, RecordedAt: now, Evidence: "coordinator_reported", Request: in,
		Outcome: in.Outcome, ReasonCodes: append([]string{}, in.ReasonCodes...), GateFreshness: []LeadDecisionFreshness{}, Results: []LeadDecisionResult{}}
	blocked := false
	for _, gate := range in.Gates {
		freshness := "fresh"
		if gate.State == "unreadable" || gate.ObservedAt == nil {
			freshness = "unreadable"
		} else if gate.ObservedAt.After(now) || now.Sub(*gate.ObservedAt) > 120*time.Second {
			freshness = "stale"
		}
		out.GateFreshness = append(out.GateFreshness, LeadDecisionFreshness{gate.Kind, freshness})
		code := ""
		if freshness != "fresh" {
			code = gate.Kind + "_" + freshness
		} else if gate.State == "full" {
			code = gate.Kind + "_full"
		}
		if code != "" {
			blocked = true
			if !containsCode(out.ReasonCodes, code) {
				out.ReasonCodes = append(out.ReasonCodes, code)
			}
		}
	}
	if in.Stage == "admission" && blocked {
		out.Outcome = "wait"
		out.ReasonCodes = withoutCode(out.ReasonCodes, "gates_ready")
	}
	if in.Stage == "release_handoff" && !containsCode(out.ReasonCodes, "person_gate_required") {
		out.ReasonCodes = append(out.ReasonCodes, "person_gate_required")
	}
	sort.Strings(out.ReasonCodes)
	return out
}

func containsCode(codes []string, code string) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

func withoutCode(codes []string, code string) []string {
	out := []string{}
	for _, c := range codes {
		if c != code {
			out = append(out, c)
		}
	}
	return out
}

func (m *Module) recordLeadDecision(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	var in LeadDecisionWrite
	r.Body = http.MaxBytesReader(nil, r.Body, 8192)
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	in, err := NormalizeLeadDecision(in)
	if err != nil {
		return nil, err
	}
	ctx := r.Context()
	if err = db.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
		return nil, err
	}
	// Fences match generation/access/tree changes. All checks precede replay,
	// and Append is the last operation acquiring a lock (event counter last).
	if err = db.LockWorkTreeTx(ctx, tx); err != nil {
		return nil, err
	}
	projectID := r.PathValue("projectId")
	if !workorders.UUID(projectID) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	if err = authz.RequireTx(ctx, tx, p, "harness.worker", authz.Scope{ProjectID: projectID}); err != nil {
		return nil, err
	}
	if err = project(ctx, tx, projectID); err != nil {
		return nil, err
	}
	s, err := worker(ctx, tx, r, p)
	if err != nil {
		return nil, err
	}
	if s.Role != "coordinator" {
		return nil, workorders.Fail(403, "lead decisions require the owning coordinator generation")
	}
	// Never call load(Session) for replay: it contains private account/host
	// labels. The persisted snapshot contains only the allowlisted audit fields.
	var raw json.RawMessage
	var eventID int64
	err = tx.QueryRow(ctx, `SELECT id,after FROM events WHERE tenant_id=$1 AND node_id=$2 AND type=$3 AND metadata->>'lead_decision_request_id'=$4 ORDER BY id LIMIT 1`,
		p.TenantID, projectID, leadDecisionEvent, in.RequestID).Scan(&eventID, &raw)
	if err == nil {
		var old LeadDecision
		if err = json.Unmarshal(raw, &old); err != nil {
			return nil, err
		}
		old.Request.RequestID = in.RequestID
		a, _ := json.Marshal(old.Request)
		b, _ := json.Marshal(in)
		if old.SessionID != s.ID || string(a) != string(b) {
			return nil, workorders.Fail(409, "lead decision request_id already bound to different evidence")
		}
		old.EventID = eventID
		return LeadDecisionRecorded{Decision: old, Replayed: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if in.PreviousEventID != nil {
		var previousRaw json.RawMessage
		err = tx.QueryRow(ctx, `SELECT after FROM events WHERE tenant_id=$1 AND node_id=$2 AND type=$3 AND id=$4`, p.TenantID, projectID, leadDecisionEvent, *in.PreviousEventID).Scan(&previousRaw)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(400, "previous decision must be visible in the same project")
		}
		if err != nil {
			return nil, err
		}
		var previous LeadDecision
		if err = json.Unmarshal(previousRaw, &previous); err != nil {
			return nil, err
		}
		if previous.Request.TicketNodeID != in.TicketNodeID || in.Attempt < previous.Request.Attempt || in.Attempt > previous.Request.Attempt+1 {
			return nil, workorders.Fail(409, "previous decision differs from this ticket or attempt lineage")
		}
	}
	if in.TicketNodeID != "" {
		var exists bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug IN ('work','ticket','task'))`, in.TicketNodeID, s.ProjectID).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, workorders.Fail(400, "decision ticket must be visible in the same project")
		}
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	out := freezeLeadDecision(in, s.ProjectID, s.ID, now)
	for _, link := range in.Results {
		result := LeadDecisionResult{LeadDecisionLink: link}
		var ticketID string
		switch link.Kind {
		case "run":
			err = tx.QueryRow(ctx, `SELECT coalesce(r.queue_node_id,n.parent_id)::text FROM agent_runs r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.work_order_id WHERE r.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL`, link.ID, s.ProjectID).Scan(&ticketID)
		case "review":
			var head string
			err = tx.QueryRow(ctx, `SELECT v.ticket_node_id::text,v.base_sha,v.head_sha,v.author_run_id::text,v.run_id::text,v.author_family,v.reviewer_family FROM work_order_reviews v JOIN nodes n ON n.tenant_id=v.tenant_id AND n.id=v.ticket_node_id WHERE v.work_order_id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL`, link.ID, s.ProjectID).Scan(&ticketID, &result.BaseSHA, &head, &result.AuthorRunID, &result.ReviewRunID, &result.AuthorFamily, &result.ReviewerFamily)
			if err == nil && head != link.HeadSHA {
				return nil, workorders.Fail(409, "review link differs from the pinned commit")
			}
		case "release":
			err = tx.QueryRow(ctx, `SELECT '' FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1 AND n.project_id=$2 AND n.deleted_at IS NULL AND k.slug='release'`, link.ID, s.ProjectID).Scan(&ticketID)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, workorders.Fail(400, "result link must be visible in the same project")
		}
		if err != nil {
			return nil, err
		}
		if in.TicketNodeID != "" && ticketID != "" && in.TicketNodeID != ticketID {
			return nil, workorders.Fail(409, "result link belongs to a different ticket")
		}
		out.Results = append(out.Results, result)
	}
	// Idempotency identity belongs in event metadata, not node snapshots:
	// the generic node-reference collector treats any UUID in a snapshot as a
	// possible node. A caller-selected request UUID must never hide an otherwise
	// public report by coinciding with an unrelated hidden node's UUID.
	stored := out
	stored.Request.RequestID = ""
	metadata, err := json.Marshal(map[string]string{"lead_decision_request_id": in.RequestID})
	if err != nil {
		return nil, err
	}
	e, err := events.Append(ctx, tx, p, events.Change{NodeID: &projectID, Type: leadDecisionEvent, After: stored, At: &now, Metadata: metadata})
	if err != nil {
		// The replay lookup obeys event RLS, but request uniqueness must not.
		// A previously recorded decision can become hidden after a node move.
		// Reject reuse without reading that snapshot or disclosing PG detail;
		// the transaction rolls back the failed append and counter allocation.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "events_lead_decision_request_identity" {
			return nil, workorders.Fail(409, "lead decision request_id already bound to recorded evidence")
		}
		return nil, err
	}
	out.EventID = e.ID
	return LeadDecisionRecorded{Decision: out}, nil
}

func (m *Module) listLeadDecisions(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := db.SetLocalStatementTimeout(r.Context(), tx, 5*time.Second); err != nil {
		return nil, err
	}
	q := r.URL.Query()
	for key, values := range q {
		if (key != "after" && key != "limit" && key != "session_id") || len(values) != 1 {
			return nil, workorders.Fail(400, "invalid lead decision query")
		}
	}
	after := int64(0)
	var err error
	if q.Has("after") {
		after, err = strconv.ParseInt(q.Get("after"), 10, 64)
		if err != nil || after < 0 {
			return nil, workorders.Fail(400, "invalid after cursor")
		}
	}
	limit, err := workorders.Limit(r)
	if err != nil {
		return nil, err
	}
	if q.Has("limit") && q.Get("limit") == "" {
		return nil, workorders.Fail(400, "invalid limit")
	}
	sessionID := q.Get("session_id")
	if q.Has("session_id") && !workorders.UUID(sessionID) {
		return nil, workorders.Fail(400, "invalid session id")
	}
	projectID := r.PathValue("projectId")
	if !workorders.UUID(projectID) {
		return nil, workorders.Fail(400, "invalid project id")
	}
	// A project member's node read grant suffices for redacted audit history.
	if err = authz.LockProjectWrite(r.Context(), tx, p.TenantID); err != nil {
		return nil, err
	}
	if err = authz.RequireTx(r.Context(), tx, p, "nodes.read", authz.Scope{ProjectID: projectID}); err != nil {
		return nil, err
	}
	if err = project(r.Context(), tx, projectID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(r.Context(), `SELECT id,after,metadata->>'lead_decision_request_id' FROM events WHERE tenant_id=$1 AND node_id=$2 AND type=$3 AND id>$4 AND ($5='' OR after->>'session_id'=$5) ORDER BY id LIMIT $6`, p.TenantID, projectID, leadDecisionEvent, after, strings.ToLower(sessionID), limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := LeadDecisionPage{Items: []LeadDecision{}}
	for rows.Next() {
		var id int64
		var raw json.RawMessage
		var requestID string
		if err = rows.Scan(&id, &raw, &requestID); err != nil {
			return nil, err
		}
		if len(out.Items) == limit {
			last := out.Items[len(out.Items)-1].EventID
			out.NextAfter = &last
			break
		}
		var item LeadDecision
		if err = json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		item.EventID = id
		item.Request.RequestID = requestID
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
