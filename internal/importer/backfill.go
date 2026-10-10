// SPDX-License-Identifier: AGPL-3.0-only
package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	ctx, batch := newImportEventBatch(ctx)
	if pool == nil {
		return report, errors.New("database pool is required")
	}
	if tenantID == "" {
		return report, errors.New("tenant id is required")
	}
	err := db.InTenant(db.AllProjects(ctx, "classic importer"), pool, tenantID, func(tx pgx.Tx) error {
		if err := lockImportTree(ctx, tx, tenantID); err != nil {
			return err
		}
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
			if err != nil {
				return fmt.Errorf("backfill %s: %w", rel.ClassicRef, err)
			}
			report.Writes += wrote
		}
		return batch.flush()
	})
	report.Counts["writes"] = report.Writes
	return report, err
}

type importedIdentity struct {
	Source string
	ID     int64
}

type importedIssue struct {
	NodeID  string
	EventID int64
}

func loadImportedIssues(ctx context.Context, tx pgx.Tx, tenantID string) (map[importedIdentity]importedIssue, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (e.node_id)
		       e.id,
		       e.node_id::text,
		       (e.after->'fields'->'classic'->>'id')::bigint,
 coalesce(e.after->'fields'->'classic'->>'source_id','')
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
	issues := map[importedIdentity]importedIssue{}
	for rows.Next() {
		var eventID, classicID int64
		var nodeID, source string
		if err := rows.Scan(&eventID, &nodeID, &classicID, &source); err != nil {
			return nil, err
		}
		key := importedIdentity{source, classicID}
		if prev, ok := issues[key]; ok && prev.NodeID != nodeID {
			return nil, fmt.Errorf("ambiguous imported identity: source %q issue %d", source, classicID)
		}
		issues[key] = importedIssue{NodeID: nodeID, EventID: eventID}
	}
	return issues, rows.Err()
}

func classicRelationFromEvent(raw []byte, issues map[importedIdentity]importedIssue) (classicRelation, bool, error) {
	var payload struct {
		ClassicRef  string `json:"classic_ref"`
		ClassicType string `json:"classic_type"`
		SourceID    string `json:"source_id"`
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
	namespace := payload.SourceID
	if before, _, ok := strings.Cut(payload.ClassicRef, ":import.relation:"); ok {
		if namespace != "" && namespace != before {
			return classicRelation{}, false, errors.New("conflicting relation source namespaces")
		}
		namespace = before
	}
	resolve := func(id int64) (string, error) {
		if namespace != "" {
			return issues[importedIdentity{namespace, id}].NodeID, nil
		}
		node := ""
		for key, item := range issues {
			if key.ID == id {
				if node != "" && node != item.NodeID {
					return "", fmt.Errorf("ambiguous unqualified relation issue %d", id)
				}
				node = item.NodeID
			}
		}
		return node, nil
	}
	source, err := resolve(sourceID)
	if err != nil {
		return classicRelation{}, false, err
	}
	target, err := resolve(targetID)
	if err != nil {
		return classicRelation{}, false, err
	}
	return classicRelation{Type: typ, SourceNodeID: source, TargetNodeID: target, ClassicRef: payload.ClassicRef}, source != "" && target != "", nil
}
