// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const maxFigureRows = 50000
const figuresMaxAge = 30 * time.Second

type figureScope struct {
	Kind          string `json:"kind"`
	Rows          int    `json:"rows"`
	Parents       int    `json:"parents"`
	Leaves        int    `json:"leaves"`
	ProjectLeaves int    `json:"project_leaves"`
	Closed        int    `json:"closed"`
}

type figureGroup struct {
	Hours    float64        `json:"hours"`
	Tickets  int            `json:"tickets"`
	Statuses map[string]int `json:"statuses"`
}

type projectFigures struct {
	ViewKey             string                   `json:"view_key"`
	ComputedAt          time.Time                `json:"computed_at"`
	Scope               figureScope              `json:"scope"`
	MarksReleases       bool                     `json:"marks_releases"`
	TotalHours          float64                  `json:"total_hours"`
	Approximate         bool                     `json:"approximate"`
	WithoutEstimate     int                      `json:"without_estimate"`
	OpenEstimatedLeaves int                      `json:"open_estimated_leaves"`
	Groups              map[string]*figureGroup  `json:"groups"`
	Columns             map[string]*columnFigure `json:"columns"`
}

type figuresEntry struct {
	revision int64
	expires  time.Time
	value    *projectFigures // immutable after publication
}

// Owned by the nodes module, never global. Entries contain already projected
// responses and are partitioned by current RLS visibility and permission facts.
type figuresCache struct {
	mu      sync.Mutex
	entries map[string]figuresEntry
}

func (c *figuresCache) get(key string, revision int64, now time.Time) *projectFigures {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || e.revision != revision || !now.Before(e.expires) {
		delete(c.entries, key)
		return nil
	}
	return e.value
}

func (c *figuresCache) put(key string, revision int64, now time.Time, value *projectFigures) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]figuresEntry{}
	}
	// Remove expired entries first, then the oldest if the bounded cache is full.
	oldest := ""
	var oldestAt time.Time
	for k, e := range c.entries {
		if !now.Before(e.expires) {
			delete(c.entries, k)
			continue
		}
		if oldest == "" || e.expires.Before(oldestAt) {
			oldest, oldestAt = k, e.expires
		}
	}
	if len(c.entries) >= 256 {
		delete(c.entries, oldest)
	}
	// An older concurrent snapshot must not overwrite a newer result.
	if e, ok := c.entries[key]; ok && e.revision > revision {
		return
	}
	c.entries[key] = figuresEntry{revision, now.Add(figuresMaxAge), value}
}

// This separate read is scheduled by the client after first paint. It never
// runs on the list request or on hover, and a warm scope needs no aggregate read.
func (m *Module) handleProjectFigures(w http.ResponseWriter, r *http.Request) {
	p, ok := requirePrincipal(w, r)
	if !ok {
		return
	}
	id, ok := parseUUID(r.PathValue("projectId"))
	if !ok {
		writeErr(w, badRequest("invalid projectId"))
		return
	}
	if len(r.URL.RawQuery) > 16384 {
		writeErr(w, badRequest("figures query exceeds 16 KiB"))
		return
	}
	// Discard presentation controls BEFORE parsing: even an expired cursor or
	// a legacy Hide choice must not change the figures or their scope key.
	copyRequest := r.Clone(r.Context())
	u := *r.URL
	params := u.Query()
	for _, name := range []string{"hide_closed", "hide_states", "sort", "direction", "facets", "limit", "cursor"} {
		params.Del(name)
	}
	if !params.Has("kind") && !params.Has("kind_id") {
		params.Set("kind", "work,ticket,task,epic")
	}
	u.RawQuery = params.Encode()
	copyRequest.URL = &u
	q, err := parseListQuery(copyRequest)
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(q.Q) > 300 {
		writeErr(w, badRequest("figures search exceeds 300 bytes"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	var result *projectFigures
	var publishKey string
	var publishRevision int64
	err = db.InTenantReadSnapshot(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.id=$1::uuid AND n.deleted_at IS NULL AND k.slug='project')`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound("project not found")
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: id}); err != nil {
			return err
		}
		if q.Within == nil {
			q.Within = &id
		} else if *q.Within != id {
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE id=$1::uuid AND project_id=$2::uuid AND deleted_at IS NULL)`, *q.Within, id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return badRequest("within must be inside this project")
			}
		}
		seen, err := assigneeAudience(ctx, tx, p)
		if err != nil {
			return err
		}
		var revision int64
		var all bool
		var projects string
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT last_id FROM event_counters WHERE tenant_id=$1::uuid),0),aeon_visible_all(),aeon_visible_projects()::text`, p.TenantID).Scan(&revision, &all, &projects); err != nil {
			return err
		}
		// Include authority beyond nodes.read because column projections depend
		// on harness.read and members.read. Re-resolve it before EVERY cache hit.
		key := figuresHash([]any{p.TenantID, p.ID, p.KeyID, p.AuthKeyID, p.Scopes, p.FullAccess, all, projects, seen.harnessAll, seen.projects, seen.members, listFingerprint(q)})
		now := time.Now().UTC()
		if cached := m.figures.get(key, revision, now); cached != nil {
			result = cached
			return nil
		}
		result, err = m.computeFigures(ctx, tx, id, q, seen, now)
		if err != nil {
			return err
		}
		result.ViewKey = listFingerprint(q)
		publishKey, publishRevision = key, revision
		return nil
	})
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			writeError(w, http.StatusServiceUnavailable, "figures read did not complete; retry")
		} else if errors.Is(err, authz.ErrForbidden) {
			writeError(w, http.StatusForbidden, "permission denied")
		} else {
			writeErr(w, err)
		}
		return
	}
	if publishKey != "" {
		m.figures.put(publishKey, publishRevision, result.ComputedAt, result)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, result)
}

func figuresHash(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type figureRow struct {
	item         listItem
	leaf         bool
	bucket       string
	status       string
	hours        *float64
	labels       map[string]any
	ep           string
	assignee     string
	assigneeKind string
	eta          *columnETA
	plan         *planningView
}

type columnETA struct {
	Progress *int
	Running  bool
	ReadyAt  *time.Time
}

func (m *Module) computeFigures(ctx context.Context, tx pgx.Tx, project string, q listQuery, seen assigneeSeen, now time.Time) (*projectFigures, error) {
	q.seen = seen
	q.leadYellow, q.leadRed = 3, 10
	if p, ok := tenant.PrincipalFrom(ctx); ok {
		var err error
		q.leadYellow, q.leadRed, err = readLeadMinutes(ctx, tx, p.ID)
		if err != nil {
			return nil, err
		}
	}
	rows, err := readFigureRows(ctx, tx, q)
	if err != nil {
		return nil, err
	}
	out := &projectFigures{ComputedAt: now, Scope: figureScope{Kind: "whole_project", Rows: len(rows)}, Groups: map[string]*figureGroup{}, Columns: map[string]*columnFigure{}}
	for _, group := range []string{"completed", "done_not_live", "in_progress", "open"} {
		out.Groups[group] = &figureGroup{Statuses: map[string]int{}}
	}
	base := listQuery{Within: &project, Kinds: []string{"work", "ticket", "task", "epic"}}
	// Comparison deliberately excludes sorting/paging and presentation flags.
	filterOnly := q
	filterOnly.Limit, filterOnly.Sort = 0, nil
	if listFingerprint(filterOnly) != listFingerprint(base) {
		// Kind values are a set; a caller may omit legacy aliases or order them.
		if !(len(q.KindsNot) == 0 && q.KindID == nil && slices.Contains(q.Kinds, "work")) {
			out.Scope.Kind = "this_view"
		} else {
			filterOnly.Kinds = base.Kinds
			if listFingerprint(filterOnly) != listFingerprint(base) {
				out.Scope.Kind = "this_view"
			}
		}
	}
	// Project totals use the exact same live work-leaf predicate, without the
	// view filters, and retain Closed in the denominator.
	prefix, args := listFilterSQL(base, false)
	if err := tx.QueryRow(ctx, prefix+` SELECT count(*)::int FROM filtered f WHERE `+figureLeafSQL("f.id"), args...).Scan(&out.Scope.ProjectLeaves); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE e.tenant_id=current_setting('aeon.tenant_id')::uuid AND n.project_id=$1::uuid AND k.slug IN ('work','ticket','task','epic') AND e.at >= $2::timestamptz-interval '90 days' AND e.at <= $2 AND `+workStatusSQL("e.after->>'state'")+` IN ('delivered','accepted') AND `+workStatusSQL("e.before->>'state'")+` IS DISTINCT FROM `+workStatusSQL("e.after->>'state'")+`)`, project, now).Scan(&out.MarksReleases); err != nil {
		return nil, err
	}
	// Only leaves need ETA/planning. Chunk roots so existing scope budgets are
	// respected; parents and their overlapping subtrees are never expanded.
	var leaves []*figureRow
	for i := range rows {
		if rows[i].leaf {
			leaves = append(leaves, &rows[i])
		}
	}
	for start := 0; start < len(leaves); start += 200 {
		batch := leaves[start:min(start+200, len(leaves))]
		ids := make([]string, len(batch))
		items := make([]listItem, len(batch))
		for i, row := range batch {
			ids[i], items[i] = row.item.ID, row.item
		}
		_, etas, err := loadWorkTotals(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		plans, err := loadPlanning(ctx, tx, items, seen, nil)
		if err != nil {
			return nil, err
		}
		for _, row := range batch {
			v := etas[row.item.ID]
			row.eta = &columnETA{v.Progress, v.HasWorkingSession, v.ReadyAt}
			row.plan = plans[row.item.ID]
		}
	}
	fillFigureColumns(out, rows, seen.costVisible(project), now)
	return out, nil
}

func figureLeafSQL(id string) string {
	return `NOT EXISTS(SELECT 1 FROM nodes c JOIN node_kinds ck ON ck.tenant_id=c.tenant_id AND ck.id=c.kind_id WHERE c.tenant_id=current_setting('aeon.tenant_id')::uuid AND c.parent_id=` + id + ` AND c.deleted_at IS NULL AND ck.slug IN ('work','ticket','task','epic'))`
}

func readFigureRows(ctx context.Context, tx pgx.Tx, q listQuery) ([]figureRow, error) {
	prefix, args := listFilterSQL(q, false)
	// The projection excludes bodies and oversized legacy field documents.
	// Bounded metadata is checked before it can accumulate in worker memory.
	projects := q.seen.projects
	if projects == nil {
		projects = []string{}
	}
	args = append(args, q.seen.harnessAll, projects, q.seen.members)
	workerJoin := assigneeWorkerJoin("n.id", "n.project_id", fmt.Sprintf("$%d", len(args)-2), fmt.Sprintf("$%d", len(args)-1), fmt.Sprintf("$%d", len(args)), q.leadYellow, q.leadRed, "ap.name IS NULL")
	sql := prefix + `, figure_states AS (WITH ` + workStateCategoryCTE() + ` SELECT * FROM configured)
 SELECT n.id::text,n.key,n.kind_id::text,f.kind_slug,n.state,n.created_at,n.updated_at,n.project_id::text,
 ` + workStatusSQL("n.state") + `,` + workCountBucketSQL("n.state", "cfg") + `,
 f.kind_slug IN ('work','ticket','task','epic') AND ` + figureLeafSQL("n.id") + `,
 ` + estimateHoursSQL("n.fields") + `::float8,
 CASE WHEN octet_length(n.fields::text)<=65536 THEN n.fields END,
 coalesce(ap.name,worker.shown_name,''),coalesce(ap.kind,'agent'),coalesce(epic.key,'')
 FROM filtered f JOIN nodes n ON n.id=f.id
 LEFT JOIN figure_states cfg ON cfg.kind_id=n.kind_id AND cfg.norm=` + workStateNormSQL("n.state") + `
 LEFT JOIN principals ap ON ap.id=f.assignee_id::uuid AND ap.tenant_id=n.tenant_id
 ` + workerJoin + `
 LEFT JOIN LATERAL (
 WITH RECURSIVE up AS (
 SELECT par.id,par.parent_id,par.kind_id,par.key,1 AS depth FROM nodes par WHERE par.id=n.parent_id AND par.deleted_at IS NULL
 UNION ALL SELECT par.id,par.parent_id,par.kind_id,par.key,up.depth+1 FROM up
 JOIN node_kinds uk ON uk.id=up.kind_id AND uk.slug NOT IN ('work','epic','project')
 JOIN nodes par ON par.id=up.parent_id AND par.deleted_at IS NULL WHERE up.depth<32
 ) SELECT u.key FROM up u JOIN node_kinds ek ON ek.id=u.kind_id WHERE ek.slug IN ('work','epic') ORDER BY u.depth LIMIT 1
 ) epic ON true
 ORDER BY regexp_replace(n.key,'-[0-9]+$',''),substring(n.key from '-([0-9]+)$')::numeric,n.key,n.id
 LIMIT ` + fmt.Sprint(maxFigureRows+1)
	result, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	rows := []figureRow{}
	bytes := 0
	for result.Next() {
		if len(rows) == maxFigureRows {
			return nil, unprocessable("figures exceed 50000 rows; narrow the view")
		}
		var row figureRow
		var raw []byte
		var project *string
		if err := result.Scan(&row.item.ID, &row.item.Key, &row.item.KindID, &row.item.KindSlug, &row.item.State, &row.item.CreatedAt, &row.item.UpdatedAt, &project, &row.status, &row.bucket, &row.leaf, &row.hours, &raw, &row.assignee, &row.assigneeKind, &row.ep); err != nil {
			return nil, err
		}
		bytes += len(raw)
		if raw == nil || bytes > 32<<20 {
			return nil, unprocessable("figures metadata exceeds read budget; narrow the view")
		}
		if err := json.Unmarshal(raw, &row.labels); err != nil {
			return nil, err
		}
		if project != nil {
			row.item.Project = &listProject{ID: *project}
		}
		rows = append(rows, row)
	}
	return rows, result.Err()
}
