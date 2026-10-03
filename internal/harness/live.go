// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/agentactivity"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
)

// LiveWindow preserves the legacy live-only API filter. State-aware viewers
// opt into inactive sessions and apply their own heartbeat thresholds.
const LiveWindow = 2 * time.Minute

// maxLive bounds one answer; a workspace has a handful of live sessions. More
// than this answers the freshest and says it is truncated.
var maxLive = 500

// LiveAgent describes session evidence in a visible project. The default
// live-only view requires a heartbeat within LiveWindow. include_inactive
// also returns idle/aging sessions and stops from the last 24 hours, bounded
// by maxLive; state-aware viewers can distinguish errors from quiet stops.
// The project is the
// session's own project, or the project its bound ticket belongs to now.
//
// Who it is (principal_id and name) is present only when the caller holds
// members.read or harness.read at that project, like approval agent names
// (AEON-171). session_id, the key to the Agents workspace, is present only
// with harness.read in the workspace, which that workspace requires.
// DisplayLabel is the session's own public label. It is present only with
// harness.read at that project, and omitted when unlabeled. Name stays the
// agent principal; members.read does not reveal the session label.
type LiveAgent struct {
	StateEvidence
	StoppedAt  *time.Time `json:"stopped_at,omitempty"`
	StopReason *string    `json:"stop_reason,omitempty"`
	// Finished is required in every live row, never omitted: the session reported
	// 100% and stopped with a recorded clean exit (aeon_session_finished, AEON-437).
	// It is derived, so a viewer who may not read the stop reason gets it too.
	Finished          bool                    `json:"finished"`
	ProjectID         string                  `json:"project_id"`
	SessionID         string                  `json:"session_id,omitempty"`
	PrincipalID       string                  `json:"principal_id,omitempty"`
	Name              string                  `json:"name,omitempty"`
	DisplayLabel      *string                 `json:"display_label,omitempty"`
	Harness           string                  `json:"harness"`
	Model             *string                 `json:"model,omitempty"`
	ReasoningEffort   *string                 `json:"reasoning_effort,omitempty"`
	AccountLabel      *string                 `json:"account_label,omitempty"`
	HarnessVersion    *string                 `json:"harness_version,omitempty"`
	Management        string                  `json:"management_mode"`
	Role              string                  `json:"role"`
	Phase             string                  `json:"phase"`
	Activity          string                  `json:"activity"`
	CurrentActivity   *agentactivity.Activity `json:"current_activity,omitempty"`
	AgentActivityMode string                  `json:"agent_activity_mode,omitempty"`
	ActivityNote      *string                 `json:"activity_note,omitempty"`
	ActivityNoteID    *int64                  `json:"activity_note_id,omitempty"`
	Ticket            *LiveTicket             `json:"ticket"`
	Since             time.Time               `json:"since"`
	HeartbeatAt       *time.Time              `json:"heartbeat_at"`
	EtaStale          bool                    `json:"eta_stale,omitempty"`
	// ProgressPct is the last reported percent. A worker at 100% that then goes
	// quiet, or stops cleanly, is finished, not lost (AEON-437).
	ProgressPct *int `json:"progress_pct,omitempty"`
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
const liveSelect = `SELECT s.id::text,s.project_id::text,s.agent_principal_id::text,coalesce(a.name,''),s.display_label,s.harness,s.model,s.reasoning_effort,s.account_label,s.harness_version,s.management,s.role,s.phase,s.activity,s.activity_note,latest.id,
       t.id::text,t.key,t.title,t.project_id::text,s.created_at,s.heartbeat_at,s.stopped_at,s.stop_reason,s.eta_reported_at,s.progress_pct,aeon_session_finished(s.stopped_at,s.stop_reason,s.progress_pct),s.doing,s.doing_at,s.tool_activity,s.tool_activity_at
  FROM harness_sessions s
  LEFT JOIN LATERAL (SELECT id FROM harness_activity_notes WHERE session_id=s.id ORDER BY id DESC LIMIT 1) latest ON true
  LEFT JOIN principals a ON a.tenant_id=s.tenant_id AND a.id=s.agent_principal_id
  LEFT JOIN nodes t ON t.tenant_id=s.tenant_id AND t.id=s.ticket_node_id AND t.deleted_at IS NULL`

const liveQuery = liveSelect + `
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
	interval, err := etaInterval(ctx, tx)
	if err != nil {
		return nil, err
	}
	out.At = out.At.UTC()
	// Explicit opt-in preserves the legacy live filter and its partial index.
	query := liveQuery
	args := []any{LiveWindow.Seconds(), maxLive + 1}
	if r.URL.Query().Get("include_inactive") == "true" {
		query = liveSelect + `
 WHERE s.archived_at IS NULL AND (s.stopped_at IS NULL OR s.stopped_at > now()-interval '24 hours')
 ORDER BY coalesce(s.stopped_at,s.heartbeat_at,s.created_at) DESC,s.id DESC LIMIT $1`
		args = []any{maxLive + 1}
	}
	mode, err := activityMode(ctx, tx)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	found := []LiveAgent{}
	for rows.Next() {
		var v LiveAgent
		projection := Session{AgentActivityMode: mode}
		var ticketID, ticketKey, ticketTitle, ticketProject *string
		var heartbeat, reported *time.Time
		if err = rows.Scan(&v.SessionID, &v.ProjectID, &v.PrincipalID, &v.Name, &v.DisplayLabel, &v.Harness, &v.Model, &v.ReasoningEffort, &v.AccountLabel, &v.HarnessVersion, &v.Management, &v.Role, &v.Phase, &v.Activity, &v.ActivityNote, &v.ActivityNoteID,
			&ticketID, &ticketKey, &ticketTitle, &ticketProject, &v.Since, &heartbeat, &v.StoppedAt, &v.StopReason, &reported, &v.ProgressPct, &v.Finished, &projection.doing, &projection.doingAt, &projection.toolActivity, &projection.toolActivityAt); err != nil {
			rows.Close()
			return nil, err
		}
		projection.ActivityNote = v.ActivityNote
		projectActivity(&projection, out.At)
		v.CurrentActivity, v.AgentActivityMode = projection.CurrentActivity, mode
		v.ActivityNote = projection.ActivityNote
		if v.ActivityNote == nil {
			v.ActivityNoteID = nil
		}
		stampLiveEta(&v, reported, interval, out.At)
		if heartbeat != nil {
			v.HeartbeatAt = heartbeat
		}
		if v.DisplayLabel != nil && strings.TrimSpace(*v.DisplayLabel) == "" {
			v.DisplayLabel = nil
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
	ids := make([]string, len(found))
	for i := range found {
		ids[i] = found[i].SessionID
	}
	evidence, err := readStateEvidence(ctx, tx, ids)
	if err != nil {
		return nil, err
	}
	for i := range found {
		found[i].StateEvidence = evidence[found[i].SessionID]
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
				agent.StopReason = nil
				agent.RunStatus = nil
				agent.VendorLimited = false
				agent.LimitWindow = ""
				agent.LimitResetsAt = nil
				agent.AttentionReasons = nil
				agent.CurrentActivity, agent.AgentActivityMode = nil, ""
				agent.ActivityNote = nil
				agent.ActivityNoteID = nil
				agent.Model, agent.ReasoningEffort, agent.AccountLabel, agent.HarnessVersion = nil, nil, nil, nil
				agent.DisplayLabel = nil
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

func stampLiveEta(v *LiveAgent, reported *time.Time, interval time.Duration, now time.Time) {
	if v == nil || v.StoppedAt != nil || reported == nil {
		return
	}
	if now.Sub(*reported) > 2*interval {
		v.EtaStale = true
	}
}
