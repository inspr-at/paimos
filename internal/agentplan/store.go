// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReadPreference reads the canonical person's linked preferences, failing
// closed on conflicting ceilings. Authorization belongs to the caller.
func ReadPreference(ctx context.Context, tx pgx.Tx, tenantID, owner string) ([]byte, *time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT pref.value,pref.updated_at FROM user_preferences pref
 JOIN principals person ON person.tenant_id=pref.tenant_id AND person.id=pref.principal_id
 WHERE pref.tenant_id=$1::uuid AND pref.key=$3 AND person.kind='person'
 AND coalesce(person.linked_to,person.id)=$2::uuid
 ORDER BY (person.id=$2::uuid) DESC,person.id LIMIT 65`, tenantID, owner, PreferenceKey)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var selected []byte
	var updatedAt *time.Time
	var selectedPlan Plan
	count := 0
	for rows.Next() {
		count++
		if count > 64 {
			return nil, nil, errors.New("linked person plans exceed bound")
		}
		var raw []byte
		var at time.Time
		if err := rows.Scan(&raw, &at); err != nil {
			return nil, nil, err
		}
		if len(raw) > 16<<10 {
			return nil, nil, errors.New("plan exceeds bound")
		}
		plan, _, err := Decode(raw)
		if err != nil {
			return nil, nil, err
		}
		if updatedAt == nil {
			selected, updatedAt, selectedPlan = raw, &at, plan
		} else if !SameLimits(selectedPlan, plan) {
			return nil, nil, errors.New("conflicting linked person plans")
		}
	}
	return selected, updatedAt, rows.Err()
}

func SameLimits(a, b Plan) bool {
	if a.Total != b.Total {
		return false
	}
	for _, limits := range []map[string]Limit{a.Limits, b.Limits} {
		for harness := range limits {
			left, right := a.Limits[harness], b.Limits[harness]
			if left.Mode == "" {
				left.Mode = NoLimit
			}
			if right.Mode == "" {
				right.Mode = NoLimit
			}
			if left != right {
				return false
			}
		}
	}
	return true
}
