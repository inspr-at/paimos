// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/attachments"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/systemactor"
)

const (
	retentionBatch   = 8
	retentionTenants = 16
	retentionRecords = 1000
)

// Person activity is immutable evidence, read in the final fenced transaction.
// node_refs preserves before/after project references, including a moved node;
// node_id also covers comments whose only snapshot is their text. No event
// trigger acquires resource locks after blob locks or the event counter.
const retentionEligible = `r.source='start' AND r.deleted_at IS NULL AND r.engaged_at IS NULL
 AND r.imported_at <= $1::timestamptz - interval '12 months'
 AND EXISTS (SELECT 1 FROM nodes n JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.tenant_id=r.tenant_id AND n.id=r.project_id AND k.slug='project')
 AND NOT EXISTS (SELECT 1 FROM events e JOIN principals p ON p.tenant_id=e.tenant_id AND p.id=e.actor_principal_id
  WHERE e.tenant_id=r.tenant_id AND p.kind='person' AND e.metadata->>'imported_from' IS DISTINCT FROM 'start'
   AND (e.node_id=r.project_id OR e.node_refs @> ARRAY[r.project_id]
    OR e.node_id IN (SELECT n.id FROM nodes n WHERE n.tenant_id=r.tenant_id AND n.project_id=r.project_id ORDER BY n.id LIMIT 1001)
    OR e.node_refs && ARRAY(SELECT n.id FROM nodes n WHERE n.tenant_id=r.tenant_id AND n.project_id=r.project_id ORDER BY n.id LIMIT 1001)))`

type RetentionCandidate struct {
	ProjectID  string    `json:"project_id"`
	ImportedAt time.Time `json:"imported_at"`
}

type RetentionResult struct {
	Candidates []RetentionCandidate
	Deleted    int
}

// RetainTenant processes at most eight projects. Claims rotate even dry-run or
// failed projects so a protected reference cannot starve later candidates.
// Every actual deletion rechecks settings, source and engagement under the
// same access-change fence as person writes. Each deletion commits separately.
func (m *Module) RetainTenant(ctx context.Context, tenantID string, now time.Time) (RetentionResult, error) {
	ctx = db.AllProjects(ctx, "START project retention")
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out := RetentionResult{Candidates: []RetentionCandidate{}}
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, tenantID); err != nil {
			return err
		}
		// Older imports predate this table; their immutable source events are
		// equally positive provenance. Bound migration work on each scheduler pass.
		if _, err := tx.Exec(ctx, `INSERT INTO import_project_retention(tenant_id,project_id,source,imported_at)
   SELECT n.tenant_id,n.id,'start',n.created_at FROM nodes n
   JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id AND k.slug='project'
   WHERE EXISTS(SELECT 1 FROM events e WHERE e.tenant_id=n.tenant_id AND e.node_id=n.id
    AND e.type='node.created' AND e.metadata->>'imported_from'='start')
    AND NOT EXISTS(SELECT 1 FROM import_project_retention r WHERE r.project_id=n.id)
   ORDER BY n.id LIMIT $1 ON CONFLICT DO NOTHING`, retentionBatch); err != nil {
			return err
		}
		s, err := m.retentionSettingsTx(ctx, tx, tenantID)
		if err != nil || !s.Enabled {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT r.project_id::text,r.imported_at FROM import_project_retention r
		 WHERE `+retentionEligible+` ORDER BY r.last_checked_at NULLS FIRST,r.imported_at,r.project_id LIMIT $2`, now, retentionBatch)
		if err != nil {
			return err
		}
		out.Candidates, err = pgx.CollectRows(rows, pgx.RowToStructByPos[RetentionCandidate])
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(out.Candidates))
		for _, c := range out.Candidates {
			ids = append(ids, c.ProjectID)
		}
		_, err = tx.Exec(ctx, `UPDATE import_project_retention SET last_checked_at=$1 WHERE project_id=ANY($2::uuid[])`, now, ids)
		return err
	})
	if err != nil {
		return out, err
	}
	var failures []error
	for _, c := range out.Candidates {
		deleted, err := m.retainProject(ctx, tenantID, c.ProjectID, now)
		if err != nil {
			failures = append(failures, fmt.Errorf("START retention project %s: %w", c.ProjectID, err))
		}
		if deleted {
			out.Deleted++
		}
	}
	return out, errors.Join(failures...)
}

func (m *Module) retainProject(ctx context.Context, tenantID, projectID string, now time.Time) (bool, error) {
	// Ensure can append a principal event on first use; keep it outside the
	// deletion transaction, before that transaction takes any resource locks.
	var actorID string
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, tenantID); err != nil {
			return err
		}
		s, err := m.retentionSettingsTx(ctx, tx, tenantID)
		if err != nil || !s.Enabled || s.DryRun {
			return err
		}
		actor, err := systemactor.Ensure(ctx, tx, tenantID)
		actorID = actor.ID
		return err
	})
	if err != nil || actorID == "" {
		return false, err
	}
	deleted := false
	err = db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if err := authz.LockProjectWrite(ctx, tx, tenantID); err != nil {
			return err
		}
		s, err := m.retentionSettingsTx(ctx, tx, tenantID)
		if err != nil || !s.Enabled || s.DryRun {
			return err
		}
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM import_project_retention r WHERE r.project_id=$2 AND `+retentionEligible+`)`, now, projectID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return nil
		}
		if _, err := tx.Exec(ctx, `SELECT project_id FROM import_project_retention WHERE project_id=$1 FOR NO KEY UPDATE`, projectID); err != nil {
			return err
		}
		if err := deleteImportedRecords(ctx, tx, tenantID, projectID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE import_project_retention SET deleted_at=$2 WHERE project_id=$1`, projectID, now); err != nil {
			return err
		}
		// The receipt contains ids and policy only, never a deleted conversation.
		actor := systemPrincipal(tenantID, actorID)
		_, err = events.Append(ctx, tx, actor, events.Change{Type: "import_retention.deleted", After: map[string]any{"project_id": projectID, "source": "start", "months": 12}, At: &now})
		deleted = err == nil
		return err
	})
	return deleted && err == nil, err
}

// deleteImportedRecords permanently removes the project tree and its ordinary
// records. Nested projects are independent ownership boundaries. Unknown or
// protected dependants fail their existing FK/immutability guards and roll the
// whole deletion back; never disable a guard or cascade into unrelated data.
func deleteImportedRecords(ctx context.Context, tx pgx.Tx, tenantID, projectID string) error {
	rows, err := tx.Query(ctx, `WITH RECURSIVE tree(id) AS (
	 SELECT id FROM nodes WHERE id=$1::uuid UNION
	 SELECT n.id FROM nodes n JOIN tree t ON n.parent_id=t.id
	), bounded AS MATERIALIZED (SELECT id FROM tree LIMIT $2)
 SELECT n.id::text,n.project_id::text FROM bounded t JOIN nodes n ON n.id=t.id`, projectID, retentionRecords+1)
	if err != nil {
		return err
	}
	type node struct{ ID, Project string }
	nodes, err := pgx.CollectRows(rows, pgx.RowToStructByPos[node])
	if err != nil {
		return err
	}
	if len(nodes) > retentionRecords {
		return errors.New("project record budget exceeded")
	}
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n.Project != projectID {
			return errors.New("nested project requires separate retention authority")
		}
		ids = append(ids, n.ID)
	}
	// Lock all node rows before blob lifetime locks. The tree fence protects
	// membership throughout the bounded discovery and final deletion.
	if _, err := tx.Exec(ctx, `SELECT id FROM nodes WHERE id=ANY($1::uuid[]) ORDER BY id FOR NO KEY UPDATE`, ids); err != nil {
		return err
	}
	// Bound auxiliary lists before locking or deleting them. Content-addressed
	// bytes become eligible for the existing file GC after their last live or
	// retained-history owner disappears. Blob locks are one sorted batch.
	rows, err = tx.Query(ctx, `SELECT sha256 FROM attachments WHERE node_id=ANY($1::uuid[]) ORDER BY sha256,id LIMIT $2 FOR NO KEY UPDATE`, ids, retentionRecords+1)
	if err != nil {
		return err
	}
	hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(hashes) > retentionRecords {
		return errors.New("attachment budget exceeded")
	}
	for _, q := range []string{
		`SELECT count(*) FROM (SELECT 1 FROM node_relations WHERE source_node_id=ANY($1::uuid[]) OR target_node_id=ANY($1::uuid[]) LIMIT 1001) b`,
		`SELECT count(*) FROM (SELECT 1 FROM node_key_aliases WHERE node_id=ANY($1::uuid[]) LIMIT 1001) b`,
	} {
		var count int
		if err := tx.QueryRow(ctx, q, ids).Scan(&count); err != nil {
			return err
		}
		if count > retentionRecords {
			return errors.New("linked record budget exceeded")
		}
	}
	// Remove known ordinary references before blobs. Existing FK constraints
	// deliberately remain authoritative for protected business/process data.
	for _, q := range []string{
		`DELETE FROM node_relations WHERE source_node_id=ANY($1::uuid[]) OR target_node_id=ANY($1::uuid[])`,
		`DELETE FROM node_key_aliases WHERE node_id=ANY($1::uuid[])`,
		`DELETE FROM ticket_live_eta WHERE node_id=ANY($1::uuid[])`,
		`DELETE FROM import_project_drafts WHERE project_id=ANY($1::uuid[])`,
	} {
		if _, err := tx.Exec(ctx, q, ids); err != nil {
			return err
		}
	}
	// Retain only the bounded deleted ids for positive, START-only file GC
	// ownership release. Other historical attachment owners are unchanged.
	if _, err := tx.Exec(ctx, `UPDATE import_project_retention SET deleted_node_ids=$2::uuid[] WHERE project_id=$1`, projectID, ids); err != nil {
		return err
	}
	if err := attachments.LockBlobs(ctx, tx, tenantID, hashes...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM attachments WHERE node_id=ANY($1::uuid[])`, ids); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM nodes WHERE id=ANY($1::uuid[])`, ids)
	return err
}

// sweepRetention advances a bounded tenant page on the existing expiry loop.
func (m *Module) sweepRetention(ctx context.Context, now time.Time, after string) (string, error) {
	ctx = db.AllProjects(ctx, "START retention tenant page")
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var tenants []string
	err := db.InTenant(ctx, m.pool, "00000000-0000-0000-0000-000000000000", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM tenants WHERE id>coalesce(nullif($1,'')::uuid,'00000000-0000-0000-0000-000000000000'::uuid) ORDER BY id LIMIT $2`, after, retentionTenants)
		if err != nil {
			return err
		}
		tenants, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return after, err
	}
	var failures []error
	for _, id := range tenants {
		if ctx.Err() != nil {
			return after, errors.Join(append(failures, ctx.Err())...)
		}
		if _, err := m.RetainTenant(ctx, id, now); err != nil {
			failures = append(failures, err)
		}
		after = id
	}
	if len(tenants) < retentionTenants {
		after = ""
	}
	return after, errors.Join(failures...)
}
