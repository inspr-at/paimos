// SPDX-License-Identifier: AGPL-3.0-only
package authz

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The authorization package cannot import assistants (events imports authz).
// This narrow read applies its stored scope/project ceiling to ordinary tools
// too, so a hosted key cannot bypass run policy through a direct node write.
func assistantCeilingTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, permission string, scope Scope) error {
	if p.Kind != tenant.Agent {
		return nil
	}
	var assistant, profiles bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(messaging_only,false) OR coalesce(to_jsonb(principals)->>'agent_class','')='assistant',to_regclass('assistant_profiles') IS NOT NULL FROM principals WHERE tenant_id=$1::uuid AND id=$2::uuid`, p.TenantID, p.ID).Scan(&assistant, &profiles); err != nil {
		return err
	}
	if !assistant {
		return nil
	}
	policy := struct {
		Scopes   []string `json:"allowed_scopes"`
		Projects []string `json:"allowed_projects"`
	}{Scopes: MessagingAssistantScopes()}
	if profiles {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT policy FROM assistant_profiles WHERE tenant_id=$1::uuid AND principal_id=$2::uuid`, p.TenantID, p.ID).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if len(raw) > 0 {
			policy.Scopes = nil
			policy.Projects = nil
		}
		if len(raw) > 0 && (len(raw) > 32768 || json.Unmarshal(raw, &policy) != nil) {
			return &denial{reason: "assistant_policy_unavailable", scope: permission}
		}
	}
	if !slices.Contains(policy.Scopes, permission) {
		return &denial{reason: "assistant_scope_denied", scope: permission}
	}
	if scope.ProjectID != "" && !slices.Contains(policy.Projects, scope.ProjectID) {
		return &denial{reason: "assistant_project_denied", scope: permission}
	}
	if scope.AnyProject {
		if len(policy.Projects) > 100 {
			return &denial{reason: "assistant_policy_unavailable", scope: permission}
		}
		for _, project := range policy.Projects {
			effective, err := loadTx(ctx, tx, p, project)
			if err != nil {
				return err
			}
			if permitEffective(p, permission, effective, Scope{ProjectID: project}) == nil {
				return nil
			}
		}
		return &denial{reason: "assistant_project_denied", scope: permission}
	}
	return nil
}

// AssistantPolicyDenial identifies a policy ceiling after ordinary role/key
// authority passed. Callers can retain a durable refusal without granting it.
func AssistantPolicyDenial(err error) (reason, permission string) {
	var d *denial
	if errors.As(err, &d) && strings.HasPrefix(d.reason, "assistant_") {
		return d.reason, d.scope
	}
	return "", ""
}
