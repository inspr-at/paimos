// SPDX-License-Identifier: AGPL-3.0-only

package dbtest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SetModelOrder gives a routing fixture an explicit Models choice. Profile ids
// keep the fixture's native efforts attached to its choices; unlike the retired
// ladder fixtures, these rows are read by the production resolver. It emits no
// events and grants no permissions or account capacity.
func SetModelOrder(ctx context.Context, tx pgx.Tx, tenantID, personID, column, situation string, profiles ...string) error {
	if len(profiles) > 50 {
		return fmt.Errorf("fixture order exceeds 50 profiles")
	}
	var owner string
	scope := "workspace"
	var person *string
	if personID != "" {
		scope, person = "person", &personID
	}
	if err := tx.QueryRow(ctx, `INSERT INTO model_pref_profiles(tenant_id,scope,person_id,template,thinking,usage,revision)
 VALUES($1,$2,$3,CASE WHEN $2='workspace' THEN 'balanced' END,CASE WHEN $2='workspace' THEN 'standard' END,CASE WHEN $2='workspace' THEN 'balanced' END,1)
 ON CONFLICT (tenant_id,scope,coalesce(person_id,'00000000-0000-0000-0000-000000000000')) DO UPDATE SET revision=model_pref_profiles.revision+1
 RETURNING id::text`, tenantID, scope, person).Scan(&owner); err != nil {
		return err
	}
	rank := []string{}
	efforts := map[string]string{}
	for _, id := range profiles {
		var line, effort string
		if err := tx.QueryRow(ctx, `SELECT aeon_models_line(family,harness,model),effort FROM model_profiles WHERE id=$1::uuid`, id).Scan(&line, &effort); err != nil {
			return fmt.Errorf("fixture profile %s: %w", id, err)
		}
		if _, exists := efforts[line]; !exists {
			rank = append(rank, line)
			efforts[line] = effort
		}
	}
	table, end := "model_pref_orders", "wait"
	if situation == "small" || situation == "review" {
		table = "model_pref_situation_orders"
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+table+`(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode)
 VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(tenant_id,profile_id,column_key,situation)
 DO UPDATE SET rank=EXCLUDED.rank,efforts=EXCLUDED.efforts,end_mode=EXCLUDED.end_mode`, tenantID, owner, column, situation, rank, efforts, end)
	return err
}
