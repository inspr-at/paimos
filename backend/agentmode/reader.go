// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/delivery"
	"github.com/inspr-at/paimos/backend/deliverytrust"
)

type ReaderOptions struct {
	Clock     delivery.Clock
	Cursor    *CursorCodec
	Freshness delivery.FreshnessPolicy
}

type Reader struct {
	db        *sql.DB
	clock     delivery.Clock
	cursor    *CursorCodec
	freshness delivery.FreshnessPolicy
	// beforeCatalog is an in-package deterministic concurrency test seam.
	beforeCatalog func()
	// observeDBCall is an in-package contract seam for proving the bounded
	// transaction/query budget without replacing the production SQLite driver.
	observeDBCall func(kind, statement string, args []any)
}

type observedDBTX struct {
	delivery.DBTX
	observe func(kind, statement string, args []any)
}

func (q observedDBTX) ExecContext(ctx context.Context, statement string, args ...any) (sql.Result, error) {
	q.observe("exec", statement, append([]any(nil), args...))
	return q.DBTX.ExecContext(ctx, statement, args...)
}

func (q observedDBTX) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	q.observe("query", statement, append([]any(nil), args...))
	return q.DBTX.QueryContext(ctx, statement, args...)
}

func (q observedDBTX) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	q.observe("query", statement, append([]any(nil), args...))
	return q.DBTX.QueryRowContext(ctx, statement, args...)
}

func (r *Reader) dbtx(conn *sql.Conn) delivery.DBTX {
	if r.observeDBCall == nil {
		return conn
	}
	return observedDBTX{DBTX: conn, observe: r.observeDBCall}
}

func NewReader(database *sql.DB, options ReaderOptions) *Reader {
	clock := options.Clock
	if clock == nil {
		clock = delivery.ClockFunc(time.Now)
	}
	cursor := options.Cursor
	if cursor == nil {
		cursor = NewCursorCodec(clock)
	}
	return &Reader{db: database, clock: clock, cursor: cursor, freshness: options.Freshness}
}

type catalogEntry struct {
	IssueID     int64
	IssueNumber int64
	IssueKey    string
	IssueType   string
	Title       string
	IssueStatus string
	UpdatedAt   string
	ProjectID   int64
	ProjectKey  string
	ProjectName string
	AccessLevel string
	DeliveryID  *int64
	DeliveryKey string
	EpicID      *int64
	EpicKey     *string
	EpicTitle   *string
	Tags        []string
}

type readCatalog struct {
	UserID           int64
	Role             string
	PermissionsEpoch int64
	PermissionBasis  string
	RetentionFloor   int64
	HighWater        int64
	Entries          []catalogEntry
	Binding          CursorBinding
}

// StreamState captures the same authorization/cursor basis used by snapshots.
// The selected delivery is deliberately irrelevant to the binding: selection
// does not shape membership and may disappear while an SSE connection is open.
type StreamState struct {
	Binding        CursorBinding
	RetentionFloor int64
	HighWater      int64
}

func (r *Reader) StreamState(ctx context.Context, request Request) (StreamState, error) {
	if r == nil || r.db == nil || request.UserID <= 0 {
		return StreamState{}, ErrInvalid
	}
	if _, err := normalizedDetailKeys(request); err != nil {
		return StreamState{}, err
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return StreamState{}, err
	}
	defer conn.Close()
	q := r.dbtx(conn)
	if _, err := q.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return StreamState{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = q.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	catalog, err := r.loadCatalog(ctx, q, request)
	if err != nil {
		return StreamState{}, err
	}
	if len(catalog.Entries) > MaxCandidateRoots {
		return StreamState{}, fmt.Errorf("%w: candidate root limit exceeded", ErrInvalid)
	}
	routeDigest, filterDigest, err := requestFingerprints(request)
	if err != nil {
		return StreamState{}, err
	}
	catalog.Binding.RouteDigest = routeDigest
	catalog.Binding.FilterDigest = filterDigest
	if _, err := q.ExecContext(ctx, "COMMIT"); err != nil {
		return StreamState{}, err
	}
	committed = true
	return StreamState{Binding: catalog.Binding, RetentionFloor: catalog.RetentionFloor, HighWater: catalog.HighWater}, nil
}

func (r *Reader) Read(ctx context.Context, request Request) (Snapshot, error) {
	if r == nil || r.db == nil || request.UserID <= 0 {
		return Snapshot{}, ErrInvalid
	}
	if request.RouteProjectID != nil && *request.RouteProjectID <= 0 {
		return Snapshot{}, ErrInvalid
	}
	detailKeys, err := normalizedDetailKeys(request)
	if err != nil {
		return Snapshot{}, err
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer conn.Close()
	q := r.dbtx(conn)
	if _, err := q.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		return Snapshot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = q.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if r.beforeCatalog != nil {
		r.beforeCatalog()
	}

	catalog, err := r.loadCatalog(ctx, q, request)
	if err != nil {
		return Snapshot{}, err
	}
	// loadCatalog is the first read in this deferred transaction and therefore
	// pins the SQLite snapshot. Capture time only after that boundary: no fact
	// visible to the remaining reads can have committed between this clock
	// instant and the snapshot pin.
	calculatedAt := r.clock.Now().UTC()
	if calculatedAt.IsZero() {
		return Snapshot{}, fmt.Errorf("%w: zero reader clock", ErrInvariant)
	}
	if len(catalog.Entries) > MaxCandidateRoots {
		return Snapshot{}, fmt.Errorf("%w: candidate root limit exceeded", ErrInvalid)
	}
	if len(detailKeys) > 0 && len(catalog.Entries) == 0 {
		return Snapshot{}, ErrNotFound
	}
	if request.Filters.SelectedDelivery != "" && !catalogContainsDelivery(catalog.Entries, request.Filters.SelectedDelivery) {
		return Snapshot{}, ErrNotFound
	}

	issueIDs := make([]int64, len(catalog.Entries))
	for index := range catalog.Entries {
		issueIDs[index] = catalog.Entries[index].IssueID
	}
	store := delivery.NewStore(r.db, delivery.Options{Clock: delivery.ClockFunc(func() time.Time { return calculatedAt }),
		Freshness: r.freshness})
	deliverySnapshots, err := store.BulkSnapshotsTx(ctx, q, issueIDs)
	if err != nil {
		return Snapshot{}, err
	}
	trustFacts, err := loadTrustFacts(ctx, q, issueIDs)
	if err != nil {
		return Snapshot{}, err
	}
	history, err := loadDurationHistory(ctx, q, catalog.Entries)
	if err != nil {
		return Snapshot{}, err
	}
	allRows, err := buildDeliveryRows(catalog.Entries, deliverySnapshots, trustFacts, history, calculatedAt)
	if err != nil {
		return Snapshot{}, err
	}

	active := make([]DeliveryRow, 0, len(allRows))
	terminal := make([]DeliveryRow, 0, len(allRows))
	byID := make(map[string]DeliveryRow, len(allRows))
	for _, row := range allRows {
		if _, duplicate := byID[row.DeliveryID]; duplicate {
			return Snapshot{}, fmt.Errorf("%w: duplicate delivery identity", ErrInvariant)
		}
		byID[row.DeliveryID] = row
		if row.active {
			active = append(active, row)
		} else {
			terminal = append(terminal, row)
		}
	}
	sortRows(active)
	filtered := make([]DeliveryRow, 0, len(active))
	for _, row := range active {
		if request.Filters.matches(row) {
			filtered = append(filtered, row)
		}
	}
	aggregates, err := BuildAggregates(filtered, request.Filters, calculatedAt)
	if err != nil {
		return Snapshot{}, err
	}
	selectedID, outside := selectDelivery(request.Filters.SelectedDelivery, filtered, active, terminal, byID)
	if request.Filters.SelectedDelivery != "" && selectedID == "" {
		return Snapshot{}, ErrNotFound
	}

	routeDigest, filterDigest, err := requestFingerprints(request)
	if err != nil {
		return Snapshot{}, err
	}
	catalog.Binding.RouteDigest = routeDigest
	catalog.Binding.FilterDigest = filterDigest
	cursor, err := r.cursor.EncodeAt(catalog.Binding, catalog.HighWater, calculatedAt)
	if err != nil {
		return Snapshot{}, err
	}
	response := Snapshot{SchemaVersion: SchemaVersion, ServerTime: calculatedAt, Cursor: cursor,
		Rows: filtered, SelectedDelivery: selectedID, SelectedOutside: outside, Aggregates: aggregates}
	if _, err := q.ExecContext(ctx, "COMMIT"); err != nil {
		return Snapshot{}, err
	}
	committed = true
	return response, nil
}

func normalizedDetailKeys(request Request) ([]string, error) {
	if request.DetailDeliveryKey != "" && len(request.DetailDeliveryKeys) > 0 {
		return nil, fmt.Errorf("%w: singular and multi detail scopes cannot be combined", ErrInvalid)
	}
	keys := append([]string(nil), request.DetailDeliveryKeys...)
	if request.DetailDeliveryKey != "" {
		keys = []string{request.DetailDeliveryKey}
	}
	if len(keys) > 4 {
		return nil, fmt.Errorf("%w: at most four detail deliveries", ErrInvalid)
	}
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		if !deliveryKeyPattern.MatchString(key) || seen[key] {
			return nil, fmt.Errorf("%w: invalid or duplicate detail delivery", ErrInvalid)
		}
		seen[key] = true
	}
	return keys, nil
}

func catalogContainsDelivery(entries []catalogEntry, key string) bool {
	for _, entry := range entries {
		if entry.DeliveryKey == key {
			return true
		}
	}
	return false
}

func selectDelivery(requested string, filtered, active, terminal []DeliveryRow, byID map[string]DeliveryRow) (string, *SelectedOutside) {
	if requested != "" {
		row, ok := byID[requested]
		if !ok {
			return "", nil
		}
		for _, candidate := range filtered {
			if candidate.DeliveryID == requested {
				return requested, nil
			}
		}
		reason := SelectedTerminal
		if row.active {
			reason = SelectedFilterExcluded
		}
		return requested, &SelectedOutside{Reason: reason, Row: row}
	}
	if len(filtered) > 0 {
		return filtered[0].DeliveryID, nil
	}
	if len(active) > 0 {
		sortRows(active)
		return active[0].DeliveryID, &SelectedOutside{Reason: SelectedActiveFallback, Row: active[0]}
	}
	if len(terminal) > 0 {
		sort.SliceStable(terminal, func(i, j int) bool {
			if terminal[i].UpdatedAt != terminal[j].UpdatedAt {
				return terminal[i].UpdatedAt > terminal[j].UpdatedAt
			}
			return terminal[i].DeliveryID < terminal[j].DeliveryID
		})
		return terminal[0].DeliveryID, &SelectedOutside{Reason: SelectedTerminalFallback, Row: terminal[0]}
	}
	return "", nil
}

func (r *Reader) loadCatalog(ctx context.Context, conn delivery.DBTX, request Request) (readCatalog, error) {
	routeID := int64(0)
	rootScope := "1=1"
	if request.RouteProjectID != nil {
		routeID = *request.RouteProjectID
		// Keep the project-route predicate as a simple equality inside each
		// agent_mode_roots consumer. An OR-with-zero form prevents SQLite from
		// using idx_issues_project and makes one project read scale with every
		// authorized live issue in the installation.
		rootScope = "roots.project_id=?"
	}
	detailKeys, err := normalizedDetailKeys(request)
	if err != nil {
		return readCatalog{}, err
	}
	detailPredicate := func(alias string) string {
		if len(detailKeys) == 0 {
			return "1=1"
		}
		return fmt.Sprintf("COALESCE(%s.delivery_key,'issue:'||roots.issue_id) IN (%s)", alias,
			strings.TrimRight(strings.Repeat("?,", len(detailKeys)), ","))
	}
	query := auth.AgentModeAuthorizationCTE + fmt.Sprintf(`,
unlinked_v1 AS (
 SELECT 1
 FROM agent_mode_roots roots
 JOIN agent_runs run ON run.issue_id=roots.issue_id
 LEFT JOIN deliveries delivery ON delivery.issue_id=roots.issue_id
 WHERE %s
  AND %s
  AND run.delivery_instrumentation_version=1
  AND NOT EXISTS(SELECT 1 FROM delivery_agent_run_links link
   WHERE link.agent_run_id=run.id AND link.root_issue_id=run.issue_id)
 LIMIT 1
),
raw_candidates AS (
 SELECT roots.issue_id,roots.project_id,roots.access_level
 FROM agent_mode_roots roots LEFT JOIN deliveries root_delivery ON root_delivery.issue_id=roots.issue_id
 WHERE %s
 AND %s
 AND (
  root_delivery.id IS NOT NULL OR
  EXISTS(SELECT 1 FROM agent_runs ar WHERE ar.issue_id=roots.issue_id
   AND ar.delivery_instrumentation_version=0 AND ar.status IN ('queued','running'))
 )
),
candidates AS (
 SELECT * FROM raw_candidates ORDER BY issue_id LIMIT 1001
),
ancestry(root_id,ancestor_id,depth,path) AS (
 SELECT candidates.issue_id,relation.source_id,1,
  ','||candidates.issue_id||','||relation.source_id||','
 FROM candidates JOIN issue_relations relation ON relation.target_id=candidates.issue_id AND relation.type='parent'
 UNION ALL
 SELECT ancestry.root_id,relation.source_id,ancestry.depth+1,
  ancestry.path||relation.source_id||','
 FROM ancestry JOIN issue_relations relation ON relation.target_id=ancestry.ancestor_id AND relation.type='parent'
 WHERE instr(ancestry.path,','||relation.source_id||',')=0
),
candidate_rows AS (
 SELECT i.id AS issue_id,i.issue_number,i.type AS issue_type,i.title,i.status AS issue_status,i.updated_at,
  p.id AS project_id,p.key AS project_key,p.name AS project_name,candidates.access_level,
  d.id AS delivery_id,COALESCE(d.delivery_key,'issue:'||i.id) AS delivery_key,
  (SELECT ancestor.id FROM ancestry path JOIN issues ancestor ON ancestor.id=path.ancestor_id
    WHERE path.root_id=i.id AND ancestor.type='epic' AND ancestor.deleted_at IS NULL
     AND ancestor.project_id=i.project_id ORDER BY path.depth,ancestor.id LIMIT 1) AS epic_id,
  (SELECT p.key||'-'||ancestor.issue_number FROM ancestry path JOIN issues ancestor ON ancestor.id=path.ancestor_id
    WHERE path.root_id=i.id AND ancestor.type='epic' AND ancestor.deleted_at IS NULL
     AND ancestor.project_id=i.project_id ORDER BY path.depth,ancestor.id LIMIT 1) AS epic_key,
  (SELECT ancestor.title FROM ancestry path JOIN issues ancestor ON ancestor.id=path.ancestor_id
    WHERE path.root_id=i.id AND ancestor.type='epic' AND ancestor.deleted_at IS NULL
     AND ancestor.project_id=i.project_id ORDER BY path.depth,ancestor.id LIMIT 1) AS epic_title,
  COALESCE((SELECT json_group_array(ordered.name) FROM (
   SELECT tag.name FROM issue_tags assignment JOIN tags tag ON tag.id=assignment.tag_id
   WHERE assignment.issue_id=i.id ORDER BY tag.name
 ) ordered),'[]') AS tags_json
 FROM candidates JOIN issues i ON i.id=candidates.issue_id
 JOIN projects p ON p.id=i.project_id LEFT JOIN deliveries d ON d.issue_id=i.id
)
SELECT requester.user_id,requester.role,requester.permissions_epoch,
 requester.role||'|'||COALESCE((SELECT group_concat(ordered.project_id||':'||ordered.access_level,'|')
  FROM (SELECT project_id,access_level FROM agent_mode_projects ORDER BY project_id) ordered),'') AS permission_basis,
 CASE WHEN ?=0 THEN 1 ELSE EXISTS(SELECT 1 FROM agent_mode_projects WHERE project_id=?) END AS route_authorized,
 COALESCE((SELECT MAX(floor_id) FROM delivery_change_retention),0) AS retention_floor,
 MAX(COALESCE((SELECT MAX(floor_id) FROM delivery_change_retention),0),
     COALESCE((SELECT MAX(id) FROM delivery_change_log),0)) AS high_water,
 EXISTS(SELECT 1 FROM unlinked_v1) AS has_unlinked_v1,
 candidate_rows.issue_id,candidate_rows.issue_number,candidate_rows.issue_type,candidate_rows.title,
 candidate_rows.issue_status,candidate_rows.updated_at,candidate_rows.project_id,candidate_rows.project_key,
 candidate_rows.project_name,candidate_rows.access_level,candidate_rows.delivery_id,candidate_rows.delivery_key,
 candidate_rows.epic_id,candidate_rows.epic_key,candidate_rows.epic_title,candidate_rows.tags_json
 FROM requester LEFT JOIN candidate_rows ON 1=1 ORDER BY candidate_rows.issue_id`, rootScope,
		detailPredicate("delivery"), rootScope, detailPredicate("root_delivery"))
	args := []any{request.UserID}
	if request.RouteProjectID != nil {
		args = append(args, routeID)
	}
	for _, key := range detailKeys {
		args = append(args, key)
	}
	if request.RouteProjectID != nil {
		args = append(args, routeID)
	}
	for _, key := range detailKeys {
		args = append(args, key)
	}
	args = append(args, routeID, routeID)
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return readCatalog{}, err
	}
	defer rows.Close()
	var catalog readCatalog
	var initialized bool
	for rows.Next() {
		var userID, epoch, floor, high int64
		var role, basis string
		var routeAuthorized, hasUnlinkedV1 int
		var issueID, issueNumber, projectID, deliveryID, epicID sql.NullInt64
		var issueType, title, issueStatus, updatedAt, projectKey, projectName, accessLevel, deliveryKey, epicKey, epicTitle, tagsJSON sql.NullString
		if err := rows.Scan(&userID, &role, &epoch, &basis, &routeAuthorized, &floor, &high, &hasUnlinkedV1,
			&issueID, &issueNumber, &issueType, &title, &issueStatus, &updatedAt, &projectID, &projectKey,
			&projectName, &accessLevel, &deliveryID, &deliveryKey, &epicID, &epicKey, &epicTitle, &tagsJSON); err != nil {
			return readCatalog{}, err
		}
		if !initialized {
			if routeAuthorized != 1 || role == "external" {
				return readCatalog{}, ErrNotFound
			}
			if hasUnlinkedV1 != 0 {
				return readCatalog{}, fmt.Errorf("%w: instrumentation-v1 run has no delivery link", ErrInvariant)
			}
			catalog.UserID, catalog.Role, catalog.PermissionsEpoch = userID, role, epoch
			catalog.PermissionBasis, catalog.RetentionFloor, catalog.HighWater = basis, floor, high
			initialized = true
		}
		if !issueID.Valid {
			continue
		}
		updated, err := parseDBTime(updatedAt.String)
		if err != nil {
			return readCatalog{}, fmt.Errorf("%w: malformed issue update time", ErrInvariant)
		}
		entry := catalogEntry{IssueID: issueID.Int64, IssueNumber: issueNumber.Int64, IssueKey: projectKey.String + "-" + fmt.Sprint(issueNumber.Int64),
			IssueType: issueType.String, Title: title.String, IssueStatus: issueStatus.String, UpdatedAt: updated.Format(time.RFC3339Nano),
			ProjectID: projectID.Int64, ProjectKey: projectKey.String, ProjectName: projectName.String,
			AccessLevel: accessLevel.String, DeliveryKey: deliveryKey.String}
		if deliveryID.Valid {
			value := deliveryID.Int64
			entry.DeliveryID = &value
		}
		if epicID.Valid {
			value := epicID.Int64
			entry.EpicID = &value
		}
		if epicKey.Valid {
			value := epicKey.String
			entry.EpicKey = &value
		}
		if epicTitle.Valid {
			value := epicTitle.String
			entry.EpicTitle = &value
		}
		if err := json.Unmarshal([]byte(tagsJSON.String), &entry.Tags); err != nil {
			return readCatalog{}, fmt.Errorf("%w: malformed tag aggregate", ErrInvariant)
		}
		if entry.Tags == nil {
			entry.Tags = []string{}
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return readCatalog{}, err
	}
	if !initialized {
		return readCatalog{}, ErrNotFound
	}
	permissionDigest, err := permissionFingerprint(catalog.UserID, catalog.PermissionsEpoch, catalog.PermissionBasis)
	if err != nil {
		return readCatalog{}, err
	}
	catalog.Binding = CursorBinding{UserID: catalog.UserID, PermissionsEpoch: catalog.PermissionsEpoch,
		PermissionDigest: permissionDigest}
	return catalog, nil
}

type trustStageFact struct {
	IssueID            int64
	AttemptID          int64
	AttemptNumber      int64
	PlanRevision       int64
	Stage              string
	SortOrder          int
	Applicability      string
	Weight             int
	ExecutionNumber    *int64
	ExecutionStartID   *int64
	ExecutionStartedAt *time.Time
	AuthorityAnchorID  *int64
	ResetID            *int64
	ReporterID         *int64
	ReporterType       string
	RunLinkID          *int64
	SemanticID         *int64
	SemanticKind       string
	SemanticState      string
	EvidenceIDs        []string
	Estimates          []deliverytrust.EstimateFact
}

type trustFacts map[int64]map[string]*trustStageFact

func valuesClause(count int) string {
	return strings.TrimRight(strings.Repeat("(?),", count), ",")
}

func anyArgs(values []int64) []any {
	out := make([]any, len(values))
	for i := range values {
		out[i] = values[i]
	}
	return out
}

func loadTrustFacts(ctx context.Context, conn delivery.DBTX, issueIDs []int64) (trustFacts, error) {
	if len(issueIDs) == 0 {
		// Preserve the four-query contract even for an empty authorized set.
		rows, err := conn.QueryContext(ctx, `SELECT NULL WHERE 0`)
		if err != nil {
			return nil, err
		}
		_ = rows.Close()
		return trustFacts{}, nil
	}
	query := `WITH selected(issue_id) AS (VALUES ` + valuesClause(len(issueIDs)) + `),
current_stages AS (
 SELECT d.issue_id,d.id AS delivery_id,a.id AS attempt_id,a.attempt_number,a.plan_revision,
  policy.stage_key,policy.sort_order,policy.applicability,policy.weight,
  latest.execution_number,latest.execution_start_stage_event_id,start.server_received_at AS execution_started_at,
  owner.id AS authority_anchor_id,latest.current_reporter_id,reporter.reporter_type,
  reset.id AS reset_id,link.link_delivery_event_id AS run_link_id,link.agent_run_id,
  COALESCE(latest.semantic_stage_event_id,semantic_telemetry.id) AS semantic_id,
  CASE WHEN latest.semantic_stage_event_id IS NOT NULL THEN 'stage_event'
       WHEN semantic_telemetry.id IS NOT NULL THEN 'telemetry' ELSE '' END AS semantic_kind,
  COALESCE(semantic.semantic_state,'pending') AS semantic_state,
	COALESCE(semantic_telemetry.activity,'') AS semantic_activity,
  COALESCE(owner.authority_source_sequence_cutoff,0) AS authority_source_cutoff,
  COALESCE(activation.telemetry_sequence_cutoff,0) AS telemetry_cutoff,
  COALESCE(reset.reset_source_cutoff,0) AS reset_source_cutoff,
  COALESCE(reset.reset_telemetry_sequence_cutoff,0) AS reset_telemetry_cutoff,
  COALESCE((SELECT json_group_array(identity) FROM (
   SELECT CAST(evidence.stage_event_id AS TEXT)||':'||CAST(evidence.ordinal AS TEXT) AS identity
   FROM delivery_evidence evidence
   WHERE evidence.stage_event_id=latest.semantic_stage_event_id ORDER BY evidence.ordinal
  )),'[]') AS evidence_ids
 FROM selected JOIN deliveries d ON d.issue_id=selected.issue_id
 JOIN delivery_attempts a ON a.id=(SELECT candidate.id FROM delivery_attempts candidate
  JOIN delivery_attempt_policy_seals seal ON seal.delivery_id=candidate.delivery_id AND seal.attempt_id=candidate.id
  WHERE candidate.delivery_id=d.id ORDER BY candidate.attempt_number DESC LIMIT 1)
 JOIN delivery_attempt_stage_policy policy ON policy.attempt_id=a.id
 LEFT JOIN delivery_stage_latest latest ON latest.attempt_id=a.id AND latest.stage_key=policy.stage_key
 LEFT JOIN delivery_stage_events start ON start.id=latest.execution_start_stage_event_id
 LEFT JOIN delivery_stage_events owner ON owner.id=(SELECT authority.id FROM delivery_stage_events authority
  WHERE authority.attempt_id=a.id AND authority.stage_key=policy.stage_key
   AND authority.execution_number=latest.execution_number AND authority.authority_epoch=latest.authority_epoch
   AND authority.reporter_id=latest.current_reporter_id AND authority.event_type IN ('execution_started','handoff')
  ORDER BY authority.event_sequence DESC LIMIT 1)
 LEFT JOIN delivery_stage_events reset ON reset.id=(SELECT boundary.id FROM delivery_stage_events boundary
  WHERE boundary.attempt_id=a.id AND boundary.stage_key=policy.stage_key
   AND boundary.execution_number=latest.execution_number AND boundary.authority_epoch=latest.authority_epoch
   AND boundary.event_type='progress_reset_authorized' ORDER BY boundary.event_sequence DESC LIMIT 1)
 LEFT JOIN delivery_reporters reporter ON reporter.id=latest.current_reporter_id
 LEFT JOIN delivery_agent_run_links link ON link.attempt_id=a.id AND link.stage_key=policy.stage_key
  AND link.execution_number=latest.execution_number AND link.reporter_id=latest.current_reporter_id
 LEFT JOIN delivery_agent_run_activations activation ON activation.attempt_id=a.id
  AND activation.stage_key=policy.stage_key AND activation.execution_number=latest.execution_number
  AND activation.authority_epoch=latest.authority_epoch AND activation.agent_run_id=link.agent_run_id
 LEFT JOIN agent_run_telemetry_latest telemetry_latest ON telemetry_latest.run_id=link.agent_run_id
 LEFT JOIN agent_run_telemetry semantic_telemetry ON semantic_telemetry.id=telemetry_latest.semantic_telemetry_id
  AND semantic_telemetry.run_id=link.agent_run_id
  AND semantic_telemetry.sequence>COALESCE(activation.telemetry_sequence_cutoff,0)
  AND semantic_telemetry.sequence>CASE
   WHEN reset.reset_source_kind='stage_and_agent_run_telemetry'
    AND reset.reset_telemetry_run_id=link.agent_run_id
    AND reset.reset_owner_reporter_id=latest.current_reporter_id
   THEN COALESCE(reset.reset_telemetry_sequence_cutoff,0) ELSE 0 END
 LEFT JOIN delivery_stage_events semantic ON semantic.id=latest.semantic_stage_event_id
),
all_estimates AS (
 SELECT current_stages.issue_id,current_stages.stage_key,current_stages.reporter_type,
  'stage_event:'||estimate.id AS identity,
  estimate.estimate_revision AS revision,estimate.source_sequence AS sequence,estimate.estimate_source AS source,
  estimate.server_received_at,estimate.estimate_confidence AS confidence,estimate.estimate_basis AS basis,
  estimate.progress_percent,estimate.eta_seconds,estimate.eta_min_seconds,estimate.eta_max_seconds
 FROM current_stages JOIN delivery_stage_events estimate ON estimate.attempt_id=current_stages.attempt_id
  AND estimate.stage_key=current_stages.stage_key AND estimate.execution_number=current_stages.execution_number
  AND estimate.reporter_id=current_stages.current_reporter_id AND estimate.event_type='estimate'
 WHERE current_stages.reporter_type='external'
  AND estimate.source_sequence>current_stages.authority_source_cutoff
  AND estimate.source_sequence>current_stages.reset_source_cutoff
 UNION ALL
 SELECT current_stages.issue_id,current_stages.stage_key,current_stages.reporter_type,'telemetry:'||telemetry.id,
  telemetry.estimate_revision,telemetry.sequence,telemetry.estimate_source,
  telemetry.server_received_at,telemetry.estimate_confidence,telemetry.estimate_basis,
  telemetry.progress_percent,telemetry.eta_seconds,telemetry.eta_min_seconds,telemetry.eta_max_seconds
 FROM current_stages JOIN agent_run_telemetry telemetry ON telemetry.run_id=current_stages.agent_run_id
 WHERE current_stages.reporter_type='agent_run' AND telemetry.estimate_revision IS NOT NULL
  AND telemetry.sequence>current_stages.telemetry_cutoff AND telemetry.sequence>current_stages.reset_telemetry_cutoff
),
classified AS (
 SELECT all_estimates.*,
  CASE WHEN ((reporter_type='agent_run' AND source IN ('agent','adapter','provider','tool'))
          OR (reporter_type='external' AND source='external'))
        AND confidence>0 AND confidence<=1 AND basis<>'' THEN 1 ELSE 0 END AS base_eligible,
  CASE WHEN ((reporter_type='agent_run' AND source IN ('agent','adapter','provider','tool'))
          OR (reporter_type='external' AND source='external'))
        AND confidence>0 AND confidence<=1 AND basis<>''
        AND progress_percent BETWEEN 0 AND 100 THEN 1 ELSE 0 END AS progress_eligible
 FROM all_estimates
),
ranked AS (
 SELECT classified.*,
  ROW_NUMBER() OVER (PARTITION BY issue_id,stage_key ORDER BY revision DESC,sequence DESC,identity DESC) AS latest_rank,
  CASE WHEN progress_eligible=1 THEN
   ROW_NUMBER() OVER (PARTITION BY issue_id,stage_key,
    progress_eligible
    ORDER BY revision DESC,sequence DESC,identity DESC) END AS progress_rank,
  CASE WHEN progress_eligible=1 THEN
   ROW_NUMBER() OVER (PARTITION BY issue_id,stage_key,
    progress_eligible
    ORDER BY progress_percent DESC,revision DESC,sequence DESC,identity DESC) END AS maximum_rank
 FROM classified
),
frontier AS (
 SELECT * FROM ranked WHERE latest_rank=1 OR progress_rank=1 OR maximum_rank=1
)
SELECT current_stages.issue_id,current_stages.attempt_id,current_stages.attempt_number,current_stages.plan_revision,
 current_stages.stage_key,current_stages.sort_order,current_stages.applicability,current_stages.weight,
 current_stages.execution_number,current_stages.execution_start_stage_event_id,current_stages.execution_started_at,
 current_stages.authority_anchor_id,current_stages.reset_id,current_stages.current_reporter_id,
 current_stages.reporter_type,current_stages.run_link_id,current_stages.semantic_id,
 current_stages.semantic_kind,current_stages.semantic_state,current_stages.semantic_activity,current_stages.evidence_ids,
 frontier.identity,frontier.revision,frontier.sequence,frontier.source,frontier.server_received_at,
 frontier.confidence,frontier.basis,frontier.progress_percent,frontier.eta_seconds,frontier.eta_min_seconds,frontier.eta_max_seconds
FROM current_stages LEFT JOIN frontier ON frontier.issue_id=current_stages.issue_id
 AND frontier.stage_key=current_stages.stage_key
ORDER BY current_stages.issue_id,current_stages.sort_order,frontier.revision,frontier.sequence,frontier.identity`
	rows, err := conn.QueryContext(ctx, query, anyArgs(issueIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := trustFacts{}
	for rows.Next() {
		var issueID, attemptID, attemptNumber, planRevision int64
		var stage, applicability, semanticKind, semanticState, semanticActivity, evidenceJSON string
		var sortOrder, weight int
		var execution, startID, authorityID, resetID, reporterID, runLinkID, semanticID sql.NullInt64
		var startedAt, reporterType sql.NullString
		var identity, source, receivedAt, basis sql.NullString
		var revision, sequence, eta, etaMin, etaMax sql.NullInt64
		var confidence, progress sql.NullFloat64
		if err := rows.Scan(&issueID, &attemptID, &attemptNumber, &planRevision, &stage, &sortOrder, &applicability,
			&weight, &execution, &startID, &startedAt, &authorityID, &resetID, &reporterID, &reporterType,
			&runLinkID, &semanticID, &semanticKind, &semanticState, &semanticActivity, &evidenceJSON, &identity, &revision, &sequence, &source,
			&receivedAt, &confidence, &basis, &progress, &eta, &etaMin, &etaMax); err != nil {
			return nil, err
		}
		byStage := out[issueID]
		if byStage == nil {
			byStage = map[string]*trustStageFact{}
			out[issueID] = byStage
		}
		fact := byStage[stage]
		if fact == nil {
			if semanticKind == "telemetry" && delivery.ContainsSecretLike(semanticActivity) {
				semanticID = sql.NullInt64{}
				semanticKind = ""
			}
			fact = &trustStageFact{IssueID: issueID, AttemptID: attemptID, AttemptNumber: attemptNumber,
				PlanRevision: planRevision, Stage: stage, SortOrder: sortOrder, Applicability: applicability,
				Weight: weight, ExecutionNumber: nullableInt64(execution), ExecutionStartID: nullableInt64(startID),
				AuthorityAnchorID: nullableInt64(authorityID), ResetID: nullableInt64(resetID),
				ReporterID: nullableInt64(reporterID), ReporterType: reporterType.String,
				RunLinkID: nullableInt64(runLinkID), SemanticID: nullableInt64(semanticID), SemanticKind: semanticKind,
				SemanticState: semanticState}
			if startedAt.Valid {
				parsed, parseErr := parseDBTime(startedAt.String)
				if parseErr != nil {
					return nil, fmt.Errorf("%w: invalid execution time", ErrInvariant)
				}
				fact.ExecutionStartedAt = &parsed
			}
			if err := json.Unmarshal([]byte(evidenceJSON), &fact.EvidenceIDs); err != nil {
				return nil, fmt.Errorf("%w: invalid evidence identities", ErrInvariant)
			}
			if err := validateEvidenceIdentities(fact.SemanticID, fact.EvidenceIDs); err != nil {
				return nil, err
			}
			byStage[stage] = fact
		}
		if identity.Valid {
			if delivery.ContainsSecretLike(basis.String) {
				continue
			}
			estimate, convertErr := estimateFactFromRow(fact, identity.String, revision, sequence, source.String,
				receivedAt.String, confidence, basis.String, progress, eta, etaMin, etaMax)
			if convertErr != nil {
				return nil, convertErr
			}
			duplicate := false
			for _, existing := range fact.Estimates {
				duplicate = duplicate || existing.Identity == estimate.Identity
			}
			if !duplicate {
				fact.Estimates = append(fact.Estimates, estimate)
			}
		}
	}
	return out, rows.Err()
}

func validateEvidenceIdentities(semanticID *int64, identities []string) error {
	if len(identities) > 16 {
		return fmt.Errorf("%w: too many evidence identities", ErrInvariant)
	}
	seen := make(map[string]bool, len(identities))
	for _, identity := range identities {
		parts := strings.Split(identity, ":")
		stageEventID, stageErr := strconv.ParseInt(parts[0], 10, 64)
		ordinal, ordinalErr := strconv.Atoi(parts[len(parts)-1])
		if len(parts) != 2 || semanticID == nil || stageErr != nil || stageEventID != *semanticID ||
			ordinalErr != nil || ordinal < 0 || ordinal > 15 || seen[identity] {
			return fmt.Errorf("%w: invalid or duplicate evidence identity", ErrInvariant)
		}
		seen[identity] = true
	}
	return nil
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	out := value.Int64
	return &out
}

func positiveUint64(value int64) (uint64, bool) {
	if value <= 0 {
		return 0, false
	}
	return uint64(value), true
}

func estimateFactFromRow(stage *trustStageFact, identity string, revision, sequence sql.NullInt64, source, receivedAt string,
	confidence sql.NullFloat64, basis string, progress sql.NullFloat64, eta, etaMin, etaMax sql.NullInt64) (deliverytrust.EstimateFact, error) {
	if !revision.Valid || !sequence.Valid || !confidence.Valid {
		return deliverytrust.EstimateFact{}, fmt.Errorf("%w: incomplete estimate frontier", ErrInvariant)
	}
	revisionValue, revisionOK := positiveUint64(revision.Int64)
	sequenceValue, sequenceOK := positiveUint64(sequence.Int64)
	if !revisionOK || !sequenceOK {
		return deliverytrust.EstimateFact{}, fmt.Errorf("%w: invalid estimate revision or sequence", ErrInvariant)
	}
	received, err := parseDBTime(receivedAt)
	if err != nil {
		return deliverytrust.EstimateFact{}, fmt.Errorf("%w: malformed estimate time", ErrInvariant)
	}
	reporter := deliverytrust.ReporterExternal
	if stage.ReporterType == "agent_run" {
		reporter = deliverytrust.ReporterAgentRun
	}
	fact := deliverytrust.EstimateFact{Identity: identity, Reporter: reporter, Revision: revisionValue,
		Sequence: sequenceValue, Source: deliverytrust.EstimateSource(source), ServerReceivedAt: received,
		Confidence: confidence.Float64, Basis: basis}
	if progress.Valid {
		value := progress.Float64
		fact.ProgressPercent = &value
	}
	if etaMin.Valid && etaMax.Valid {
		rangeValue := deliverytrust.EstimateRange{MinimumSeconds: etaMin.Int64, MaximumSeconds: etaMax.Int64}
		if eta.Valid {
			value := eta.Int64
			rangeValue.PointSeconds = &value
		}
		fact.ETA = &rangeValue
	}
	return fact, nil
}

type durationKey struct {
	ProjectID int64
	Stage     string
}

type durationHistory map[durationKey][]deliverytrust.DurationSample

func loadDurationHistory(ctx context.Context, conn delivery.DBTX, entries []catalogEntry) (durationHistory, error) {
	projects := map[int64]bool{}
	for _, entry := range entries {
		projects[entry.ProjectID] = true
	}
	projectIDs := make([]int64, 0, len(projects))
	for id := range projects {
		projectIDs = append(projectIDs, id)
	}
	sort.Slice(projectIDs, func(i, j int) bool { return projectIDs[i] < projectIDs[j] })
	if len(projectIDs) == 0 {
		rows, err := conn.QueryContext(ctx, `SELECT NULL WHERE 0`)
		if err != nil {
			return nil, err
		}
		_ = rows.Close()
		return durationHistory{}, nil
	}
	query := `WITH ranked AS (
 SELECT stage_execution_id,project_id_at_completion,stage_key,estimator_policy_version,completed_at,
  full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds,
  ROW_NUMBER() OVER (PARTITION BY project_id_at_completion,stage_key,estimator_policy_version
   ORDER BY completed_at DESC,stage_execution_id DESC) AS sample_rank
 FROM delivery_stage_durations WHERE project_id_at_completion IN (` + strings.TrimRight(strings.Repeat("?,", len(projectIDs)), ",") + `)
)
SELECT stage_execution_id,project_id_at_completion,stage_key,estimator_policy_version,completed_at,
 full_lead_seconds,active_seconds,blocked_seconds,human_wait_seconds
FROM ranked WHERE sample_rank<=100
ORDER BY project_id_at_completion,stage_key,completed_at DESC,stage_execution_id DESC`
	rows, err := conn.QueryContext(ctx, query, anyArgs(projectIDs)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := durationHistory{}
	for rows.Next() {
		var executionID, projectID, full, active, blocked, human int64
		var stage, completedRaw string
		var policy int
		if err := rows.Scan(&executionID, &projectID, &stage, &policy, &completedRaw, &full, &active, &blocked, &human); err != nil {
			return nil, err
		}
		stageExecutionID, ok := positiveUint64(executionID)
		if !ok {
			return nil, fmt.Errorf("%w: invalid duration execution identity", ErrInvariant)
		}
		completed, err := parseDBTime(completedRaw)
		if err != nil {
			return nil, fmt.Errorf("%w: malformed duration time", ErrInvariant)
		}
		key := durationKey{ProjectID: projectID, Stage: stage}
		out[key] = append(out[key], deliverytrust.DurationSample{Identity: fmt.Sprintf("duration:%d", executionID),
			StageExecutionID: stageExecutionID, ProjectIdentity: fmt.Sprintf("project:%d", projectID),
			Stage: deliverytrust.StageKey(stage), PolicyVersion: policy, CompletedAt: completed,
			FullLeadSeconds: full, ActiveSeconds: active, BlockedSeconds: blocked, HumanWaitSeconds: human})
	}
	return out, rows.Err()
}

func parseDBTime(raw string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("invalid time")
}
