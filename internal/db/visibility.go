// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"context"
	"crypto/sha256"
	"errors"
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
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.account_use_capable','on',true),set_config('aeon.chat_session_id','',true),set_config('aeon.chat_role_id','',true),set_config('aeon.chat_conversation_id','',true)`); err != nil {
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
		_, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, tenantID, p.ID, creator, ActingPersonID(ctx, p))
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

// ActingPersonID returns the server-resolved request ceiling, or the API key's
// explicit binding. An internal run resolver may only narrow the same caller.
// nil preserves the legacy no-person contract at SQL entry.
func ActingPersonID(ctx context.Context, p tenant.Principal) any {
	if p.Kind != tenant.Agent {
		return nil
	}
	if current, ok := tenant.PrincipalFrom(ctx); ok && current.ID == p.ID && current.TenantID == p.TenantID && current.KeyPersonID != "" {
		if p.KeyPersonID != "" && p.KeyPersonID != current.KeyPersonID {
			return "00000000-0000-0000-0000-000000000000"
		}
		return current.KeyPersonID
	}
	if p.KeyPersonID != "" {
		return p.KeyPersonID
	}
	return nil
}

// RunPrincipalTx binds only a server-verified run of this authenticated agent.
// It reads no owner/issuer fallback: null remains legacy until S1-07. Conflicting
// key and run bindings fail closed instead of replacing the key's ceiling.
func RunPrincipalTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, runID string) (tenant.Principal, error) {
	if p.Kind != tenant.Agent || !uuidLike(runID) {
		return p, errors.New("invalid run principal")
	}
	var person *string
	err := tx.QueryRow(ctx, `SELECT acting_person_id::text FROM agent_runs WHERE tenant_id=$1::uuid AND id=$2::uuid AND agent_principal_id=$3::uuid`, p.TenantID, runID, p.ID).Scan(&person)
	if err != nil {
		return p, err
	}
	if person != nil {
		if p.KeyPersonID != "" && p.KeyPersonID != *person {
			return p, errors.New("conflicting run person")
		}
		p.KeyPersonID = *person
	}
	return p, nil
}

// WorkerRunPrincipalTx resolves the existing harness worker lease to its run.
// Unknown or shared leases retain the legacy principal; endpoint-specific
// proof checks still reject them. Only one live session supplies a person.
//
// Project row security would hide a session outside the key's current ceiling,
// and a miss must not keep that wider ceiling. The lookup widens visibility
// for this read only. Every success re-enters the resolved principal before
// return. A lookup or conflict error leaves the transaction for the caller to
// roll back, so the widened setting does not survive.
func WorkerRunPrincipalTx(ctx context.Context, tx pgx.Tx, p tenant.Principal, lease string) (tenant.Principal, error) {
	if p.Kind != tenant.Agent || lease == "" {
		return p, nil
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('aeon.visible_projects','*',true)`); err != nil {
		return p, err
	}
	sum := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
	rows, err := tx.Query(ctx, `SELECT run_id::text FROM harness_sessions WHERE tenant_id=$1::uuid AND agent_principal_id=$2::uuid AND lease_digest=$3 AND archived_at IS NULL LIMIT 2`, p.TenantID, p.ID, sum[:])
	if err != nil {
		return p, err
	}
	var runs []*string
	for rows.Next() {
		var run *string
		if err := rows.Scan(&run); err != nil {
			rows.Close()
			return p, err
		}
		runs = append(runs, run)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return p, err
	}
	// Several live sessions can share one lease. The endpoint still proves the
	// lease against its own session. This lookup must not guess a person, and
	// it must not fail the request: an unresolved lease stays the legacy key.
	next := p
	if len(runs) == 1 && runs[0] != nil {
		next, err = RunPrincipalTx(ctx, tx, p, *runs[0])
		if err != nil {
			return p, err
		}
	}
	if err = reenterActingPrincipal(ctx, tx, next); err != nil {
		return p, err
	}
	return next, nil
}

// reenterActingPrincipal installs this principal's own ceiling. It passes the
// resolved key person explicitly: a context principal from an earlier resolver
// must not disagree with the value this call just returned.
func reenterActingPrincipal(ctx context.Context, tx pgx.Tx, p tenant.Principal) error {
	var creator, person any
	if uuidLike(p.KeyCreatorID) {
		creator = p.KeyCreatorID
	}
	if uuidLike(p.KeyPersonID) {
		person = p.KeyPersonID
	}
	_, err := tx.Exec(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, p.TenantID, p.ID, creator, person)
	return err
}
