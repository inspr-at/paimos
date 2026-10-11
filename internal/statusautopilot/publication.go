// SPDX-License-Identifier: AGPL-3.0-only
package statusautopilot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/config"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/jackc/pgx/v5"
)

// PublishTx runs after journey settlement. One bounded batch is immediate;
// remaining tickets are durably queued for the worker. A savepoint prevents an
// automation failure from rolling back an otherwise valid release settlement.
func PublishTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) error {
	mode, err := config.StatusAutopilotMode()
	if err != nil || mode == "off" {
		return err
	}
	batch, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	err = queueReleaseTx(ctx, batch, tenantID, releaseID)
	if err == nil {
		err = lock(ctx, batch)
	}
	if err == nil {
		_, err = deliveryBatchTx(ctx, batch, tenantID, releaseID, time.Now().UTC())
	}
	if err != nil {
		if rollbackErr := batch.Rollback(ctx); rollbackErr != nil {
			return rollbackErr
		}
		slog.Error("status autopilot publication deferred", "release_id", releaseID, "err", err)
		return nil
	}
	return batch.Commit(ctx)
}

func queueReleaseTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO status_autopilot_releases(tenant_id,release_id)
 SELECT tenant_id,release_node_id FROM journey_releases WHERE release_node_id=$1 AND state IN ('released','superseded')
 ON CONFLICT DO NOTHING`, releaseID)
	if err != nil || tag.RowsAffected() == 0 {
		return err
	}
	// Membership is read once, including frozen notes. No candidate ticket is
	// row-locked until its bounded delivery batch. Missing releases are ignored.
	_, err = tx.Exec(ctx, `INSERT INTO status_autopilot_deliveries(tenant_id,release_id,node_id)
 SELECT $2,$1,n.id FROM nodes n JOIN (
 SELECT ticket_node_id::text AS id FROM journey_tickets WHERE release_node_id=$1
 UNION
 SELECT ticket->>'id' FROM journey_release_note_snapshots s
 JOIN journey_releases r ON r.tenant_id=s.tenant_id AND r.release_node_id=s.release_node_id AND r.project_node_id=s.project_node_id
 CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') ticket WHERE s.release_node_id=$1
 UNION
 SELECT ticket->>'id' FROM release_manifest_note_snapshots s
 JOIN journey_releases r ON r.tenant_id=s.tenant_id AND r.project_node_id=s.project_node_id AND r.version=s.version
 CROSS JOIN LATERAL jsonb_array_elements(s.snapshot->'tickets') ticket WHERE r.release_node_id=$1
 ) members ON members.id=n.id::text
 JOIN journey_releases r ON r.release_node_id=$1 AND r.tenant_id=n.tenant_id AND n.project_id=r.project_node_id
 JOIN nodes release ON release.tenant_id=r.tenant_id AND release.id=r.release_node_id AND release.deleted_at IS NULL
 WHERE n.deleted_at IS NULL AND `+candidateStateSQL+`='done'
 AND n.fields->'no_release_needed' IS DISTINCT FROM 'true'::jsonb ON CONFLICT DO NOTHING`, releaseID, tenantID)
	return err
}

func deliveryBatchTx(ctx context.Context, tx pgx.Tx, tenantID, releaseID string, now time.Time) (int, error) {
	s, err := Load(ctx, tx)
	if err != nil || !s.Rules["publish"].Enabled || s.ModeAt(now) == "off" {
		return 0, err
	}
	// Remove disappeared work without loading a missing release/ticket. These
	// rows have no remaining automation action and must not poison the cursor.
	cleaned, err := tx.Exec(ctx, `DELETE FROM status_autopilot_deliveries WHERE (release_id,node_id) IN
 (SELECT d.release_id,d.node_id FROM status_autopilot_deliveries d WHERE ($1='' OR d.release_id=nullif($1,'')::uuid) AND (
 NOT EXISTS(SELECT 1 FROM nodes n WHERE n.tenant_id=d.tenant_id AND n.id=d.node_id AND n.deleted_at IS NULL)
 OR NOT EXISTS(SELECT 1 FROM nodes r WHERE r.tenant_id=d.tenant_id AND r.id=d.release_id AND r.deleted_at IS NULL))
 ORDER BY d.release_id,d.node_id LIMIT $2)`, releaseID, batchSize)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT d.release_id::text,d.node_id::text,r.project_node_id::text,release.title,coalesce(r.version,''),r.released_at
 FROM status_autopilot_deliveries d JOIN nodes n ON n.tenant_id=d.tenant_id AND n.id=d.node_id
 JOIN journey_releases r ON r.tenant_id=d.tenant_id AND r.release_node_id=d.release_id
 JOIN nodes release ON release.tenant_id=r.tenant_id AND release.id=r.release_node_id
 LEFT JOIN status_autopilot_projects o ON o.tenant_id=r.tenant_id AND o.project_id=r.project_node_id
 WHERE ($1='' OR d.release_id=nullif($1,'')::uuid) AND d.retry_after<=$2
 AND (NOT d.checked OR nullif(btrim(n.human_check),'') IS NULL)
 AND CASE coalesce(o.mode,'inherit') WHEN 'off' THEN false WHEN 'on' THEN true ELSE $3 END
 ORDER BY d.release_id,d.node_id LIMIT $4`, releaseID, now, s.Enabled, batchSize)
	if err != nil {
		return 0, err
	}
	type delivery struct {
		release, id, project, title, version string
		published                            time.Time
	}
	var work []delivery
	for rows.Next() {
		var d delivery
		if err = rows.Scan(&d.release, &d.id, &d.project, &d.title, &d.version, &d.published); err != nil {
			rows.Close()
			return 0, err
		}
		work = append(work, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(work) == 0 {
		return int(cleaned.RowsAffected()), err
	}
	p, err := systemactor.Ensure(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}
	for _, d := range work {
		item, err := tx.Begin(ctx)
		if err != nil {
			return 0, err
		}
		waiting, suggesting := false, false
		c, _, err := loadCandidate(ctx, item, d.id, d.published)
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		} else if err == nil && c.Node.ProjectID != nil && *c.Node.ProjectID == d.project && normaliseState(c.Node.State) == "done" && !noReleaseNeeded(c.Node) && !c.Since.After(d.published) {
			waiting = pending(c.Node)
			suggesting = s.ModeAt(now) == "suggest"
			err = enact(ctx, item, p, c.Node, deliveryDecision(c.Node, d.release, d.title, d.version), s.ModeAt(now))
		}
		if err != nil {
			if rollbackErr := item.Rollback(ctx); rollbackErr != nil {
				return 0, rollbackErr
			}
			slog.Error("status autopilot ticket deferred", "node_id", d.id, "err", err)
			_, err = tx.Exec(ctx, `UPDATE status_autopilot_deliveries SET retry_after=$3 WHERE release_id=$1 AND node_id=$2`, d.release, d.id, now.Add(24*time.Hour))
		} else {
			if suggesting {
				// Preserve publication work through the suggestion period. Deferring
				// the row also makes a full proposal batch yield instead of spinning.
				_, err = item.Exec(ctx, `UPDATE status_autopilot_deliveries SET retry_after=$3 WHERE release_id=$1 AND node_id=$2`, d.release, d.id, now.Add(time.Minute))
			} else if waiting {
				_, err = item.Exec(ctx, `UPDATE status_autopilot_deliveries SET checked=true WHERE release_id=$1 AND node_id=$2`, d.release, d.id)
			} else {
				_, err = item.Exec(ctx, `DELETE FROM status_autopilot_deliveries WHERE release_id=$1 AND node_id=$2`, d.release, d.id)
			}
			if err == nil {
				err = item.Commit(ctx)
			} else {
				_ = item.Rollback(ctx)
			}
		}
		if err != nil {
			return 0, err
		}
	}
	return max(len(work), int(cleaned.RowsAffected())), nil
}

func deliveryDecision(n node, releaseID, title, version string) decision {
	d := decision{Rule: "publish", To: "delivered", Anchor: releaseID, Reason: fmt.Sprintf("Shipped in release %s (%s).", title, version)}
	if pr := fieldString(n, "pr_url"); pr != "" {
		d.Reason += " PR: " + pr + "."
	}
	if merge := fieldString(n, "merge_commit"); merge != "" {
		d.Reason += " Merge: " + merge + "."
	}
	if pending(n) {
		d.Skip = true
		d.To = ""
		d.Anchor += "/human-check/" + *n.HumanCheck
		d.Reason = "Needs a human check: " + strings.TrimSpace(*n.HumanCheck) + ". Delivery in " + title + " (" + version + ") was skipped."
	}
	return d
}
