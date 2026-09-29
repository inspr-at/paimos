// SPDX-License-Identifier: AGPL-3.0-only

package releases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/operatoractor"
	"github.com/inspr-at/paimos/internal/releasehistory"
	"github.com/inspr-at/paimos/internal/tenant"
)

const noteBackfillEvent = "release.notes_backfilled"

// NoteBackfillItem is one published release whose snapshot would be inserted,
// or was inserted when the report's Applied is true.
type NoteBackfillItem struct {
	ProjectID  string    `json:"project_node_id"`
	ReleaseID  string    `json:"release_node_id"`
	Version    string    `json:"version"`
	ReleasedAt time.Time `json:"released_at"`
	Revision   int64     `json:"release_revision"`
	Tickets    int       `json:"ticket_count"`
}

// NoteBackfillSkip is a published release whose current membership could not
// be captured, so nothing was inserted.
type NoteBackfillSkip struct {
	ProjectID string `json:"project_node_id"`
	ReleaseID string `json:"release_node_id"`
	Reason    string `json:"reason"`
}

// NoteBackfillReport is the operator plan. Inserted is zero unless Applied.
// Planned lists the releases a dry-run would insert, or the releases this
// apply inserted. An existing snapshot is counted in Unchanged and never rewritten.
type NoteBackfillReport struct {
	Applied   bool               `json:"applied"`
	TenantID  string             `json:"tenant_id"`
	Planned   []NoteBackfillItem `json:"planned"`
	Inserted  int                `json:"inserted"`
	Unchanged int                `json:"unchanged"`
	Skipped   []NoteBackfillSkip `json:"skipped"`
}

const backfillSkipReason = "Release note snapshot could not be captured."

// Readers never call aeon_release_note_snapshot for a published release that
// has no stored row (AEON-256). This statement is the audited exception: one
// insert, labelled backfilled, with the release's original released_at and
// captured_at set by the snapshot function to statement time. The immutability
// trigger still rejects any later update or delete.
const backfillApplySQL = `
WITH unchanged AS (
    SELECT count(*)::int AS n
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = $1::uuid
      AND r.state IN ('released', 'superseded')
      AND EXISTS (
          SELECT 1 FROM journey_release_note_snapshots s
          WHERE s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id)
),
locked AS MATERIALIZED (
    SELECT r.tenant_id, r.release_node_id, r.project_node_id, r.released_at, r.revision,
           coalesce(r.version, '') AS version,
           aeon_release_note_snapshot(r.project_node_id, r.release_node_id) AS snap
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = $1::uuid
      AND r.state IN ('released', 'superseded')
      AND NOT EXISTS (
          SELECT 1 FROM journey_release_note_snapshots s
          WHERE s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id)
    ORDER BY r.released_at, r.release_node_id
    FOR UPDATE OF r
),
ready AS (
    SELECT tenant_id, release_node_id, project_node_id, released_at, revision, version,
           CASE WHEN snap IS NOT NULL THEN jsonb_set(jsonb_set(jsonb_set(
               snap, '{frozen}', 'true'::jsonb),
               '{label}', to_jsonb($2::text)),
               '{released_at}', to_jsonb(released_at)) END AS labelled
    FROM locked
),
inserted AS (
    INSERT INTO journey_release_note_snapshots (tenant_id, release_node_id, project_node_id, snapshot)
    SELECT tenant_id, release_node_id, project_node_id, labelled
    FROM ready
    WHERE labelled IS NOT NULL
    ON CONFLICT (tenant_id, release_node_id) DO NOTHING
    RETURNING release_node_id
)
SELECT u.n,
       ready.project_node_id::text,
       ready.release_node_id::text,
       ready.version,
       ready.released_at,
       ready.revision,
       coalesce(jsonb_array_length(ready.labelled -> 'tickets'), 0),
       CASE WHEN ready.labelled IS NULL THEN NULL ELSE (ready.labelled ->> 'captured_at')::timestamptz END,
       CASE
           WHEN inserted.release_node_id IS NOT NULL THEN 'inserted'
           WHEN ready.release_node_id IS NULL THEN NULL
           WHEN ready.labelled IS NULL THEN 'skipped'
           ELSE 'unchanged'
       END
FROM unchanged u
LEFT JOIN ready ON true
LEFT JOIN inserted ON inserted.release_node_id = ready.release_node_id
ORDER BY ready.released_at, ready.release_node_id`

const backfillPlanSQL = `
WITH unchanged AS (
    SELECT count(*)::int AS n
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = $1::uuid
      AND r.state IN ('released', 'superseded')
      AND EXISTS (
          SELECT 1 FROM journey_release_note_snapshots s
          WHERE s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id)
),
missing AS (
    SELECT r.project_node_id, r.release_node_id, coalesce(r.version, '') AS version,
           r.released_at, r.revision,
           aeon_release_note_snapshot(r.project_node_id, r.release_node_id) AS snap
    FROM journey_releases r
    JOIN nodes rn ON rn.tenant_id = r.tenant_id AND rn.id = r.release_node_id AND rn.deleted_at IS NULL
    JOIN nodes pn ON pn.tenant_id = r.tenant_id AND pn.id = r.project_node_id AND pn.deleted_at IS NULL
    WHERE r.tenant_id = $1::uuid
      AND r.state IN ('released', 'superseded')
      AND NOT EXISTS (
          SELECT 1 FROM journey_release_note_snapshots s
          WHERE s.tenant_id = r.tenant_id AND s.release_node_id = r.release_node_id)
)
SELECT u.n,
       missing.project_node_id::text,
       missing.release_node_id::text,
       missing.version,
       missing.released_at,
       missing.revision,
       coalesce(jsonb_array_length(missing.snap -> 'tickets'), 0),
       NULL::timestamptz,
       CASE
           WHEN missing.release_node_id IS NULL THEN NULL
           WHEN missing.snap IS NULL THEN 'skipped'
           ELSE 'planned'
       END
FROM unchanged u
LEFT JOIN missing ON true
ORDER BY missing.released_at, missing.release_node_id`

type backfillRow struct {
	item       NoteBackfillItem
	capturedAt time.Time
	outcome    string
}

// BackfillNoteSnapshots plans, or inserts, one snapshot for each published
// release in the tenant that does not have one. It sees every project: a
// partial plan would look complete. Dry-run writes nothing. Apply inserts
// and appends one release.notes_backfilled event per inserted release.
// actorID empty attributes those events to the tenant's access operator.
func BackfillNoteSnapshots(ctx context.Context, pool *pgxpool.Pool, tenantID, actorID string, apply bool) (NoteBackfillReport, error) {
	report := NoteBackfillReport{Applied: apply, TenantID: tenantID, Planned: []NoteBackfillItem{}, Skipped: []NoteBackfillSkip{}}
	if pool == nil || !uuid.MatchString(tenantID) {
		return report, errors.New("tenant id is required")
	}
	if actorID != "" && !uuid.MatchString(actorID) {
		return report, errors.New("actor principal id must be a UUID")
	}
	ctx = db.AllProjects(ctx, "release note backfill")
	var inserted []backfillRow
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if apply {
			if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `SET LOCAL transaction_read_only = on`); err != nil {
			return err
		}
		query := backfillPlanSQL
		args := []any{tenantID}
		if apply {
			query = backfillApplySQL
			args = append(args, releasehistory.BackfillLabel)
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var seenCount bool
		for rows.Next() {
			var n int
			var projectID, releaseID, version, outcome *string
			var releasedAt, capturedAt *time.Time
			var revision *int64
			var tickets *int
			if err := rows.Scan(&n, &projectID, &releaseID, &version, &releasedAt, &revision, &tickets, &capturedAt, &outcome); err != nil {
				return err
			}
			if !seenCount {
				report.Unchanged = n
				seenCount = true
			}
			if releaseID == nil || projectID == nil || outcome == nil {
				continue
			}
			item := NoteBackfillItem{ProjectID: *projectID, ReleaseID: *releaseID}
			if version != nil {
				item.Version = *version
			}
			if releasedAt != nil {
				item.ReleasedAt = *releasedAt
			}
			if revision != nil {
				item.Revision = *revision
			}
			if tickets != nil {
				item.Tickets = *tickets
			}
			switch *outcome {
			case "planned", "inserted":
				report.Planned = append(report.Planned, item)
				if *outcome == "inserted" {
					row := backfillRow{item: item}
					if capturedAt != nil {
						row.capturedAt = *capturedAt
					}
					inserted = append(inserted, row)
				}
			case "skipped":
				report.Skipped = append(report.Skipped, NoteBackfillSkip{ProjectID: *projectID, ReleaseID: *releaseID, Reason: backfillSkipReason})
			case "unchanged":
				report.Unchanged++
			default:
				return fmt.Errorf("unexpected backfill outcome %q", *outcome)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !apply || len(inserted) == 0 {
			return nil
		}
		actor, err := backfillActor(ctx, tx, tenantID, actorID)
		if err != nil {
			return err
		}
		for _, row := range inserted {
			releaseID := row.item.ReleaseID
			if _, err := events.Append(ctx, tx, actor, events.Change{
				NodeID: &releaseID,
				Type:   noteBackfillEvent,
				After: map[string]any{
					"label":            releasehistory.BackfillLabel,
					"project_node_id":  row.item.ProjectID,
					"release_node_id":  row.item.ReleaseID,
					"released_at":      row.item.ReleasedAt,
					"captured_at":      row.capturedAt,
					"release_revision": row.item.Revision,
					"ticket_count":     row.item.Tickets,
				},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return report, errors.New("release note backfill timed out waiting for a lock")
		}
		return report, err
	}
	if apply {
		report.Inserted = len(report.Planned)
	}
	return report, nil
}

func backfillActor(ctx context.Context, tx pgx.Tx, tenantID, actorID string) (tenant.Principal, error) {
	if actorID == "" {
		id, err := operatoractor.Ensure(ctx, tx, tenantID)
		if err != nil {
			return tenant.Principal{}, err
		}
		return tenant.Principal{ID: id, TenantID: tenantID, Kind: tenant.Agent}, nil
	}
	var kind, status string
	if err := tx.QueryRow(ctx, `SELECT kind, status FROM principals WHERE id=$1::uuid`, actorID).Scan(&kind, &status); err != nil {
		return tenant.Principal{}, fmt.Errorf("actor: %w", err)
	}
	if status != "active" {
		return tenant.Principal{}, errors.New("actor is inactive")
	}
	actor := tenant.Principal{ID: actorID, TenantID: tenantID, Kind: tenant.Person}
	if kind == string(tenant.Agent) {
		actor.Kind = tenant.Agent
	} else if kind != string(tenant.Person) {
		return tenant.Principal{}, errors.New("actor must be a person or agent")
	}
	return actor, nil
}
