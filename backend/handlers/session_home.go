// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/managedharness"
	"github.com/inspr-at/paimos/backend/models"
)

const sessionHomeSchemaVersion = 1

type sessionHomeHarnessCandidate struct {
	ProjectID      int64
	ProjectAgentID int64
	AgentName      string
	Harness        string
	ManagementMode string
	Phase          string
	Fresh          bool
	Capabilities   models.HarnessCapabilities
}

// SessionHomeV1 projects durable product sessions without widening or
// relabelling the frozen Agent Mode delivery contract.
func SessionHomeV1(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	projectID, ok := productSessionProjectID(w, r)
	if !ok {
		return
	}
	tx, err := db.DB.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		sessionHomeProblem(w, err)
		return
	}
	defer tx.Rollback()

	user, _, err := auth.ReauthorizeRequestPrincipalTx(r.Context(), tx, r, time.Now().UTC())
	if err != nil || user == nil || !sessionHomeProjectViewTx(r.Context(), tx, user, projectID) {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}

	rows, err := tx.QueryContext(r.Context(), `
		SELECT ps.product_session_id,ps.target_kind,ps.target_project_agent_id,
		       pa.id,pa.name,ps.node_id,i.id,COALESCE(p.key||'-'||i.issue_number,''),COALESCE(i.title,''),
		       ps.title,ps.summary,ps.revision,ps.updated_at
		FROM projects p
		JOIN product_sessions ps ON ps.project_id=p.id
		LEFT JOIN project_agents pa ON pa.id=ps.target_project_agent_id AND pa.project_id=p.id
		LEFT JOIN issues i ON i.id=ps.node_id AND i.project_id=p.id AND i.deleted_at IS NULL
		WHERE p.id=? AND ps.project_id=?
		ORDER BY ps.updated_at DESC,ps.product_session_id ASC`, projectID, projectID)
	if err != nil {
		sessionHomeProblem(w, err)
		return
	}
	defer rows.Close()

	type baseRow struct {
		item        models.SessionHomeSession
		targetOwned bool
	}
	baseRows := []baseRow{}
	for rows.Next() {
		item, targetOwned, err := scanSessionHomeBase(rows)
		if err != nil {
			sessionHomeProblem(w, err)
			return
		}
		baseRows = append(baseRows, baseRow{item: item, targetOwned: targetOwned})
	}
	if err := rows.Err(); err != nil {
		sessionHomeProblem(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		sessionHomeProblem(w, err)
		return
	}

	snapshot := models.SessionHomeSnapshot{SchemaVersion: sessionHomeSchemaVersion, ProjectID: projectID, Sessions: []models.SessionHomeSession{}}
	countedInboxes := map[int64]bool{}
	agentCompositions := map[int64]models.SessionHomeSession{}
	for _, base := range baseRows {
		item := base.item
		if item.Target.Kind == "paimos" {
			setPaimosTarget(&item)
		} else if !base.targetOwned {
			setUnavailableTarget(&item, "ownership_mismatch")
		} else {
			agentID := *item.Target.ProjectAgentID
			if composed, ok := agentCompositions[agentID]; ok {
				item.Target.Address = composed.Target.Address
				item.Status = composed.Status
				item.Harness = composed.Harness
				item.Controls = composed.Controls
				item.Inbox = composed.Inbox
				item.Attention = composed.Attention
			} else {
				if err := composeSessionHomeAgent(r.Context(), tx, projectID, &item); err != nil {
					sessionHomeProblem(w, err)
					return
				}
				agentCompositions[agentID] = item
			}
		}
		if item.Target.ProjectAgentID != nil && !countedInboxes[*item.Target.ProjectAgentID] {
			countedInboxes[*item.Target.ProjectAgentID] = true
			snapshot.Totals.Unread += item.Inbox.UnreadCount
		}
		if item.Attention.Required {
			snapshot.Totals.Attention++
		}
		snapshot.Sessions = append(snapshot.Sessions, item)
	}
	snapshot.Totals.Sessions = len(snapshot.Sessions)
	if err := tx.Commit(); err != nil {
		sessionHomeProblem(w, err)
		return
	}
	jsonOK(w, snapshot)
}

func sessionHomeProjectViewTx(ctx context.Context, tx *sql.Tx, user *models.User, projectID int64) bool {
	var allowed int
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM projects p WHERE p.id=? AND (
			? IN ('admin','super_admin') OR
			EXISTS(SELECT 1 FROM project_members pm WHERE pm.user_id=? AND pm.project_id=p.id AND pm.access_level IN ('viewer','editor')) OR
			(?='member' AND NOT EXISTS(SELECT 1 FROM project_members pm WHERE pm.user_id=? AND pm.project_id=p.id))
		))`, projectID, user.Role, user.ID, user.Role, user.ID).Scan(&allowed)
	return err == nil && allowed == 1
}

func scanSessionHomeBase(row interface{ Scan(...any) error }) (models.SessionHomeSession, bool, error) {
	var item models.SessionHomeSession
	var targetAgentID, ownedAgentID, nodeID, ownedNodeID sql.NullInt64
	var agentName sql.NullString
	var nodeKey, nodeTitle string
	err := row.Scan(&item.ProductSessionID, &item.Target.Kind, &targetAgentID,
		&ownedAgentID, &agentName, &nodeID, &ownedNodeID, &nodeKey, &nodeTitle,
		&item.Title, &item.Summary, &item.Revision, &item.UpdatedAt)
	if err != nil {
		return item, false, err
	}
	item.Controls = models.SessionHomeControls{Steer: "paimos_nudge"}
	item.Status = models.SessionHomeStatus{Phase: "unavailable", Reason: "no_active_harness"}
	if targetAgentID.Valid {
		id := targetAgentID.Int64
		item.Target.ProjectAgentID = &id
	}
	if ownedAgentID.Valid && agentName.Valid {
		name := agentName.String
		item.Target.AgentName = &name
	}
	if nodeID.Valid && ownedNodeID.Valid {
		item.Node = &models.ProductSessionNode{NodeID: ownedNodeID.Int64, NodeKey: nodeKey, Label: nodeKey + " · " + nodeTitle}
	}
	return item, item.Target.ProjectAgentID != nil && item.Target.AgentName != nil, nil
}

func setPaimosTarget(item *models.SessionHomeSession) {
	address := "paimos"
	item.Target = models.SessionHomeTarget{Kind: "paimos", Address: &address}
	item.Status = models.SessionHomeStatus{Phase: "paimos", Reason: "paimos_target"}
	item.Harness = nil
	item.Controls = models.SessionHomeControls{Steer: "paimos_nudge"}
}

func setUnavailableTarget(item *models.SessionHomeSession, reason string) {
	item.Target.Address = nil
	item.Status = models.SessionHomeStatus{Phase: "unavailable", Reason: reason}
	item.Harness = nil
	item.Controls = models.SessionHomeControls{Steer: "paimos_nudge"}
}

func composeSessionHomeAgent(ctx context.Context, tx *sql.Tx, projectID int64, item *models.SessionHomeSession) error {
	agentID := *item.Target.ProjectAgentID
	agentName := *item.Target.AgentName
	candidates, err := loadSessionHomeHarnessCandidates(ctx, tx, projectID, agentID)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		setUnavailableTarget(item, "no_active_harness")
	} else if len(candidates) != 1 {
		setUnavailableTarget(item, "ambiguous_harness")
	} else {
		candidate := candidates[0]
		if candidate.ProjectID != projectID || candidate.ProjectAgentID != agentID || candidate.AgentName != agentName {
			setUnavailableTarget(item, "ownership_mismatch")
		} else if !candidate.Fresh {
			setUnavailableTarget(item, "stale_harness")
		} else {
			caps := candidate.Capabilities
			if candidate.ManagementMode != managedharness.ManagementManaged {
				caps.Interrupt = false
				caps.Stop = false
			}
			address := candidate.Harness + ":" + agentName
			item.Target.Address = &address
			item.Status = models.SessionHomeStatus{Phase: candidate.Phase, Reason: "active"}
			item.Harness = &models.SessionHomeHarness{Harness: candidate.Harness, ManagementMode: candidate.ManagementMode, Capabilities: caps}
			item.Controls = models.SessionHomeControls{Steer: "paimos_nudge", Interrupt: caps.Interrupt, Stop: caps.Stop}
			if caps.Steer {
				item.Controls.Steer = "direct"
			}
		}
	}
	return loadSessionHomeInbox(ctx, tx, projectID, agentID, item)
}

func loadSessionHomeHarnessCandidates(ctx context.Context, tx *sql.Tx, projectID, agentID int64) ([]sessionHomeHarnessCandidate, error) {
	rows, err := tx.QueryContext(ctx, `SELECT project_id,project_agent_id,agent_name,harness,management_mode,phase,
		advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,
		CASE WHEN phase='starting' THEN julianday(updated_at)>=julianday('now','-90 seconds')
		     ELSE heartbeat_at IS NOT NULL AND julianday(heartbeat_at)>=julianday('now','-90 seconds') END
		FROM harness_sessions WHERE project_id=? AND project_agent_id=? AND phase<>'stopped'
		ORDER BY id`, projectID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sessionHomeHarnessCandidate{}
	for rows.Next() {
		var candidate sessionHomeHarnessCandidate
		var inbox, status, steer, interrupt, stop int
		if err := rows.Scan(&candidate.ProjectID, &candidate.ProjectAgentID, &candidate.AgentName, &candidate.Harness,
			&candidate.ManagementMode, &candidate.Phase, &inbox, &status, &steer, &interrupt, &stop, &candidate.Fresh); err != nil {
			return nil, err
		}
		candidate.Capabilities = models.HarnessCapabilities{Inbox: inbox == 1, Status: status == 1, Steer: steer == 1, Interrupt: interrupt == 1, Stop: stop == 1}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func loadSessionHomeInbox(ctx context.Context, tx *sql.Tx, projectID, agentID int64, item *models.SessionHomeSession) error {
	var latest sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),strftime('%Y-%m-%dT%H:%M:%fZ',MAX(am.created_at))
		FROM agent_messages am
		JOIN project_agents receiver ON receiver.id=am.to_agent_id AND receiver.project_id=?
		WHERE am.to_agent_id=? AND am.delivered=1 AND am.is_action_request=0 AND am.read_at IS NULL`,
		projectID, agentID).Scan(&item.Inbox.UnreadCount, &latest); err != nil {
		return err
	}
	if latest.Valid {
		value := latest.String
		item.Inbox.LatestUnreadAt = &value
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN am.is_action_request=1 THEN 1 ELSE 0 END),0)
		FROM agent_messages am
		JOIN project_agents receiver ON receiver.id=am.to_agent_id AND receiver.project_id=?
		WHERE am.to_agent_id=? AND am.delivered=0`, projectID, agentID).
		Scan(&item.Attention.ExceptionCount, &item.Attention.ActionRequestCount); err != nil {
		return err
	}
	item.Attention.Required = item.Attention.ExceptionCount > 0
	if item.Attention.Required {
		reason := "sender_not_allowed"
		if item.Attention.ActionRequestCount > 0 {
			reason = "action_request"
		}
		item.Attention.Reason = &reason
	}
	return nil
}

func sessionHomeProblem(w http.ResponseWriter, err error) {
	if !errors.Is(err, context.Canceled) {
		log.Printf("session home v1: %v", err)
	}
	jsonError(w, "session home unavailable", http.StatusInternalServerError)
}
