// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// RLS must apply the project ceiling to list reads as well as target writes.
// Intersect after ordinary role/creator/person visibility; never turn a policy
// allowlist into project access. Service visibility remains explicitly owned
// by its callers, which evaluate policy for each on-behalf-of assistant action.
func narrowAssistantVisibility(ctx context.Context, tx pgx.Tx, tid, id string) error {
	if p, ok := tenant.PrincipalFrom(ctx); ok && p.ID == id && p.Kind == tenant.Person {
		return nil
	}
	var assistant, profiles bool
	if err := tx.QueryRow(ctx, `SELECT kind='agent' AND (coalesce(messaging_only,false) OR coalesce(to_jsonb(principals)->>'agent_class','')='assistant'),to_regclass('assistant_profiles') IS NOT NULL FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid`, tid, id).Scan(&assistant, &profiles); err != nil {
		// Principal entry already gives missing or foreign identities no project
		// or node visibility. Preserve that empty result, including for list reads.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if !assistant {
		return nil
	}
	policy := struct {
		Projects []string `json:"allowed_projects"`
	}{Projects: []string{}}
	if profiles {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT policy FROM assistant_profiles WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, tid, id).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if len(raw) > 0 && (len(raw) > 32768 || json.Unmarshal(raw, &policy) != nil) {
			policy.Projects = []string{}
		}
	}
	if len(policy.Projects) > 100 {
		policy.Projects = []string{}
	}
	for _, project := range policy.Projects {
		if !uuidLike(project) {
			policy.Projects = []string{}
			break
		}
	}
	_, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects',coalesce((SELECT array_agg(project)::text FROM unnest($1::uuid[]) project WHERE
 current_setting('aeon.visible_projects',true)='*' OR project=ANY(coalesce(nullif(nullif(current_setting('aeon.visible_projects',true),'*'),''),'{}')::uuid[])),'{}'),true)`, policy.Projects)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT set_config('aeon.visible_nodes',coalesce((SELECT array_agg(n.id)::text FROM nodes n WHERE n.id=ANY(coalesce(nullif(current_setting('aeon.visible_nodes',true),''),'{}')::uuid[]) AND n.project_id=ANY($1::uuid[])),'{}'),true)`, policy.Projects)
	return err
}
