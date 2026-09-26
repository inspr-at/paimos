// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

// LiveWindow is how fresh a heartbeat must be for a session to count as live:
// the same two minutes the orchestrator resolution and the web app's
// HEARTBEAT_STALE_MS use.
const LiveWindow = 2 * time.Minute

// maxLive bounds one answer; a workspace has a handful of live sessions. More
// than this answers the freshest and says it is truncated.
var maxLive = 500

// LiveAgent is one agent actively working in a project right now (AEON-184):
// a session that is not stopped, heartbeated within LiveWindow, is starting,
// working or stopping, and has not reported itself idle. The project is the
// session's own project, or the project its bound ticket belongs to now.
//
// Who it is (principal_id and name) is present only when the caller holds
// members.read or harness.read at that project, like approval agent names
// (AEON-171). session_id, the key to the Agents workspace, is present only
// with harness.read in the workspace, which that workspace requires.
type LiveAgent struct {
	ProjectID      string      `json:"project_id"`
	SessionID      string      `json:"session_id,omitempty"`
	PrincipalID    string      `json:"principal_id,omitempty"`
	Name           string      `json:"name,omitempty"`
	Harness        string      `json:"harness"`
	Management     string      `json:"management_mode"`
	Role           string      `json:"role"`
	Phase          string      `json:"phase"`
	Activity       string      `json:"activity"`
	ActivityNote   *string     `json:"activity_note,omitempty"`
	ActivityNoteID *int64      `json:"activity_note_id,omitempty"`
	Ticket         *LiveTicket `json:"ticket"`
	Since          time.Time   `json:"since"`
	HeartbeatAt    time.Time   `json:"heartbeat_at"`
}

// LiveTicket is the bound ticket and the project it lives in now, so a link to
// it is right even on the card of the project the session started in.
type LiveTicket struct {
	NodeSummary
	ProjectID string `json:"project_id"`
}

// LivePage answers GET /api/harness-sessions/live. At is the server's clock,
// so a client can show elapsed time without trusting its own. Truncated says
// more sessions were live than one answer holds (the freshest are kept).
type LivePage struct {
	Items        []LiveAgent `json:"items"`
	At           time.Time   `json:"at"`
	FreshSeconds int         `json:"fresh_seconds"`
	Truncated    bool        `json:"truncated"`
}

// liveQuery walks harness_sessions_live_idx (0867) from the freshest heartbeat
// down to the freshness window: now() is stable, so the window bounds the index
// range itself, and the LIMIT ends the walk. The predicates match the partial
// index's so the planner can use it.
const liveQuery = `SELECT s.id::text,s.project_id::text,s.agent_principal_id::text,coalesce(a.name,''),s.harness,s.management,s.role,s.phase,s.activity,s.activity_note,latest.id,
       t.id::text,t.key,t.title,t.project_id::text,s.created_at,s.heartbeat_at
  FROM harness_sessions s
  LEFT JOIN LATERAL (SELECT id FROM harness_activity_notes WHERE session_id=s.id ORDER BY id DESC LIMIT 1) latest ON true
  LEFT JOIN principals a ON a.tenant_id=s.tenant_id AND a.id=s.agent_principal_id
  LEFT JOIN nodes t ON t.tenant_id=s.tenant_id AND t.id=s.ticket_node_id AND t.deleted_at IS NULL
 WHERE s.phase IN ('starting', 'working', 'stopping') AND s.activity <> 'idle' AND s.stopped_at IS NULL
   AND s.heartbeat_at > now()-make_interval(secs=>$1)
 ORDER BY s.heartbeat_at DESC,s.id DESC LIMIT $2`

// live reads inside the caller's transaction, so tenant row-level security and
// project visibility (ADR-003 P2) decide which sessions and tickets exist for
// it: a project the caller cannot see contributes nothing, not even a count.
// Who may know which agent is decided from one read of the caller's bindings.
func (m *Module) live(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	ctx := r.Context()
	out := LivePage{Items: []LiveAgent{}, FreshSeconds: int(LiveWindow.Seconds())}
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&out.At); err != nil {
		return nil, err
	}
	out.At = out.At.UTC()
	rows, err := tx.Query(ctx, liveQuery, LiveWindow.Seconds(), maxLive+1)
	if err != nil {
		return nil, err
	}
	found := []LiveAgent{}
	for rows.Next() {
		var v LiveAgent
		var ticketID, ticketKey, ticketTitle, ticketProject *string
		var heartbeat *time.Time
		if err = rows.Scan(&v.SessionID, &v.ProjectID, &v.PrincipalID, &v.Name, &v.Harness, &v.Management, &v.Role, &v.Phase, &v.Activity, &v.ActivityNote, &v.ActivityNoteID,
			&ticketID, &ticketKey, &ticketTitle, &ticketProject, &v.Since, &heartbeat); err != nil {
			rows.Close()
			return nil, err
		}
		if heartbeat != nil {
			v.HeartbeatAt = *heartbeat
		}
		if ticketID != nil && ticketKey != nil && ticketTitle != nil && ticketProject != nil {
			v.Ticket = &LiveTicket{NodeSummary: NodeSummary{ID: *ticketID, Key: *ticketKey, Title: *ticketTitle}, ProjectID: *ticketProject}
		}
		found = append(found, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(found) > maxLive {
		found, out.Truncated = found[:maxLive], true
	}
	if len(found) == 0 {
		return out, nil
	}
	allowed, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	// The Agents workspace lists sessions tenant-wide: its key needs harness.read there.
	openSessions := allowed("harness.read", "")
	for _, v := range found {
		projects := []string{v.ProjectID}
		// A ticket moved on to another project takes its agent along; the
		// ticket row is visible only when that project is.
		if v.Ticket != nil && v.Ticket.ProjectID != v.ProjectID {
			projects = append(projects, v.Ticket.ProjectID)
		}
		for _, projectID := range projects {
			agent := v
			agent.ProjectID = projectID
			if !allowed("harness.read", projectID) {
				agent.ActivityNote = nil
				agent.ActivityNoteID = nil
			}
			if !allowed("harness.read", projectID) && !allowed("members.read", projectID) {
				agent.PrincipalID, agent.Name = "", ""
			}
			if !openSessions {
				agent.SessionID = ""
			}
			out.Items = append(out.Items, agent)
		}
	}
	return out, nil
}
