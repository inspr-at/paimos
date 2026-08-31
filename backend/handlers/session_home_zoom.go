// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const sessionHomeZoomCompositionCTE = `
WITH scope(project_id,freshness_at) AS (VALUES(?,?)),
project_session_rows AS (
 SELECT ps.product_session_id,ps.target_kind,ps.target_project_agent_id,
        pa.id AS owned_agent_id,pa.name AS agent_name,ps.node_id,i.id AS owned_node_id,
        COALESCE(p.key||'-'||i.issue_number,'') AS node_key,COALESCE(i.title,'') AS node_title,
        ps.title,ps.summary,ps.revision,ps.updated_at
 FROM scope
 JOIN projects p ON p.id=scope.project_id
 JOIN product_sessions ps ON ps.project_id=p.id
 LEFT JOIN project_agents pa ON pa.id=ps.target_project_agent_id AND pa.project_id=p.id
 LEFT JOIN issues i ON i.id=ps.node_id AND i.project_id=p.id AND i.deleted_at IS NULL
),
target_ids AS (
 SELECT DISTINCT target_project_agent_id AS agent_id FROM project_session_rows
 WHERE target_kind='project_agent' AND owned_agent_id IS NOT NULL
),
target_metrics AS (
 SELECT target.agent_id,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=1
          AND message.is_action_request=0 AND message.read_at IS NULL THEN message.id END) AS unread_count,
        strftime('%Y-%m-%dT%H:%M:%fZ',MAX(CASE WHEN sender.id IS NOT NULL AND message.delivered=1
          AND message.is_action_request=0 AND message.read_at IS NULL THEN message.created_at END)) AS latest_unread_at,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=0 THEN message.id END) AS exception_count,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=0
          AND message.is_action_request=1 THEN message.id END) AS action_request_count
 FROM target_ids target
 JOIN scope
 JOIN project_agents receiver ON receiver.id=target.agent_id AND receiver.project_id=scope.project_id
 LEFT JOIN agent_messages message ON message.to_agent_id=receiver.id
 LEFT JOIN project_agents sender ON sender.id=message.from_agent_id AND sender.project_id=scope.project_id
 GROUP BY target.agent_id
),
active_harnesses AS (
 SELECT hs.project_id,hs.project_agent_id,hs.agent_name,hs.harness,hs.management_mode,hs.phase,
        hs.advertised_inbox,hs.advertised_status,hs.advertised_steer,hs.advertised_interrupt,hs.advertised_stop,
        CASE WHEN hs.phase='starting' THEN julianday(hs.updated_at)>=julianday(scope.freshness_at,'-90 seconds')
             ELSE hs.heartbeat_at IS NOT NULL AND julianday(hs.heartbeat_at)>=julianday(scope.freshness_at,'-90 seconds') END AS fresh,
        COUNT(*) OVER(PARTITION BY hs.project_id,hs.project_agent_id) AS candidate_count,
        ROW_NUMBER() OVER(PARTITION BY hs.project_id,hs.project_agent_id ORDER BY hs.id) AS candidate_rank
 FROM harness_sessions hs
 JOIN scope ON scope.project_id=hs.project_id
 JOIN target_ids target ON target.agent_id=hs.project_agent_id
 WHERE hs.phase<>'stopped'
),
composed AS (
 SELECT base.*,
        COALESCE(metric.unread_count,0) AS unread_count,metric.latest_unread_at,
        COALESCE(metric.exception_count,0) AS exception_count,
        COALESCE(metric.action_request_count,0) AS action_request_count,
        COALESCE(harness.candidate_count,0) AS candidate_count,
        harness.project_id AS harness_project_id,harness.project_agent_id AS harness_agent_id,
        harness.agent_name AS harness_agent_name,harness.harness,harness.management_mode,harness.phase,
        harness.advertised_inbox,harness.advertised_status,harness.advertised_steer,
        harness.advertised_interrupt,harness.advertised_stop,harness.fresh
 FROM project_session_rows base
 LEFT JOIN target_metrics metric ON metric.agent_id=base.target_project_agent_id
 LEFT JOIN active_harnesses harness ON harness.project_agent_id=base.target_project_agent_id
                                      AND harness.candidate_rank=1
)`

const sessionHomeZoomComposedColumns = `
 product_session_id,target_kind,target_project_agent_id,owned_agent_id,agent_name,
 node_id,owned_node_id,node_key,node_title,title,summary,revision,updated_at,
 unread_count,latest_unread_at,exception_count,action_request_count,candidate_count,
 harness_project_id,harness_agent_id,harness_agent_name,harness,management_mode,phase,
 advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,fresh`

const sessionHomeZoomSampleSQL = sessionHomeZoomCompositionCTE + `,
ranked AS (
 SELECT composed.*,
        ROW_NUMBER() OVER(PARTITION BY target_project_agent_id
                          ORDER BY updated_at DESC,product_session_id ASC) AS target_session_rank
 FROM composed
)
SELECT ` + sessionHomeZoomComposedColumns + ` FROM ranked
ORDER BY
 CASE WHEN target_kind='project_agent' AND exception_count>0 AND target_session_rank=1 THEN 0 ELSE 1 END ASC,
 CASE WHEN target_kind='project_agent' AND exception_count>0 AND target_session_rank=1
      THEN CASE WHEN action_request_count>0 THEN 1 ELSE 0 END END DESC,
 CASE WHEN target_kind='project_agent' AND exception_count>0 AND target_session_rank=1
      THEN exception_count END DESC,
 CASE WHEN target_kind='project_agent' AND exception_count>0 AND target_session_rank=1
      THEN unread_count END DESC,
 CASE WHEN NOT(target_kind='project_agent' AND exception_count>0 AND target_session_rank=1)
      THEN CASE WHEN exception_count>0 THEN 1 ELSE 0 END END DESC,
 CASE WHEN NOT(target_kind='project_agent' AND exception_count>0 AND target_session_rank=1)
      THEN unread_count END DESC,
 updated_at DESC,product_session_id ASC
LIMIT ?`

const sessionHomeZoomSelectedSQL = sessionHomeZoomCompositionCTE + `
SELECT ` + sessionHomeZoomComposedColumns + ` FROM composed WHERE product_session_id=?`

const sessionHomeZoomTotalsSQL = `
WITH scope(project_id) AS (VALUES(?)),
project_sessions AS (
 SELECT ps.product_session_id,ps.target_kind,ps.target_project_agent_id
 FROM product_sessions ps JOIN scope ON scope.project_id=ps.project_id
),
target_ids AS (
 SELECT DISTINCT ps.target_project_agent_id AS agent_id
 FROM project_sessions ps
 JOIN project_agents agent ON agent.id=ps.target_project_agent_id
 JOIN scope ON scope.project_id=agent.project_id
 WHERE ps.target_kind='project_agent'
),
target_metrics AS (
 SELECT target.agent_id,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=1
          AND message.is_action_request=0 AND message.read_at IS NULL THEN message.id END) AS unread_count,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=0 THEN message.id END) AS exception_count,
        COUNT(DISTINCT CASE WHEN sender.id IS NOT NULL AND message.delivered=0
          AND message.is_action_request=1 THEN message.id END) AS action_request_count
 FROM target_ids target
 JOIN scope
 JOIN project_agents receiver ON receiver.id=target.agent_id AND receiver.project_id=scope.project_id
 LEFT JOIN agent_messages message ON message.to_agent_id=receiver.id
 LEFT JOIN project_agents sender ON sender.id=message.from_agent_id AND sender.project_id=scope.project_id
 GROUP BY target.agent_id
)
SELECT
 (SELECT COUNT(*) FROM project_sessions),
 COALESCE((SELECT SUM(unread_count) FROM target_metrics),0),
 (SELECT COUNT(*) FROM project_sessions session JOIN target_metrics metric
   ON metric.agent_id=session.target_project_agent_id WHERE metric.exception_count>0),
 COALESCE((SELECT SUM(exception_count) FROM target_metrics),0),
 COALESCE((SELECT SUM(action_request_count) FROM target_metrics),0),
 (SELECT COUNT(*) FROM target_metrics WHERE exception_count>0)`

type sessionHomeZoomInput struct {
	Zoom              string
	Band              string
	SampleLimit       int
	SelectedSessionID string
}

// sessionHomeZoomAuthorizationHookKey is package-private synchronization for
// proving that authorization and projection reads share one database snapshot.
type sessionHomeZoomAuthorizationHookKey struct{}

func parseSessionHomeZoomQuery(rawQuery string) (sessionHomeZoomInput, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return sessionHomeZoomInput{}, err
	}
	for key, entries := range values {
		if (key != "zoom" && key != "selected_session_id") || len(entries) != 1 {
			return sessionHomeZoomInput{}, errors.New("invalid query shape")
		}
	}
	zoom := "10"
	if entries, present := values["zoom"]; present {
		zoom = entries[0]
	}
	if len(zoom) == 0 || len(zoom) > 64 || zoom[0] < '1' || zoom[0] > '9' {
		return sessionHomeZoomInput{}, errors.New("invalid zoom")
	}
	for index := 1; index < len(zoom); index++ {
		if zoom[index] < '0' || zoom[index] > '9' {
			return sessionHomeZoomInput{}, errors.New("invalid zoom")
		}
	}
	input := sessionHomeZoomInput{Zoom: zoom, Band: "far", SampleLimit: 100}
	switch len(zoom) {
	case 1:
		input.Band = "detail"
		input.SampleLimit = int(zoom[0] - '0')
	case 2:
		input.Band = "overview"
		input.SampleLimit = int(zoom[0]-'0')*10 + int(zoom[1]-'0')
	case 3:
		input.Band = "aggregate"
	}
	if entries, present := values["selected_session_id"]; present {
		selected := entries[0]
		parsed, parseErr := uuid.Parse(selected)
		if parseErr != nil || len(selected) != 36 || parsed.String() != selected {
			return sessionHomeZoomInput{}, errors.New("invalid selected session")
		}
		input.SelectedSessionID = selected
	}
	return input, nil
}

// SessionHomeZoomV1 returns a bounded semantic-zoom projection without
// widening the frozen session-home/v1 or Agent Mode contracts.
func SessionHomeZoomV1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	input, err := parseSessionHomeZoomQuery(r.URL.RawQuery)
	if err != nil {
		jsonError(w, "invalid session home zoom query", http.StatusBadRequest)
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		sessionHomeZoomProblem(w, err)
		return
	}
	defer tx.Rollback()

	requestNow := time.Now().UTC()
	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, requestNow)
	if err != nil || user == nil || !sessionHomeProjectViewTx(r.Context(), tx, user, projectID) {
		sessionHomeZoomNotFound(w)
		return
	}
	if hook, ok := r.Context().Value(sessionHomeZoomAuthorizationHookKey{}).(func()); ok {
		hook()
	}

	snapshot := models.SessionHomeZoomSnapshot{
		SchemaVersion: 1, ProjectID: projectID, Zoom: input.Zoom, Band: input.Band,
		SampleLimit: input.SampleLimit, Sessions: []models.SessionHomeSession{},
	}
	if err := tx.QueryRowContext(r.Context(), sessionHomeZoomTotalsSQL, projectID).Scan(
		&snapshot.Totals.Sessions, &snapshot.Totals.Unread, &snapshot.Totals.AttentionSessions,
		&snapshot.Totals.ExceptionMessages, &snapshot.Totals.ActionRequests,
		&snapshot.Totals.ExceptionTargets); err != nil {
		sessionHomeZoomProblem(w, err)
		return
	}

	freshnessAt := requestNow.Format("2006-01-02T15:04:05.000Z")
	rows, err := tx.QueryContext(r.Context(), sessionHomeZoomSampleSQL, projectID, freshnessAt, input.SampleLimit)
	if err != nil {
		sessionHomeZoomProblem(w, err)
		return
	}
	for rows.Next() {
		item, scanErr := scanSessionHomeZoomRow(rows, projectID)
		if scanErr != nil {
			rows.Close()
			sessionHomeZoomProblem(w, scanErr)
			return
		}
		snapshot.Sessions = append(snapshot.Sessions, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		sessionHomeZoomProblem(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		sessionHomeZoomProblem(w, err)
		return
	}

	exceptionTargets := map[int64]bool{}
	for index := range snapshot.Sessions {
		item := snapshot.Sessions[index]
		if item.Attention.Required && item.Target.ProjectAgentID != nil {
			exceptionTargets[*item.Target.ProjectAgentID] = true
		}
		if input.SelectedSessionID != "" && item.ProductSessionID == input.SelectedSessionID {
			selected := item
			snapshot.SelectedSession = &selected
		}
	}
	snapshot.Totals.SampledExceptionTargets = int64(len(exceptionTargets))
	snapshot.SampleTruncated = snapshot.Totals.Sessions > int64(len(snapshot.Sessions))

	if input.SelectedSessionID != "" && snapshot.SelectedSession == nil {
		selected, selectedErr := scanSessionHomeZoomRow(
			tx.QueryRowContext(r.Context(), sessionHomeZoomSelectedSQL, projectID, freshnessAt, input.SelectedSessionID), projectID)
		if errors.Is(selectedErr, sql.ErrNoRows) {
			sessionHomeZoomNotFound(w)
			return
		}
		if selectedErr != nil {
			sessionHomeZoomProblem(w, selectedErr)
			return
		}
		snapshot.SelectedSession = &selected
	}
	if err := tx.Commit(); err != nil {
		sessionHomeZoomProblem(w, err)
		return
	}
	jsonOK(w, snapshot)
}

func scanSessionHomeZoomRow(row interface{ Scan(...any) error }, projectID int64) (models.SessionHomeSession, error) {
	var item models.SessionHomeSession
	var targetAgentID, ownedAgentID, nodeID, ownedNodeID sql.NullInt64
	var agentName sql.NullString
	var nodeKey, nodeTitle string
	var latestUnread sql.NullString
	var candidateCount int64
	var candidateProjectID, candidateAgentID sql.NullInt64
	var candidateAgentName, harness, managementMode, phase sql.NullString
	var inbox, status, steer, interrupt, stop, fresh sql.NullInt64
	err := row.Scan(&item.ProductSessionID, &item.Target.Kind, &targetAgentID, &ownedAgentID, &agentName,
		&nodeID, &ownedNodeID, &nodeKey, &nodeTitle, &item.Title, &item.Summary, &item.Revision, &item.UpdatedAt,
		&item.Inbox.UnreadCount, &latestUnread, &item.Attention.ExceptionCount, &item.Attention.ActionRequestCount,
		&candidateCount, &candidateProjectID, &candidateAgentID, &candidateAgentName, &harness, &managementMode,
		&phase, &inbox, &status, &steer, &interrupt, &stop, &fresh)
	if err != nil {
		return item, err
	}
	item.Controls = models.SessionHomeControls{Steer: "paimos_nudge"}
	item.Status = models.SessionHomeStatus{Phase: "unavailable", Reason: "no_active_harness"}
	if targetAgentID.Valid {
		value := targetAgentID.Int64
		item.Target.ProjectAgentID = &value
	}
	if ownedAgentID.Valid && agentName.Valid {
		value := agentName.String
		item.Target.AgentName = &value
	}
	if nodeID.Valid && ownedNodeID.Valid {
		item.Node = &models.ProductSessionNode{NodeID: ownedNodeID.Int64, NodeKey: nodeKey, Label: nodeKey + " · " + nodeTitle}
	}
	if latestUnread.Valid {
		value := latestUnread.String
		item.Inbox.LatestUnreadAt = &value
	}
	item.Attention.Required = item.Attention.ExceptionCount > 0
	if item.Attention.Required {
		reason := "sender_not_allowed"
		if item.Attention.ActionRequestCount > 0 {
			reason = "action_request"
		}
		item.Attention.Reason = &reason
	}
	if item.Target.Kind == "paimos" {
		setPaimosTarget(&item)
		return item, nil
	}
	if item.Target.ProjectAgentID == nil || item.Target.AgentName == nil {
		setUnavailableTarget(&item, "ownership_mismatch")
		return item, nil
	}
	if candidateCount == 0 {
		setUnavailableTarget(&item, "no_active_harness")
		return item, nil
	}
	if candidateCount != 1 {
		setUnavailableTarget(&item, "ambiguous_harness")
		return item, nil
	}
	if !candidateProjectID.Valid || !candidateAgentID.Valid || !candidateAgentName.Valid ||
		candidateProjectID.Int64 != projectID || candidateAgentID.Int64 != *item.Target.ProjectAgentID ||
		candidateAgentName.String != *item.Target.AgentName {
		setUnavailableTarget(&item, "ownership_mismatch")
		return item, nil
	}
	if !fresh.Valid || fresh.Int64 != 1 {
		setUnavailableTarget(&item, "stale_harness")
		return item, nil
	}
	caps := models.HarnessCapabilities{Inbox: inbox.Int64 == 1, Status: status.Int64 == 1, Steer: steer.Int64 == 1, Interrupt: interrupt.Int64 == 1, Stop: stop.Int64 == 1}
	if managementMode.String != managedharness.ManagementManaged {
		caps.Interrupt = false
		caps.Stop = false
	}
	address := harness.String + ":" + *item.Target.AgentName
	item.Target.Address = &address
	item.Status = models.SessionHomeStatus{Phase: phase.String, Reason: "active"}
	item.Harness = &models.SessionHomeHarness{Harness: harness.String, ManagementMode: managementMode.String, Capabilities: caps}
	item.Controls = models.SessionHomeControls{Steer: "paimos_nudge", Interrupt: caps.Interrupt, Stop: caps.Stop}
	if caps.Steer {
		item.Controls.Steer = "direct"
	}
	return item, nil
}

func sessionHomeZoomNotFound(w http.ResponseWriter) {
	http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
}

func sessionHomeZoomProblem(w http.ResponseWriter, err error) {
	if !errors.Is(err, context.Canceled) {
		log.Printf("session home zoom v1: %v", err)
	}
	jsonError(w, "session home zoom unavailable", http.StatusInternalServerError)
}
