// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type refreshCooldown struct{ RetryAfter int }

func (*refreshCooldown) Error() string   { return "model refresh cooldown" }
func (*refreshCooldown) StatusCode() int { return 429 }

// Successor identity and numeric ordering come from AEON-1000's policy.
// An unused line, a disabled predecessor or a retired line grants no acceptance.
func acceptUsedSuccessor(ctx context.Context, tx pgx.Tx, p tenant.Principal, pin profileWrite) (bool, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT p.id::text FROM model_profiles p
 WHERE p.harness=$1 AND p.family=$2 AND p.enabled
 AND (aeon_model_line(p.harness,p.model))[1]=(aeon_model_line($1,$3))[1]
 AND aeon_model_version_newer((aeon_model_line($1,$3))[2],(aeon_model_line(p.harness,p.model))[2])
 AND NOT EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.tenant_id=p.tenant_id AND r.profile_id=p.id)
 AND (EXISTS(SELECT 1 FROM model_pref_orders o WHERE aeon_model_board_line(p.family,p.harness,p.model)=ANY(o.rank))
 OR EXISTS(SELECT 1 FROM model_rules r WHERE r.line=aeon_model_board_line(p.family,p.harness,p.model)))
 AND NOT EXISTS(SELECT 1 FROM model_profiles n WHERE n.harness=$1 AND n.model=$3 AND n.effort=$4 AND n.enabled)
 ORDER BY p.created_at DESC,p.id LIMIT 1`, pin.Harness, pin.Family, pin.Model, pin.Effort).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	c, err := loadBoardCatalog(ctx, tx)
	if err != nil {
		return false, err
	}
	var predecessor Profile
	for _, ps := range c.profiles {
		for _, prof := range ps {
			if prof.ID == id {
				predecessor = prof
			}
		}
	}
	pin.Tier, pin.Note, pin.Source = predecessor.Tier, predecessor.Note, "auto"
	pin.Version += "-accepted"
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE slug=$1 AND version=$2)`, pin.Slug, pin.Version).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	prof, err := insertProfile(ctx, tx, p.TenantID, pin)
	if err != nil {
		return false, err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT aeon_account_allows_profile($1,ARRAY[$2::uuid],$3::uuid)`, pin.Harness, predecessor.ID, prof.ID).Scan(&allowed); err != nil {
		return false, err
	}
	if !allowed {
		return false, fail(409, "discovered profile is not a qualified successor")
	}
	// Board orders and rules hold stable family:line IDs. They already point
	// at the accepted version; advance their write tokens without changing rank
	// or native effort while the remaining efforts are being discovered.
	line := boardLineID(prof)
	if _, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET revision=revision+1 WHERE EXISTS(SELECT 1 FROM model_pref_orders o WHERE o.profile_id=model_pref_profiles.id AND $1=ANY(o.rank))`, line); err != nil {
		return false, err
	}
	for _, table := range []string{"model_role_routes", "model_security_role_routes"} {
		if _, err := tx.Exec(ctx, `UPDATE `+table+` r SET profile_id=$3 FROM model_profiles old WHERE old.tenant_id=r.tenant_id AND old.id=r.profile_id AND old.harness=$1 AND old.effort=$2 AND aeon_account_allows_profile($1,ARRAY[old.id],$3::uuid) AND NOT EXISTS(SELECT 1 FROM `+table+` n WHERE n.tenant_id=r.tenant_id AND n.role=r.role AND n.profile_id=$3)`, pin.Harness, pin.Effort, prof.ID); err != nil {
			return false, err
		}
	}
	return true, nil
}
