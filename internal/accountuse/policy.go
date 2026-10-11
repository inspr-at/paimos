// SPDX-License-Identifier: AGPL-3.0-only
// Package accountuse owns the account/context matrix and its canonical writes.
// It imports neither the account nor model registry modules.
package accountuse

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

const Permission = "account.use.manage"
const matrixLock = `SELECT pg_advisory_xact_lock(hashtextextended('aeon-account-use:' || current_setting('aeon.tenant_id'),0))`

type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string              { return e.Message }
func (e *Error) StatusCode() int            { return e.Status }
func (e *Error) HTTPStatus() int            { return e.Status }
func (e *Error) ErrorCode() string          { return e.Message }
func fail(status int, message string) error { return &Error{status, message} }

type RuleValues struct {
	NewAccounts string `json:"new_accounts"`
	NewContexts string `json:"new_contexts"`
	NewProjects string `json:"new_projects"`
	NewModels   string `json:"new_models"`
}
type Rules struct {
	RuleValues
	Revision             int64   `json:"revision"`
	EnforcedAt           *string `json:"enforced_at"`
	ConfirmationRequired bool    `json:"confirmation_required"`
	ConfirmedAt          *string `json:"confirmed_at"`
}

const ruleColumns = `new_accounts,new_contexts,new_projects,new_models,revision,enforced_at::text,confirmation_required,confirmed_at::text`

func scanRules(row pgx.Row) (out Rules, err error) {
	err = row.Scan(&out.NewAccounts, &out.NewContexts, &out.NewProjects, &out.NewModels, &out.Revision, &out.EnforcedAt, &out.ConfirmationRequired, &out.ConfirmedAt)
	return
}
func ReadRules(ctx context.Context, tx pgx.Tx) (Rules, error) {
	return scanRules(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM account_use_rules`))
}

// LockShared precedes account and run row locks in selection/claim paths.
// The caller already owns its tenant/tree/pairing authority fences.
func LockShared(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended('aeon-account-use:' || current_setting('aeon.tenant_id'),0))`)
	return err
}

// LockExclusive precedes catalog preparation and its resource rows in
// compatibility settings writes. The caller owns tenant/tree/catalog fences.
func LockExclusive(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, matrixLock)
	return err
}

// FenceWrite rechecks live person authority and the revision under the matrix
// lock. Call after any tree/pairing/catalog fences and before resource locks.
func FenceWrite(ctx context.Context, tx pgx.Tx, p tenant.Principal, expected int64) (Rules, error) {
	if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
		return Rules{}, err
	}
	if _, err := tx.Exec(ctx, matrixLock); err != nil {
		return Rules{}, err
	}
	if p.Kind != tenant.Person {
		return Rules{}, authz.ErrForbidden
	}
	if err := authz.RequireTx(ctx, tx, p, Permission, authz.Scope{}); err != nil {
		return Rules{}, err
	}
	current, err := scanRules(tx.QueryRow(ctx, `SELECT `+ruleColumns+` FROM account_use_rules FOR UPDATE`))
	if err != nil {
		return Rules{}, err
	}
	if expected < 1 || current.Revision != expected {
		return Rules{}, fail(409, "account_use_revision_conflict")
	}
	return current, nil
}

func validRules(v RuleValues) bool {
	return (v.NewAccounts == "allow" || v.NewAccounts == "ask") && (v.NewContexts == "allow" || v.NewContexts == "ask") && (v.NewProjects == "default" || v.NewProjects == "holding") && (v.NewModels == "allow" || v.NewModels == "shipped_only" || v.NewModels == "deny")
}

// RulePatch is a partial rule write (AEON-1173): an absent rule keeps its
// stored value. Settings › Accounts sends only the three rules it edits, so its
// saves never carry new_models; Models' Auto-update edits that one (AEON-1148).
type RulePatch struct {
	NewAccounts *string `json:"new_accounts"`
	NewContexts *string `json:"new_contexts"`
	NewProjects *string `json:"new_projects"`
	NewModels   *string `json:"new_models"`
}

func (r RulePatch) empty() bool {
	return r.NewAccounts == nil && r.NewContexts == nil && r.NewProjects == nil && r.NewModels == nil
}
func (r RulePatch) apply(v RuleValues) RuleValues {
	for _, f := range []struct {
		from *string
		to   *string
	}{{r.NewAccounts, &v.NewAccounts}, {r.NewContexts, &v.NewContexts}, {r.NewProjects, &v.NewProjects}, {r.NewModels, &v.NewModels}} {
		if f.from != nil {
			*f.to = *f.from
		}
	}
	return v
}

// WriteRules is the sole model-default writer, including the compatibility
// Auto-update endpoint. The one-shot setting is armed only for its mirror
// update, then cleared even when the existing settings already match.
func WriteRules(ctx context.Context, tx pgx.Tx, p tenant.Principal, expected int64, values RuleValues) (Rules, error) {
	if !validRules(values) {
		return Rules{}, fail(400, "invalid account-use rules")
	}
	return writeRules(ctx, tx, p, expected, func(RuleValues) RuleValues { return values })
}

// WriteRulePatch saves the rules a patch names over the values stored under the
// matrix fence; the revision guard is the same as for a full write.
func WriteRulePatch(ctx context.Context, tx pgx.Tx, p tenant.Principal, expected int64, patch RulePatch) (Rules, error) {
	// Every named value is checked before any lock: over valid values, the patch is valid only if each of its own is.
	if patch.empty() || !validRules(patch.apply(RuleValues{NewAccounts: "ask", NewContexts: "ask", NewProjects: "default", NewModels: "allow"})) {
		return Rules{}, fail(400, "invalid account-use rules")
	}
	return writeRules(ctx, tx, p, expected, patch.apply)
}

func writeRules(ctx context.Context, tx pgx.Tx, p tenant.Principal, expected int64, next func(RuleValues) RuleValues) (Rules, error) {
	if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
		return Rules{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id'),0))`); err != nil {
		return Rules{}, err
	}
	before, err := FenceWrite(ctx, tx, p, expected)
	if err != nil {
		return Rules{}, err
	}
	values := next(before.RuleValues)
	if _, err = tx.Exec(ctx, `UPDATE account_use_rules SET new_accounts=$1,new_contexts=$2,new_projects=$3,new_models=$4,revision=revision+1`, values.NewAccounts, values.NewContexts, values.NewProjects, values.NewModels); err != nil {
		return Rules{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.account_use_rule_write','on',true)`); err != nil {
		return Rules{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE model_refresh_settings SET auto_add_profiles=$1`, values.NewModels == "allow"); err != nil {
		return Rules{}, err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.account_use_rule_write','',true)`); err != nil {
		return Rules{}, err
	}
	after, err := ReadRules(ctx, tx)
	if err != nil {
		return Rules{}, err
	}
	_, err = events.Append(ctx, tx, p, events.Change{Type: "account_use.rules_changed", Before: before, After: after})
	return after, err
}

// ActivateForLedger is called by the daemon enrolment slice while it holds
// tenant and matrix fences. It never relaxes an existing activation.
func ActivateForLedger(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `UPDATE account_use_rules SET enforced_at=coalesce(enforced_at,clock_timestamp())`)
	return err
}

func bump(ctx context.Context, tx pgx.Tx) (Rules, error) {
	return scanRules(tx.QueryRow(ctx, `UPDATE account_use_rules SET revision=revision+1 RETURNING `+ruleColumns))
}

func missing(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fail(404, "account-use resource not found")
	}
	return err
}
