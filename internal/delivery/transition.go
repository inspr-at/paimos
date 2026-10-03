// SPDX-License-Identifier: AGPL-3.0-only

package delivery

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type TransitionRequest struct {
	ProjectID, ReleaseID   string
	ExpectedRevision       int64
	Action                 string // planned, building, freeze, unfreeze, cut, close, abandon
	VersionScheme, Version string
}

// ValidProjectVersion preserves the P2 store API and shares the tagged validator.
func ValidProjectVersion(scheme, version string) bool {
	return releasehistory.ValidProjectVersion(scheme, version)
}

func (s *Store) Transition(ctx context.Context, p tenant.Principal, in TransitionRequest) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 {
		return Release{}, errors.New("invalid release transition")
	}
	permission, person := "releases.deploy", false
	switch in.Action {
	case "planned", "building":
		permission, person = "releases.write", true
	case "abandon":
		// Its required permission depends on the current fenced state.
		permission, person = "", true
	case "state_building":
		permission, person = "", false
	case "freeze", "unfreeze", "close":
	case "cut":
		if !ValidProjectVersion(in.VersionScheme, in.Version) {
			return Release{}, errors.New("invalid scheme/version pair")
		}
	default:
		return Release{}, ErrTransition
	}
	var out Release
	err := s.mutate(ctx, p, in.ProjectID, permission, person, func(w *write) error {
		// Discover the successor before taking release locks, then lock the
		// complete existing batch in UUID order before any item rows.
		old, err := scanRelease(w.tx.QueryRow(w.ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3`, p.TenantID, w.project, in.ReleaseID))
		if err != nil {
			return err
		}
		if in.Action == "cut" && old.CutAt != nil {
			if old.State == "frozen" && old.VersionScheme == in.VersionScheme && old.Version == in.Version {
				out = old
				return nil
			}
			return ErrTransition
		}
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		if in.Action == "state_building" {
			if old.State == "frozen" {
				in.Action = "unfreeze"
				if err = w.require("releases.deploy"); err != nil {
					return err
				}
			} else {
				in.Action = "building"
				if p.Kind != tenant.Person {
					return authz.ErrForbidden
				}
				if err = w.require("releases.write"); err != nil {
					return err
				}
			}
		}
		state := old.State
		if in.Action == "abandon" {
			permission := "releases.deploy"
			if state == "planned" {
				permission = "releases.write"
			}
			if err = w.require(permission); err != nil {
				return err
			}
		}
		switch in.Action {
		case "planned":
			if state != "building" {
				return ErrTransition
			}
			state = "planned"
		case "building":
			if state != "planned" {
				return ErrTransition
			}
			state = "building"
		case "freeze":
			if state != "planned" && state != "building" {
				return ErrTransition
			}
			state = "frozen"
		case "unfreeze":
			if state != "frozen" || old.CutAt != nil {
				return ErrTransition
			}
			state = "building"
		case "cut":
			if state != "frozen" || old.Visibility != "published" {
				return ErrTransition
			}
			if err = w.firstPublished(old); err != nil {
				return err
			}
			var taken bool
			if err = w.tx.QueryRow(w.ctx, `SELECT EXISTS(SELECT 1 FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND version=$3 AND release_node_id<>$4)`, p.TenantID, w.project, in.Version, old.ID).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return &Conflict{"version_taken", "This version is already reserved by a release, including abandoned releases."}
			}
		case "close":
			if state != "frozen" || old.Visibility != "internal" {
				return ErrTransition
			}
			state = "released"
		case "abandon":
			if state == "released" || state == "abandoned" {
				return ErrTransition
			}
			state = "abandoned"
		}
		var movedBefore, movedAfter []Placement
		if in.Action == "cut" || in.Action == "close" || in.Action == "abandon" {
			movedBefore, movedAfter, err = w.rollover(old, in.Action)
			if err != nil {
				return err
			}
		} else {
			if _, err = w.lockReleases([]string{old.ID}); err != nil {
				return err
			}
		}
		if in.Action == "close" {
			if err = w.requireCompletion(old.ID); err != nil {
				return err
			}
		}
		// The clock is sampled after fences and row locks, never at transaction
		// start. A Close waiting behind Cut cannot land in that cut window.
		now := w.now().UTC()
		var cut, released, abandoned *time.Time
		scheme, version := old.VersionScheme, old.Version
		cut = old.CutAt
		if in.Action == "cut" {
			scheme, version = in.VersionScheme, in.Version
			cut = &now
		}
		if state == "released" {
			released = &now
		}
		if state == "abandoned" {
			abandoned = &now
		}
		out, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET state=$4,version_scheme=$5,version=$6,cut_at=$7,released_at=$8,abandoned_at=$9,build_authorized_by=NULL,build_authorized_at=NULL,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$10 RETURNING `+releaseColumns, p.TenantID, w.project, old.ID, state, nullable(scheme), nullable(version), cut, released, abandoned, old.Revision))
		if err != nil {
			return err
		}
		if err = w.counts([]string{old.ID}); err != nil {
			return err
		}
		// Maintenance moves cannot be undone independently of the one-way cut,
		// close or abandon that owns them.
		start := len(w.changes)
		w.placementEvents(movedBefore, movedAfter)
		for i := start; i < len(w.changes); i++ {
			w.changes[i].Type = "ships_in.rolled_over"
		}
		typ := "release.state_changed"
		if in.Action == "cut" {
			typ = "release.cut"
		}
		if in.Action == "close" {
			typ = "release.closed"
		}
		w.audit(typ, old.ID, old, out)
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}

func (w *write) firstPublished(r Release) error {
	var first int
	if err := w.tx.QueryRow(w.ctx, `SELECT sequence FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND visibility='published' AND state IN ('planned','building','frozen') ORDER BY sequence LIMIT 1`, w.p.TenantID, w.project).Scan(&first); err != nil {
		return err
	}
	if first != r.Sequence {
		return &Conflict{"cut_order", fmt.Sprintf("Cut Release %d first.", first)}
	}
	return nil
}

// rollover is maintenance: it deliberately includes tombstones, cancelled
// rows and hidden kinds, unlike live-item admission through Effective.
func (w *write) rollover(source Release, action string) ([]Placement, []Placement, error) {
	ids := []string{}
	rows, err := w.tx.Query(w.ctx, `SELECT item_node_id::text FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 ORDER BY item_node_id LIMIT 1001`, w.p.TenantID, w.project, source.ID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, nil, err
	}
	if len(ids) > 1000 {
		return nil, nil, ErrReleaseCapacity
	}
	// Discover open-unit ancestors in a bounded tree walk, so tracking epics
	// without unfinished live units stay in the cut/closed release.
	epics := map[string]bool{}
	if action != "abandon" && len(ids) > 0 {
		rows, err = w.tx.Query(w.ctx, `WITH RECURSIVE ancestors AS (
 SELECT n.parent_id AS id,1 AS depth FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.id=ANY($2::uuid[]) AND n.deleted_at IS NULL AND k.slug IN ('ticket','task') AND n.state NOT IN ('done','accepted','delivered','cancelled','canceled')
 UNION ALL SELECT n.parent_id,a.depth+1 FROM ancestors a JOIN nodes n ON n.tenant_id=$1 AND n.id=a.id WHERE a.depth<128 AND n.parent_id IS NOT NULL
 ) SELECT a.id::text,bool_or(a.depth=128 AND n.parent_id IS NOT NULL) FROM ancestors a JOIN nodes n ON n.tenant_id=$1 AND n.id=a.id GROUP BY a.id LIMIT 128001`, w.p.TenantID, ids)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			var capped bool
			if err = rows.Scan(&id, &capped); err != nil {
				rows.Close()
				return nil, nil, err
			}
			if capped {
				rows.Close()
				return nil, nil, &Conflict{"tree_depth", "This tree exceeds the bounded rollover depth."}
			}
			epics[id] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, err
		}
	}
	// The held tree fence makes this bounded preflight stable until item locks.
	var moving bool
	if action == "abandon" {
		err = w.tx.QueryRow(w.ctx, `SELECT EXISTS(SELECT 1 FROM ships_in s JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.item_node_id WHERE s.tenant_id=$1 AND s.project_node_id=$2 AND s.release_node_id=$3 AND n.state NOT IN ('cancelled','canceled'))`, w.p.TenantID, w.project, source.ID).Scan(&moving)
	} else {
		err = w.tx.QueryRow(w.ctx, `SELECT EXISTS(SELECT 1 FROM ships_in s JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.item_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE s.tenant_id=$1 AND s.project_node_id=$2 AND s.release_node_id=$3 AND n.state NOT IN ('done','accepted','delivered','cancelled','canceled') AND (k.slug<>'epic' OR n.id=ANY($4::uuid[])))`, w.p.TenantID, w.project, source.ID, mapIDs(epics)).Scan(&moving)
	}
	if err != nil {
		return nil, nil, err
	}
	successor := Release{}
	if moving {
		successor, err = scanRelease(w.tx.QueryRow(w.ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND visibility=$3 AND rank>$4 AND state IN ('planned','building') ORDER BY rank LIMIT 1`, w.p.TenantID, w.project, source.Visibility, source.Rank))
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, nil, err
		}
	}
	if _, err = w.lockReleases([]string{source.ID, successor.ID}); err != nil {
		return nil, nil, err
	}
	if moving && successor.ID == "" {
		title := "Internal release"
		if source.Visibility == "published" {
			title = fmt.Sprintf("Release %d", w.nextSequence)
		}
		successor, err = w.plan(source.Visibility, title, "")
		if err != nil {
			return nil, nil, err
		}
	}
	if moving {
		hasWrite := w.require("releases.write") == nil
		if err = CheckAdmission(w.p.Kind, hasWrite, successor.State, successor.EntryClosesAt, w.now().UTC(), true); err != nil {
			return nil, nil, err
		}
	}
	nodes, placements, err := w.lockItems(ids)
	if err != nil {
		return nil, nil, err
	}
	// Locks use UUID order; maintenance retains the source's display order.
	sort.Slice(ids, func(i, j int) bool { return placements[ids[i]].Rank < placements[ids[j]].Rank })
	before, after := []Placement{}, []Placement{}
	for _, id := range ids {
		n, old := nodes[id], placements[id]
		if action != "abandon" && (Completed(n.state) || Cancelled(n.state) || n.kind == "epic" && !epics[id]) {
			continue
		}
		dest := successor.ID
		if action == "abandon" && Cancelled(n.state) {
			dest = ""
		}
		rank, e := w.rank(Container{TenantID: w.p.TenantID, ProjectID: w.project, ReleaseID: dest}, Slot{}, id, true)
		if e != nil {
			return nil, nil, e
		}
		next := old
		next.ReleaseID, next.Rank = dest, rank
		old, e = w.neighboursFor(old)
		if e != nil {
			return nil, nil, e
		}
		next, e = w.savePlacement(old, next, "build", w.now().UTC())
		if e != nil {
			return nil, nil, e
		}
		before = append(before, old)
		after = append(after, next)
	}
	if moving {
		if _, err = w.tx.Exec(w.ctx, `UPDATE project_releases SET revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$4`, w.p.TenantID, w.project, successor.ID, successor.Revision); err != nil {
			return nil, nil, err
		}
	}
	if moving {
		var count int
		if err = w.tx.QueryRow(w.ctx, `SELECT count(*) FROM (SELECT 1 FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 LIMIT 1001) bounded`, w.p.TenantID, w.project, successor.ID).Scan(&count); err != nil {
			return nil, nil, err
		}
		if count > 1000 {
			return nil, nil, ErrReleaseCapacity
		}
	}
	// Global caps are checked after the source's final state is written: a
	// terminal transition frees the very slot its new successor consumes.
	return before, after, nil
}

func mapIDs(m map[string]bool) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	return ids
}

func (w *write) requireCompletion(release string) error {
	var completed, open int
	if err := w.tx.QueryRow(w.ctx, `SELECT count(*) FILTER (WHERE state IN ('done','accepted','delivered')),count(*) FILTER (WHERE state NOT IN ('done','accepted','delivered','cancelled','canceled')) FROM `+Effective+` e WHERE e.tenant_id=$1 AND e.project_node_id=$2 AND e.release_node_id=$3 AND e.kind IN ('ticket','task')`, w.p.TenantID, w.project, release).Scan(&completed, &open); err != nil {
		return err
	}
	if completed == 0 {
		return ErrEmptyRelease
	}
	if open != 0 {
		return ErrTransition
	}
	return nil
}

// Publication is produced by P4a's trusted project-bound reservation/completion
// and notes implementation, inside the final fenced transaction. Capture must
// write the immutable snapshot and cumulative inclusion links atomically.
// It must acquire all its resource locks before its first event append.
type Publication struct {
	Basis, Reference string
	Capture          func(context.Context, pgx.Tx, Release) error
	// P4a supplies statusautopilot.PublishTx here. Keeping the consumer hook
	// injected lets P4b's statusautopilot readers import delivery without an
	// import cycle. It runs in this transaction before the store audit flush.
	Settle func(context.Context, pgx.Tx, Release) error
}
type PreparePublication func(context.Context, pgx.Tx, tenant.Principal, Release) (Publication, error)

// Publish provides the P2 transaction boundary for P4a's publication logic.
// No caller-provided proof is accepted without a trusted prepare function.
// P4b owns switching PublishTx's legacy queries to the new publication source.
func (s *Store) Publish(ctx context.Context, p tenant.Principal, in ReleaseEdit, prepare PreparePublication) (Release, error) {
	if !uuid(in.ReleaseID) || in.ExpectedRevision < 1 || prepare == nil {
		return Release{}, errors.New("publication requires a fenced preparation function")
	}
	var out Release
	err := s.mutate(ctx, p, in.ProjectID, "releases.deploy", false, func(w *write) error {
		r, err := w.publicationLocks(in.ReleaseID)
		if err != nil {
			return err
		}
		old := r[in.ReleaseID]
		if old.Revision != in.ExpectedRevision {
			return ErrRevisionChanged
		}
		if old.State != "frozen" || old.Visibility != "published" || old.CutAt == nil || !ValidProjectVersion(old.VersionScheme, old.Version) {
			return ErrTransition
		}
		if err = w.firstPublished(old); err != nil {
			return err
		}
		proof, err := prepare(w.ctx, w.tx, p, old)
		if err != nil {
			return err
		}
		if proof.Capture == nil || proof.Settle == nil || proof.Basis != "history" && proof.Basis != "attested" || len(proof.Reference) > 512 || proof.Basis == "history" && proof.Reference != "" {
			return errors.New("invalid publication proof")
		}
		if proof.Basis == "attested" && (p.Kind != tenant.Person || proof.Reference == "") {
			return authz.ErrForbidden
		}
		out, err = scanRelease(w.tx.QueryRow(w.ctx, `UPDATE project_releases SET state='released',released_at=$4,released_by=$5,reservation_basis=$6,reservation_ref=$7,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND revision=$8 RETURNING `+releaseColumns, p.TenantID, w.project, old.ID, w.now().UTC(), p.ID, proof.Basis, proof.Reference, old.Revision))
		if err != nil {
			return err
		}
		if err = proof.Capture(w.ctx, w.tx, out); err != nil {
			return err
		}
		// PublishTx may re-enter the tree lock, already held exclusively from
		// the start. It runs before our audit flush; no lock upgrade occurs.
		if err = proof.Settle(w.ctx, w.tx, out); err != nil {
			return err
		}
		w.audit("release.state_changed", old.ID, old, out)
		return nil
	})
	if err != nil {
		return Release{}, err
	}
	return out, nil
}
