// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workquery"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidInput = errors.New("invalid delivery input")

func invalidInput(message string) error { return fmt.Errorf("%w: %s", ErrInvalidInput, message) }

type Rollup struct {
	Units     int     `json:"units"`
	Completed int     `json:"completed"`
	OpenHours float64 `json:"open_hours"`
}
type ReleaseView struct {
	DisplayName string       `json:"display_name,omitempty"`
	Matches     *MatchCounts `json:"matches,omitempty"`
	Release
	Title                 string            `json:"title"`
	Body                  string            `json:"body"`
	Rollup                Rollup            `json:"rollup"`
	BuildSettings         BuildSettings     `json:"build_settings"`
	ResolvedBuildSettings BuildSettings     `json:"resolved_build_settings"`
	SettingSources        map[string]string `json:"setting_sources"`
	BuildSummary          map[string]any    `json:"build_summary"`
	ReservationBasis      string            `json:"reservation_basis,omitempty"`
	ReservationRef        string            `json:"reservation_ref,omitempty"`
	ReleasedBy            string            `json:"released_by,omitempty"`
	IncludedIn            string            `json:"included_in_release_id,omitempty"`
}
type ReleasePage struct {
	Matches    *MatchCounts  `json:"matches,omitempty"`
	Items      []ReleaseView `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}
type ReadOptions struct {
	View                              string
	Work                              workquery.Query
	HideClosed                        bool
	HideStates                        []string
	ProductNames                      map[int]string `json:"-"`
	Limit                             int
	Cursor, State, Part, Through      string
	CompletedLater, CompletedUnplaced bool
}
type pageCursor struct {
	Scope       string    `json:"scope"`
	Rank        string    `json:"rank"`
	ReleaseRank string    `json:"release_rank"`
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
}

func encodeCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func decodeCursor(raw, scope string) (pageCursor, error) {
	c := pageCursor{Scope: scope, ID: zeroUUID(""), At: time.Unix(0, 0).UTC()}
	if raw == "" {
		return c, nil
	}
	if len(raw) > 2048 {
		return c, invalidInput("invalid cursor")
	}
	b, e := base64.RawURLEncoding.DecodeString(raw)
	if e != nil {
		return c, invalidInput("invalid cursor")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Scope != scope || !uuid(c.ID) || (c.Rank != "" && !ValidRank(c.Rank)) || (c.ReleaseRank != "" && !ValidRank(c.ReleaseRank)) {
		return c, invalidInput("cursor does not match this read")
	}
	return c, nil
}
func pageLimit(limit, maximum int) (int, error) {
	if limit == 0 {
		return maximum, nil
	}
	if limit < 1 || limit > maximum {
		return 0, invalidInput(fmt.Sprintf("limit must be 1–%d", maximum))
	}
	return limit, nil
}
func (s *Store) read(ctx context.Context, p tenant.Principal, project string, fn func(context.Context, pgx.Tx) error) error {
	if project != "" && !uuid(project) {
		return invalidInput("invalid project identity")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	return db.ReadSnapshot(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if e := transactionLimits(ctx, tx); e != nil {
			return e
		}
		if project != "" {
			if e := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); e != nil {
				return e
			}
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2 AND n.deleted_at IS NULL AND k.slug='project')`, p.TenantID, project).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return ErrNotFound
			}
		}
		return fn(ctx, tx)
	})
}

const estimateSQL = `CASE WHEN jsonb_typeof(n.fields->'estimate_hours')='number' AND n.fields->>'estimate_hours' ~ '^[0-9]{1,7}(\.[0-9]{1,6})?$' THEN (n.fields->>'estimate_hours')::float8 ELSE NULL END`
const releaseViewSQL = `SELECT r.project_node_id::text,r.release_node_id::text,r.visibility,coalesce(r.sequence,0),r.state,r.rank,r.revision,r.entry_closes_at,coalesce(r.version_scheme,''),coalesce(r.version,''),r.cut_at,r.released_at,n.title,n.body,r.build_settings,d.build_defaults,(r.build_authorized_by IS NOT NULL),coalesce(r.reservation_basis,''),r.reservation_ref,coalesce(r.released_by::text,''),coalesce(r.included_in_release_id::text,''),coalesce(stats.units,0),coalesce(stats.completed,0),coalesce(stats.hours,0) FROM project_releases r JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL JOIN project_delivery d ON d.tenant_id=r.tenant_id AND d.project_node_id=r.project_node_id JOIN nodes pn ON pn.tenant_id=r.tenant_id AND pn.id=r.project_node_id AND pn.deleted_at IS NULL LEFT JOIN LATERAL (SELECT count(*) FILTER(WHERE e.kind IN ('ticket','task') AND e.state NOT IN ('cancelled','canceled')) units,count(*) FILTER(WHERE e.kind IN ('ticket','task') AND e.state IN ('done','accepted','delivered')) completed,sum(CASE WHEN e.kind IN ('ticket','task') AND e.state NOT IN ('done','accepted','delivered','cancelled','canceled') THEN ` + estimateSQL + ` ELSE 0 END) hours FROM ` + Effective + ` e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id WHERE e.tenant_id=r.tenant_id AND e.project_node_id=r.project_node_id AND e.release_node_id=r.release_node_id) stats ON true WHERE r.tenant_id=$1 AND r.project_node_id=$2`

func scanView(row pgx.Row, extra ...any) (ReleaseView, error) {
	var v ReleaseView
	var overrides, defaults []byte
	var authorized bool
	dest := []any{&v.ProjectID, &v.ID, &v.Visibility, &v.Sequence, &v.State, &v.Rank, &v.Revision, &v.EntryClosesAt, &v.VersionScheme, &v.Version, &v.CutAt, &v.ReleasedAt, &v.Title, &v.Body, &overrides, &defaults, &authorized, &v.ReservationBasis, &v.ReservationRef, &v.ReleasedBy, &v.IncludedIn, &v.Rollup.Units, &v.Rollup.Completed, &v.Rollup.OpenHours}
	err := row.Scan(append(dest, extra...)...)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = ErrNotFound
		}
		return v, err
	}
	v.BuildSettings, err = ParseBuildSettings(overrides)
	if err != nil {
		return v, err
	}
	d, err := ParseBuildSettings(defaults)
	if err != nil {
		return v, err
	}
	v.ResolvedBuildSettings, v.SettingSources = ResolveBuildSettings(d, v.BuildSettings)
	v.BuildSummary = map[string]any{"authorized": authorized, "budget_outlook": "unknown"}
	return v, nil
}
func (s *Store) GetRelease(ctx context.Context, p tenant.Principal, project, release string) (ReleaseView, error) {
	var out ReleaseView
	if !uuid(release) {
		return out, invalidInput("invalid release identity")
	}
	err := s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = scanView(tx.QueryRow(ctx, releaseViewSQL+` AND r.release_node_id=$3`, p.TenantID, project, release))
		return err
	})
	return out, err
}
func listReleases(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string, opt ReadOptions) (ReleasePage, error) {
	if opt.View == "planning" {
		return planningReleases(ctx, tx, p, project, opt)
	}
	out := ReleasePage{Items: []ReleaseView{}}
	limit, err := pageLimit(opt.Limit, 50)
	if err != nil {
		return out, err
	}
	if opt.State != "" && opt.State != "planned" && opt.State != "building" && opt.State != "frozen" && opt.State != "released" && opt.State != "abandoned" && opt.State != "active" {
		return out, invalidInput("invalid release state")
	}
	scope := p.TenantID + ":" + project + ":releases:" + opt.State
	c, err := decodeCursor(opt.Cursor, scope)
	if err != nil {
		return out, err
	}
	where := ` AND ($3='' OR r.state=$3 OR ($3='active' AND r.state IN ('planned','building','frozen'))) AND (r.rank,r.release_node_id)>($4 COLLATE "C",$5::uuid) ORDER BY r.rank,r.release_node_id LIMIT $6`
	args := []any{p.TenantID, project, opt.State, c.Rank, c.ID, limit + 1}
	if opt.State == "released" {
		where = ` AND r.state=$3 AND ($4::timestamptz IS NULL OR (r.released_at,r.release_node_id)<($4,$5::uuid)) ORDER BY r.released_at DESC,r.release_node_id DESC LIMIT $6`
		var at any
		if opt.Cursor != "" {
			at = c.At
		}
		args[3] = at
	}
	rows, err := tx.Query(ctx, releaseViewSQL+where, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanView(rows)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		v := out.Items[limit-1]
		at := time.Unix(0, 0).UTC()
		if v.ReleasedAt != nil {
			at = *v.ReleasedAt
		}
		out.NextCursor = encodeCursor(pageCursor{Scope: scope, Rank: v.Rank, ID: v.ID, At: at})
	}
	return out, nil
}
func (s *Store) ListReleases(ctx context.Context, p tenant.Principal, project string, opt ReadOptions) (ReleasePage, error) {
	var out ReleasePage
	err := s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if opt.View == "planning" {
			if err = s.productNames(ctx, tx, p, project, &opt); err != nil {
				return err
			}
		}
		out, err = listReleases(ctx, tx, p, project, opt)
		return err
	})
	return out, err
}

type Overview struct {
	Matches          *MatchCounts           `json:"matches,omitempty"`
	BacklogMatches   map[string]MatchCounts `json:"backlog_matches,omitempty"`
	Active           []ReleaseView          `json:"active"`
	ActiveNextCursor string                 `json:"active_next_cursor,omitempty"`
	Released         ReleasePage            `json:"released"`
	Backlog          map[string]int         `json:"backlog"`
	Abandoned        int                    `json:"abandoned"`
	CountsIncomplete bool                   `json:"counts_incomplete"`
}

func (s *Store) Overview(ctx context.Context, p tenant.Principal, project, cursor string) (Overview, error) {
	out := Overview{}
	err := s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		active, e := listReleases(ctx, tx, p, project, ReadOptions{State: "active"})
		if e != nil {
			return e
		}
		out.Active = active.Items
		out.ActiveNextCursor = active.NextCursor
		out.CountsIncomplete = active.NextCursor != ""
		out.Released, e = listReleases(ctx, tx, p, project, ReadOptions{State: "released", Cursor: cursor})
		if e != nil {
			return e
		}
		out.Backlog = map[string]int{}
		var ranked, tail int
		e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE rank IS NOT NULL),count(*) FILTER(WHERE rank IS NULL) FROM (SELECT rank FROM `+Effective+` WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id IS NULL AND state NOT IN ('done','accepted','delivered','cancelled','canceled') LIMIT 10001) bounded`, p.TenantID, project).Scan(&ranked, &tail)
		if e != nil {
			return e
		}
		out.Backlog["ranked"], out.Backlog["tail"] = ranked, tail
		out.CountsIncomplete = out.CountsIncomplete || ranked+tail > 10000
		e = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND state='abandoned' LIMIT 10001) bounded`, p.TenantID, project).Scan(&out.Abandoned)
		out.CountsIncomplete = out.CountsIncomplete || out.Abandoned > 10000
		return e
	})
	return out, err
}

type ItemView struct {
	Placement
	NodeRevision   time.Time  `json:"node_revision"`
	Key            string     `json:"key"`
	Title          string     `json:"title"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"created_at"`
	EstimatedHours *float64   `json:"estimated_hours"`
	EpicID         string     `json:"epic_id,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ReleaseRank    string     `json:"-"`
}
type ItemPage struct {
	Matches    *MatchCounts `json:"matches,omitempty"`
	Items      []ItemView   `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
	Count      int          `json:"count"`
	Incomplete bool         `json:"incomplete"`
}

const itemColumns = `e.item_node_id::text,e.project_node_id::text,coalesce(e.release_node_id::text,''),coalesce(e.rank,''),e.revision,e.expedite,e.due_on::text,n.updated_at,n.key,n.title,e.kind,e.state,e.created_at,` + estimateSQL + `,coalesce((WITH RECURSIVE ancestors AS (SELECT n.parent_id AS id,1 AS depth UNION ALL SELECT a.parent_id,ancestors.depth+1 FROM ancestors JOIN nodes a ON a.tenant_id=n.tenant_id AND a.id=ancestors.id WHERE ancestors.depth<64) SELECT a.id::text FROM ancestors JOIN nodes a ON a.tenant_id=n.tenant_id AND a.id=ancestors.id JOIN node_kinds ak ON ak.tenant_id=a.tenant_id AND ak.id=a.kind_id WHERE ak.slug='epic' AND a.deleted_at IS NULL ORDER BY ancestors.depth LIMIT 1),''),coalesce(e.release_rank,'')`

func scanItem(row pgx.Row) (ItemView, error) {
	var v ItemView
	e := row.Scan(&v.ItemID, &v.ProjectID, &v.ReleaseID, &v.Rank, &v.Revision, &v.Expedite, &v.DueOn, &v.NodeRevision, &v.Key, &v.Title, &v.Kind, &v.State, &v.CreatedAt, &v.EstimatedHours, &v.EpicID, &v.ReleaseRank)
	return v, e
}
func (s *Store) Items(ctx context.Context, p tenant.Principal, project, release string, opt ReadOptions) (ItemPage, error) {
	out := ItemPage{Items: []ItemView{}}
	limit, err := pageLimit(opt.Limit, 200)
	if err != nil {
		return out, err
	}
	if release != "" && !uuid(release) || opt.Through != "" && !uuid(opt.Through) || opt.CompletedLater && opt.Through != "" || opt.CompletedLater && opt.CompletedUnplaced || opt.Through != "" && release == "" || opt.Part != "" && opt.Part != "ranked" && opt.Part != "tail" {
		return out, invalidInput("invalid item filters")
	}
	err = s.read(ctx, p, project, func(ctx context.Context, tx pgx.Tx) error {
		if opt.View == "planning" {
			if opt.CompletedLater || opt.CompletedUnplaced || opt.Through != "" {
				return invalidInput("planning conflicts with recovery or through")
			}
			var e error
			out, e = planningItems(ctx, tx, p, project, release, opt, limit)
			return e
		}
		if e := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); e != nil {
			return e
		}
		if opt.CompletedUnplaced || opt.CompletedLater {
			var e error
			out, e = recoveryItems(ctx, tx, p, project, release, opt, limit)
			return e
		}
		rank := ""
		target := release
		if opt.Through != "" {
			target = opt.Through
		}
		if target != "" {
			if e := tx.QueryRow(ctx, `SELECT rank FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3`, p.TenantID, project, target).Scan(&rank); e != nil {
				if errors.Is(e, pgx.ErrNoRows) {
					return ErrNotFound
				}
				return e
			}
		}
		scope := p.TenantID + ":" + project + ":items:" + release + ":" + opt.Through + ":" + opt.Part
		c, e := decodeCursor(opt.Cursor, scope)
		if e != nil {
			return e
		}
		pred := `e.release_node_id=$3::uuid`
		a3 := any(nullable(release))
		if opt.Through != "" {
			pred = `e.release_rank<=$3 COLLATE "C"`
			a3 = rank
		} else if release == "" {
			pred = `e.release_node_id IS NULL AND e.state NOT IN ('done','accepted','delivered','cancelled','canceled')`
			if opt.Part == "tail" {
				pred += ` AND e.rank IS NULL`
			} else {
				pred += ` AND e.rank IS NOT NULL`
			}
		}
		seek := ` AND (coalesce(e.release_rank,''),coalesce(e.rank,''),e.item_node_id)>($4 COLLATE "C",$5 COLLATE "C",$6::uuid) ORDER BY e.release_rank NULLS LAST,e.rank NULLS LAST,e.item_node_id LIMIT $7`
		args := []any{p.TenantID, project, a3, c.ReleaseRank, c.Rank, c.ID, limit + 1}
		if release == "" && opt.Through == "" {
			pred += ` AND $3::text IS NULL`
		}
		if release == "" && opt.Part == "tail" {
			seek = ` AND (e.created_at,e.item_node_id)>($4::timestamptz,$6::uuid) AND $5::text='' ORDER BY e.created_at,e.item_node_id LIMIT $7`
			args[3] = c.At
		}
		rows, e := tx.Query(ctx, `SELECT `+itemColumns+` FROM `+Effective+` e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id WHERE e.tenant_id=$1 AND e.project_node_id=$2 AND `+pred+seek, args...)
		if e != nil {
			return e
		}
		for rows.Next() {
			v, e := scanItem(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out.Items = append(out.Items, v)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if len(out.Items) > limit {
			out.Items = out.Items[:limit]
			v := out.Items[limit-1]
			out.NextCursor = encodeCursor(pageCursor{Scope: scope, Rank: v.Rank, ReleaseRank: v.ReleaseRank, ID: v.ItemID, At: v.CreatedAt})
		}
		e = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM `+Effective+` e WHERE tenant_id=$1 AND project_node_id=$2 AND `+pred+` LIMIT 10001) bounded`, p.TenantID, project, a3).Scan(&out.Count)
		out.Incomplete = out.Count > 10000
		return e
	})
	return out, err
}

func recoveryItems(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, release string, opt ReadOptions, limit int) (ItemPage, error) {
	out := ItemPage{Items: []ItemView{}}
	if opt.CompletedUnplaced && release != "" || opt.CompletedLater && release == "" {
		return out, invalidInput("invalid recovery scope")
	}
	mode := "unplaced"
	if opt.CompletedLater {
		mode = "later"
	}
	scope := p.TenantID + ":" + project + ":recovery:" + mode + ":" + release
	c, err := decodeCursor(opt.Cursor, scope)
	if err != nil {
		return out, err
	}
	rank := ""
	if release != "" {
		if err = tx.QueryRow(ctx, `SELECT rank FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3`, p.TenantID, project, release).Scan(&rank); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, ErrNotFound
			}
			return out, err
		}
	}
	pred := `e.release_node_id IS NULL`
	if opt.CompletedLater {
		pred = `e.release_rank>$3 COLLATE "C" AND e.release_state IN ('planned','building','frozen')`
	} else {
		pred += ` AND $3::text=''`
	}
	// Admit only this recovery population before the cap. Unrelated completed
	// rows cannot consume its slots; per-node event lookups remain bounded.
	cte := `WITH candidates AS MATERIALIZED (SELECT e.*,n.updated_at FROM ` + recoveryEffective + ` e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id JOIN project_delivery d ON d.tenant_id=e.tenant_id AND d.project_node_id=e.project_node_id WHERE e.tenant_id=$1 AND e.project_node_id=$2 AND e.kind IN ('ticket','task') AND e.state IN ('done','accepted','delivered') AND n.updated_at>=d.adopted_at AND ` + pred + ` ORDER BY n.updated_at DESC,e.item_node_id DESC LIMIT 5001), recovered AS MATERIALIZED (SELECT e.*,episode.at AS completed_at FROM (SELECT * FROM candidates ORDER BY updated_at DESC,item_node_id DESC LIMIT 5000) e JOIN project_delivery d ON d.tenant_id=e.tenant_id AND d.project_node_id=e.project_node_id JOIN LATERAL (SELECT at FROM events ev WHERE ev.tenant_id=e.tenant_id AND ev.node_id=e.item_node_id AND ev.after->>'state' IN ('done','accepted','delivered') AND ev.before->>'state' IS DISTINCT FROM ev.after->>'state' ORDER BY ev.id DESC LIMIT 1) episode ON episode.at>d.adopted_at) `
	err = tx.QueryRow(ctx, cte+`SELECT (SELECT count(*) FROM recovered),(SELECT count(*) FROM candidates)>5000`, p.TenantID, project, rank).Scan(&out.Count, &out.Incomplete)
	if err != nil {
		return out, err
	}
	var at any
	if opt.Cursor != "" {
		at = c.At
	}
	rows, err := tx.Query(ctx, cte+`SELECT `+itemColumns+`,e.completed_at FROM recovered e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id WHERE ($4::timestamptz IS NULL OR (e.completed_at,e.item_node_id)<($4,$5::uuid)) ORDER BY e.completed_at DESC,e.item_node_id DESC LIMIT $6`, p.TenantID, project, rank, at, c.ID, limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var v ItemView
		if err = rows.Scan(&v.ItemID, &v.ProjectID, &v.ReleaseID, &v.Rank, &v.Revision, &v.Expedite, &v.DueOn, &v.NodeRevision, &v.Key, &v.Title, &v.Kind, &v.State, &v.CreatedAt, &v.EstimatedHours, &v.EpicID, &v.ReleaseRank, &v.CompletedAt); err != nil {
			return out, err
		}
		out.Items = append(out.Items, v)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		v := out.Items[limit-1]
		out.NextCursor = encodeCursor(pageCursor{Scope: scope, ID: v.ItemID, At: *v.CompletedAt})
	}
	return out, nil
}

// RecoveryCounts describes omitted completed work; freeze/cut/close never
// silently pull it into a release. A single bounded recovery population feeds both
// headings and event lookup happens only after the candidate admission cap.
type RecoveryCounts struct {
	Unplaced   int  `json:"completed_unplaced"`
	Later      int  `json:"completed_later"`
	Incomplete bool `json:"incomplete"`
}

func recoveryCounts(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Release) (*RecoveryCounts, error) {
	out := &RecoveryCounts{}
	err := tx.QueryRow(ctx, `WITH candidates AS MATERIALIZED (
 SELECT e.*,n.updated_at FROM `+recoveryEffective+` e JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.item_node_id
 JOIN project_delivery d ON d.tenant_id=e.tenant_id AND d.project_node_id=e.project_node_id
 WHERE e.tenant_id=$1 AND e.project_node_id=$2 AND e.kind IN ('ticket','task')
 AND e.state IN ('done','accepted','delivered') AND n.updated_at>=d.adopted_at
 AND (e.release_node_id IS NULL OR (e.release_rank>$3 COLLATE "C" AND e.release_state IN ('planned','building','frozen')))
 ORDER BY n.updated_at DESC,e.item_node_id DESC LIMIT 5001), recovered AS MATERIALIZED (
 SELECT e.* FROM (SELECT * FROM candidates ORDER BY updated_at DESC,item_node_id DESC LIMIT 5000) e
 JOIN project_delivery d ON d.tenant_id=e.tenant_id AND d.project_node_id=e.project_node_id
 JOIN LATERAL (SELECT at FROM events ev WHERE ev.tenant_id=e.tenant_id AND ev.node_id=e.item_node_id
 AND ev.after->>'state' IN ('done','accepted','delivered') AND ev.before->>'state' IS DISTINCT FROM ev.after->>'state'
 ORDER BY ev.id DESC LIMIT 1) episode ON episode.at>d.adopted_at)
 SELECT count(*) FILTER(WHERE release_node_id IS NULL), count(*) FILTER(WHERE release_rank>$3 COLLATE "C" AND release_state IN ('planned','building','frozen')),(SELECT count(*) FROM candidates)>5000 FROM recovered`, p.TenantID, r.ProjectID, r.Rank).Scan(&out.Unplaced, &out.Later, &out.Incomplete)
	return out, err
}
