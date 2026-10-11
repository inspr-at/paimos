// SPDX-License-Identifier: AGPL-3.0-only
package modelregistry

import (
	"context"
	"github.com/inspr-at/paimos/internal/modelactivation"
	"github.com/inspr-at/paimos/internal/modelprefs"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"sort"
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
 AND (EXISTS(SELECT 1 FROM (`+modelprefs.OrdersSQL+`) o WHERE aeon_models_line(p.family,p.harness,p.model)=ANY(o.rank) OR aeon_model_board_line(p.family,p.harness,p.model)=ANY(o.rank) OR aeon_models_line(p.family,p.harness,p.model)=ANY(o.not_allowed) OR aeon_model_board_line(p.family,p.harness,p.model)=ANY(o.not_allowed))
 OR EXISTS(SELECT 1 FROM model_rules r WHERE r.line IN (aeon_models_line(p.family,p.harness,p.model),aeon_model_board_line(p.family,p.harness,p.model)) AND r.moved_to_models IS NOT TRUE)
 OR EXISTS(SELECT 1 FROM model_row_constraints r WHERE r.line=aeon_models_line(p.family,p.harness,p.model))
 OR EXISTS(SELECT 1 FROM model_pauses r WHERE r.harness=p.harness AND r.model=p.model))
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
	pin.Tier, pin.Note = predecessor.Tier, predecessor.Note
	pin.Version += "-accepted"
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_profiles WHERE slug=$1 AND version=$2)`, pin.Slug, pin.Version).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	prof, err := insertActivatedProfile(ctx, tx, p, pin, true, modelactivation.VendorSuccessor)
	if err != nil {
		return false, err
	}
	if !prof.Enabled {
		return false, nil
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
	line, _ := modelsLine(prof)
	if _, err := tx.Exec(ctx, `UPDATE model_pref_profiles SET revision=revision+1 WHERE EXISTS(SELECT 1 FROM (`+modelprefs.OrdersSQL+`) o WHERE o.profile_id=model_pref_profiles.id AND ($1=ANY(o.rank) OR $1=ANY(o.not_allowed) OR $2=ANY(o.rank) OR $2=ANY(o.not_allowed)))`, line, boardLineID(predecessor)); err != nil {
		return false, err
	}
	if err := copyModelPause(ctx, tx, predecessor, prof); err != nil {
		return false, err
	}
	return true, nil
}

// Vendor model lists omit native efforts. For a used, registered successor,
// carry only the predecessor's registered efforts instead of publishing a
// synthetic "default" level. Both identity and ordering use AEON-1000 SQL.
func successorObservations(ctx context.Context, tx pgx.Tx, observations []Observation, limit int) ([]Observation, bool, error) {
	out := []Observation{}
	limited := false
	for _, o := range observations {
		efforts := []string{o.Effort}
		if o.Effort == "default" {
			rows, err := tx.Query(ctx, `WITH predecessor AS (
 SELECT p.model FROM model_profiles p WHERE p.harness=$1 AND p.enabled
 AND (aeon_model_line(p.harness,p.model))[1]=(aeon_model_line($1,$2))[1]
 AND aeon_model_version_newer((aeon_model_line($1,$2))[2],(aeon_model_line(p.harness,p.model))[2])
 AND NOT EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.profile_id=p.id)
 AND (EXISTS(SELECT 1 FROM (`+modelprefs.OrdersSQL+`) o WHERE aeon_models_line(p.family,p.harness,p.model)=ANY(o.rank) OR aeon_model_board_line(p.family,p.harness,p.model)=ANY(o.rank) OR aeon_models_line(p.family,p.harness,p.model)=ANY(o.not_allowed) OR aeon_model_board_line(p.family,p.harness,p.model)=ANY(o.not_allowed))
 OR EXISTS(SELECT 1 FROM model_rules r WHERE r.line IN (aeon_models_line(p.family,p.harness,p.model),aeon_model_board_line(p.family,p.harness,p.model)) AND r.moved_to_models IS NOT TRUE)
 OR EXISTS(SELECT 1 FROM model_row_constraints r WHERE r.line=aeon_models_line(p.family,p.harness,p.model))
 OR EXISTS(SELECT 1 FROM model_pauses r WHERE r.harness=p.harness AND r.model=p.model))
 ORDER BY p.created_at DESC,p.id LIMIT 1)
 SELECT DISTINCT p.effort FROM model_profiles p JOIN predecessor old ON old.model=p.model
 WHERE p.harness=$1 AND p.enabled AND NOT EXISTS(SELECT 1 FROM model_profile_retirements r WHERE r.profile_id=p.id)
 ORDER BY p.effort LIMIT 17`, o.Harness, o.Model)
			if err != nil {
				return nil, false, err
			}
			registered := []string{}
			for rows.Next() {
				var e string
				if err := rows.Scan(&e); err != nil {
					rows.Close()
					return nil, false, err
				}
				registered = append(registered, e)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, false, err
			}
			if len(registered) > 16 {
				return nil, false, modelprefs.ErrBoardBounds
			}
			if len(registered) > 0 {
				efforts = registered
				sort.Strings(efforts)
			}
		}
		for _, e := range efforts {
			if len(out) >= limit {
				limited = true
				break
			}
			next := o
			next.Effort = e
			if e != o.Effort {
				next.ReportID = EvidenceID(o.ReportID + "/" + e)
			}
			out = append(out, next)
		}
		if limited {
			break
		}
	}
	return out, limited, nil
}

// A successor carries the predecessor's pause, including its actor, timestamp
// and reason. A stricter existing successor pause wins without being shortened.
func copyModelPause(ctx context.Context, tx pgx.Tx, old, next Profile) error {
	if old.Harness == next.Harness && old.Model == next.Model {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO model_pauses(tenant_id,harness,model,until_at,reason,set_by,set_at)
 SELECT tenant_id,$3,$4,until_at,reason,set_by,set_at FROM model_pauses WHERE harness=$1 AND model=$2
 ON CONFLICT(tenant_id,harness,model) DO UPDATE SET until_at=EXCLUDED.until_at,reason=EXCLUDED.reason,set_by=EXCLUDED.set_by,set_at=EXCLUDED.set_at
 WHERE model_pauses.until_at IS NOT NULL AND (EXCLUDED.until_at IS NULL OR EXCLUDED.until_at>model_pauses.until_at)`, old.Harness, old.Model, next.Harness, next.Model)
	return err
}
