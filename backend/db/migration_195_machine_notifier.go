// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

func checkM195SchemaIsUnapplied(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `
		SELECT 'api_keys.'||name FROM pragma_table_info('api_keys') WHERE name='credential_kind'
		UNION ALL
		SELECT 'agent_messages.'||name FROM pragma_table_info('agent_messages') WHERE name='machine_notifier_api_key_id'
		UNION ALL
		SELECT type||':'||name FROM sqlite_master WHERE name IN (
		 'machine_notifier_bindings','idx_machine_notifier_project','idx_agent_messages_machine_notifier',
		 'trg_api_keys_credential_kind_immutable','trg_machine_notifier_binding_guard',
		 'trg_machine_notifier_binding_no_update','trg_machine_notifier_no_target_recovery'
		)
		ORDER BY 1`)
	if err != nil {
		return fmt.Errorf("inspect M195 schema ownership: %w", err)
	}
	defer rows.Close()
	var collisions []string
	for rows.Next() {
		var collision string
		if err := rows.Scan(&collision); err != nil {
			return fmt.Errorf("scan M195 schema ownership: %w", err)
		}
		collisions = append(collisions, collision)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate M195 schema ownership: %w", err)
	}
	if len(collisions) > 0 {
		return fmt.Errorf("M195 schema is partially present or locally incompatible: %s", strings.Join(collisions, ", "))
	}
	return nil
}
