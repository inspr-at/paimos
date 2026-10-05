// SPDX-License-Identifier: AGPL-3.0-only
package releases

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

const noteBackfillEvent = "release.notes_backfilled"

// NoteBackfillOptions scopes both native journey and embedded tag releases.
// History is the same offline manifest used by /api/releases.
type NoteBackfillOptions struct {
	Project    string
	Release    string
	AllMissing bool
	History    releasehistory.History
}

type NoteBackfillItem struct {
	ProjectID        string    `json:"project_node_id"`
	ReleaseID        string    `json:"release_node_id"`
	Version          string    `json:"version"`
	ReleasedAt       time.Time `json:"released_at"`
	Revision         int64     `json:"release_revision"`
	Tickets          int       `json:"ticket_count"`
	Notes            int       `json:"notes_count"`
	Hidden           int       `json:"hidden_count"`
	ExcludedKeys     []string  `json:"excluded_keys"`
	GapKeys          []string  `json:"gap_keys"`
	MembershipSource string    `json:"membership_source"`
}

type NoteBackfillSkip struct {
	ProjectID string `json:"project_node_id"`
	ReleaseID string `json:"release_node_id"`
	Version   string `json:"version"`
	Reason    string `json:"reason"`
}

type NoteBackfillReport struct {
	Applied   bool               `json:"applied"`
	TenantID  string             `json:"tenant_id"`
	Planned   []NoteBackfillItem `json:"planned"`
	Inserted  int                `json:"inserted"`
	Unchanged int                `json:"unchanged"`
	Skipped   []NoteBackfillSkip `json:"skipped"`
}

// BackfillNoteSnapshots is an offline maintenance operation. A named, active
// person with workspace administration authority is required even for planning.
// All reads and writes use that principal's live project visibility. Applying
// both sources and their audit events is atomic; existing rows are never changed.
func BackfillNoteSnapshots(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID string, apply bool, opts NoteBackfillOptions) (NoteBackfillReport, error) {
	report := NoteBackfillReport{Applied: apply, TenantID: tenantID, Planned: []NoteBackfillItem{}, Skipped: []NoteBackfillSkip{}}
	if pool == nil || !uuid.MatchString(tenantID) {
		return report, errors.New("tenant id is required")
	}
	if !uuid.MatchString(actorID) {
		return report, errors.New("actor principal id must name a person with tenant-admin authority")
	}
	opts.Project = strings.TrimSpace(opts.Project)
	opts.Release = strings.TrimPrefix(opts.Release, "v")
	if opts.Project == "" || (opts.Release != "" && opts.AllMissing) {
		return report, errors.New("project is required; --release and --all-missing cannot be combined")
	}
	if p, ok := tenant.PrincipalFrom(ctx); ok && (p.ID != actorID || p.TenantID != tenantID || p.Kind != tenant.Person) {
		return report, errors.New("actor must be the calling person")
	}
	actor := tenant.Principal{ID: actorID, TenantID: tenantID, Kind: tenant.Person}
	ctx = tenant.WithPrincipal(ctx, actor)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if !apply {
			if _, err := tx.Exec(ctx, `SET LOCAL transaction_read_only = on`); err != nil {
				return err
			}
		}
		if err := backfillAuthorize(ctx, tx, actor); err != nil {
			return err
		}
		if apply {
			if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
				return err
			}
		}
		projectID, err := releasehistory.ResolveProject(ctx, tx, opts.Project)
		if err != nil {
			return fmt.Errorf("project %q: %w", opts.Project, err)
		}
		found := opts.Release == ""
		// Serialize operators for this project. Snapshot capture and all writes remain
		// inside the transaction; the unique key is a second idempotency guard.
		if apply {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":release-notes:"+projectID); err != nil {
				return err
			}
		}
		save := func(snap releasehistory.NoteSnapshot, excluded []string) error {
			snap.Label = releasehistory.BackfillLabel
			snap.Backfilled = true
			snap.ActorID = actorID
			snap.Frozen = true
			raw, err := json.Marshal(snap)
			if err != nil {
				return err
			}
			// Native journey captures can have legacy or unassigned versions.
			// Do not apply the calendar-only tag import validator to those rows.
			notesCount, hiddenCount, gapKeys := snapshotNoteCounts(snap.Tickets)
			if snap.MembershipSource == releasehistory.ManifestMembershipSource {
				if _, err := releasehistory.NotesFromSnapshot(raw, snap.Version, "backfill"); err != nil {
					return err
				}
			}
			item := NoteBackfillItem{ProjectID: projectID, ReleaseID: snap.ReleaseID, Version: snap.Version, ReleasedAt: *snap.ReleasedAt, Revision: snap.Revision, Tickets: len(snap.Tickets), Notes: notesCount, Hidden: hiddenCount, MembershipSource: snap.MembershipSource, ExcludedKeys: append([]string{}, excluded...), GapKeys: gapKeys}
			if apply {
				var tag pgconn.CommandTag
				if snap.MembershipSource == releasehistory.ManifestMembershipSource {
					tag, err = tx.Exec(ctx, `INSERT INTO release_manifest_note_snapshots(tenant_id,project_node_id,version,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tenantID, projectID, snap.Version, raw)
				} else {
					tag, err = tx.Exec(ctx, `INSERT INTO journey_release_note_snapshots(tenant_id,project_node_id,release_node_id,snapshot) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tenantID, projectID, snap.ReleaseID, raw)
				}
				if err != nil {
					return err
				}
				if tag.RowsAffected() == 0 {
					report.Unchanged++
					return nil
				}
				nodeID := snap.ReleaseID
				if nodeID == "" {
					nodeID = projectID
				}
				if _, err := events.Append(ctx, tx, actor, events.Change{NodeID: &nodeID, Type: noteBackfillEvent, After: map[string]any{
					"label": snap.Label, "backfilled": true, "actor_principal_id": actorID, "project_node_id": projectID, "release_node_id": snap.ReleaseID,
					"version": snap.Version, "released_at": snap.ReleasedAt, "captured_at": snap.CapturedAt, "release_revision": snap.Revision,
					"ticket_count": item.Tickets, "notes_count": item.Notes, "hidden_count": item.Hidden, "membership_source": snap.MembershipSource,
				}}); err != nil {
					return err
				}
				report.Inserted++
			}
			report.Planned = append(report.Planned, item)
			return nil
		}

		// Keep the original native journey path. Only the requested project/version
		// is considered; a publication snapshot always wins over later ticket fields.
		query := `SELECT r.release_node_id::text, coalesce(r.version,''), r.released_at,
       EXISTS(SELECT 1 FROM journey_release_note_snapshots s WHERE s.tenant_id=r.tenant_id AND s.release_node_id=r.release_node_id)
   FROM journey_releases r
   JOIN nodes rn ON rn.tenant_id=r.tenant_id AND rn.id=r.release_node_id AND rn.deleted_at IS NULL
   WHERE r.project_node_id=$1 AND r.state IN ('released','superseded') AND ($2='' OR r.version=$2)
   ORDER BY r.released_at,r.release_node_id`
		if apply {
			query += ` FOR UPDATE OF r`
		}
		rows, err := tx.Query(ctx, query, projectID, opts.Release)
		if err != nil {
			return err
		}
		type native struct {
			id, version string
			released    *time.Time
			exists      bool
		}
		nativeRows := []native{}
		nativeVersions := map[string]bool{}
		for rows.Next() {
			var n native
			if err := rows.Scan(&n.id, &n.version, &n.released, &n.exists); err != nil {
				rows.Close()
				return err
			}
			nativeRows = append(nativeRows, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, n := range nativeRows {
			found = true
			nativeVersions[n.version] = true
			if n.exists {
				report.Unchanged++
				continue
			}
			if n.released == nil {
				report.Skipped = append(report.Skipped, NoteBackfillSkip{ProjectID: projectID, ReleaseID: n.id, Version: n.version, Reason: "Original release time is unavailable."})
				continue
			}
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT aeon_release_note_snapshot($1,$2)`, projectID, n.id).Scan(&raw); err != nil {
				return err
			}
			var snap releasehistory.NoteSnapshot
			if err := json.Unmarshal(raw, &snap); err != nil {
				return err
			}
			snap.ReleasedAt = n.released
			if err := save(snap, nil); err != nil {
				return err
			}
		}

		for _, rel := range opts.History.Releases {
			if rel.State != releasehistory.StatePublished || (opts.Release != "" && rel.Version != opts.Release) {
				continue
			}
			found = true
			// A native row for this version is authoritative (including its frozen
			// membership); the reader can consume it without creating a second capture.
			if nativeVersions[rel.Version] {
				continue
			}
			if releasehistory.HasSnapshot(rel) {
				report.Unchanged++
				continue
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM release_manifest_note_snapshots WHERE project_node_id=$1 AND version=$2)`, projectID, rel.Version).Scan(&exists); err != nil {
				return err
			}
			if exists {
				report.Unchanged++
				continue
			}
			released := rel.PublishedAt
			if released == nil {
				released = rel.TaggedAt
			}
			if released == nil || released.IsZero() {
				report.Skipped = append(report.Skipped, NoteBackfillSkip{ProjectID: projectID, Version: rel.Version, Reason: "Original release time is unavailable."})
				continue
			}
			snap, excluded, err := manifestSnapshot(ctx, tx, actor, projectID, rel, *released)
			if err != nil {
				return err
			}
			if err := save(snap, excluded); err != nil {
				return err
			}
		}
		if !found {
			return fmt.Errorf("release %q not found in project journey or embedded history", opts.Release)
		}
		return nil
	})
	if err != nil {
		// A rolled-back apply must never report successful writes.
		report.Planned = []NoteBackfillItem{}
		report.Inserted = 0
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return report, errors.New("release note backfill timed out waiting for a lock")
		}
	}
	return report, err
}

func backfillAuthorize(ctx context.Context, tx pgx.Tx, actor tenant.Principal) error {
	var kind, status string
	if err := tx.QueryRow(ctx, `SELECT kind,status FROM principals WHERE tenant_id=$1 AND id=$2`, actor.TenantID, actor.ID).Scan(&kind, &status); err != nil {
		return errors.New("actor must be an active person with tenant-admin authority")
	}
	if kind != "person" || status != "active" {
		return errors.New("actor must be an active person with tenant-admin authority")
	}
	if err := authz.RequireTx(ctx, tx, actor, "roles.manage", authz.Scope{}); err != nil {
		return errors.New("actor requires tenant-admin authority")
	}
	return nil
}

func manifestSnapshot(ctx context.Context, tx pgx.Tx, actor tenant.Principal, projectID string, rel releasehistory.Release, released time.Time) (releasehistory.NoteSnapshot, []string, error) {
	snap := releasehistory.NoteSnapshot{Schema: releasehistory.SnapshotSchema, TenantID: actor.TenantID, ProjectID: projectID, Version: rel.Version, VersionScheme: releasehistory.SchemeOf(rel.Version), Revision: 1, ReleasedAt: &released, MembershipSource: releasehistory.ManifestMembershipSource, FieldSource: releasehistory.FieldSource, Tickets: []releasehistory.NoteTicket{}}
	excluded := []string{}
	keys := append([]string{}, rel.Tickets...)
	for _, c := range rel.Changes {
		keys = append(keys, c.Tickets...)
	}
	// Capture the complete field set in one MVCC statement. Only current tickets
	// in this tenant/project can resolve the manifest's approximate membership.
	rows, err := tx.Query(ctx, `SELECT n.id::text,n.key,n.updated_at,coalesce(n.state,''),
  (SELECT coalesce(jsonb_object_agg(f.key,f.value),'{}'::jsonb) FROM jsonb_each(n.fields) f WHERE f.key IN ('pill_en','pill_de','benefit_en','benefit_de','hide_from_release_notes')),
  statement_timestamp(), aeon_release_note_group(n.fields)
  FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.project_id=$1 AND n.key=ANY($2::text[]) AND n.deleted_at IS NULL AND k.slug IN ('work','ticket')
  ORDER BY n.key,n.id`, projectID, keys)
	if err != nil {
		return snap, excluded, err
	}
	defer rows.Close()
	for rows.Next() {
		var ticket releasehistory.NoteTicket
		var state string
		if err := rows.Scan(&ticket.ID, &ticket.Key, &ticket.UpdatedAt, &state, &ticket.Fields, &snap.CapturedAt, &ticket.Group); err != nil {
			return snap, excluded, err
		}
		if !ticketbenefits.Completed(state) {
			excluded = append(excluded, ticket.Key)
			continue
		}
		ticket.Position = len(snap.Tickets)
		snap.Tickets = append(snap.Tickets, ticket)
	}
	if err := rows.Err(); err != nil {
		return snap, excluded, err
	}
	if snap.CapturedAt.IsZero() {
		err = tx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&snap.CapturedAt)
	}
	return snap, excluded, err
}

// Both capture queries return unique ticket identities. Count their usable
// notes with the same field validator as the release history renderer.
func snapshotNoteCounts(tickets []releasehistory.NoteTicket) (notes, hidden int, gapKeys []string) {
	gapKeys = []string{}
	for _, ticket := range tickets {
		var flags struct {
			Hidden bool `json:"hide_from_release_notes"`
		}
		_ = json.Unmarshal(ticket.Fields, &flags)
		if flags.Hidden {
			hidden++
			continue
		}
		if ticket.Unavailable == "" && len(ticketbenefits.Issues(ticket.Fields)) == 0 {
			notes++
		} else {
			gapKeys = append(gapKeys, ticket.Key)
		}
	}
	return
}
