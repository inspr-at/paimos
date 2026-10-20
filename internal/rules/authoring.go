// SPDX-License-Identifier: AGPL-3.0-only
package rules

import (
	"context"
	"net/http"
	"strings"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
)

const DBAuthoringDisabled = "rules_db_authoring_disabled"
const DBAuthoringMessage = "Database rule authoring is disabled (rules.db_authoring; rules_db_authoring_disabled). Propose a change through /api/rules/doctrine/inbox; a person opens the pull request."

// DBAuthoring is independent of delivery, pins and the published snapshots.
func DBAuthoring(ctx context.Context, tx pgx.Tx) (bool, error) {
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT db_authoring FROM rule_budget_settings),true)`).Scan(&enabled)
	return enabled, err
}

// RequireDBAuthoring is called under the tenant access fence in a writer's final transaction.
func RequireDBAuthoring(ctx context.Context, tx pgx.Tx) error {
	enabled, err := DBAuthoring(ctx, tx)
	if err != nil {
		return err
	}
	if !enabled {
		return fail(http.StatusConflict, DBAuthoringDisabled, DBAuthoringMessage)
	}
	return nil
}

func databaseWriter(r *http.Request) bool {
	path := r.URL.Path
	return (r.Method == "POST" && (path == "/api/rules/layers" || path == "/api/rules/sets" || path == "/api/rules/publish" || strings.HasSuffix(path, "/publish") || strings.HasSuffix(path, "/restore"))) ||
		(r.Method == "PUT" && (strings.HasSuffix(path, "/draft") || strings.HasSuffix(path, "/tldr")))
}

type authoringSetting struct {
	Enabled      bool   `json:"db_authoring"`
	ProposalPath string `json:"proposal_path"`
}

func (m *Module) getAuthoring(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	enabled, err := DBAuthoring(r.Context(), tx)
	return authoringSetting{enabled, "/api/rules/doctrine/inbox"}, err
}
func (m *Module) putAuthoring(r *http.Request, tx pgx.Tx, p tenant.Principal) (any, error) {
	if err := authz.RequireTx(r.Context(), tx, p, "settings.manage", authz.Scope{}); err != nil {
		return nil, err
	}
	var in struct {
		Enabled *bool `json:"db_authoring"`
	}
	if err := workorders.Decode(r, &in); err != nil {
		return nil, err
	}
	if in.Enabled == nil {
		return nil, fail(400, "invalid_request", "db_authoring is required")
	}
	before, err := DBAuthoring(r.Context(), tx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO rule_budget_settings(tenant_id,db_authoring,updated_by) VALUES($1,$2,$3)
 ON CONFLICT(tenant_id) DO UPDATE SET db_authoring=excluded.db_authoring,updated_by=excluded.updated_by,updated_at=clock_timestamp()`, p.TenantID, *in.Enabled, p.ID); err != nil {
		return nil, err
	}
	if _, err = events.Append(r.Context(), tx, p, events.Change{Type: "settings.rules_db_authoring_changed", Before: map[string]bool{"db_authoring": before}, After: map[string]bool{"db_authoring": *in.Enabled}}); err != nil {
		return nil, err
	}
	return authoringSetting{*in.Enabled, "/api/rules/doctrine/inbox"}, nil
}
