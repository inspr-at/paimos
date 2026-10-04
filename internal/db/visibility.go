// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/tenant"
)

// VisibleProjectsSetting is the per-transaction project visibility that the
// project row-level security policies (migration 0821) read: "*" for every
// project, a Postgres uuid array literal for some projects, and "" (or unset)
// for none. Nothing project-scoped is visible unless InTenant sets it.
const VisibleProjectsSetting = "aeon.visible_projects"

type visibilityKey struct{}

type visibility struct {
	value  string
	reason string
}

// AllProjects marks ctx for a service path that must read or write every
// project regardless of any caller: the importer, the embedding worker, quote
// confirmation jobs, the public quote link, file gc and other system jobs.
// It overrides a principal in ctx, so request handlers use it only for a
// narrowly scoped step whose authorization they have already checked (the
// quote portal). The reason names the path at the call site.
func AllProjects(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, visibilityKey{}, visibility{value: "*", reason: reason})
}

// OnlyProjects restricts ctx to the given project node IDs, overriding any
// principal in ctx. No IDs means no project at all.
func OnlyProjects(ctx context.Context, projectIDs ...string) context.Context {
	value := "{}"
	if len(projectIDs) > 0 {
		value = "{" + strings.Join(projectIDs, ",") + "}"
	}
	return context.WithValue(ctx, visibilityKey{}, visibility{value: value, reason: "explicit projects"})
}

// NoProjects marks ctx for system code that touches only workspace rows (the
// sign-in flow, key issuance, tenant bootstrap, the inbox wake worker), so an
// incidental caller in ctx cannot change what it sees. Nothing
// project-scoped is visible; workspace events that name no project are. A
// transaction with neither a principal nor a service visibility sees neither.
func NoProjects(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, visibilityKey{}, visibility{value: "", reason: reason})
}

// enterTenant scopes a new transaction to tenantID and its project visibility:
// an explicit service visibility first, else the visibility of the principal
// in ctx (computed once, in the database, from its bindings), else none.
func enterTenant(ctx context.Context, tx pgx.Tx, tenantID string) error {
	// Never inherit verified chat authority into a nested legacy/service query.
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.chat_session_id','',true),set_config('aeon.chat_role_id','',true),set_config('aeon.chat_conversation_id','',true)`); err != nil {
		return err
	}
	if v, ok := ctx.Value(visibilityKey{}).(visibility); ok {
		_, err := tx.Exec(ctx, `SELECT set_config($1,$2,true),set_config($3,$4,true),set_config('aeon.principal_ids','',true),set_config('aeon.system','on',true)`,
			TenantSetting, tenantID, VisibleProjectsSetting, v.value)
		return err
	}
	if p, ok := tenant.PrincipalFrom(ctx); ok && p.TenantID == tenantID && uuidLike(p.ID) && uuidLike(tenantID) {
		var creator any
		if uuidLike(p.KeyCreatorID) {
			creator = p.KeyCreatorID
		}
		_, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, tenantID, p.ID, creator)
		return err
	}
	_, err := tx.Exec(ctx, `SELECT set_config($1,$2,true),set_config($3,'',true),set_config('aeon.principal_ids','',true),set_config('aeon.system','',true)`,
		TenantSetting, tenantID, VisibleProjectsSetting)
	return err
}

func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}
