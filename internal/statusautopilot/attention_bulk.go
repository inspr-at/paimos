// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const attentionBulkLimit = 1000
const attentionBulkConflict = "Another bulk change is running here"

func attentionKindOrder(kind string) int {
	switch kind {
	case "proposed":
		return 0
	case "triage":
		return 1
	case "cancel":
		return 2
	case "blocked":
		return 3
	default:
		return 4
	}
}

type attentionScope struct {
	ProjectID string `json:"project_id,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	Q         string `json:"q,omitempty"`
}

func (s attentionScope) valid() bool {
	return attentionKind(s.Kind) && len(s.Q) <= 200 && (s.ProjectID == "" || uuid.MatchString(s.ProjectID)) && (s.Assignee == "" || s.Assignee == "none" || uuid.MatchString(s.Assignee))
}
func (s attentionScope) args() []any { return []any{s.Kind, s.ProjectID, s.Assignee, s.Q} }
func attentionQueryScope(r *http.Request) attentionScope {
	q := r.URL.Query()
	return attentionScope{q.Get("project_id"), q.Get("kind"), q.Get("assignee"), q.Get("q")}
}

// Read-side eligibility uses the same predicates and words as prepareAttention.
// Live grants are resolved once for a bounded SQL projection; the final write
// still rechecks RequireTx under the existing tenant/tree mutation fence.
const attentionPreparedCTE = `, prepared AS (
 SELECT a.*, CASE target WHEN 'triage_list' THEN 'backlog' WHEN 'cancel_suggested' THEN 'cancelled' WHEN 'blocked_reminder' THEN 'open' WHEN 'missed_release' THEN 'release' ELSE target END destination,
 coalesce(r.release_id,'') release_id,coalesce(r.title,'') release_title,coalesce(r.revision,0) release_revision,coalesce(r.project_revision,0) release_project_revision,
 ($5::boolean OR coalesce(a.project_id::text,'')=ANY($6::text[])) editable,
 CASE WHEN aeon_work_status_is_parent(a.node_id) THEN 'Parent statuses follow their children.'
 WHEN a.stale THEN 'Ticket changed since the proposal. Dismiss it and review the ticket.'
 WHEN target='missed_release' AND NOT ($10::boolean AND ($7::boolean OR coalesce(a.project_id::text,'')=ANY($8::text[]))) THEN 'Adding to a release needs permission to manage releases.'
 WHEN target='missed_release' AND r.release_id IS NULL THEN 'Choose a planning release in the ticket’s project first.'
 WHEN kind='proposed' AND $9::boolean THEN 'The server operator has paused status autopilot.' ELSE '' END unavailable
 FROM filtered a
 LEFT JOIN LATERAL (SELECT r.release_node_id::text release_id,n.title,r.revision,j.revision project_revision
 FROM journey_projects j JOIN journey_releases r ON r.tenant_id=j.tenant_id AND r.release_node_id=j.current_release_node_id
 JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id
 WHERE a.target='missed_release' AND j.project_node_id=a.project_id AND r.state='planning' AND n.deleted_at IS NULL
 AND NOT EXISTS(SELECT 1 FROM journey_tickets t WHERE t.ticket_node_id=a.node_id AND t.release_node_id IS NOT NULL)) r ON true
) `

func attentionPreparedArgs(ctx context.Context, tx pgx.Tx, p tenant.Principal, scope attentionScope) ([]any, error) {
	check, err := authz.ProjectsTx(ctx, tx, p)
	if err != nil {
		return nil, err
	}
	writes, err := authz.GrantedProjectIDsTx(ctx, tx, p, "nodes.write")
	if err != nil {
		return nil, err
	}
	releases, err := authz.GrantedProjectIDsTx(ctx, tx, p, "releases.write")
	if err != nil {
		return nil, err
	}
	s, err := Load(ctx, tx)
	if err != nil {
		return nil, err
	}
	return append(scope.args(), check("nodes.write", ""), writes, check("releases.write", ""), releases, s.ServerMode == "off", p.Kind == tenant.Person), nil
}

type attentionGroup struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind,omitempty"`
	ProjectID    string         `json:"project_id,omitempty"`
	Key          string         `json:"key,omitempty"`
	Title        string         `json:"title,omitempty"`
	Total        int            `json:"total"`
	Counts       map[string]int `json:"counts"`
	Applicable   int            `json:"applicable"`
	Editable     int            `json:"editable"`
	OverrideMode string         `json:"override_mode,omitempty"`
	CanManage    bool           `json:"can_manage"`
}
type attentionGroupsPage struct {
	Groups    []attentionGroup `json:"groups"`
	Total     int              `json:"total"`
	Truncated bool             `json:"truncated"`
}

func (m *Module) attentionGroups(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	scope := attentionQueryScope(r)
	by := r.URL.Query().Get("by")
	if !scope.valid() || by != "project" && by != "kind" {
		httpapi.WriteError(w, 400, "invalid filters or grouping")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := attentionGroupsPage{Groups: []attentionGroup{}}
	err := db.InTenantReadSnapshot(db.WithReadStatementTimeout(ctx, 8*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		args, err := attentionPreparedArgs(ctx, tx, p, scope)
		if err != nil {
			return err
		}
		check, err := authz.ProjectsTx(ctx, tx, p)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, attentionCTE+attentionPreparedCTE+`, counts AS (
 SELECT CASE WHEN $11='kind' THEN kind ELSE coalesce(project_id::text,'') END id,kind,count(*) total,
 count(*) FILTER(WHERE editable) editable,count(*) FILTER(WHERE editable AND unavailable='') applicable FROM prepared GROUP BY 1,2
 ), groups AS (SELECT id,sum(total)::int total,sum(editable)::int editable,sum(applicable)::int applicable,jsonb_object_agg(kind,total) counts FROM counts GROUP BY id)
 SELECT g.id,g.total,g.editable,g.applicable,g.counts,coalesce(n.key,''),coalesce(n.title,''),coalesce(o.mode,'inherit'),sum(g.total) OVER()::int
 FROM groups g LEFT JOIN nodes n ON $11='project' AND n.id=nullif(CASE WHEN $11='project' THEN g.id ELSE '' END,'')::uuid
 LEFT JOIN status_autopilot_projects o ON o.project_id=n.id
 ORDER BY CASE WHEN $11='kind' THEN CASE g.id WHEN 'proposed' THEN 0 WHEN 'triage' THEN 1 WHEN 'cancel' THEN 2 WHEN 'blocked' THEN 3 ELSE 4 END ELSE 0 END,g.id LIMIT 501`, append(args, by)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var g attentionGroup
			if err = rows.Scan(&g.ID, &g.Total, &g.Editable, &g.Applicable, &g.Counts, &g.Key, &g.Title, &g.OverrideMode, &out.Total); err != nil {
				return err
			}
			if by == "project" {
				g.ProjectID = g.ID
				g.CanManage = check("settings.manage", g.ID)
			} else {
				g.Kind = g.ID
				g.OverrideMode = ""
				g.CanManage = check("settings.manage", "")
			}
			out.Groups = append(out.Groups, g)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if len(out.Groups) > 500 {
			out.Groups = out.Groups[:500]
			out.Truncated = true
		}
		return nil
	})
	respond(w, out, err)
}

type attentionBulkRequest struct {
	Action       string         `json:"action"`
	Scope        attentionScope `json:"scope"`
	Exclude      []string       `json:"exclude"`
	Through      int64          `json:"through_event_id"`
	DryRun       *bool          `json:"dry_run"`
	PreviewToken string         `json:"preview_token"`
}
type attentionSkipped struct {
	Reason     string   `json:"reason"`
	Count      int      `json:"count"`
	SampleKeys []string `json:"sample_keys"`
}
type attentionMove struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	ReleaseID    string   `json:"release_id,omitempty"`
	ReleaseTitle string   `json:"release_title,omitempty"`
	Count        int      `json:"count"`
	SampleKeys   []string `json:"sample_keys"`
}
type attentionPreview struct {
	Total     int                `json:"total"`
	Moves     []attentionMove    `json:"moves"`
	Skipped   []attentionSkipped `json:"skipped"`
	Through   int64              `json:"through_event_id"`
	Token     string             `json:"preview_token"`
	Limit     int                `json:"limit"`
	Truncated bool               `json:"truncated"`
}
type attentionFailure struct {
	EventID int64  `json:"event_id"`
	Key     string `json:"key"`
	Error   string `json:"error"`
}
type attentionBulkResult struct {
	BatchID   string             `json:"batch_id"`
	Changed   int                `json:"changed"`
	Skipped   []attentionSkipped `json:"skipped"`
	Failed    []attentionFailure `json:"failed"`
	Completed bool               `json:"completed"`
}
type attentionSnapshot struct {
	Preview attentionPreview `json:"preview"`
	Items   []attentionItem  `json:"items"`
}
type attentionProgress struct {
	Next     int                                `json:"next"`
	Result   attentionBulkResult                `json:"result"`
	Advances map[string]attentionReleaseAdvance `json:"advances"`
}

func newAttentionProgress(id string) attentionProgress {
	return attentionProgress{Result: attentionBulkResult{BatchID: id, Skipped: []attentionSkipped{}, Failed: []attentionFailure{}}, Advances: map[string]attentionReleaseAdvance{}}
}
func attentionMoveID(item attentionItem) string {
	if item.To == "release" {
		return "m:" + item.ReleaseID
	}
	if item.Kind == "proposed" {
		return "p:" + item.From + ">" + item.To
	}
	return item.Kind
}
func addAttentionSkip(out *[]attentionSkipped, reason, key string) {
	for i := range *out {
		if (*out)[i].Reason == reason {
			(*out)[i].Count++
			addAttentionSample(&(*out)[i].SampleKeys, key)
			return
		}
	}
	samples := []string{}
	addAttentionSample(&samples, key)
	*out = append(*out, attentionSkipped{reason, 1, samples})
}
func addAttentionSample(out *[]string, key string) {
	if key != "" && len(*out) < 4 && !slices.Contains(*out, key) {
		*out = append(*out, key)
	}
}
func attentionSkipReason(action string, item attentionItem) string {
	if !item.Editable {
		return "You may no longer edit this ticket."
	}
	if action == "apply" && !item.Applicable {
		return item.Unavailable
	}
	return ""
}
func (m *Module) attentionBulk(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var in attentionBulkRequest
	if decodeAttention(w, r, &in) != nil || in.DryRun == nil || in.Action != "apply" && in.Action != "dismiss" || !in.Scope.valid() || in.Scope.ProjectID == "" && in.Scope.Kind == "" || len(in.Exclude) > attentionBulkLimit || in.Through < 0 {
		httpapi.WriteError(w, 400, "action, group scope and dry_run required")
		return
	}
	// Exclusions are a set; normalise ordering for exact idempotency comparisons.
	slices.Sort(in.Exclude)
	for i, id := range in.Exclude {
		if id == "" || len(id) > 100 || i > 0 && in.Exclude[i-1] == id {
			httpapi.WriteError(w, 400, "invalid or duplicate excluded move")
			return
		}
	}
	if in.Exclude == nil {
		in.Exclude = []string{}
	}
	if *in.DryRun {
		m.previewAttentionBulk(w, r, p, in)
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !uuid.MatchString(in.PreviewToken) || in.Through < 0 || key == "" || len(key) > 128 || strings.TrimSpace(key) != key {
		httpapi.WriteError(w, 400, "preview_token, through_event_id and Idempotency-Key required")
		return
	}
	m.runAttentionBulk(w, r, p, in, key)
}
func (m *Module) previewAttentionBulk(w http.ResponseWriter, r *http.Request, p tenant.Principal, in attentionBulkRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	snapshot := attentionSnapshot{Items: []attentionItem{}, Preview: attentionPreview{Moves: []attentionMove{}, Skipped: []attentionSkipped{}, Limit: attentionBulkLimit}}
	err := db.InTenant(db.WithReadStatementTimeout(attentionMutationContext(ctx), 8*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		args, err := attentionPreparedArgs(ctx, tx, p, in.Scope)
		if err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, attentionCTE+`SELECT coalesce(max(event_id),0) FROM filtered`, in.Scope.args()...).Scan(&snapshot.Preview.Through); err != nil {
			return err
		}
		if in.Through > 0 && in.Through < snapshot.Preview.Through {
			snapshot.Preview.Through = in.Through
		}
		args = append(args, snapshot.Preview.Through)
		if err = tx.QueryRow(ctx, attentionCTE+`SELECT count(*) FROM filtered WHERE event_id<=$5`, append(in.Scope.args(), snapshot.Preview.Through)...).Scan(&snapshot.Preview.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, attentionCTE+attentionPreparedCTE+`SELECT `+attentionColumns+`,destination,release_id,release_title,release_revision,release_project_revision,editable,unavailable FROM prepared WHERE event_id<=$11 ORDER BY `+attentionOrder+`,at DESC,event_id DESC LIMIT 1000`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item attentionItem
			var destination string
			if err = rows.Scan(&item.EventID, &item.NodeID, &item.Key, &item.Title, &item.ProjectID, &item.Revision, &item.Kind, &item.From, &item.To, &item.Reason, &item.At, &item.stale, &destination, &item.ReleaseID, &item.ReleaseTitle, &item.ReleaseRevision, &item.ReleaseProjectRevision, &item.Editable, &item.Unavailable); err != nil {
				rows.Close()
				return err
			}
			item.To = destination
			item.Applicable = item.Editable && item.Unavailable == ""
			snapshot.Items = append(snapshot.Items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		snapshot.Preview.Truncated = snapshot.Preview.Total > len(snapshot.Items)
		for _, item := range snapshot.Items {
			reason := attentionSkipReason(in.Action, item)
			if slices.Contains(in.Exclude, attentionMoveID(item)) {
				reason = "Excluded from the preview."
			}
			if reason != "" {
				addAttentionSkip(&snapshot.Preview.Skipped, reason, item.Key)
				continue
			}
			id := attentionMoveID(item)
			index := slices.IndexFunc(snapshot.Preview.Moves, func(move attentionMove) bool { return move.ID == id })
			if index < 0 {
				snapshot.Preview.Moves = append(snapshot.Preview.Moves, attentionMove{ID: id, Kind: item.Kind, From: item.From, To: item.To, ReleaseID: item.ReleaseID, ReleaseTitle: item.ReleaseTitle, SampleKeys: []string{}})
				index = len(snapshot.Preview.Moves) - 1
			}
			move := &snapshot.Preview.Moves[index]
			move.Count++
			addAttentionSample(&move.SampleKeys, item.Key)
		}
		scope, _ := json.Marshal(in.Scope)
		// Allocate the opaque token before serialising its snapshot.
		if err = tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&snapshot.Preview.Token); err != nil {
			return err
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO attention_batches(tenant_id,id,actor_id,action,scope,through_event_id,snapshot) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, snapshot.Preview.Token, p.ID, in.Action, scope, snapshot.Preview.Through, raw)
		return err
	})
	respond(w, snapshot.Preview, err)
}

// Every mutating transaction retains the authentication guard and refreshes
// visibility under the same tenant/pairing/tree fences as individual actions.
func attentionMutationContext(ctx context.Context) context.Context {
	return db.WithAdditionalTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
		if err := db.LockTenant(ctx, tx, tid); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aeon-pairing:"+tid); err != nil {
			return err
		}
		return lock(ctx, tx)
	})
}

// A dedicated connection holds the scope fence across independent item commits.
// PostgreSQL releases it if the process dies; durable progress lets an exact
// idempotent retry continue, rather than repeating already committed items.
func (m *Module) attentionScopeLock(ctx context.Context, tenantID string, scope attentionScope) (*pgxpool.Conn, string, error) {
	raw, _ := json.Marshal(scope)
	key := "attention-bulk:" + tenantID + ":" + string(raw)
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return nil, "", err
	}
	var acquired bool
	if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&acquired); err != nil || !acquired {
		conn.Release()
		if err == nil {
			err = events.ErrConflict
		}
		return nil, "", err
	}
	return conn, key, nil
}
func releaseAttentionScopeLock(conn *pgxpool.Conn, key string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var unlocked bool
	if err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&unlocked); err != nil || !unlocked {
		_ = conn.Conn().Close(ctx)
	}
	conn.Release()
}
func attentionAdvance(progress *attentionProgress, item attentionInput, result attentionResult) {
	if result.ReleaseID == "" {
		return
	}
	a, found := progress.Advances[result.ReleaseID]
	if !found {
		a.Original = item.ReleaseRevision
		a.OriginalProject = item.ReleaseProjectRevision
	}
	a.Current = result.ReleaseRevision
	a.CurrentProject = result.ReleaseProjectRevision
	progress.Advances[result.ReleaseID] = a
}
func attentionResultError(err error) string {
	switch {
	case errors.Is(err, events.ErrConflict):
		return "The ticket or suggestion changed. Reload before trying again."
	case errors.Is(err, authz.ErrForbidden), errors.Is(err, events.ErrForbidden):
		return "You may no longer edit this ticket."
	case errors.Is(err, pgx.ErrNoRows):
		return "The ticket or suggestion is no longer available."
	default:
		return "The suggestion could not be changed. Try again."
	}
}
func lockAttentionBatch(ctx context.Context, tx pgx.Tx, p tenant.Principal, id string, undo bool) error {
	var actor string
	if err := tx.QueryRow(ctx, `SELECT actor_id::text FROM attention_batches WHERE id=$1 AND created_at>clock_timestamp()-interval '24 hours' FOR NO KEY UPDATE`, id).Scan(&actor); err != nil {
		return err
	}
	if actor != p.ID {
		if !undo {
			return authz.ErrForbidden
		}
		if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
			return err
		}
	}
	return authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{AnyProject: true})
}
func saveAttentionProgress(ctx context.Context, tx pgx.Tx, id string, progress attentionProgress, undo bool, resolution int64) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	column := "progress"
	if undo {
		column = "undo_progress"
	}
	// The batch row is already locked, before node/resource/event-counter locks.
	_, err = tx.Exec(ctx, `UPDATE attention_batches SET `+column+`=$2,item_events=CASE WHEN $3::bigint=0 THEN item_events ELSE array_append(item_events,$3) END WHERE id=$1`, id, raw, resolution)
	return err
}
func (m *Module) saveAttentionStep(ctx context.Context, p tenant.Principal, id string, progress attentionProgress, undo bool) error {
	return db.InTenant(attentionMutationContext(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockAttentionBatch(ctx, tx, p, id, undo); err != nil {
			return err
		}
		return saveAttentionProgress(ctx, tx, id, progress, undo, 0)
	})
}
func (m *Module) runAttentionBulk(w http.ResponseWriter, r *http.Request, p tenant.Principal, in attentionBulkRequest, key string) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	conn, lockKey, err := m.attentionScopeLock(ctx, p.TenantID, in.Scope)
	if errors.Is(err, events.ErrConflict) {
		httpapi.WriteError(w, 409, attentionBulkConflict)
		return
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer releaseAttentionScopeLock(conn, lockKey)
	request, _ := json.Marshal(in)
	var snapshot attentionSnapshot
	progress := newAttentionProgress(in.PreviewToken)
	var completed bool
	err = db.InTenant(attentionMutationContext(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		var previousKey *string
		var previousRequest, raw, stored []byte
		var action string
		var scope attentionScope
		var through int64
		// Scope matching is checked before any state change. Token possession never
		// grants another actor access to a preview or an idempotency receipt.
		err := tx.QueryRow(ctx, `SELECT action,scope,through_event_id,snapshot,idempotency_key,request,progress,completed_at IS NOT NULL FROM attention_batches WHERE id=$1 AND actor_id=$2 AND created_at>clock_timestamp()-interval '24 hours' FOR NO KEY UPDATE`, in.PreviewToken, p.ID).Scan(&action, &scope, &through, &raw, &previousKey, &previousRequest, &stored, &completed)
		if err != nil {
			return err
		}
		if action != in.Action || scope != in.Scope || through != in.Through {
			return events.ErrConflict
		}
		if previousKey != nil && (*previousKey != key || string(previousRequest) != string(request)) {
			var previous attentionBulkRequest
			if json.Unmarshal(previousRequest, &previous) != nil || *previousKey != key || !attentionBulkRequestsEqual(previous, in) {
				return events.ErrConflict
			}
		}
		if err = json.Unmarshal(raw, &snapshot); err != nil {
			return err
		}
		if stored != nil {
			if err = json.Unmarshal(stored, &progress); err != nil {
				return err
			}
		}
		for _, id := range in.Exclude {
			if !slices.ContainsFunc(snapshot.Items, func(item attentionItem) bool { return attentionMoveID(item) == id }) {
				return events.ErrConflict
			}
		}
		if previousKey == nil {
			var exists bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM attention_batches WHERE actor_id=$1 AND idempotency_key=$2)`, p.ID, key).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return events.ErrConflict
			}
			_, err = tx.Exec(ctx, `UPDATE attention_batches SET idempotency_key=$2,request=$3 WHERE id=$1`, in.PreviewToken, key, request)
		}
		return err
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	if completed {
		respond(w, progress.Result, nil)
		return
	}
	m.processAttentionBatch(ctx, p, in.PreviewToken, in.Action, in.Scope, in.Exclude, snapshot.Items, &progress, false)
	respond(w, progress.Result, nil)
}
func attentionBulkRequestsEqual(a, b attentionBulkRequest) bool {
	return a.Action == b.Action && a.Scope == b.Scope && a.Through == b.Through && a.PreviewToken == b.PreviewToken && a.DryRun != nil && b.DryRun != nil && *a.DryRun == *b.DryRun && slices.Equal(a.Exclude, b.Exclude)
}

func (m *Module) attentionBulkUndo(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("batch_id")
	if !uuid.MatchString(id) {
		httpapi.WriteError(w, 400, "invalid batch identity")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var scope attentionScope
	var items []attentionItem
	var resolutions []int64
	var run attentionProgress
	progress := newAttentionProgress(id)
	var completed bool
	// Read only scope first; authorisation and snapshot access are repeated under
	// the final tenant and batch fence, after taking the scope session lock.
	err := db.InTenant(db.WithReadStatementTimeout(ctx, 3*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT scope FROM attention_batches WHERE id=$1 AND completed_at IS NOT NULL AND created_at>clock_timestamp()-interval '24 hours'`, id).Scan(&scope)
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	conn, lockKey, err := m.attentionScopeLock(ctx, p.TenantID, scope)
	if errors.Is(err, events.ErrConflict) {
		httpapi.WriteError(w, 409, attentionBulkConflict)
		return
	}
	if err != nil {
		respond(w, nil, err)
		return
	}
	defer releaseAttentionScopeLock(conn, lockKey)
	err = db.InTenant(attentionMutationContext(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := lockAttentionBatch(ctx, tx, p, id, true); err != nil {
			return err
		}
		var actor string
		var snapshotRaw, runRaw, undoRaw []byte
		var currentScope attentionScope
		if err := tx.QueryRow(ctx, `SELECT actor_id::text,scope,snapshot,item_events,progress,undo_progress,undone_at IS NOT NULL FROM attention_batches WHERE id=$1`, id).Scan(&actor, &currentScope, &snapshotRaw, &resolutions, &runRaw, &undoRaw, &completed); err != nil {
			return err
		}
		if currentScope != scope {
			return events.ErrConflict
		}
		if actor != p.ID {
			if err := authz.RequireTx(ctx, tx, p, "settings.manage", authz.Scope{}); err != nil {
				return err
			}
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.write", authz.Scope{AnyProject: true}); err != nil {
			return err
		}
		var snapshot attentionSnapshot
		if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
			return err
		}
		items = snapshot.Items
		if err := json.Unmarshal(runRaw, &run); err != nil {
			return err
		}
		if undoRaw != nil {
			return json.Unmarshal(undoRaw, &progress)
		}
		return nil
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	if completed {
		respond(w, progress.Result, nil)
		return
	}
	// Successful resolutions may be a subset of the preview. Obtain only those
	// identities and immutable after revisions, under event and node RLS.
	undoItems := []attentionItem{}
	err = db.InTenantReadSnapshot(db.WithReadStatementTimeout(ctx, 3*time.Second), m.pool, p.TenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,(metadata->'resolution'->>'original')::bigint,(after->>'updated_at')::timestamptz FROM events WHERE id=ANY($1::bigint[]) ORDER BY array_position($1::bigint[],id)`, resolutions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var resolution, original int64
			var revision time.Time
			if err = rows.Scan(&resolution, &original, &revision); err != nil {
				return err
			}
			index := slices.IndexFunc(items, func(item attentionItem) bool { return item.EventID == original })
			if index < 0 {
				return fmt.Errorf("batch identity missing")
			}
			item := items[index]
			item.Resolution = resolution
			item.Revision = revision
			if a, found := run.Advances[item.ReleaseID]; found {
				item.ReleaseRevision = a.Current
				item.ReleaseProjectRevision = a.CurrentProject
			}
			undoItems = append(undoItems, item)
		}
		return rows.Err()
	})
	if err != nil {
		respond(w, nil, err)
		return
	}
	// Missing event rows are explicit failures; never silently call a partial
	// RLS projection a complete undo. Preserve index order across retries.
	for _, resolution := range resolutions {
		if !slices.ContainsFunc(undoItems, func(item attentionItem) bool { return item.Resolution == resolution }) {
			undoItems = append(undoItems, attentionItem{attentionInput: attentionInput{Resolution: resolution}})
		}
	}
	m.processAttentionBatch(ctx, p, id, "undo", scope, nil, undoItems, &progress, true)
	respond(w, progress.Result, nil)
}

func (m *Module) processAttentionBatch(ctx context.Context, p tenant.Principal, id, action string, scope attentionScope, exclude []string, items []attentionItem, progress *attentionProgress, undo bool) {
	// Reserve the final two seconds for a durable audit/result transaction.
	deadline, _ := ctx.Deadline()
	workCtx, cancel := context.WithDeadline(ctx, deadline.Add(-2*time.Second))
	defer cancel()
	for progress.Next < len(items) && workCtx.Err() == nil {
		item := items[progress.Next]
		reason := ""
		if !undo {
			reason = attentionSkipReason(action, item)
			if slices.Contains(exclude, attentionMoveID(item)) {
				reason = "Excluded from the preview."
			}
		}
		var result attentionResult
		var err error
		if reason == "" {
			input := item.attentionInput
			input.BatchID = id
			err = db.InTenant(attentionMutationContext(workCtx), m.pool, p.TenantID, func(tx pgx.Tx) error {
				if err := db.SetLocalStatementTimeout(workCtx, tx, 3*time.Second); err != nil {
					return err
				}
				if err := lockAttentionBatch(workCtx, tx, p, id, undo); err != nil {
					return err
				}
				if undo && input.NodeID == "" {
					return pgx.ErrNoRows
				}
				if err := m.resolveAttention(workCtx, tx, p, action, input, &result, progress.Advances); err != nil {
					return err
				}
				// Work on a copy: a failed commit must not advance the public counters.
				next := cloneAttentionProgress(*progress)
				next.Next++
				next.Result.Changed++
				attentionAdvance(&next, input, result)
				resolution := result.Resolution
				if undo {
					resolution = 0
				}
				return saveAttentionProgress(workCtx, tx, id, next, undo, resolution)
			})
			if err == nil {
				progress.Next++
				progress.Result.Changed++
				attentionAdvance(progress, input, result)
				continue
			}
			if workCtx.Err() != nil {
				break
			}
			if !undo && errors.Is(err, events.ErrConflict) {
				reason = "Changed since the preview."
			}
			if !undo && (errors.Is(err, authz.ErrForbidden) || errors.Is(err, events.ErrForbidden)) {
				reason = "You may no longer edit this ticket."
			}
			if !undo && errors.Is(err, pgx.ErrNoRows) {
				reason = "The ticket or suggestion is no longer available."
			}
		}
		next := cloneAttentionProgress(*progress)
		next.Next++
		if reason != "" {
			addAttentionSkip(&next.Result.Skipped, reason, item.Key)
		} else {
			next.Result.Failed = append(next.Result.Failed, attentionFailure{item.EventID, item.Key, attentionResultError(err)})
		}
		if err = m.saveAttentionStep(workCtx, p, id, next, undo); err != nil {
			break
		}
		*progress = next
	}
	final := cloneAttentionProgress(*progress)
	final.Result.Completed = final.Next == len(items)
	if !final.Result.Completed {
		remaining := attentionSkipped{Reason: "The bulk deadline was reached; these items were not processed.", Count: len(items) - final.Next, SampleKeys: []string{}}
		for _, item := range items[final.Next:] {
			addAttentionSample(&remaining.SampleKeys, item.Key)
		}
		final.Result.Skipped = append(final.Result.Skipped, remaining)
	}
	err := db.InTenant(attentionMutationContext(ctx), m.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := db.SetLocalStatementTimeout(ctx, tx, time.Second); err != nil {
			return err
		}
		if err := lockAttentionBatch(ctx, tx, p, id, undo); err != nil {
			return err
		}
		// A deadline result is terminal and replayable for run. Undo may resume its
		// stored progress, without the transient unprocessed count being persisted.
		stored := final
		if undo && !final.Result.Completed {
			stored = cloneAttentionProgress(*progress)
		}
		if err := saveAttentionProgress(ctx, tx, id, stored, undo, 0); err != nil {
			return err
		}
		if undo {
			if final.Result.Completed {
				_, err := tx.Exec(ctx, `UPDATE attention_batches SET undone_at=clock_timestamp() WHERE id=$1`, id)
				return err
			}
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE attention_batches SET completed_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		raw, err := json.Marshal(map[string]any{"job": Job, "batch_id": id, "action": action, "scope": scope, "changed": final.Result.Changed, "skipped": final.Result.Skipped, "failed": len(final.Result.Failed), "completed": final.Result.Completed})
		if err != nil {
			return err
		}
		_, err = events.Append(ctx, tx, p, events.Change{Type: "status_autopilot.attention_bulk", Metadata: raw})
		return err
	})
	if err != nil {
		final.Result.Completed = false
		final.Result.Failed = append(final.Result.Failed, attentionFailure{Error: "The batch result could not be recorded. Retry the same request."})
	}
	progress.Result = final.Result
}
func cloneAttentionProgress(p attentionProgress) attentionProgress {
	out := p
	out.Advances = map[string]attentionReleaseAdvance{}
	for id, a := range p.Advances {
		out.Advances[id] = a
	}
	out.Result.Skipped = append([]attentionSkipped{}, p.Result.Skipped...)
	for i := range out.Result.Skipped {
		out.Result.Skipped[i].SampleKeys = append([]string{}, p.Result.Skipped[i].SampleKeys...)
	}
	out.Result.Failed = append([]attentionFailure{}, p.Result.Failed...)
	return out
}
