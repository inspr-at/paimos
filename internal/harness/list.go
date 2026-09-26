// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
)

type NodeSummary struct {
	ID    string `json:"id"`
	Key   string `json:"key"`
	Title string `json:"title"`
}

// PrincipalSummary names the agent principal that runs a session, so a list can
// show "aeon-coordinator" rather than the machine it happens to run on.
type PrincipalSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type SessionSummary struct {
	Session
	ActivityNoteID *int64            `json:"activity_note_id,omitempty"`
	Project        NodeSummary       `json:"project"`
	Ticket         *NodeSummary      `json:"ticket"`
	Agent          *PrincipalSummary `json:"agent"`
}

type sessionPage struct {
	Items      []SessionSummary `json:"items"`
	NextCursor *string          `json:"next_cursor"`
}

type sessionCursor struct {
	Fingerprint string    `json:"query"`
	At          time.Time `json:"at"`
	ID          string    `json:"id"`
}

// listAll is mounted by New; Plugin remains the coordinator's manifest entry.
// Paging is exclusive on the immutable (created_at,id) pair, including ties.
func (m *Module) listAll(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	q := r.URL.Query()
	limit, err := workorders.Limit(r)
	if err != nil {
		return nil, err
	}
	state, harness := q.Get("state"), q.Get("harness")
	if state != "" && state != "stopped" && !validPhase(state) {
		return nil, workorders.Fail(400, "invalid state")
	}
	if harness != "" && !validHarness(harness) {
		return nil, workorders.Fail(400, "invalid harness")
	}
	agent, projectID, ticket := q.Get("agent"), q.Get("project"), q.Get("ticket")
	for _, id := range []string{agent, projectID, ticket} {
		if id != "" && !workorders.UUID(id) {
			return nil, workorders.Fail(400, "invalid filter id")
		}
	}
	filters, _ := json.Marshal([]string{p.TenantID, p.ID, state, harness, agent, projectID, ticket})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(filters))
	var cursor sessionCursor
	if raw := q.Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 2048 || e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.At.IsZero() || !workorders.UUID(cursor.ID) {
			return nil, workorders.Fail(400, "invalid cursor")
		}
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sessionColumns+` FROM harness_sessions
 WHERE ($1='' OR CASE WHEN stopped_at IS NOT NULL THEN 'stopped' ELSE phase END=$1)
 AND ($2='' OR harness=$2) AND ($3::uuid IS NULL OR agent_principal_id=$3)
 AND ($4::uuid IS NULL OR project_id=$4) AND ($5::uuid IS NULL OR ticket_node_id=$5)
 AND ($6::timestamptz IS NULL OR (created_at,id)<($6,$7::uuid))
 ORDER BY created_at DESC,id DESC LIMIT $8`, state, harness, nullable(agent), nullable(projectID), nullable(ticket), cursorTime(cursor), nullable(cursor.ID), limit+1)
	if err != nil {
		return nil, err
	}
	out := sessionPage{Items: []SessionSummary{}}
	for rows.Next() {
		s, e := scanSession(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out.Items = append(out.Items, SessionSummary{Session: s})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		b, _ := json.Marshal(sessionCursor{fingerprint, last.CreatedAt, last.ID})
		next := base64.RawURLEncoding.EncodeToString(b)
		out.NextCursor = &next
	}
	// The latest entry ID is stable across ordinary heartbeats and monotonically
	// increases only when a new note is recorded. Fetch once for the bounded page.
	sessionIDs := make([]string, 0, len(out.Items))
	for _, s := range out.Items {
		sessionIDs = append(sessionIDs, s.ID)
	}
	noteRows, err := tx.Query(r.Context(), `SELECT DISTINCT ON (session_id) session_id::text,id FROM harness_activity_notes WHERE session_id=ANY($1::uuid[]) ORDER BY session_id,id DESC`, sessionIDs)
	if err != nil {
		return nil, err
	}
	latestNotes := map[string]int64{}
	for noteRows.Next() {
		var sessionID string
		var noteID int64
		if err = noteRows.Scan(&sessionID, &noteID); err != nil {
			noteRows.Close()
			return nil, err
		}
		latestNotes[sessionID] = noteID
	}
	err = noteRows.Err()
	noteRows.Close()
	if err != nil {
		return nil, err
	}
	// Fetch the bounded set of node summaries in one query after closing rows.
	ids := []string{}
	for _, s := range out.Items {
		ids = append(ids, s.ProjectID)
		if s.TicketNodeID != nil {
			ids = append(ids, *s.TicketNodeID)
		}
	}
	nodes, err := tx.Query(r.Context(), `SELECT id::text,key,title FROM nodes WHERE id=ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}
	defer nodes.Close()
	summaries := map[string]NodeSummary{}
	for nodes.Next() {
		var n NodeSummary
		if err = nodes.Scan(&n.ID, &n.Key, &n.Title); err != nil {
			return nil, err
		}
		summaries[n.ID] = n
	}
	if err = nodes.Err(); err != nil {
		return nil, err
	}
	// Agent principals' names, in one more bounded query.
	agents := []string{}
	for _, s := range out.Items {
		agents = append(agents, s.AgentPrincipalID)
	}
	principals, err := tx.Query(r.Context(), `SELECT id::text,name FROM principals WHERE id=ANY($1::uuid[])`, agents)
	if err != nil {
		return nil, err
	}
	defer principals.Close()
	names := map[string]PrincipalSummary{}
	for principals.Next() {
		var a PrincipalSummary
		if err = principals.Scan(&a.ID, &a.Name); err != nil {
			return nil, err
		}
		names[a.ID] = a
	}
	if err = principals.Err(); err != nil {
		return nil, err
	}
	for i := range out.Items {
		s := &out.Items[i]
		if noteID, ok := latestNotes[s.ID]; ok {
			s.ActivityNoteID = &noteID
		}
		s.Project = summaries[s.ProjectID]
		if s.TicketNodeID != nil {
			n := summaries[*s.TicketNodeID]
			s.Ticket = &n
		}
		if a, ok := names[s.AgentPrincipalID]; ok {
			s.Agent = &a
		}
	}
	return out, nil
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func cursorTime(c sessionCursor) any {
	if c.At.IsZero() {
		return nil
	}
	return c.At
}
