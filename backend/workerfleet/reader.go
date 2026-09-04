// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package workerfleet

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/managedharness"
)

type TrustFact struct {
	ProgressTrusted bool
	ETATrusted      bool
	Reason          string
	TrustRevision   string
	ObservedAt      *time.Time
	Progress        *int
	ETA             *time.Time
}

type TrustLoader func(context.Context, *sql.Tx, []int64, time.Time) (map[int64]TrustFact, error)

type ReaderOptions struct {
	Clock     func() time.Time
	LoadTrust TrustLoader
}

type Reader struct {
	db        *sql.DB
	clock     func() time.Time
	loadTrust TrustLoader
}

func NewReader(database *sql.DB, options ReaderOptions) *Reader {
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Reader{db: database, clock: clock, loadTrust: options.LoadTrust}
}

func ParseZoom(raw string) (string, string, int, error) {
	if raw == "" {
		raw = "10"
	}
	if len(raw) > 64 || raw[0] < '1' || raw[0] > '9' {
		return "", "", 0, ErrInvalid
	}
	for index := 1; index < len(raw); index++ {
		if raw[index] < '0' || raw[index] > '9' {
			return "", "", 0, ErrInvalid
		}
	}
	band, limit := "far", MaxSample
	switch len(raw) {
	case 1:
		band, limit = "detail", int(raw[0]-'0')
	case 2:
		band, limit = "overview", int(raw[0]-'0')*10+int(raw[1]-'0')
	case 3:
		band = "aggregate"
	}
	return raw, band, limit, nil
}

const authorizationSQL = auth.AgentModeAuthorizationCTE + `
SELECT EXISTS(SELECT 1 FROM requester WHERE role<>'external'),
 CASE WHEN ?=0 THEN 1 ELSE EXISTS(SELECT 1 FROM agent_mode_projects WHERE project_id=?) END`

const fleetSampleSQL = auth.AgentModeAuthorizationCTE + `,
fleet_candidates AS (
 SELECT hs.id,hs.project_id,p.key,p.name,hs.project_agent_id,pa.name AS agent_name,
        hs.parent_harness_session_id,hs.ticket_id,
        CASE WHEN i.id IS NULL THEN '' ELSE p.key||'-'||i.issue_number END AS ticket_key,
        COALESCE(i.title,'') AS ticket_title,hs.role,hs.harness,hs.management_mode,hs.phase,
        hs.heartbeat_at,hs.activity_state,hs.activity_reason,hs.activity_event_kind,hs.activity_at,
        hs.closed_reason,hs.revision,hs.created_at,
        hs.advertised_inbox,hs.advertised_status,hs.advertised_steer,
        hs.advertised_interrupt,hs.advertised_stop,
        ROW_NUMBER() OVER(PARTITION BY hs.project_agent_id,CASE WHEN hs.phase='stopped' THEN 1 ELSE 0 END
          ORDER BY hs.created_at DESC,hs.rowid DESC,hs.id DESC) AS generation_rank
 FROM harness_sessions hs
 JOIN agent_mode_projects access ON access.project_id=hs.project_id
 JOIN projects p ON p.id=hs.project_id
 JOIN project_agents pa ON pa.id=hs.project_agent_id AND pa.project_id=hs.project_id
 LEFT JOIN issues i ON i.id=hs.ticket_id AND i.project_id=hs.project_id AND i.deleted_at IS NULL
 WHERE (?=0 OR hs.project_id=?)
),
retained AS (
 SELECT * FROM fleet_candidates WHERE phase<>'stopped' OR generation_rank<=?
),
fleet AS (
 SELECT retained.*,
        SUM(CASE WHEN role='coordinator' AND phase<>'stopped' THEN 1 ELSE 0 END)
          OVER(PARTITION BY project_id) AS active_coordinator_count,
        MAX(CASE WHEN role='coordinator' AND phase<>'stopped' THEN id END)
          OVER(PARTITION BY project_id) AS active_coordinator_id,
        COUNT(*) OVER(PARTITION BY project_id) AS project_worker_count
 FROM retained
),
ranked AS (
 SELECT fleet.*,
  ROW_NUMBER() OVER(PARTITION BY project_id ORDER BY
   CASE role WHEN 'coordinator' THEN 0 ELSE 1 END,
   CASE activity_state WHEN 'busy' THEN 0 WHEN 'idle' THEN 1 WHEN 'unknown' THEN 2 ELSE 3 END,
   created_at DESC,id) AS within_project_rank
 FROM fleet
)
SELECT id,project_id,key,name,project_agent_id,agent_name,parent_harness_session_id,ticket_id,
 ticket_key,ticket_title,role,harness,management_mode,phase,heartbeat_at,activity_state,
 activity_reason,activity_event_kind,activity_at,closed_reason,revision,
 advertised_inbox,advertised_status,advertised_steer,advertised_interrupt,advertised_stop,
 active_coordinator_count,active_coordinator_id,project_worker_count,
 (SELECT COUNT(*) FROM fleet),(SELECT COUNT(DISTINCT project_id) FROM fleet)
FROM ranked ORDER BY within_project_rank,key,project_id,id LIMIT ?`

type fleetRow struct {
	worker                 Worker
	heartbeat              sql.NullString
	activityAt             sql.NullString
	activityState          string
	activityReason         string
	activityKind           string
	closedReason           string
	advertised             Capabilities
	activeCoordinatorCount int64
	activeCoordinatorID    sql.NullString
	projectWorkerCount     int64
}

func (r *Reader) Read(ctx context.Context, request Request) (Snapshot, error) {
	if r == nil || r.db == nil || request.UserID <= 0 || (request.RouteProjectID != nil && *request.RouteProjectID <= 0) {
		return Snapshot{}, ErrInvalid
	}
	zoom, band, sampleLimit, err := ParseZoom(request.Zoom)
	if err != nil {
		return Snapshot{}, err
	}
	routeID := int64(0)
	if request.RouteProjectID != nil {
		routeID = *request.RouteProjectID
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var requesterExists, routeAuthorized int
	if err := tx.QueryRowContext(ctx, authorizationSQL, request.UserID, routeID, routeID).Scan(&requesterExists, &routeAuthorized); err != nil {
		return Snapshot{}, err
	}
	if requesterExists != 1 || routeAuthorized != 1 {
		return Snapshot{}, ErrNotFound
	}
	// The authorization read pins the SQLite snapshot. Capture the projection
	// clock only after that boundary so evidence committed into this snapshot
	// cannot be newer merely because a separate read raced the clock.
	observedAt := r.clock().UTC()
	if observedAt.IsZero() {
		return Snapshot{}, fmt.Errorf("%w: zero clock", ErrInvariant)
	}
	rows, err := tx.QueryContext(ctx, fleetSampleSQL, request.UserID, routeID, routeID, TerminalGenerationsPerAgent, sampleLimit)
	if err != nil {
		return Snapshot{}, err
	}
	fleetRows := []fleetRow{}
	var totalWorkers, totalProjects int64
	for rows.Next() {
		var row fleetRow
		var parent sql.NullString
		var ticketID sql.NullInt64
		var ticketKey, ticketTitle string
		var advertisedInbox, advertisedStatus, advertisedSteer, advertisedInterrupt, advertisedStop int
		if err := rows.Scan(&row.worker.HarnessSessionID, &row.worker.Project.ID, &row.worker.Project.Key,
			&row.worker.Project.Name, &row.worker.Agent.ID, &row.worker.Agent.Name, &parent, &ticketID,
			&ticketKey, &ticketTitle, &row.worker.Role, &row.worker.Harness, &row.worker.ManagementMode,
			&row.worker.Phase, &row.heartbeat, &row.activityState, &row.activityReason, &row.activityKind,
			&row.activityAt, &row.closedReason, &row.worker.Revision, &advertisedInbox, &advertisedStatus,
			&advertisedSteer, &advertisedInterrupt, &advertisedStop, &row.activeCoordinatorCount,
			&row.activeCoordinatorID, &row.projectWorkerCount, &totalWorkers, &totalProjects); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		if parent.Valid {
			value := parent.String
			row.worker.ParentSessionID = &value
		}
		if ticketID.Valid {
			row.worker.Ticket = &Ticket{ID: ticketID.Int64}
			if ticketKey != "" {
				row.worker.Ticket.DetailsAvailable = true
				row.worker.Ticket.Key, row.worker.Ticket.Title = &ticketKey, &ticketTitle
			}
		}
		row.advertised = Capabilities{Inbox: advertisedInbox == 1, Status: advertisedStatus == 1,
			Steer: advertisedSteer == 1, Interrupt: advertisedInterrupt == 1, Stop: advertisedStop == 1}
		row.worker.RecentCommunication = []Communication{}
		fleetRows = append(fleetRows, row)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}
	ticketIDs := make([]int64, 0, len(fleetRows))
	seenTickets := map[int64]bool{}
	for _, row := range fleetRows {
		if row.worker.Ticket != nil && !seenTickets[row.worker.Ticket.ID] {
			seenTickets[row.worker.Ticket.ID] = true
			ticketIDs = append(ticketIDs, row.worker.Ticket.ID)
		}
	}
	trust := map[int64]TrustFact{}
	if r.loadTrust != nil {
		trust, err = r.loadTrust(ctx, tx, ticketIDs, observedAt)
		if err != nil {
			return Snapshot{}, err
		}
	}
	workers := make([]Worker, len(fleetRows))
	sampledIDs := make(map[string]bool, len(fleetRows))
	for _, row := range fleetRows {
		sampledIDs[row.worker.HarnessSessionID] = true
	}
	for index, row := range fleetRows {
		row.worker.ParentInSample = row.worker.ParentSessionID != nil && sampledIDs[*row.worker.ParentSessionID]
		row.worker.Liveness = projectLiveness(row, observedAt)
		row.worker.Capabilities = effectiveCapabilities(row.advertised, row.worker.ManagementMode, row.worker.Phase, row.worker.Liveness)
		row.worker.DeliveryTrust = trustProjection(row.worker.Ticket, trust)
		workers[index] = row.worker
	}
	if err := loadRecentCommunication(ctx, tx, workers); err != nil {
		return Snapshot{}, err
	}
	projects := projectProjection(fleetRows, workers)
	if err := tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	scope := Scope{Kind: "portfolio"}
	if request.RouteProjectID != nil {
		scope = Scope{Kind: "project", ProjectID: request.RouteProjectID}
	}
	snapshot := Snapshot{SchemaVersion: SchemaVersion, ObservedAt: observedAt, Scope: scope, Zoom: zoom,
		Band: band, SampleLimit: sampleLimit, SampleTruncated: totalWorkers > int64(len(workers)),
		Projects: projects, Workers: workers, Provenance: Provenance{Source: "authoritative_database",
			Cache: "none", RemoteCache: false, ProjectionVersion: SchemaVersion,
			TerminalGenerationsPerAgent: TerminalGenerationsPerAgent}}
	snapshot.Totals = Totals{Projects: totalProjects, SampledProjects: len(projects),
		OmittedProjects: totalProjects - int64(len(projects)), Workers: totalWorkers, SampledWorkers: len(workers),
		OmittedWorkers: totalWorkers - int64(len(workers))}
	return snapshot, nil
}

func projectLiveness(row fleetRow, observedAt time.Time) Liveness {
	result := Liveness{State: "unknown", Reason: row.activityReason, ObservedAt: observedAt,
		Source: "unknown"}
	if row.activityState == managedharness.ActivityDead && row.worker.Phase == managedharness.PhaseStopped && row.closedReason != "" {
		result.State, result.Reason, result.ClosedReason = managedharness.ActivityDead, row.activityReason, row.closedReason
		result.Source = terminalSource(row.closedReason)
		return result
	}
	if row.worker.ManagementMode == managedharness.ManagementUnmanaged {
		result.Reason, result.Source = managedharness.ActivityUnmanaged, "unmanaged"
		return result
	}
	heartbeat, err := parseTimestamp(row.heartbeat.String)
	if !row.heartbeat.Valid || err != nil || heartbeat.After(observedAt.Add(5*time.Second)) {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	age := int64(observedAt.Sub(heartbeat) / time.Second)
	if age < 0 {
		age = 0
	}
	result.ReporterAgeSeconds = &age
	if observedAt.Sub(heartbeat) > managedharness.DefaultActivityHeartbeatTimeout {
		result.Reason, result.Source = managedharness.ActivityStale, "agentd_reporter"
		return result
	}
	if row.activityState != managedharness.ActivityBusy && row.activityState != managedharness.ActivityIdle {
		return result
	}
	if !row.activityAt.Valid {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	activityAt, err := parseTimestamp(row.activityAt.String)
	if err != nil || activityAt.After(observedAt.Add(5*time.Second)) {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	if row.activityState == managedharness.ActivityBusy && row.activityReason != managedharness.ActivityAdapter {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	if row.activityState == managedharness.ActivityIdle && row.activityReason != managedharness.ActivityCompleted {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	if (row.activityState == managedharness.ActivityBusy && row.activityKind != "turn_started" && row.activityKind != "tool_started" && row.activityKind != "control_applied") ||
		(row.activityState == managedharness.ActivityIdle && row.activityKind != "turn_completed") {
		result.Reason = managedharness.ActivityMalformed
		return result
	}
	result.State, result.Source = row.activityState, "agentd_reporter"
	return result
}

func parseTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func terminalSource(reason string) string {
	switch reason {
	case managedharness.ClosedStopped, managedharness.ClosedProcessExited, managedharness.ClosedProcessFailed:
		return "agentd_reporter"
	case managedharness.ClosedOwnershipLost:
		return "control_plane"
	default:
		return "unknown"
	}
}

func effectiveCapabilities(advertised Capabilities, mode, phase string, liveness Liveness) Capabilities {
	result := Capabilities{Inbox: advertised.Inbox, Status: advertised.Status}
	if mode != managedharness.ManagementManaged {
		return result
	}
	if liveness.State == managedharness.ActivityDead || phase == managedharness.PhaseStopped {
		return Capabilities{}
	}
	if liveness.State == managedharness.ActivityBusy || liveness.State == managedharness.ActivityIdle {
		result.Steer = advertised.Steer
	}
	result.Interrupt, result.Stop = advertised.Interrupt, advertised.Stop
	return result
}

func trustProjection(ticket *Ticket, facts map[int64]TrustFact) DeliveryTrust {
	if ticket == nil {
		return DeliveryTrust{Reason: "ticket_unbound"}
	}
	fact, ok := facts[ticket.ID]
	if !ok {
		return DeliveryTrust{Reason: "trust_unavailable"}
	}
	progressTrusted := fact.ProgressTrusted && fact.Progress != nil
	etaTrusted := fact.ETATrusted && fact.ETA != nil
	if strings.TrimSpace(fact.TrustRevision) == "" || fact.ObservedAt == nil || fact.ObservedAt.IsZero() {
		reason := strings.TrimSpace(fact.Reason)
		if reason == "" {
			reason = "trust_malformed"
		}
		return DeliveryTrust{Reason: reason}
	}
	if !progressTrusted && !etaTrusted {
		reason := strings.TrimSpace(fact.Reason)
		if reason == "" {
			reason = "no_estimate"
		}
		return DeliveryTrust{Reason: reason}
	}
	revision := fact.TrustRevision
	result := DeliveryTrust{ProgressTrusted: progressTrusted, ETATrusted: etaTrusted,
		Reason: "trusted", TrustRevision: &revision, ObservedAt: fact.ObservedAt}
	if progressTrusted {
		result.Progress = fact.Progress
	}
	if etaTrusted {
		result.ETA = fact.ETA
	}
	return result
}

func projectProjection(rows []fleetRow, workers []Worker) []Project {
	byProject := map[int64]*Project{}
	workerByID := make(map[string]Worker, len(workers))
	for _, worker := range workers {
		workerByID[worker.HarnessSessionID] = worker
	}
	for _, row := range rows {
		project := byProject[row.worker.Project.ID]
		if project == nil {
			project = &Project{ID: row.worker.Project.ID, Key: row.worker.Project.Key, Name: row.worker.Project.Name,
				TotalWorkers: row.projectWorkerCount}
			project.Orchestrator = Orchestrator{State: "unset", Reason: "no_active_coordinator"}
			if row.activeCoordinatorCount > 1 {
				project.Orchestrator = Orchestrator{State: "ambiguous", Reason: "multiple_active_coordinators"}
			} else if row.activeCoordinatorCount == 1 && row.activeCoordinatorID.Valid {
				id := row.activeCoordinatorID.String
				candidate, sampled := workerByID[id]
				if sampled && (candidate.Liveness.State == managedharness.ActivityBusy || candidate.Liveness.State == managedharness.ActivityIdle) {
					project.Orchestrator = Orchestrator{State: "resolved", Reason: "single_active_coordinator", SessionID: &id}
				} else {
					project.Orchestrator = Orchestrator{State: "unset", Reason: "coordinator_unknown"}
				}
			}
			byProject[project.ID] = project
		}
		project.SampledWorkers++
	}
	projects := make([]Project, 0, len(byProject))
	for _, project := range byProject {
		project.OmittedWorkers = project.TotalWorkers - int64(project.SampledWorkers)
		projects = append(projects, *project)
	}
	sort.Slice(projects, func(i, j int) bool {
		if projects[i].Key != projects[j].Key {
			return projects[i].Key < projects[j].Key
		}
		return projects[i].ID < projects[j].ID
	})
	return projects
}

func loadRecentCommunication(ctx context.Context, tx *sql.Tx, workers []Worker) error {
	if len(workers) == 0 {
		return nil
	}
	agentIDs := make([]int64, 0, len(workers))
	seen := map[int64]bool{}
	for _, worker := range workers {
		if !seen[worker.Agent.ID] {
			seen[worker.Agent.ID] = true
			agentIDs = append(agentIDs, worker.Agent.ID)
		}
	}
	encodedAgentIDs, err := json.Marshal(agentIDs)
	if err != nil {
		return err
	}
	query := `WITH selected_agents AS (
	SELECT agent.id,agent.project_id
	FROM project_agents agent
	JOIN json_each(?) selected ON CAST(selected.value AS INTEGER)=agent.id
	WHERE json_type(selected.value)='integer'
), associations AS (
	SELECT selected.id AS agent_id,message.id AS message_row_id,'outgoing' AS direction
	FROM selected_agents selected JOIN agent_messages message ON message.from_agent_id=selected.id
	JOIN project_agents receiver ON receiver.id=message.to_agent_id AND receiver.project_id=selected.project_id
	UNION SELECT selected.id,message.id,'incoming'
	FROM selected_agents selected JOIN agent_messages message ON message.to_agent_id=selected.id
	LEFT JOIN project_agents sender ON sender.id=message.from_agent_id
	WHERE message.to_agent_id IS NOT message.from_agent_id
	 AND (message.from_agent_id IS NULL OR sender.project_id=selected.project_id)
), ranked AS (
 SELECT association.agent_id,message.message_id,delivery.delivery_id,association.direction,
        delivery.requested_level,delivery.effective_level,delivery.state,delivery.fallback_reason,
        delivery.last_error_code,COALESCE(delivery.handed_off_at,delivery.updated_at,message.created_at) AS occurred_at,
        ROW_NUMBER() OVER(PARTITION BY association.agent_id ORDER BY message.id DESC,association.direction) AS item_rank,
		COUNT(*) OVER(PARTITION BY association.agent_id) AS total_count
 FROM associations association JOIN agent_messages message ON message.id=association.message_row_id
 LEFT JOIN agent_message_deliveries delivery ON delivery.message_row_id=message.id
)
SELECT agent_id,message_id,delivery_id,direction,requested_level,effective_level,state,
 fallback_reason,last_error_code,strftime('%Y-%m-%dT%H:%M:%fZ',occurred_at),total_count FROM ranked WHERE item_rank<=? ORDER BY agent_id,item_rank`
	rows, err := tx.QueryContext(ctx, query, string(encodedAgentIDs), RecentMessagesPerWorker)
	if err != nil {
		return err
	}
	type result struct {
		items []Communication
		total int64
	}
	byAgent := map[int64]result{}
	for rows.Next() {
		var agentID, total int64
		var item Communication
		var deliveryID, requested, effective, state, fallback, errorCode sql.NullString
		if err := rows.Scan(&agentID, &item.MessageID, &deliveryID, &item.Direction, &requested, &effective,
			&state, &fallback, &errorCode, &item.OccurredAt, &total); err != nil {
			rows.Close()
			return err
		}
		item.DeliveryID = nullableString(deliveryID)
		item.Attribution = "project_agent"
		item.RequestedLevel = nullableString(requested)
		item.EffectiveLevel = nullableString(effective)
		item.State = nullableString(state)
		item.FallbackCode = nullableNonEmptyString(fallback)
		item.ErrorCode = nullableNonEmptyString(errorCode)
		current := byAgent[agentID]
		current.items = append(current.items, item)
		current.total = total
		byAgent[agentID] = current
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index := range workers {
		result := byAgent[workers[index].Agent.ID]
		workers[index].RecentCommunication = result.items
		if workers[index].RecentCommunication == nil {
			workers[index].RecentCommunication = []Communication{}
		}
		workers[index].RecentCommunicationOmitted = result.total - int64(len(result.items))
	}
	return nil
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableNonEmptyString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	return nullableString(value)
}
