// SPDX-License-Identifier: AGPL-3.0-only
package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const MaxSnapshotBytes = 12 << 20

var ErrNotesTooLarge = &Conflict{"notes_too_large", "Release notes exceed 12 MiB. Shorten the largest notes or move work out before publishing."}

type SnapshotResult struct {
	Raw            json.RawMessage
	Unavailable    bool
	Waiting        []string
	CarriedForward []string
}
type noteSelection struct {
	releases, waiting, carried []string
	lower, upper               time.Time
}

// selection is shared by preview and capture. Rank orders entries, never
// eligibility. Every old unincluded internal release can carry forward.
func selectNotes(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Release, now time.Time) (noteSelection, error) {
	out := noteSelection{releases: []string{r.ID}, waiting: []string{}, carried: []string{}, upper: now.UTC()}
	if r.CutAt != nil {
		out.upper = *r.CutAt
	}
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT coalesce(cut_at,released_at) FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND visibility='published' AND state='released' AND sequence<$3 ORDER BY sequence DESC LIMIT 1),adopted_at) FROM project_delivery WHERE tenant_id=$1 AND project_node_id=$2`, p.TenantID, r.ProjectID, r.Sequence).Scan(&out.lower)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT r.release_node_id::text,r.released_at,EXISTS(SELECT 1 FROM ships_in s JOIN nodes n ON n.tenant_id=s.tenant_id AND n.project_id=s.project_node_id AND n.id=s.item_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE s.tenant_id=r.tenant_id AND s.project_node_id=r.project_node_id AND s.release_node_id=r.release_node_id AND n.deleted_at IS NULL AND k.slug IN ('ticket','task') AND n.state NOT IN ('done','accepted','delivered','cancelled','canceled')) FROM project_releases r JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id AND rn.deleted_at IS NULL WHERE r.tenant_id=$1 AND r.project_node_id=$2 AND r.visibility='internal' AND r.state='released' AND r.included_in_release_id IS NULL AND r.released_at<=$3 ORDER BY r.rank,r.release_node_id LIMIT 4001`, p.TenantID, r.ProjectID, out.upper)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		if count > 4000 {
			return out, ErrPendingCapacity
		}
		var id string
		var closed time.Time
		var open bool
		if err = rows.Scan(&id, &closed, &open); err != nil {
			return out, err
		}
		if open {
			out.waiting = append(out.waiting, id)
			continue
		}
		out.releases = append(out.releases, id)
		if !closed.After(out.lower) {
			out.carried = append(out.carried, id)
		}
	}
	return out, rows.Err()
}

const noteFields = `(SELECT coalesce(jsonb_object_agg(f.key,f.value),'{}'::jsonb) FROM jsonb_each(n.fields) f WHERE f.key IN ('pill_en','pill_de','benefit_en','benefit_de','hide_from_release_notes'))`
const noteMembers = ` FROM ships_in s JOIN project_releases r ON r.tenant_id=s.tenant_id AND r.project_node_id=s.project_node_id AND r.release_node_id=s.release_node_id JOIN nodes n ON n.tenant_id=s.tenant_id AND n.id=s.item_node_id AND n.project_id=s.project_node_id JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE s.tenant_id=$1 AND s.project_node_id=$2 AND s.release_node_id=ANY($3::uuid[]) AND n.deleted_at IS NULL AND k.slug IN ('ticket','task') AND n.state IN ('done','accepted','delivered')`

// cappedWriter checks before allocation. Encoding keeps one row/page and the
// bounded output only; no whole capture marshal precedes this hard ceiling.
type cappedWriter struct {
	bytes.Buffer
	limit int
}

func (w *cappedWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit-w.Len() {
		return 0, ErrNotesTooLarge
	}
	return w.Buffer.Write(b)
}

func encodeSnapshot(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Release, selection noteSelection, frozen bool, now time.Time) ([]byte, error) {
	var rowsCount int
	var sourceBytes int64
	// This aggregate reads sizes only. No note text reaches Go before admission.
	err := tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(source_bytes),0)::bigint FROM (SELECT octet_length(n.key)+octet_length(`+noteFields+`::text) AS source_bytes`+noteMembers+` LIMIT 5001) note_preflight`, p.TenantID, r.ProjectID, selection.releases).Scan(&rowsCount, &sourceBytes)
	if err != nil {
		return nil, err
	}
	if rowsCount > 5000 || sourceBytes > (MaxSnapshotBytes-4096-int64(rowsCount)*512)/6 {
		// Small diagnostics contain keys only, never the oversized texts.
		rows, e := tx.Query(ctx, `SELECT n.key`+noteMembers+` ORDER BY octet_length(`+noteFields+`::text) DESC,n.id LIMIT 20`, p.TenantID, r.ProjectID, selection.releases)
		if e != nil {
			return nil, e
		}
		keys := []string{}
		for rows.Next() {
			var key string
			if e = rows.Scan(&key); e != nil {
				rows.Close()
				return nil, e
			}
			keys = append(keys, key)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		return nil, &Conflict{ErrNotesTooLarge.Code, fmt.Sprintf("%s Largest items: %v", ErrNotesTooLarge.Message, keys)}
	}
	output := &cappedWriter{limit: MaxSnapshotBytes}
	header := releasehistory.NoteSnapshot{Schema: releasehistory.SnapshotSchema, TenantID: p.TenantID, ProjectID: r.ProjectID, ReleaseID: r.ID, Version: r.Version, VersionScheme: r.VersionScheme, Revision: r.Revision, CapturedAt: now.UTC(), MembershipSource: releasehistory.ShipsInMembershipSource, FieldSource: releasehistory.FieldSource, Frozen: frozen, Tickets: []releasehistory.NoteTicket{}}
	// Header is length bounded and has an empty tickets array; stream its array.
	head, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(head, []byte(`"tickets":[]}`)) {
		return nil, errors.New("snapshot header contract changed")
	}
	if _, err = output.Write(head[:len(head)-2]); err != nil {
		return nil, err
	}
	releaseRank, itemRank, lastID := "", "", ""
	position := 0
	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		rows, e := tx.Query(ctx, `SELECT r.rank,s.rank,n.id::text,n.key,n.updated_at,`+noteFields+`,aeon_release_note_group(n.fields)`+noteMembers+` AND (r.rank,s.rank,n.id) > ($4 COLLATE "C",$5 COLLATE "C",$6::uuid) ORDER BY r.rank,s.rank,n.id LIMIT 500`, p.TenantID, r.ProjectID, selection.releases, releaseRank, itemRank, zeroUUID(lastID))
		if e != nil {
			return nil, e
		}
		page := 0
		for rows.Next() {
			if err = ctx.Err(); err != nil {
				rows.Close()
				return nil, err
			}
			var ticket releasehistory.NoteTicket
			var rank string
			ticket.Position = position
			ticket.Unavailable = ""
			if e = rows.Scan(&releaseRank, &rank, &ticket.ID, &ticket.Key, &ticket.UpdatedAt, &ticket.Fields, &ticket.Group); e != nil {
				rows.Close()
				return nil, e
			}
			itemRank, lastID = rank, ticket.ID
			if position >= 5000 {
				rows.Close()
				return nil, ErrNotesTooLarge
			}
			if position > 0 {
				if _, e = output.Write([]byte(",")); e != nil {
					rows.Close()
					return nil, e
				}
			}
			if e = json.NewEncoder(output).Encode(ticket); e != nil {
				rows.Close()
				return nil, e
			}
			page++
			position++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if page < 500 {
			break
		}
	}
	if position != rowsCount {
		return nil, errors.New("snapshot population changed after preflight")
	}
	if _, err = output.Write([]byte("]}")); err != nil {
		return nil, err
	}
	return output.Bytes(), ctx.Err()
}
func zeroUUID(id string) string {
	if id == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return id
}

func (s *Store) NoteSnapshot(ctx context.Context, p tenant.Principal, project, release string) (SnapshotResult, error) {
	var out SnapshotResult
	if !uuid(project) || !uuid(release) {
		return out, errors.New("invalid snapshot identity")
	}
	ctx, cancel := context.WithTimeout(tenant.WithPrincipal(ctx, p), 60*time.Second)
	defer cancel()
	err := db.ReadSnapshot(ctx, s.pool, p.TenantID, func(tx pgx.Tx) error {
		if err := transactionLimits(ctx, tx); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "releases.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		if err := authz.RequireTx(ctx, tx, p, "nodes.read", authz.Scope{ProjectID: project}); err != nil {
			return err
		}
		var r Release
		var err error
		r, err = scanRelease(tx.QueryRow(ctx, `SELECT `+releaseColumns+` FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL) AND EXISTS(SELECT 1 FROM nodes WHERE tenant_id=$1 AND id=$3 AND deleted_at IS NULL)`, p.TenantID, project, release))
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT snapshot FROM project_release_note_snapshots WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3`, p.TenantID, project, release).Scan(&out.Raw)
		if err == nil {
			_, err = releasehistory.ProjectNotesFromSnapshot(out.Raw, releasehistory.ProjectSnapshotBinding{TenantID: p.TenantID, ProjectID: project, ReleaseID: release, VersionScheme: r.VersionScheme, Version: r.Version}, "database-snapshot")
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if r.State == "released" || r.State == "abandoned" {
			out.Unavailable = true
			return nil
		}
		if r.Visibility != "published" {
			return &Conflict{"internal_notes", "Internal releases have no notes capture or preview."}
		}
		selected, err := selectNotes(ctx, tx, p, r, s.now())
		if err != nil {
			return err
		}
		out.Waiting, out.CarriedForward = selected.waiting, selected.carried
		out.Raw, err = encodeSnapshot(ctx, tx, p, r, selected, false, s.now())
		return err
	})
	if err != nil {
		return SnapshotResult{}, err
	}
	return out, nil
}

// publicationLocks takes the entire sorted release batch before any member
// rows. Pending internal rows include waiting/future closes so the preflight
// cannot be invalidated by any node or membership writer.
func (w *write) publicationLocks(release string) (map[string]Release, error) {
	ids := []string{release}
	rows, err := w.tx.Query(w.ctx, `SELECT release_node_id::text FROM project_releases WHERE tenant_id=$1 AND project_node_id=$2 AND visibility='internal' AND state='released' AND included_in_release_id IS NULL ORDER BY release_node_id LIMIT 4001`, w.p.TenantID, w.project)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(ids) > 4001 {
		return nil, ErrPendingCapacity
	}
	locked, err := w.lockReleases(ids)
	if err != nil {
		return nil, err
	}
	rows, err = w.tx.Query(w.ctx, `SELECT item_node_id::text FROM ships_in WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=ANY($3::uuid[]) ORDER BY item_node_id LIMIT 5001`, w.p.TenantID, w.project, ids)
	if err != nil {
		return nil, err
	}
	members := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		members = append(members, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(members) > 5000 {
		return nil, ErrPendingCapacity
	}
	if len(members) > 0 {
		if _, _, err = w.lockItems(members); err != nil {
			return nil, err
		}
	}
	return locked, nil
}

type PublishRequest struct {
	ReleaseEdit
	ReservationRef string
}
type PublishHook func(context.Context, pgx.Tx, string, string) error

func (s *Store) PublishNotes(ctx context.Context, p tenant.Principal, in PublishRequest, history releasehistory.History, settle PublishHook) (Release, error) {
	if len(in.ReservationRef) > 512 {
		return Release{}, errors.New("reservation reference exceeds 512 bytes")
	}
	if settle == nil {
		return Release{}, errors.New("publication settlement hook required")
	}
	return s.Publish(ctx, p, in.ReleaseEdit, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Release) (Publication, error) {
		// Resolve the explicit product route binding; member key prefixes do
		// not identify a product, and ambiguous bindings fail closed.
		productID, err := releasehistory.ResolveProject(ctx, tx, "AEON")
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Publication{}, err
		}
		product := err == nil && productID == r.ProjectID

		basis := "attested"
		if product {
			if in.ReservationRef != "" {
				return Publication{}, &Conflict{"reservation_ref", "The product uses its exact embedded history reservation."}
			}
			if history.Product != "PAIMOS AEON" || (history.Repository != "inspr-at/aeon" && history.Repository != "inspr-at/paimos") {
				return Publication{}, errors.New("invalid embedded product history identity")
			}
			found := false
			for _, h := range history.Releases {
				if h.Version == r.Version && h.ReleaseSequence == r.Sequence && (h.State == releasehistory.StateReserved || h.State == releasehistory.StatePublished) && r.VersionScheme == releasehistory.SchemeOf(h.Version) {
					found = true
					break
				}
			}
			if !found {
				return Publication{}, &Conflict{"reservation_missing", "Reservation not in this build's history."}
			}
			basis = "history"
		} else {
			if p.Kind != tenant.Person {
				return Publication{}, authz.ErrForbidden
			}
			if in.ReservationRef == "" {
				return Publication{}, &Conflict{"reservation_required", "A person's reservation attestation is required."}
			}
		}
		// Own open units block publication; reopened internal releases simply wait.
		var open int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM `+Effective+` e WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=$3 AND kind IN ('ticket','task') AND state NOT IN ('done','accepted','delivered','cancelled','canceled') LIMIT 1001) bounded`, p.TenantID, r.ProjectID, r.ID).Scan(&open); err != nil {
			return Publication{}, err
		}
		if open > 0 {
			return Publication{}, &Conflict{"release_open", "Open units remain in this release."}
		}
		selection, err := selectNotes(ctx, tx, p, r, s.now())
		if err != nil {
			return Publication{}, err
		}
		var completed int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1`+noteMembers+` LIMIT 5001) bounded`, p.TenantID, r.ProjectID, selection.releases).Scan(&completed); err != nil {
			return Publication{}, err
		}
		if completed < 1 {
			return Publication{}, ErrEmptyRelease
		}
		return Publication{Basis: basis, Reference: in.ReservationRef, Capture: func(ctx context.Context, tx pgx.Tx, final Release) error {
			raw, err := encodeSnapshot(ctx, tx, p, final, selection, true, s.now())
			if err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, `UPDATE project_releases SET included_in_release_id=$4,revision=revision+1 WHERE tenant_id=$1 AND project_node_id=$2 AND release_node_id=ANY($3::uuid[]) AND release_node_id<>$4 AND included_in_release_id IS NULL`, p.TenantID, r.ProjectID, selection.releases, r.ID); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO project_release_note_snapshots(tenant_id,project_node_id,release_node_id,version,snapshot) VALUES($1,$2,$3,$4,$5)`, p.TenantID, r.ProjectID, r.ID, r.Version, raw)
			return err
		}, Settle: func(ctx context.Context, tx pgx.Tx, final Release) error {
			if err := settle(ctx, tx, p.TenantID, final.ID); err != nil {
				slog.Error("status autopilot publication deferred", "release_id", final.ID, "err", err)
			}
			return nil
		}}, nil
	})
}

// Keep io in the shape contract: a capped writer satisfies the streaming encoder.
var _ io.Writer = (*cappedWriter)(nil)
