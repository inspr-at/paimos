// SPDX-License-Identifier: AGPL-3.0-only

package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
	"github.com/inspr-at/paimos/internal/tenant"
)

const legacyDismissReason = "auto-dismissed: legacy title-only nomination without a lesson (Markus 2026-10-14)"
const legacyCleanupBatch = 100

// recordDismissal is shared by agent/person dismissals and the release cleanup.
// Its caller holds the tenant/tree and learning locks. All referenced rows are
// locked before Append takes the event counter; decisions reuse those locks.
func recordDismissal(ctx context.Context, tx pgx.Tx, p tenant.Principal, item Learning, reason string) (LearningDecision, error) {
	if _, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE tenant_id=$1 AND id=ANY($2::uuid[]) ORDER BY id FOR KEY SHARE`, p.TenantID, nonEmpty(item.NodeID, item.projectID)); err != nil {
		return LearningDecision{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT id FROM principals WHERE tenant_id=$1 AND id=$2::uuid FOR KEY SHARE`, p.TenantID, p.ID); err != nil {
		return LearningDecision{}, err
	}
	var metadata json.RawMessage
	if reason == legacyDismissReason {
		metadata = json.RawMessage(`{"job":"learning-legacy-cleanup","ticket":"AEON-1330"}`)
	}
	ev, err := events.Append(ctx, tx, p, events.Change{NodeID: &item.NodeID, Type: evLearningDismissed, Metadata: metadata, After: learningAudit{
		SourceKey: item.ID, Source: item.Source, NodeID: item.NodeID, CommentID: item.CommentID, ProjectID: item.projectID, Text: item.Text, Reason: reason,
	}})
	if err != nil {
		return LearningDecision{}, err
	}
	if err := insertDecision(ctx, tx, p, item.projectID, item.ID, "dismissed", nil, nil, nil, ev.ID, nil, nil, nil); err != nil {
		return LearningDecision{}, err
	}
	return LearningDecision{ID: item.ID, Decision: "dismissed", EventID: ev.ID}, nil
}

// CleanupLegacyLearnings runs on every instance's next startup. The migration
// records the release cutoff for existing tenants. Each tenant resumes its
// bounded keyset scan after interruption, then remains complete even after undo.
// Any recommendation, including a legacy or stale one, is preserved.
func CleanupLegacyLearnings(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	total := 0
	after := ""
	var first error
	for {
		rows, err := pool.Query(ctx, `SELECT id::text FROM tenants WHERE id::text>$1 ORDER BY id::text LIMIT 100`, after)
		if err != nil {
			return total, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return total, err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, first
		}
		for _, id := range ids {
			n, err := cleanupLegacyTenant(ctx, pool, id)
			total += n
			if err != nil && first == nil {
				first = err
			}
		}
		after = ids[len(ids)-1]
	}
}

// Legacy sources are collected independently of inbox pagination and sensitive
// text. The audit deliberately copies no source text; cleanup never copies a
// credential from an old source into a new event.
const legacySources = `
 SELECT m.source_key FROM method_learning_nominations m JOIN nodes n ON n.tenant_id=m.tenant_id AND n.id=m.node_id
 WHERE m.tenant_id=$1 AND m.nominated_at<$2 AND n.deleted_at IS NULL
 UNION
 SELECT 'n-'||n.id::text FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
 WHERE n.tenant_id=$1 AND n.deleted_at IS NULL AND n.updated_at<$2 AND n.project_id IS NOT NULL
 AND k.slug IN ('work','ticket','task','epic','incident')
 AND EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(n.fields->'tags')='array' THEN n.fields->'tags' ELSE '[]'::jsonb END) tag
 WHERE lower(btrim(CASE WHEN jsonb_typeof(tag)='string' THEN tag #>> '{}' ELSE coalesce(tag->>'name','') END))='process-learning')
 UNION
 SELECT 'c-'||n.id::text||'-'||c.id::text FROM events c JOIN nodes n ON n.tenant_id=c.tenant_id AND n.id=c.node_id
 LEFT JOIN LATERAL (SELECT after FROM events e WHERE e.tenant_id=c.tenant_id AND e.node_id=c.node_id
 AND e.type IN ('comment.updated','comment.deleted') AND e.after->>'comment_id'=c.id::text ORDER BY e.id DESC LIMIT 1) latest ON true
 WHERE c.tenant_id=$1 AND c.type='comment.created' AND c.at<$2 AND n.deleted_at IS NULL
 AND coalesce(latest.after->>'deleted','false')<>'true'
 AND coalesce(latest.after->>'body_markdown',c.after->>'body_markdown','') ~* '(^|[^A-Za-z0-9_-])#?process-learning([^A-Za-z0-9_-]|$)'
`

func cleanupLegacyTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ctx = db.AllProjects(ctx, "method learning legacy cleanup")
	var actor tenant.Principal
	var complete bool
	// Create the System principal separately, before any source/event transaction.
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO method_learning_legacy_cleanup(tenant_id) VALUES($1) ON CONFLICT DO NOTHING`, tenantID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT completed_at IS NOT NULL FROM method_learning_legacy_cleanup WHERE tenant_id=$1 FOR UPDATE`, tenantID).Scan(&complete); err != nil {
			return err
		}
		if complete {
			return nil
		}
		var err error
		actor, err = systemactor.Ensure(ctx, tx, tenantID)
		return err
	})
	if err != nil || complete {
		return 0, err
	}
	total := 0
	for {
		var keys []string
		err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			var cutoff time.Time
			if err := tx.QueryRow(ctx, `SELECT cutoff FROM method_learning_legacy_cleanup WHERE tenant_id=$1`, tenantID).Scan(&cutoff); err != nil {
				return err
			}
			if err := db.SetLocalStatementTimeout(ctx, tx, 20*time.Second); err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT source_key FROM (`+legacySources+`) old
   WHERE source_key COLLATE "C">coalesce((SELECT after_key FROM method_learning_legacy_cleanup WHERE tenant_id=$1),'') COLLATE "C"
   AND NOT EXISTS (SELECT 1 FROM method_learning_decisions d WHERE d.tenant_id=$1 AND d.source_key=old.source_key)
   AND NOT EXISTS (SELECT 1 FROM method_learning_recommendations r WHERE r.tenant_id=$1 AND r.source_key=old.source_key)
   ORDER BY source_key COLLATE "C" LIMIT $3`, tenantID, cutoff, legacyCleanupBatch)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var key string
				if err := rows.Scan(&key); err != nil {
					return err
				}
				keys = append(keys, key)
			}
			return rows.Err()
		})
		if err != nil {
			return total, err
		}
		if len(keys) == 0 {
			err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
				if err := authz.LockProjectWrite(ctx, tx, tenantID); err != nil {
					return err
				}
				_, err := tx.Exec(ctx, `UPDATE method_learning_legacy_cleanup SET completed_at=clock_timestamp() WHERE tenant_id=$1 AND completed_at IS NULL`, tenantID)
				return err
			})
			if err != nil {
				return total, err
			}
			return total, logLegacyCounts(ctx, pool, tenantID)
		}
		for _, key := range keys {
			wrote, err := cleanupLegacyItem(ctx, pool, actor, key)
			if err != nil {
				return total, err
			}
			if wrote {
				total++
			}
		}
	}
}

func cleanupLegacyItem(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	wrote := false
	err := db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, p.TenantID); err != nil {
			return err
		}
		var done bool
		var after string
		if err := tx.QueryRow(ctx, `SELECT completed_at IS NOT NULL,coalesce(after_key,'') FROM method_learning_legacy_cleanup WHERE tenant_id=$1 FOR UPDATE`, p.TenantID).Scan(&done, &after); err != nil {
			return err
		}
		if done || key <= after {
			return nil
		}
		if err := lockLearning(ctx, tx, p.TenantID, key); err != nil {
			return err
		}
		var protected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM method_learning_recommendations WHERE tenant_id=$1 AND source_key=$2) OR EXISTS(SELECT 1 FROM method_learning_decisions WHERE tenant_id=$1 AND source_key=$2)`, p.TenantID, key).Scan(&protected); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE method_learning_legacy_cleanup SET after_key=$2 WHERE tenant_id=$1`, p.TenantID, key); err != nil {
			return err
		}
		if protected {
			return nil
		}
		nodeID, commentID, comment, err := parseLearningID(key)
		if err != nil {
			return err
		}
		item := Learning{ID: key, NodeID: nodeID, CommentID: commentID, Source: "ticket"}
		if comment {
			item.Source = "comment"
		}
		err = tx.QueryRow(ctx, `SELECT CASE WHEN k.slug='project' THEN n.id::text ELSE coalesce(n.project_id::text,'') END FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id WHERE n.tenant_id=$1 AND n.id=$2::uuid AND n.deleted_at IS NULL`, p.TenantID, nodeID).Scan(&item.projectID)
		if errors.Is(err, pgx.ErrNoRows) || item.projectID == "" {
			return nil
		}
		if err != nil {
			return err
		}
		var cutoff time.Time
		if err := tx.QueryRow(ctx, `SELECT cutoff FROM method_learning_legacy_cleanup WHERE tenant_id=$1`, p.TenantID).Scan(&cutoff); err != nil {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM (`+legacySources+`) old WHERE source_key=$3)`, p.TenantID, cutoff, key).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return nil
		}
		// System has no user role binding. This internal job checks its exact
		// active service identity under the tenant/tree access fence. HTTP
		// dismissals continue to use fenceLearningWrite and RequireTx.
		if err := requireCleanupActor(ctx, tx, p); err != nil {
			return err
		}
		if _, err := recordDismissal(ctx, tx, p, item, legacyDismissReason); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

func logLegacyCounts(ctx context.Context, pool *pgxpool.Pool, tenantID string) error {
	return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.project_id::text,n.key,count(*) FROM method_learning_decisions d JOIN nodes n ON n.tenant_id=d.tenant_id AND n.id=d.project_id JOIN events e ON e.tenant_id=d.tenant_id AND e.id=d.event_id WHERE d.tenant_id=$1 AND e.metadata->>'job'='learning-legacy-cleanup' GROUP BY d.project_id,n.key ORDER BY n.key`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, key string
			var count int
			if err := rows.Scan(&id, &key, &count); err != nil {
				return err
			}
			slog.Info("method learning legacy cleanup", "tenant_id", tenantID, "project_id", id, "project_key", key, "dismissed", count)
		}
		return rows.Err()
	})
}

func requireCleanupActor(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	if p.Kind != tenant.Agent || p.Name != "System" {
		return authz.ErrForbidden
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM principals WHERE tenant_id=$1 AND id=$2::uuid AND kind='agent' AND name='System' AND status='active' AND roles=ARRAY['system']::text[] FOR KEY SHARE`, p.TenantID, p.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return authz.ErrForbidden
	}
	return err
}
