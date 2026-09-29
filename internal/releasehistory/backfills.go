// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/tenant"
)

// HasSnapshot includes empty and hidden-only captures: neither is a fallback.
func HasSnapshot(rel Release) bool {
	return rel.Notes != nil && rel.Notes.Source != "unavailable"
}

// ResolveProject resolves an explicit route key, node key or UUID through the
// caller's tenant/project visibility. Ambiguous route keys fail closed.
func ResolveProject(ctx context.Context, tx pgx.Tx, key string) (string, error) {
	rows, err := tx.Query(ctx, `SELECT n.id::text FROM nodes n
  JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
  WHERE n.deleted_at IS NULL AND k.slug='project' AND
  (n.id::text=$1 OR n.key=$1 OR coalesce(nullif(n.fields->>'project_key',''),nullif(n.fields->'classic'->>'key',''),split_part(n.key,'-',1))=$1)`, key)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) == 0 {
		return "", pgx.ErrNoRows
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("ambiguous project key")
	}
	return ids[0], nil
}

// WithBackfills adds a tenant-scoped database overlay for one product project.
// The build's tag snapshots stay authoritative. No live ticket fields are read.
func (m *Module) WithBackfills(pool *pgxpool.Pool, projectKey string) *Module {
	m.pool, m.projectKey = pool, projectKey
	return m
}

func (m *Module) historyFor(ctx context.Context) (History, error) {
	h := m.history
	if m.pool == nil {
		return h, nil
	}
	p, ok := tenant.PrincipalFrom(ctx)
	if !ok || p.TenantID == "" {
		return h, nil
	}
	// Copy before overlaying: a request must never alter another tenant's cache.
	h.Releases = append([]Release{}, h.Releases...)
	err := db.InTenant(ctx, m.pool, p.TenantID, func(tx pgx.Tx) error {
		project, err := ResolveProject(ctx, tx, m.projectKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, permission := range []string{"releases.read", "nodes.read"} {
			if err := authz.RequireTx(ctx, tx, p, permission, authz.Scope{ProjectID: project}); err != nil {
				return nil
			}
		}
		// Native snapshots retain precedence too. A journey publication must never
		// be replaced by a later manifest approximation for the same version.
		rows, err := tx.Query(ctx, `SELECT version,snapshot FROM (
   SELECT r.version,s.snapshot,0 AS precedence
    FROM journey_release_note_snapshots s JOIN journey_releases r
      ON r.tenant_id=s.tenant_id AND r.release_node_id=s.release_node_id
    JOIN nodes n ON n.tenant_id=r.tenant_id AND n.id=r.release_node_id AND n.deleted_at IS NULL
    WHERE s.project_node_id=$1 AND r.version IS NOT NULL
   UNION ALL
   SELECT version,snapshot,1 FROM release_manifest_note_snapshots WHERE project_node_id=$1
  ) captures ORDER BY precedence`, project)
		if err != nil {
			return err
		}
		defer rows.Close()
		snapshots := map[string][]byte{}
		for rows.Next() {
			var version string
			var raw []byte
			if err := rows.Scan(&version, &raw); err != nil {
				return err
			}
			if _, exists := snapshots[version]; !exists {
				snapshots[version] = raw
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		presentations, err := LoadPresentations(ctx, tx, project)
		if err != nil {
			return err
		}
		for i, rel := range h.Releases {
			if p, ok := presentations[rel.Version]; ok {
				h.Releases[i].Presentation = &p
			}
			if HasSnapshot(rel) {
				continue
			}
			raw, ok := snapshots[rel.Version]
			if !ok {
				continue
			}
			notes, err := NotesFromSnapshot(raw, rel.Version, "database-snapshot")
			if err != nil {
				// A malformed stored capture must not hide every other release.
				// Log identifiers only: decoder errors can include private field names.
				slog.WarnContext(ctx, "invalid release note snapshot; using historical fallback", "version", rel.Version, "project_node_id", project)
				h.Releases[i].Notes = MissingNotes()
				continue
			}
			h.Releases[i].Notes = notes
		}
		return nil
	})
	return h, err
}
