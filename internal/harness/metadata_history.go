// SPDX-License-Identifier: AGPL-3.0-only

package harness

import (
	"context"

	"github.com/jackc/pgx/v5"
)

func sameMetadataValue(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// The session row is locked by worker(), so concurrent heartbeats cannot
// interleave change ordering or pruning for one session.
func recordMetadataChanges(ctx context.Context, tx pgx.Tx, tenantID string, before, after Session) error {
	fields := []struct {
		name string
		old  *string
		new  *string
	}{
		{"display_label", before.DisplayLabel, after.DisplayLabel},
		{"model", before.Model, after.Model},
		{"reasoning_effort", before.ReasoningEffort, after.ReasoningEffort},
	}
	changed := false
	for _, field := range fields {
		if sameMetadataValue(field.old, field.new) {
			continue
		}
		changed = true
		if _, err := tx.Exec(ctx, `INSERT INTO harness_metadata_changes(tenant_id,session_id,field,previous_value,value) VALUES($1,$2,$3,$4,$5)`, tenantID, after.ID, field.name, field.old, field.new); err != nil {
			return err
		}
	}
	if changed {
		_, err := tx.Exec(ctx, `DELETE FROM harness_metadata_changes WHERE tenant_id=$1 AND session_id=$2 AND id NOT IN (SELECT id FROM harness_metadata_changes WHERE tenant_id=$1 AND session_id=$2 ORDER BY id DESC LIMIT 20)`, tenantID, after.ID)
		return err
	}
	return nil
}
