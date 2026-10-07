// SPDX-License-Identifier: AGPL-3.0-only
package agentplan

import (
	"context"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Snapshot contains the same owner-bound facts exposed by GET /api/agents/plan.
type Snapshot struct {
	Plan
	PrincipalID  string         `json:"principal_id"`
	Running      map[string]int `json:"running"`
	RunningTotal int            `json:"running_total"`
	Source       string         `json:"source"`
	UpdatedAt    *time.Time     `json:"updated_at"`
}

// ReadTx checks live read authority and canonical ownership. Only the running
// aggregate temporarily sees all projects, with explicit tenant/owner predicates;
// no session or project identity leaves that aggregate.
func ReadTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (Snapshot, error) {
	out := Snapshot{Running: map[string]int{}}
	if err := authz.RequireTx(ctx, tx, p, ReadScope, authz.Scope{}); err != nil {
		return out, err
	}
	var err error
	out.PrincipalID, err = CallerOwnerTx(ctx, tx, p)
	if err != nil {
		return out, err
	}
	raw, at, err := readPreference(ctx, tx, p.TenantID, out.PrincipalID)
	if err != nil {
		return out, err
	}
	out.UpdatedAt = at
	out.Plan, out.Source, err = Decode(raw)
	if err != nil {
		return out, err
	}
	var visibility, system string
	if err = tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),''),coalesce(current_setting('aeon.system',true),'')`).Scan(&visibility, &system); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true),set_config('aeon.system','on',true)`); err != nil {
		return out, err
	}
	err = func() error {
		rows, err := tx.Query(ctx, `SELECT s.harness,count(*) FROM harness_sessions s
			WHERE s.tenant_id=$1::uuid AND (
			 (s.stopped_at IS NULL AND s.archived_at IS NULL AND s.phase<>'stopped')
			 OR (EXISTS(SELECT 1 FROM project_leads l WHERE l.tenant_id=s.tenant_id AND l.session_id=s.id)
			     AND NOT aeon_work_session_stopped(s.stopped_at,s.stop_reason)))
			AND (s.owner_principal_id IN (SELECT id FROM principals WHERE tenant_id=$1::uuid AND coalesce(linked_to,id)=$2::uuid)
			OR (s.owner_principal_id IS NULL AND EXISTS (
				SELECT 1 FROM agent_keys k JOIN principals creator ON creator.tenant_id=k.tenant_id AND creator.id=k.created_by_principal_id
				WHERE k.tenant_id=s.tenant_id AND k.principal_id=s.agent_principal_id AND creator.kind='person'
				AND coalesce(creator.linked_to,creator.id)=$2::uuid
			) AND NOT EXISTS (
				SELECT 1 FROM agent_keys k LEFT JOIN principals creator ON creator.tenant_id=k.tenant_id AND creator.id=k.created_by_principal_id
				WHERE k.tenant_id=s.tenant_id AND k.principal_id=s.agent_principal_id
				AND (creator.id IS NULL OR creator.kind<>'person' OR coalesce(creator.linked_to,creator.id)<>$2::uuid)
			))) GROUP BY s.harness`, p.TenantID, out.PrincipalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var harness string
			var count int
			if err := rows.Scan(&harness, &count); err != nil {
				return err
			}
			out.Running[harness] = count
			out.RunningTotal += count
		}
		return rows.Err()
	}()
	if err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',$1,true),set_config('aeon.system',$2,true)`, visibility, system)
	return out, err
}

// CallerOwnerTx refuses ownerless agents and inactive linked person identities.
func CallerOwnerTx(ctx context.Context, tx pgx.Tx, p tenant.Principal) (string, error) {
	owner := p.ID
	if p.Kind == tenant.Agent {
		owner = p.KeyCreatorID
	} else if p.Kind != tenant.Person {
		return "", authz.ErrForbidden
	}
	if owner == "" {
		return "", authz.ErrForbidden
	}
	return planOwner(ctx, tx, p.TenantID, owner)
}

func planOwner(ctx context.Context, tx pgx.Tx, tenantID, owner string) (string, error) {
	var canonical string
	err := tx.QueryRow(ctx, `SELECT canonical.id::text FROM principals person
		JOIN principals canonical ON canonical.tenant_id=person.tenant_id AND canonical.id=coalesce(person.linked_to,person.id)
		WHERE person.tenant_id=$1::uuid AND person.id=$2::uuid AND person.kind='person'
		AND person.status='active' AND canonical.kind='person' AND canonical.status='active'`, tenantID, owner).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", authz.ErrForbidden
	}
	return canonical, err
}

// Read every saved plan in the family, preferring the canonical row. Comparing
// effective ceilings preserves legacy shapes and treats a missing limit like
// explicit no_limit, while never guessing between conflicting start allowances.
func readPreference(ctx context.Context, tx pgx.Tx, tenantID, owner string) ([]byte, *time.Time, error) {
	rows, err := tx.Query(ctx, `SELECT pref.value,pref.updated_at FROM user_preferences pref
		JOIN principals person ON person.tenant_id=pref.tenant_id AND person.id=pref.principal_id
		WHERE pref.tenant_id=$1::uuid AND pref.key=$3 AND person.kind='person'
		AND coalesce(person.linked_to,person.id)=$2::uuid
		ORDER BY (person.id=$2::uuid) DESC,person.id`, tenantID, owner, PreferenceKey)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var selected []byte
	var updatedAt *time.Time
	var selectedPlan Plan
	for rows.Next() {
		var raw []byte
		var at time.Time
		if err := rows.Scan(&raw, &at); err != nil {
			return nil, nil, err
		}
		plan, _, err := Decode(raw)
		if err != nil {
			return nil, nil, err
		}
		if updatedAt == nil {
			selected, updatedAt, selectedPlan = raw, &at, plan
		} else if !samePlan(selectedPlan, plan) {
			return nil, nil, errors.New("conflicting linked person plans")
		}
	}
	return selected, updatedAt, rows.Err()
}

func samePlan(a, b Plan) bool {
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
