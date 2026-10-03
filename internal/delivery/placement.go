// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type PlacementRequest struct {
	ItemID                  string
	ExpectedProjectID       string
	ExpectedRevision        int64
	ReleaseID               string
	ExpectedReleaseRevision int64
	Slot                    Slot
	Expedite                bool
	DueOn                   *string
	Top                     bool
	PreserveExpedite        bool
	PreserveDueOn           bool
}

type Placement struct {
	ItemID    string  `json:"item_id"`
	ProjectID string  `json:"project_id"`
	ReleaseID string  `json:"release_id,omitempty"`
	Rank      string  `json:"rank,omitempty"`
	Revision  int64   `json:"revision"`
	Expedite  bool    `json:"expedite"`
	DueOn     *string `json:"due_on"`
	BeforeID  string  `json:"before_id,omitempty"`
	AfterID   string  `json:"after_id,omitempty"`
}

type placementSnapshot struct {
	ProjectID string      `json:"project_id"`
	Members   []Placement `json:"members"`
}

type item struct {
	id, project, kind, state string
	created                  time.Time
	deleted                  *time.Time
}

func validatePlacements(project string, requests []PlacementRequest) error {
	if !uuid(project) || len(requests) == 0 || len(requests) > 100 {
		return errors.New("a placement batch needs 1–100 items in one project")
	}
	seen := map[string]bool{}
	for _, r := range requests {
		if !uuid(r.ItemID) || !uuid(r.ExpectedProjectID) || r.ExpectedRevision < 0 || !validSlot(r.Slot) || r.ReleaseID != "" && (!uuid(r.ReleaseID) || r.ExpectedReleaseRevision < 1) || seen[r.ItemID] {
			return errors.New("invalid or duplicate placement identity/revision")
		}
		if r.DueOn != nil {
			if len(*r.DueOn) != 10 {
				return errors.New("invalid due date")
			}
			if _, err := time.Parse("2006-01-02", *r.DueOn); err != nil {
				return errors.New("invalid due date")
			}
		}
		seen[r.ItemID] = true
	}
	return nil
}

type PlacementResult struct {
	Items           []Placement `json:"items"`
	ReleaseRevision int64       `json:"release_revision,omitempty"`
}

func (s *Store) Place(ctx context.Context, p tenant.Principal, project string, requests []PlacementRequest) ([]Placement, error) {
	result, err := s.PlaceWithRevision(ctx, p, project, requests)
	return result.Items, err
}
func (s *Store) PlaceWithRevision(ctx context.Context, p tenant.Principal, project string, requests []PlacementRequest) (PlacementResult, error) {
	if err := validatePlacements(project, requests); err != nil {
		return PlacementResult{}, err
	}
	var out PlacementResult
	err := s.mutate(ctx, p, project, "", false, func(w *write) error {
		before, after, err := w.place(requests, nil)
		if err != nil {
			return err
		}
		out.Items = after
		if requests[0].ReleaseID != "" {
			if err = w.tx.QueryRow(w.ctx, `SELECT revision FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3`, p.TenantID, project, requests[0].ReleaseID).Scan(&out.ReleaseRevision); err != nil {
				return err
			}
		}
		w.placementEvents(before, after)
		return nil
	})
	if err != nil {
		return PlacementResult{}, err
	}
	return out, nil
}

// placementLocks takes the entire sorted release -> node -> ships_in batch.
// The tree/project fences already serialize release and placement discovery.
func (w *write) placementLocks(requests []PlacementRequest) (map[string]Release, map[string]item, map[string]Placement, error) {
	ids, releaseIDs := []string{}, []string{}
	for _, r := range requests {
		ids = append(ids, r.ItemID)
		releaseIDs = append(releaseIDs, r.ReleaseID)
	}
	// At most one stale expedite row exists by the unique project index. Add
	// it to the same sorted batch, never acquire its locks after an event.
	for _, r := range requests {
		if !r.Expedite {
			continue
		}
		var id, release string
		err := w.tx.QueryRow(w.ctx, `SELECT s.item_node_id::text,coalesce(s.release_node_id::text,'') FROM ships_in s JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.item_node_id WHERE s.tenant_id=$1 AND s.project_node_id=$2 AND s.expedite AND (n.deleted_at IS NOT NULL OR n.state IN ('done','accepted','delivered','cancelled','canceled')) LIMIT 1`, w.p.TenantID, w.project).Scan(&id, &release)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, err
		}
		if err == nil {
			ids = append(ids, id)
			releaseIDs = append(releaseIDs, release)
		}
		break
	}
	// Cleanup may add one item to the 100-item request. Deduplicate first and
	// bound discovery by this complete mutation set, not the request limit.
	ids = sortedIDs(ids)
	sources := map[string]string{}
	rows, err := w.tx.Query(w.ctx, `SELECT item_node_id::text,coalesce(release_node_id::text,'') FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND item_node_id=ANY($3::uuid[]) LIMIT $4`, w.p.TenantID, w.project, ids, len(ids))
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var itemID, releaseID string
		if err = rows.Scan(&itemID, &releaseID); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		sources[itemID] = releaseID
		releaseIDs = append(releaseIDs, releaseID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, nil, err
	}
	releases, err := w.lockReleases(releaseIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	nodes, placements, err := w.lockItems(ids)
	if err != nil {
		return nil, nil, nil, err
	}
	// The tree fence holds discovery steady. Every existing locked placement
	// must have contributed its exact source, including backlog and tombstones.
	for id, placement := range placements {
		if placement.ProjectID != w.project {
			return nil, nil, nil, ErrProjectChanged
		}
		if placement.Revision == 0 {
			continue
		}
		if source, ok := sources[id]; !ok || source != placement.ReleaseID {
			return nil, nil, nil, errors.New("incomplete placement source discovery")
		}
		if placement.ReleaseID != "" {
			if _, ok := releases[placement.ReleaseID]; !ok {
				return nil, nil, nil, errors.New("unlocked placement source release")
			}
		}
	}
	return releases, nodes, placements, nil
}

func (w *write) lockItems(ids []string) (map[string]item, map[string]Placement, error) {
	ids = sortedIDs(ids)
	nodes, placements := map[string]item{}, map[string]Placement{}
	rows, err := w.tx.Query(w.ctx, `SELECT n.id::text,coalesce(n.project_id::text,''),k.slug,n.state,n.created_at,n.deleted_at
 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.id=ANY($2::uuid[]) ORDER BY n.id FOR SHARE OF n`, w.p.TenantID, ids)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var n item
		if err = rows.Scan(&n.id, &n.project, &n.kind, &n.state, &n.created, &n.deleted); err != nil {
			rows.Close()
			return nil, nil, err
		}
		nodes[n.id] = n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) != len(ids) {
		return nil, nil, ErrNotFound
	}
	rows, err = w.tx.Query(w.ctx, `SELECT item_node_id::text,project_node_id::text,coalesce(release_node_id::text,''),rank,revision,expedite,due_on::text FROM ships_in WHERE tenant_id=$1 AND item_node_id=ANY($2::uuid[]) ORDER BY item_node_id FOR UPDATE`, w.p.TenantID, ids)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var p Placement
		if err = rows.Scan(&p.ItemID, &p.ProjectID, &p.ReleaseID, &p.Rank, &p.Revision, &p.Expedite, &p.DueOn); err != nil {
			rows.Close()
			return nil, nil, err
		}
		placements[p.ItemID] = p
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	for _, id := range ids {
		if _, ok := placements[id]; !ok {
			placements[id] = Placement{ItemID: id, ProjectID: nodes[id].project}
		}
	}
	return nodes, placements, nil
}

func position(n item, p Placement, releases map[string]Release) Position {
	r := releases[p.ReleaseID]
	return Position{ProjectID: n.project, ItemID: n.id, ReleaseID: p.ReleaseID, ReleaseRank: r.Rank, ReleaseState: r.State, Rank: p.Rank, CreatedAt: n.created}
}

func (w *write) neighboursFor(p Placement) (Placement, error) {
	if p.Rank == "" {
		return p, nil
	}
	n, err := ItemNeighbours(w.ctx, w.tx, Container{TenantID: w.p.TenantID, ProjectID: w.project, ReleaseID: p.ReleaseID}, p.Rank, p.ItemID)
	if err != nil {
		return Placement{}, err
	}
	p.BeforeID, p.AfterID = "", ""
	if n.Previous != nil {
		p.AfterID = n.Previous.ID
	}
	if n.Next != nil {
		p.BeforeID = n.Next.ID
	}
	return p, nil
}

// restore is private event data, never client ranks. A missing previous row
// restores the unranked tail, rather than silently inventing a ranked row.
func (w *write) place(requests []PlacementRequest, restore map[string]Placement) ([]Placement, []Placement, error) {
	releases, nodes, placements, err := w.placementLocks(requests)
	if err != nil {
		return nil, nil, err
	}
	now := w.now().UTC()
	before, after := []Placement{}, []Placement{}
	touched := []string{}
	// Preserve all original snapshots before either requested or automatic
	// changes. Cleanup never replaces a requested item's CAS base or snapshot.
	for _, id := range sortedIDs(mapItemIDs(nodes)) {
		placements[id], err = w.neighboursFor(placements[id])
		if err != nil {
			return nil, nil, err
		}
	}
	for i := range requests {
		old := placements[requests[i].ItemID]
		if requests[i].PreserveExpedite {
			requests[i].Expedite = old.Expedite
		}
		if requests[i].PreserveDueOn {
			requests[i].DueOn = old.DueOn
		}
	}
	requested := map[string]bool{}
	// Release unique expedite flags first without changing revisions. Both a
	// forward batch and undo may transfer the flag before the old holder's turn;
	// each requested item still has exactly one final CAS and event member.
	for _, request := range requests {
		requested[request.ItemID] = true
		old := placements[request.ItemID]
		if old.Expedite && !request.Expedite {
			tag, e := w.tx.Exec(w.ctx, `UPDATE ships_in SET expedite=false WHERE tenant_id=$1 AND project_node_id=$2 AND item_node_id=$3 AND revision=$4`, w.p.TenantID, w.project, old.ItemID, old.Revision)
			if e != nil {
				return nil, nil, e
			}
			if tag.RowsAffected() != 1 {
				return nil, nil, ErrRevisionChanged
			}
		}
	}
	for _, id := range sortedIDs(mapItemIDs(nodes)) {
		n, old := nodes[id], placements[id]
		if requested[id] || !old.Expedite || n.deleted == nil && !Completed(n.state) && !Cancelled(n.state) {
			continue
		}
		clear := false
		for _, request := range requests {
			clear = clear || request.Expedite && request.ItemID != id
		}
		if !clear {
			continue
		}
		// Stale expedite cleanup and its flag-only undo share write authority,
		// even when the unchanged container is already released.
		if err = w.require("releases.write"); err != nil {
			return nil, nil, err
		}
		next := old
		next.Expedite = false
		next, err = w.savePlacement(old, next, "build", now)
		if err != nil {
			return nil, nil, err
		}
		before = append(before, old)
		after = append(after, next)
		touched = append(touched, old.ReleaseID)
	}
	for _, request := range requests {
		n := nodes[request.ItemID]
		old := placements[n.id]
		if err = CheckPrecondition(n.project, old.Revision, request.ExpectedProjectID, request.ExpectedRevision); err != nil {
			return nil, nil, err
		}
		if n.project != w.project {
			return nil, nil, ErrProjectChanged
		}
		restored, restoring := restore[n.id]
		// The automatic stale-expedite change retains the existing row's
		// container, rank and due date. Restoring only that flag is maintenance,
		// not permission to insert, move or rerank a deleted item.
		maintenance := restoring && (n.deleted != nil || Completed(n.state) || Cancelled(n.state)) &&
			old.Revision > 0 && restored.Revision > 0 &&
			old.ReleaseID == restored.ReleaseID && old.Rank == restored.Rank &&
			sameDate(old.DueOn, restored.DueOn) && !old.Expedite && restored.Expedite
		if n.deleted != nil && !maintenance || !ItemKind(n.kind) {
			return nil, nil, ErrNotFound
		}
		dest := releases[request.ReleaseID]
		if request.ReleaseID != "" && restore == nil && dest.Revision != request.ExpectedReleaseRevision {
			return nil, nil, ErrRevisionChanged
		}
		history := !maintenance && (releases[old.ReleaseID].State == "released" || dest.State == "released")
		permission := "releases.write"
		if history {
			if w.p.Kind != tenant.Person {
				return nil, nil, ErrHistoryCorrection
			}
			permission = "roles.manage"
		}
		if err = w.require(permission); err != nil {
			if history && errors.Is(err, authz.ErrForbidden) {
				return nil, nil, ErrHistoryCorrection
			}
			return nil, nil, err
		}
		hasWrite := !history
		if history {
			writeErr := w.require("releases.write")
			if writeErr != nil && !errors.Is(writeErr, authz.ErrForbidden) {
				return nil, nil, writeErr
			}
			hasWrite = writeErr == nil
		}
		if request.ReleaseID != "" {
			if err = checkAdmission(w.p.Kind, hasWrite, dest.State, dest.EntryClosesAt, now, request.ReleaseID != old.ReleaseID, history); err != nil {
				return nil, nil, err
			}
		}
		next := Placement{ItemID: n.id, ProjectID: w.project, ReleaseID: request.ReleaseID, Expedite: request.Expedite, DueOn: request.DueOn}
		if restoring {
			next = restored
			if restored.Revision > 0 {
				next.Rank, err = w.restoreRank(restored, true)
			}
		} else {
			if request.Top {
				neighbours, e := ItemNeighbours(w.ctx, w.tx, Container{TenantID: w.p.TenantID, ProjectID: w.project, ReleaseID: request.ReleaseID}, "", n.id)
				if e != nil {
					return nil, nil, e
				}
				if neighbours.Next != nil {
					request.Slot = Slot{BeforeID: neighbours.Next.ID}
				}
			}
			next.Rank, err = w.rank(Container{TenantID: w.p.TenantID, ProjectID: w.project, ReleaseID: request.ReleaseID}, request.Slot, n.id, true)
		}
		if err != nil {
			return nil, nil, err
		}
		if w.p.Kind == tenant.Agent {
			if old.Expedite != next.Expedite || !sameDate(old.DueOn, next.DueOn) {
				return nil, nil, ErrPromotion
			}
			if err = CheckAgentMove(position(n, old, releases), position(n, next, releases)); err != nil {
				return nil, nil, err
			}
		}
		if next.Expedite {
			var occupied bool
			if err = w.tx.QueryRow(w.ctx, `SELECT EXISTS(SELECT 1 FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND expedite AND item_node_id<>$3)`, w.p.TenantID, w.project, n.id).Scan(&occupied); err != nil {
				return nil, nil, err
			}
			if occupied {
				return nil, nil, &Conflict{"expedite_taken", "Another item is already expedited."}
			}
		}
		source := string(w.p.Kind)
		if history {
			source = "correction"
		}
		if next.Rank == "" {
			tag, e := w.tx.Exec(w.ctx, `DELETE FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND item_node_id=$3 AND revision=$4`, w.p.TenantID, w.project, n.id, old.Revision)
			err = e
			if e == nil && tag.RowsAffected() != 1 {
				err = ErrRevisionChanged
			}
			next.Revision = 0
		} else {
			next, err = w.savePlacement(old, next, source, now)
		}
		if err != nil {
			return nil, nil, err
		}
		before = append(before, old)
		after = append(after, next)
		touched = append(touched, old.ReleaseID, next.ReleaseID)
	}
	if err = w.counts(touched); err != nil {
		return nil, nil, err
	}
	for _, id := range sortedIDs(touched) {
		old := releases[id]
		tag, e := w.tx.Exec(w.ctx, `UPDATE project_releases SET revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$4`, w.p.TenantID, w.project, id, old.Revision)
		if e != nil {
			return nil, nil, e
		}
		if tag.RowsAffected() != 1 {
			return nil, nil, ErrRevisionChanged
		}
	}
	for i := range after {
		after[i], err = w.neighboursFor(after[i])
		if err != nil {
			return nil, nil, err
		}
	}
	return before, after, nil
}

func sameDate(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func mapItemIDs(items map[string]item) []string {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	return ids
}

func (w *write) savePlacement(old, next Placement, source string, now time.Time) (Placement, error) {
	var revision int64
	var err error
	if old.Revision == 0 {
		err = w.tx.QueryRow(w.ctx, `INSERT INTO ships_in(tenant_id,project_node_id,item_node_id,release_node_id,rank,expedite,due_on,source,placed_by,placed_at)
 VALUES($1,$2,$3,$4,$5,$6,$7::date,$8,$9,$10) ON CONFLICT (tenant_id,item_node_id) DO NOTHING RETURNING revision`, w.p.TenantID, w.project, next.ItemID, nullable(next.ReleaseID), next.Rank, next.Expedite, next.DueOn, source, w.p.ID, now).Scan(&revision)
	} else {
		err = w.tx.QueryRow(w.ctx, `UPDATE ships_in SET release_node_id=$4,rank=$5,expedite=$6,due_on=$7::date,source=$8,placed_by=$9,placed_at=$10,revision=revision+1
 WHERE tenant_id=$1 AND project_node_id=$2 AND item_node_id=$3 AND revision=$11 RETURNING revision`, w.p.TenantID, w.project, next.ItemID, nullable(next.ReleaseID), next.Rank, next.Expedite, next.DueOn, source, w.p.ID, now, old.Revision).Scan(&revision)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrRevisionChanged
	}
	next.Revision = revision
	return next, err
}

func (w *write) restoreRank(p Placement, items bool) (string, error) {
	table, column := "project_releases", "release_node_id"
	if items {
		table, column = "ships_in", "item_node_id"
	}
	query := `SELECT EXISTS(SELECT 1 FROM ` + table + ` WHERE tenant_id=$1 AND project_node_id=$2 AND rank=$3 AND ` + column + `<>$4`
	args := []any{w.p.TenantID, w.project, p.Rank, p.ItemID}
	if items {
		query += ` AND release_node_id IS NOT DISTINCT FROM $5::uuid`
		args = append(args, nullable(p.ReleaseID))
	}
	query += `)`
	var occupied bool
	if err := w.tx.QueryRow(w.ctx, query, args...).Scan(&occupied); err != nil {
		return "", err
	}
	if !occupied {
		return p.Rank, nil
	}
	// Do not invent a new location when no stored neighbour survives.
	if p.BeforeID == "" && p.AfterID == "" {
		return "", ErrRevisionChanged
	}
	return w.rank(Container{TenantID: w.p.TenantID, ProjectID: w.project, ReleaseID: p.ReleaseID}, Slot{BeforeID: p.BeforeID, AfterID: p.AfterID}, p.ItemID, items)
}

func (w *write) placementEvents(before, after []Placement) {
	for start := 0; start < len(before); start += 100 {
		end := min(start+100, len(before))
		w.audit("ships_in.changed", w.project, placementSnapshot{w.project, before[start:end]}, placementSnapshot{w.project, after[start:end]})
	}
}
