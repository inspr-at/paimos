// SPDX-License-Identifier: AGPL-3.0-only
package dbtest

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ModelsOrder saves the tenant's workspace Models order for one column and
// situation, in the storage Models dispatch reads. The rank lists each
// profile's provider line once, in the given order, and keeps that line's first
// native effort. This is the Models v2 migration's conversion of a role ladder
// (1333), so fixtures state routing in today's terms rather than writing the
// retired ladder tables. end is the order's ending: wait, row, default or auto.
func ModelsOrder(ctx context.Context, tx pgx.Tx, tenantID, column, situation, end string, profileIDs ...string) error {
	return modelsOrder(ctx, tx, tenantID, nil, column, situation, end, profileIDs)
}

// PersonModelsOrder saves a person's own Models order ("yours"), as the
// Models board migration (1275) converted a person's pinned matrix cell. The
// person's profile is created without a template, so other rows still follow
// the workspace.
func PersonModelsOrder(ctx context.Context, tx pgx.Tx, tenantID, personID, column, situation, end string, profileIDs ...string) error {
	return modelsOrder(ctx, tx, tenantID, &personID, column, situation, end, profileIDs)
}

func modelsOrder(ctx context.Context, tx pgx.Tx, tenantID string, person *string, column, situation, end string, profileIDs []string) error {
	table := "model_pref_orders"
	if situation == "small" || situation == "review" {
		table = "model_pref_situation_orders"
	}
	var found int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tenantID, profileIDs).Scan(&found); err != nil {
		return err
	}
	unique := map[string]bool{}
	for _, id := range profileIDs {
		unique[id] = true
	}
	if found != len(unique) {
		return fmt.Errorf("models order fixture: %d of %d profiles exist", found, len(unique))
	}
	var owner string
	// The CTE's own insert is invisible to the second branch: exactly one row.
	if err := tx.QueryRow(ctx, `WITH added AS (INSERT INTO model_pref_profiles(tenant_id,scope,person_id,template,thinking,usage)
 SELECT $1,CASE WHEN $2::uuid IS NULL THEN 'workspace' ELSE 'person' END,$2,
  CASE WHEN $2::uuid IS NULL THEN 'balanced' END,CASE WHEN $2::uuid IS NULL THEN 'standard' END,CASE WHEN $2::uuid IS NULL THEN 'balanced' END
 WHERE NOT EXISTS(SELECT 1 FROM model_pref_profiles WHERE tenant_id=$1 AND person_id IS NOT DISTINCT FROM $2::uuid) RETURNING id)
 SELECT id::text FROM added UNION ALL SELECT id::text FROM model_pref_profiles WHERE tenant_id=$1 AND person_id IS NOT DISTINCT FROM $2::uuid`, tenantID, person).Scan(&owner); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO `+table+`(tenant_id,profile_id,column_key,situation,rank,efforts,end_mode)
 SELECT $1,$2,$3,$4,coalesce(array_agg(line ORDER BY n),'{}'),coalesce(jsonb_object_agg(line,effort),'{}'),nullif($5,'')
 FROM (SELECT DISTINCT ON (aeon_models_line(p.family,p.harness,p.model)) aeon_models_line(p.family,p.harness,p.model) AS line,u.n,p.effort
  FROM unnest($6::uuid[]) WITH ORDINALITY u(id,n) JOIN model_profiles p ON p.tenant_id=$1 AND p.id=u.id
  ORDER BY aeon_models_line(p.family,p.harness,p.model),u.n) steps
 ON CONFLICT (tenant_id,profile_id,column_key,situation) DO UPDATE SET rank=EXCLUDED.rank,efforts=EXCLUDED.efforts,end_mode=EXCLUDED.end_mode`,
		tenantID, owner, column, situation, end, profileIDs); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET revision=revision+1 WHERE tenant_id=$1 AND id=$2`, tenantID, owner)
	return err
}
