// SPDX-License-Identifier: AGPL-3.0-only

package inbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// LaneStatus is explicitly published progress against a goal or work item.
// It never changes the target node, its state or its own ETA fields.
type LaneStatus struct {
	TargetNodeID string     `json:"target_node_id"`
	Done         string     `json:"done"`
	Blocker      string     `json:"blocker"`
	BlockerOwner string     `json:"blocker_owner"`
	Next         string     `json:"next"`
	ETA          *time.Time `json:"eta"`
}

// Validate normalizes a bounded single-line report, without inferring a lane.
func (s *LaneStatus) Validate() error {
	id, ok := parseUUID(s.TargetNodeID)
	if !ok {
		return badRequest("invalid lane_status.target_node_id")
	}
	s.TargetNodeID = id
	for _, field := range []struct {
		name string
		text *string
		max  int
	}{{"done", &s.Done, 2000}, {"blocker", &s.Blocker, 2000}, {"blocker_owner", &s.BlockerOwner, 300}, {"next", &s.Next, 2000}} {
		if !utf8.ValidString(*field.text) || utf8.RuneCountInString(*field.text) > field.max || strings.ContainsFunc(*field.text, func(r rune) bool { return unicode.IsControl(r) && !unicode.IsSpace(r) }) {
			return badRequest("invalid lane_status." + field.name)
		}
		*field.text = strings.Join(strings.Fields(*field.text), " ")
	}
	if s.Done == "" || s.Next == "" || (s.Blocker == "") != (s.BlockerOwner == "") {
		return badRequest("lane_status requires done, next and an owner for each blocker")
	}
	return nil
}

// Line is the human-readable inbox body, also returned by the latest API.
func (s LaneStatus) Line() string {
	blocker := "none"
	if s.Blocker != "" {
		blocker = s.Blocker + " (" + s.BlockerOwner + ")"
	}
	eta := "unknown"
	if s.ETA != nil {
		eta = s.ETA.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("done: %s | blocker: %s | next: %s | ETA: %s", s.Done, blocker, s.Next, eta)
}

func normalizeLaneStatus(kind string, status *LaneStatus, session *string) error {
	if kind == "" && status == nil {
		return nil
	}
	if kind != "status" || status == nil || session == nil {
		return badRequest("kind=status requires lane_status and sender_session_id")
	}
	return status.Validate()
}

// Caller holds the tenant/access-change/tree fence. Target locks precede all
// session, message and projection locks and the final event-counter lock.
func authorizeLaneStatusTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, session *string, status *LaneStatus, project string) error {
	if status == nil {
		return nil
	}
	if session == nil || p.Kind != tenant.Agent {
		return errForbidden
	}
	var scope, kind, sessionProject string
	if err := tx.QueryRow(ctx, `SELECT coalesce(n.project_id::text,''),k.slug
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.id=$1::uuid AND n.deleted_at IS NULL FOR SHARE OF n`, status.TargetNodeID).Scan(&scope, &kind); err != nil {
		return err
	}
	if kind != "work" && kind != "goal" && kind != "release" {
		return badRequest("lane status target must be a goal or work item")
	}
	if err := tx.QueryRow(ctx, `SELECT project_id::text FROM harness_sessions WHERE id=$1::uuid AND agent_principal_id=$2::uuid`, *session, p.ID).Scan(&sessionProject); err != nil {
		return err
	}
	if scope != "" && scope != sessionProject || project != "" && project != sessionProject {
		return errNotFound
	}
	if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: scope}); err != nil {
		return errForbidden
	}
	return nil
}

// recordLaneStatusTx is called before the first event append. Concurrent
// reports serialize on the projection row; exact retries never reach it.
func recordLaneStatusTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, session *string, status *LaneStatus, messageID string, at time.Time) error {
	if status == nil {
		return nil
	}
	if at.IsZero() {
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&at); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO inbox_lane_statuses(tenant_id,session_id,target_node_id,sender_principal_id,message_id,reported_at)
 VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6)
 ON CONFLICT (tenant_id,session_id,target_node_id) DO UPDATE SET message_id=EXCLUDED.message_id,reported_at=EXCLUDED.reported_at`, p.TenantID, session, status.TargetNodeID, p.ID, messageID, at)
	return err
}

func lanePayload(status *LaneStatus) []byte {
	if status == nil {
		return nil
	}
	raw, _ := json.Marshal(status)
	return raw
}

// Lane exposes published status only, never ordinary message content.
type Lane struct {
	SessionID         string      `json:"session_id"`
	TargetNodeID      string      `json:"target_node_id"`
	SenderPrincipalID string      `json:"sender_principal_id"`
	Label             string      `json:"label"`
	MessageID         *string     `json:"message_id"`
	Status            *LaneStatus `json:"status"`
	Line              string      `json:"line"`
	ReportedAt        *time.Time  `json:"reported_at"`
	AgeSeconds        *int64      `json:"age_seconds"`
	SilentSeconds     int64       `json:"silent_seconds"`
}

type LanePage struct {
	Items     []Lane    `json:"items"`
	NextAfter string    `json:"next_after"`
	AsOf      time.Time `json:"as_of"`
}

type laneQuery struct {
	target, session, afterSession, afterTarget string
	limit, silentMinutes                       int
}

func (m *module) handleLanes(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	q := laneQuery{limit: 50, afterSession: "00000000-0000-0000-0000-000000000000", afterTarget: "00000000-0000-0000-0000-000000000000"}
	values := r.URL.Query()
	for _, field := range []struct {
		key string
		dst *string
	}{{"target", &q.target}, {"session", &q.session}} {
		if values.Has(field.key) {
			id, valid := parseUUID(values.Get(field.key))
			if !valid {
				failure(w, badRequest("invalid "+field.key))
				return
			}
			*field.dst = id
		}
	}
	if after := values.Get("after"); values.Has("after") {
		if len(after) != 73 {
			failure(w, badRequest("invalid after"))
			return
		}
		parts := strings.Split(after, "/")
		if len(parts) != 2 {
			failure(w, badRequest("invalid after"))
			return
		}
		var a, b bool
		q.afterSession, a = parseUUID(parts[0])
		q.afterTarget, b = parseUUID(parts[1])
		if !a || !b {
			failure(w, badRequest("invalid after"))
			return
		}
	}
	for _, field := range []struct {
		key string
		dst *int
		max int
	}{{"limit", &q.limit, 100}, {"silent_minutes", &q.silentMinutes, 525600}} {
		if values.Has(field.key) {
			v, err := parseNonNeg(values.Get(field.key))
			if err != nil || v < 1 || v > int64(field.max) {
				failure(w, badRequest("invalid "+field.key))
				return
			}
			*field.dst = int(v)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out := LanePage{Items: []Lane{}}
	err := db.InTenant(tenant.WithPrincipal(ctx, p), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "inbox.read", authz.Scope{AnyProject: true}); err != nil {
			return errForbidden
		}
		var err error
		out.AsOf, err = m.laneNow(ctx, tx)
		if err != nil {
			return err
		}
		out.Items, out.NextAfter, err = readLanesTx(ctx, tx, p, q, out.AsOf)
		return err
	})
	if err != nil {
		failure(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (m *module) laneNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	if m.laneClock != nil {
		return m.laneClock(ctx, tx)
	}
	var now time.Time
	err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

func readLanesTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, q laneQuery, now time.Time) ([]Lane, string, error) {
	// Explicit permission filtering precedes pagination. RLS on sessions and
	// target nodes still applies, including node-scoped visibility restrictions.
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, "", err
	}
	// Enumerate only visible project scopes, then bind permitted UUIDs. A
	// workspace binding is evaluated separately for workspace-level goals.
	rows, err := tx.Query(ctx, `SELECT DISTINCT project_id::text FROM harness_sessions WHERE stopped_at IS NULL AND archived_at IS NULL LIMIT 1001`)
	if err != nil {
		return nil, "", err
	}
	projects := []string{}
	count := 0
	for rows.Next() {
		var project string
		if err := rows.Scan(&project); err != nil {
			rows.Close()
			return nil, "", err
		}
		count++
		if check("harness.read", project) && check("inbox.read", project) && check("nodes.read", project) {
			projects = append(projects, project)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, "", err
	}
	if count > 1000 {
		return nil, "", &httpError{413, "too_many_projects", "lane query exceeds 1000 visible active projects"}
	}
	rows, err = tx.Query(ctx, `WITH candidates AS (
 SELECT l.session_id,l.target_node_id FROM inbox_lane_statuses l
 UNION
 SELECT s.id,s.ticket_node_id FROM harness_sessions s WHERE s.ticket_node_id IS NOT NULL
 )
 SELECT s.id::text,n.id::text,s.agent_principal_id::text,coalesce(s.display_label,p.name),
 l.message_id::text,i.lane_status,l.reported_at,s.created_at
 FROM candidates c
 JOIN harness_sessions s ON s.id=c.session_id
 JOIN nodes n ON n.id=c.target_node_id AND n.deleted_at IS NULL
 JOIN principals p ON p.id=s.agent_principal_id
 LEFT JOIN inbox_lane_statuses l ON l.session_id=c.session_id AND l.target_node_id=c.target_node_id
 LEFT JOIN inbox_messages i ON i.id=l.message_id AND i.content_mode='durable' AND i.lane_status IS NOT NULL
 WHERE s.stopped_at IS NULL AND s.archived_at IS NULL AND s.project_id=ANY($1::uuid[])
 AND (n.project_id=ANY($1::uuid[]) OR n.project_id IS NULL AND $2)
 AND ($3='' OR n.id::text=$3) AND ($4='' OR s.id::text=$4)
 AND (s.id,n.id)>($5::uuid,$6::uuid)
 AND ($7=0 OR coalesce(l.reported_at,s.created_at)<$8::timestamptz-make_interval(mins=>$7))
 ORDER BY s.id,n.id LIMIT $9`, projects, check("nodes.read", ""), q.target, q.session, q.afterSession, q.afterTarget, q.silentMinutes, now, q.limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Lane{}
	for rows.Next() {
		var lane Lane
		var raw []byte
		var started time.Time
		if err := rows.Scan(&lane.SessionID, &lane.TargetNodeID, &lane.SenderPrincipalID, &lane.Label, &lane.MessageID, &raw, &lane.ReportedAt, &started); err != nil {
			return nil, "", err
		}
		if raw != nil {
			if err := json.Unmarshal(raw, &lane.Status); err != nil {
				return nil, "", err
			}
			lane.Line = lane.Status.Line()
		}
		if lane.ReportedAt != nil {
			started = *lane.ReportedAt
			age := max(int64(0), int64(now.Sub(started)/time.Second))
			lane.AgeSeconds = &age
		}
		lane.SilentSeconds = max(int64(0), int64(now.Sub(started)/time.Second))
		items = append(items, lane)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > q.limit {
		items = items[:q.limit]
		last := items[len(items)-1]
		next = last.SessionID + "/" + last.TargetNodeID
	}
	return items, next, nil
}
