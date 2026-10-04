// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("delivery record not found")
	ErrTransition   = &Conflict{"release_transition", "This release cannot make that transition."}
	ErrEmptyRelease = &Conflict{"release_empty", "At least one completed unit is required."}
)

// Store owns releases-mode writes only. It never adopts projects, writes the
// journey archive, mounts endpoints, or authorizes spending.
type Store struct {
	pool                          *pgxpool.Pool
	now                           func() time.Time
	productTenant, productProject string
}

// WithProductProject pins the explicitly bound product; names never infer a product from a project title.
func (s *Store) WithProductProject(tenantID, projectID string) *Store {
	copy := *s
	copy.productTenant, copy.productProject = tenantID, projectID
	return &copy
}
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool, now: time.Now} }

// WithClock returns a store using the trusted server clock supplied by its
// caller. Request timestamps must never be used as clocks.
func (s *Store) WithClock(now func() time.Time) *Store {
	copy := *s
	copy.now = now
	return &copy
}

type Release struct {
	UndoEventID   *int64          `json:"undo_event_id"`
	Recovery      *RecoveryCounts `json:"recovery,omitempty"`
	ProjectID     string          `json:"project_id"`
	ID            string          `json:"release_id"`
	Visibility    string          `json:"visibility"`
	Sequence      int             `json:"sequence,omitempty"`
	State         string          `json:"state"`
	Rank          string          `json:"rank"`
	Revision      int64           `json:"revision"`
	EntryClosesAt *time.Time      `json:"entry_closes_at"`
	VersionScheme string          `json:"version_scheme,omitempty"`
	Version       string          `json:"version,omitempty"`
	CutAt         *time.Time      `json:"cut_at,omitempty"`
	ReleasedAt    *time.Time      `json:"released_at,omitempty"`
}

const releaseColumns = `project_node_id::text,release_node_id::text,visibility,coalesce(sequence,0),state,rank,revision,entry_closes_at,coalesce(version_scheme,''),coalesce(version,''),cut_at,released_at`

func scanRelease(row pgx.Row) (Release, error) {
	var r Release
	err := row.Scan(&r.ProjectID, &r.ID, &r.Visibility, &r.Sequence, &r.State, &r.Rank, &r.Revision, &r.EntryClosesAt, &r.VersionScheme, &r.Version, &r.CutAt, &r.ReleasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return r, err
}

type write struct {
	ctx          context.Context
	tx           pgx.Tx
	p            tenant.Principal
	project      string
	now          func() time.Time
	nextSequence int
	changes      []events.Change
}

func transactionLimits(ctx context.Context, tx pgx.Tx) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("delivery write requires an operation deadline")
	}
	remaining := time.Until(deadline).Milliseconds()
	if remaining <= 0 {
		return context.DeadlineExceeded
	}
	_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true),set_config('statement_timeout','30s',true),set_config('transaction_timeout',$1,true),set_config('idle_in_transaction_session_timeout',$1,true)`, fmt.Sprintf("%dms", remaining))
	return err
}

// fence uses the canonical helper: tree, then tenant (pairing, when needed,
// belongs before both). Never substitute a shared tree or reverse the order.
// An empty permission defers RequireTx until the locked operation selects its
// authority; placement/history and abandon must authorize before any commit.
func fence(ctx context.Context, tx pgx.Tx, p tenant.Principal, project, permission string) error {
	if err := transactionLimits(ctx, tx); err != nil {
		return err
	}
	if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
		return err
	}
	if permission == "" {
		return nil
	}
	return authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project})
}

func (s *Store) mutate(ctx context.Context, p tenant.Principal, project, permission string, person bool, fn func(*write) error) error {
	return s.mutateWithReceipt(ctx, p, project, permission, person, nil, fn)
}

// The receipt is assigned from this transaction only; callers discard it on rollback.
func (s *Store) mutateWithReceipt(ctx context.Context, p tenant.Principal, project, permission string, person bool, receipt **int64, fn func(*write) error) error {
	if !uuid(project) || !uuid(p.TenantID) || !uuid(p.ID) {
		return errors.New("delivery write requires UUID identities")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	err := db.InTenant(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := fence(ctx, tx, p, project, permission); err != nil {
			return err
		}
		if person && p.Kind != tenant.Person {
			return authz.ErrForbidden
		}
		w, err := s.projectWrite(ctx, tx, p, project)
		if err != nil {
			return err
		}
		if err = fn(w); err != nil {
			return err
		}
		// No resource locks or writes follow the event-counter acquisition.
		for _, change := range w.changes {
			event, appendErr := events.Append(ctx, tx, p, change)
			if appendErr != nil {
				return appendErr
			}
			if receipt != nil && (change.Type == "ships_in.changed" || change.Type == "release.reranked") {
				id := event.ID
				*receipt = &id
			}
		}
		return ctx.Err()
	})
	return err
}

func (s *Store) projectWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) (*write, error) {
	w := &write{ctx: ctx, tx: tx, p: p, project: project, now: s.now}
	err := tx.QueryRow(ctx, `SELECT d.next_sequence FROM project_delivery d
 JOIN nodes n ON n.tenant_id=d.tenant_id AND n.id=d.project_node_id
 JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE d.tenant_id=$1 AND d.project_node_id=$2 AND n.deleted_at IS NULL AND k.slug='project'
 FOR NO KEY UPDATE OF d`, p.TenantID, project).Scan(&w.nextSequence)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return w, err
}

func (w *write) require(permission string) error {
	return authz.RequireTx(w.ctx, w.tx, w.p, permission, authz.Scope{ProjectID: w.project})
}

func sortedIDs(ids []string) []string {
	sort.Strings(ids)
	out := ids[:0]
	for _, id := range ids {
		if id != "" && (len(out) == 0 || out[len(out)-1] != id) {
			out = append(out, id)
		}
	}
	return out
}

func (w *write) lockReleases(ids []string) (map[string]Release, error) {
	ids = sortedIDs(ids)
	out := map[string]Release{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := w.tx.Query(w.ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=ANY($3::uuid[]) ORDER BY release_node_id FOR NO KEY UPDATE`, w.p.TenantID, w.project, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRelease(rows)
		if err != nil {
			return nil, err
		}
		out[r.ID] = r
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(ids) {
		return nil, ErrNotFound
	}
	return out, nil
}

func (w *write) audit(typ string, node string, before, after any) {
	w.changes = append(w.changes, events.Change{NodeID: &node, Type: typ, Before: before, After: after})
}

// counts includes tombstones and hidden kinds. Every read stops at cap+1;
// checking after writes is safe because any refusal rolls back the whole unit.
func (w *write) counts(releases []string) error {
	var open, pending int
	if err := w.tx.QueryRow(w.ctx, `SELECT count(*) FROM (SELECT 1 FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND state IN ('planned','building','frozen') LIMIT 51) bounded`, w.p.TenantID, w.project).Scan(&open); err != nil {
		return err
	}
	if err := w.tx.QueryRow(w.ctx, `SELECT count(*) FROM (SELECT 1 FROM ships_in s JOIN project_releases r ON r.tenant_id=s.tenant_id AND r.project_node_id=s.project_node_id AND r.release_node_id=s.release_node_id WHERE s.tenant_id=$1 AND s.project_node_id=$2 AND r.visibility='internal' AND r.state='released' AND r.included_in_release_id IS NULL LIMIT 4001) bounded`, w.p.TenantID, w.project).Scan(&pending); err != nil {
		return err
	}
	if err := CheckCounts(0, open, pending); err != nil {
		return err
	}
	for _, id := range sortedIDs(releases) {
		var count int
		if err := w.tx.QueryRow(w.ctx, `SELECT count(*) FROM (SELECT 1 FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 LIMIT 1001) bounded`, w.p.TenantID, w.project, id).Scan(&count); err != nil {
			return err
		}
		if err := CheckCounts(count, open, pending); err != nil {
			return err
		}
	}
	return nil
}

type PlanRequest struct {
	ProjectID      string
	Visibility     string
	Title          string
	CreationKey    string
	EntryClosesAt  *time.Time
	AfterReleaseID string
}

func (s *Store) Plan(ctx context.Context, p tenant.Principal, in PlanRequest) (Release, error) {
	if (in.Visibility != "internal" && in.Visibility != "published") || len(in.Title) > 512 || (in.Title != "" && strings.TrimSpace(in.Title) == "") || len(in.CreationKey) > 128 || (in.AfterReleaseID != "" && !uuid(in.AfterReleaseID)) {
		return Release{}, errors.New("invalid release plan")
	}
	var out Release
	err := s.mutate(ctx, p, in.ProjectID, "releases.write", true, func(w *write) error {
		if in.CreationKey != "" {
			r, err := scanRelease(w.tx.QueryRow(w.ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND creation_key=$3`, p.TenantID, w.project, in.CreationKey))
			if err == nil {
				out = r
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		title := in.Title
		if title == "" {
			title = "Internal release"
			if in.Visibility == "published" {
				title = fmt.Sprintf("Release %d", w.nextSequence)
			}
		}
		r, err := w.plan(in.Visibility, title, in.CreationKey)
		if err != nil {
			return err
		}
		if in.AfterReleaseID != "" {
			rank, e := w.rank(Container{TenantID: p.TenantID, ProjectID: w.project}, Slot{AfterID: in.AfterReleaseID}, r.ID, false)
			if e != nil {
				return e
			}
			r, e = w.setRank(r, rank)
			if e != nil {
				return e
			}
		}
		if in.EntryClosesAt != nil {
			r, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET entry_closes_at=$4 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 RETURNING `+releaseColumns, p.TenantID, w.project, r.ID, in.EntryClosesAt))
			if err != nil {
				return err
			}
		}
		// The creation event describes the final optional position/deadline.
		for i := range w.changes {
			if w.changes[i].Type == "release.planned" && w.changes[i].NodeID != nil && *w.changes[i].NodeID == r.ID {
				w.changes[i].After = r
			}
		}
		out = r
		return w.counts(nil)
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}

// plan is also used by maintenance rollover, under the caller's deploy
// authority. It allocates no sequence for internal releases.
func (w *write) plan(visibility, title, key string) (Release, error) {
	n, err := ReleaseNeighbours(w.ctx, w.tx, w.p.TenantID, w.project, "", "")
	if err != nil {
		return Release{}, err
	}
	last := ""
	if n.Previous != nil {
		last = n.Previous.Rank
	}
	rank, err := After(last)
	if err != nil {
		return Release{}, err
	}
	sequence := 0
	if visibility == "published" {
		sequence = w.nextSequence
		if sequence == 2147483647 {
			return Release{}, &Conflict{"sequence_exhausted", "No further release sequence is available."}
		}
		tag, e := w.tx.Exec(w.ctx, `UPDATE project_delivery SET next_sequence=next_sequence+1,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND next_sequence=$3`, w.p.TenantID, w.project, sequence)
		if e != nil {
			return Release{}, e
		}
		if tag.RowsAffected() != 1 {
			return Release{}, ErrRevisionChanged
		}
		w.nextSequence++
	}
	var node, keyValue string
	if err = w.tx.QueryRow(w.ctx, `SELECT aeon_next_node_key($1::uuid,'REL')`, w.p.TenantID).Scan(&keyValue); err != nil {
		return Release{}, err
	}
	if err = w.tx.QueryRow(w.ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id)
 SELECT $1,id,$2,$3,$4 FROM node_kinds WHERE tenant_id=$1 AND slug='release' RETURNING id::text`, w.p.TenantID, keyValue, title, w.project).Scan(&node); err != nil {
		return Release{}, err
	}
	r, err := scanRelease(w.tx.QueryRow(w.ctx, `INSERT INTO project_releases(tenant_id,project_node_id,release_node_id,visibility,sequence,rank,creation_key) VALUES($1,$2,$3,$4,nullif($5,0),$6,$7) RETURNING `+releaseColumns, w.p.TenantID, w.project, node, visibility, sequence, rank, nullable(key)))
	if err == nil {
		w.audit("release.planned", node, nil, r)
	}
	return r, err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Slot identifies a gap by record identities, never by client-supplied ranks.
// With one neighbour supplied the other is resolved by a bounded index seek.
// With neither supplied the write appends to the ranked portion.
type Slot struct{ BeforeID, AfterID, Position string }

func (w *write) rank(c Container, slot Slot, exclude string, items bool) (string, error) {
	read := func(id string) (*Neighbour, error) {
		if id == exclude {
			return nil, errors.New("an item cannot be its own neighbour")
		}
		table, column := "project_releases", "release_node_id"
		if items {
			table, column = "ships_in", "item_node_id"
		}
		query := `SELECT p.` + column + `::text,p.rank FROM ` + table + ` p JOIN nodes n ON n.tenant_id=p.tenant_id AND n.id=p.` + column + ` JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE p.tenant_id=$1 AND p.project_node_id=$2 AND p.` + column + `=$3 AND n.deleted_at IS NULL AND n.project_id=$2`
		if items {
			query += ` AND k.slug IN ('epic','ticket','task')`
		} else {
			// A caller-selected anchor must still be Upcoming at commit. Terminal
			// ranks remain occupied in the separate physical-neighbour seeks.
			query += ` AND k.slug='release' AND p.state IN ('planned','building','frozen')`
		}
		args := []any{c.TenantID, c.ProjectID, id}
		if items {
			query += ` AND p.release_node_id IS NOT DISTINCT FROM $4::uuid`
			args = append(args, nullable(c.ReleaseID))
		}
		n, err := readNeighbour(w.ctx, w.tx, query, args)
		if err == nil && n == nil {
			err = ErrNotFound
		}
		return n, err
	}
	// Explicit item top uses physical occupied keys, including tombstones.
	// It must not turn an internal extremum into a caller-visible direct anchor.
	if items && slot.Position == "top" {
		n, err := ItemNeighbours(w.ctx, w.tx, c, "", exclude)
		if err != nil {
			return "", err
		}
		if n.Next == nil {
			return After("")
		}
		return Between("", n.Next.Rank)
	}
	if !items && slot.Position != "" {
		order := "ASC"
		if slot.Position == "append" {
			order = "DESC"
		}
		n, err := readNeighbour(w.ctx, w.tx, `SELECT release_node_id::text,rank FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id<>$3 AND state IN ('planned','building','frozen') ORDER BY rank `+order+` LIMIT 1`, []any{c.TenantID, c.ProjectID, exclude})
		if err != nil {
			return "", err
		}
		slot = Slot{}
		if n != nil {
			// Keep terminal occupied keys as physical neighbours of the active extremum.
			physical, err := neighbours(w.ctx, w.tx, c, n.Rank, exclude, false)
			if err != nil {
				return "", err
			}
			if order == "ASC" {
				lower := ""
				if physical.Previous != nil {
					lower = physical.Previous.Rank
				}
				return Between(lower, n.Rank)
			}
			upper := ""
			if physical.Next != nil {
				upper = physical.Next.Rank
			}
			return Between(n.Rank, upper)
		}
	}
	var lower, upper *Neighbour
	var err error
	if slot.AfterID != "" {
		lower, err = read(slot.AfterID)
		if err != nil {
			return "", err
		}
	}
	if slot.BeforeID != "" {
		upper, err = read(slot.BeforeID)
		if err != nil {
			return "", err
		}
	}
	anchor := ""
	if lower != nil {
		anchor = lower.Rank
	} else if upper != nil {
		anchor = upper.Rank
	}
	n, err := neighbours(w.ctx, w.tx, c, anchor, exclude, items)
	if err != nil {
		return "", err
	}
	if lower == nil && upper == nil {
		if n.Previous == nil {
			return After("")
		}
		return After(n.Previous.Rank)
	}
	if lower != nil && upper != nil {
		if n.Next == nil || n.Next.ID != upper.ID {
			return "", &Conflict{"neighbours_changed", "The selected neighbours are no longer adjacent."}
		}
	} else if lower == nil {
		lower = n.Previous
	} else {
		upper = n.Next
	}
	a, b := "", ""
	if lower != nil {
		a = lower.Rank
	}
	if upper != nil {
		b = upper.Rank
	}
	return Between(a, b)
}

// ValidPosition preserves anchor-free append and strict two-anchor legacy callers.
func ValidPosition(position, before, after string) bool {
	return position == "" || (position == "top" || position == "append") && before == "" && after == ""
}

func validSlot(slot Slot) bool {
	return ValidPosition(slot.Position, slot.BeforeID, slot.AfterID) && (slot.BeforeID == "" || uuid(slot.BeforeID)) && (slot.AfterID == "" || uuid(slot.AfterID))
}

type ReleaseEdit struct {
	ProjectID, ReleaseID string
	ExpectedRevision     int64
	Slot                 Slot
}

func (s *Store) Rerank(ctx context.Context, p tenant.Principal, in ReleaseEdit) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 || !validSlot(in.Slot) {
		return Release{}, errors.New("invalid release rank request")
	}
	var out Release
	err := s.mutateWithReceipt(ctx, p, in.ProjectID, "releases.write", true, &out.UndoEventID, func(w *write) error {
		r, err := w.lockReleases([]string{in.ReleaseID})
		if err != nil {
			return err
		}
		old := r[in.ReleaseID]
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		before, err := w.rankSnapshot(old)
		if err != nil {
			return err
		}
		rank, err := w.rank(Container{TenantID: p.TenantID, ProjectID: w.project}, in.Slot, old.ID, false)
		if err != nil {
			return err
		}
		out, err = w.setRank(old, rank)
		if err != nil {
			return err
		}
		after, err := w.rankSnapshot(out)
		if err != nil {
			return err
		}
		w.audit("release.reranked", old.ID, before, after)
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}

func (w *write) setRank(old Release, rank string) (Release, error) {
	if old.Visibility == "published" {
		lower, higher, err := PublishedBounds(w.ctx, w.tx, w.p.TenantID, w.project, old.Sequence)
		if err != nil {
			return Release{}, err
		}
		if err = CheckPublishedRank(old.Sequence, rank, lower, higher); err != nil {
			return Release{}, err
		}
	}
	r, err := scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET rank=$4,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$5 RETURNING `+releaseColumns, w.p.TenantID, w.project, old.ID, rank, old.Revision))
	if errors.Is(err, ErrNotFound) {
		err = ErrRevisionChanged
	}
	return r, err
}

func (s *Store) PromoteRelease(ctx context.Context, p tenant.Principal, in ReleaseEdit) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 {
		return Release{}, errors.New("invalid release conversion")
	}
	var out Release
	err := s.mutate(ctx, p, in.ProjectID, "releases.write", true, func(w *write) error {
		r, err := w.lockReleases([]string{in.ReleaseID})
		if err != nil {
			return err
		}
		old := r[in.ReleaseID]
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		if old.State != "planned" || old.Visibility != "internal" {
			return ErrTransition
		}
		lower, _, err := PublishedBounds(w.ctx, w.tx, p.TenantID, w.project, w.nextSequence)
		if err != nil {
			return err
		}
		anchor := ""
		if lower != nil {
			anchor = lower.Rank
		}
		n, err := ReleaseNeighbours(w.ctx, w.tx, p.TenantID, w.project, anchor, old.ID)
		if err != nil {
			return err
		}
		upper := ""
		if n.Next != nil {
			upper = n.Next.Rank
		}
		rank, err := Between(anchor, upper)
		if err != nil {
			return err
		}
		if w.nextSequence == 2147483647 {
			return ErrOpenReleaseCapacity
		}
		out, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET visibility='published',sequence=$4,rank=$5,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$6 RETURNING `+releaseColumns, p.TenantID, w.project, old.ID, w.nextSequence, rank, old.Revision))
		if err != nil {
			return err
		}
		if _, err = w.tx.Exec(w.ctx, `UPDATE project_delivery SET next_sequence=next_sequence+1,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2`, p.TenantID, w.project); err != nil {
			return err
		}
		w.audit("release.updated", old.ID, old, out)
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}

func (s *Store) SetEntryDeadline(ctx context.Context, p tenant.Principal, in ReleaseEdit, at *time.Time) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 {
		return Release{}, errors.New("invalid deadline edit")
	}
	var out Release
	err := s.mutate(ctx, p, in.ProjectID, "releases.write", true, func(w *write) error {
		r, err := w.lockReleases([]string{in.ReleaseID})
		if err != nil {
			return err
		}
		old := r[in.ReleaseID]
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		if old.State != "planned" {
			return ErrTransition
		}
		out, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET entry_closes_at=$4,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$5 RETURNING `+releaseColumns, p.TenantID, w.project, old.ID, at, old.Revision))
		if err != nil {
			return err
		}
		w.audit("release.updated", old.ID, old, out)
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}
