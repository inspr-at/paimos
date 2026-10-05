// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
)

// BackfillRelations replays stored import.relation events for one tenant and
// applies the classic relation mapping. tenantID is tenants.id, not the slug.
// The coordinator wires this from cmd/aeon; see the package doc for the call.
// A second call in a tenant whose relations are already applied returns
// Writes == 0 and does not append events.
func BackfillRelations(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Report, error) {
	report := Report{Counts: map[string]int{}}
	if pool == nil {
		return report, errors.New("database pool is required")
	}
	if tenantID == "" {
		return report, errors.New("tenant id is required")
	}
	err := db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,42))`, tenantID+":relation-backfill"); err != nil {
			return err
		}
		issues, err := loadImportedIssues(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		var seen bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM events WHERE tenant_id=$1 AND type='import.relation')`, tenantID).Scan(&seen); err != nil {
			return err
		}
		if !seen {
			return nil
		}
		actor, err := ensureImportActor(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		// Drain the cursor before applying. pgx rejects another query on this
		// transaction while import.relation rows are still open.
		rows, err := tx.Query(ctx, `SELECT after FROM events WHERE tenant_id=$1 AND type='import.relation' ORDER BY id`, tenantID)
		if err != nil {
			return err
		}
		var payloads [][]byte
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			payloads = append(payloads, raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, raw := range payloads {
			rel, ok, err := classicRelationFromEvent(raw, issues)
			if err != nil {
				return err
			}
			report.Counts["relations"]++
			if !ok {
				report.Counts["skipped"]++
				continue
			}
			wrote, err := applyClassicRelation(ctx, tx, tenantID, actor, rel)
			if errors.Is(err, errReleasesModeSkipped) {
				report.Counts["release_links_skipped"]++
				report.Counts["skipped"]++
				continue
			}
			if err != nil {
				return fmt.Errorf("backfill %s: %w", rel.ClassicRef, err)
			}
			report.Writes += wrote
		}
		return nil
	})
	report.Counts["writes"] = report.Writes
	return report, err
}

type importedIssue struct {
	NodeID  string
	EventID int64
}

func loadImportedIssues(ctx context.Context, tx pgx.Tx, tenantID string) (map[int64]importedIssue, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (e.node_id)
		       e.id,
		       e.node_id::text,
		       (e.after->'fields'->'classic'->>'id')::bigint
		FROM events e
		JOIN nodes n ON n.tenant_id=e.tenant_id AND n.id=e.node_id
		JOIN node_kinds k ON k.tenant_id=n.tenant_id AND k.id=n.kind_id
		WHERE e.tenant_id=$1
		  AND e.type IN ('import.node_created','import.node_updated')
		  AND k.slug <> 'project'
		  AND coalesce(e.after->'fields'->'classic'->>'id','') ~ '^[0-9]+$'
		ORDER BY e.node_id, e.id DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues := map[int64]importedIssue{}
	for rows.Next() {
		var eventID, classicID int64
		var nodeID string
		if err := rows.Scan(&eventID, &nodeID, &classicID); err != nil {
			return nil, err
		}
		if prev, ok := issues[classicID]; ok && prev.EventID > eventID {
			continue
		}
		issues[classicID] = importedIssue{NodeID: nodeID, EventID: eventID}
	}
	return issues, rows.Err()
}

func classicRelationFromEvent(raw []byte, issues map[int64]importedIssue) (classicRelation, bool, error) {
	var payload struct {
		ClassicRef  string `json:"classic_ref"`
		ClassicType string `json:"classic_type"`
		Record      Record `json:"record"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return classicRelation{}, false, fmt.Errorf("import.relation payload: %w", err)
	}
	if payload.Record == nil {
		return classicRelation{}, false, nil
	}
	typ := stringField(payload.Record, "type")
	if typ == "" {
		typ = payload.ClassicType
	}
	sourceID, sourceOK := intField(payload.Record, "source_id")
	targetID, targetOK := intField(payload.Record, "target_id")
	if typ == "" || !sourceOK || !targetOK {
		return classicRelation{}, false, nil
	}
	return classicRelation{
		Type:         typ,
		SourceNodeID: issues[sourceID].NodeID,
		TargetNodeID: issues[targetID].NodeID,
		ClassicRef:   payload.ClassicRef,
	}, true, nil
}
