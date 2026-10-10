// SPDX-License-Identifier: AGPL-3.0-only
// Package modelactivation is the shared immutable-pin activation boundary.
package modelactivation

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

type Cause string

const (
	ShippedCatalog  Cause = "shipped_catalog"
	VendorSuccessor Cause = "vendor_successor"
	Person          Cause = "person"
)

// Pin includes the existing permission of the initiating person operation.
// Account model writes use account.manage; registry writes use models.manage.
type Pin struct {
	Slug, Version, Harness, Family, Model, Effort, Tier string
	Enabled                                             bool
	DisplayOverrides                                    map[string]string
	Source, Note                                        string
	RegisteredEffortLevel                               *int
	Permission                                          string
}

type Profile struct {
	ID, Slug, Version, Harness, Family, Model, Effort, Tier string
	Enabled                                                 bool
	CreatedAt                                               time.Time
}

type permissionError struct{}

func (permissionError) Error() string   { return "model activation permission denied" }
func (permissionError) StatusCode() int { return 403 }
func (permissionError) Unwrap() error   { return authz.ErrForbidden }

// Lock takes catalog then matrix, after the caller's tenant/tree/pairing fences
// and before any record locks. Activate never acquires a later fence itself.
func Lock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id',true),0))`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-account-use:' || current_setting('aeon.tenant_id',true),0))`)
	return err
}

// Activate inserts exactly one pin under the caller's tenant and catalog/matrix
// fences. Person authority is re-read in this final write transaction. A nested
// transaction lets a failed insert roll back its cause before a caller retries.
func Activate(ctx context.Context, tx pgx.Tx, p tenant.Principal, pin Pin, cause Cause) (out Profile, err error) {
	// Never preserve a cause left by another statement, including a disabled pin.
	if _, err = tx.Exec(ctx, `SELECT set_config('aeon.model_activation_cause','',true)`); err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_, clearErr := tx.Exec(cleanup, `SELECT set_config('aeon.model_activation_cause','',true)`)
		err = errors.Join(err, clearErr)
	}()
	if cause != ShippedCatalog && cause != VendorSuccessor && cause != Person && cause != "" {
		return out, errors.New("unknown model activation cause")
	}
	if cause == Person {
		if p.Kind != tenant.Person || (pin.Permission != "models.manage" && (pin.Permission != "account.manage" || pin.Harness != "pi")) {
			return out, permissionError{}
		}
		if err = authz.RequireTx(ctx, tx, p, pin.Permission, authz.Scope{}); err != nil {
			return out, err
		}
		var rule string
		if err = tx.QueryRow(ctx, `SELECT new_models FROM account_use_rules WHERE tenant_id=$1`, p.TenantID).Scan(&rule); err != nil {
			return out, err
		}
		if pin.Enabled && rule == "deny" {
			if err = authz.RequireTx(ctx, tx, p, "account.use.manage", authz.Scope{}); err != nil {
				return out, err
			}
		}
	}
	if pin.DisplayOverrides == nil {
		pin.DisplayOverrides = map[string]string{}
	}
	raw, err := json.Marshal(pin.DisplayOverrides)
	if err != nil {
		return out, err
	}
	sub, err := tx.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = sub.Rollback(cleanup)
	}()
	if pin.Enabled {
		if _, err = sub.Exec(ctx, `SELECT set_config('aeon.model_activation_cause',$1,true)`, string(cause)); err != nil {
			return out, err
		}
	}
	err = sub.QueryRow(ctx, `INSERT INTO model_profiles
		(tenant_id,slug,version,harness,family,model,effort,tier,enabled,display_overrides,source,note,registered_effort_level)
		VALUES($1::uuid,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$12,$13)
		RETURNING id::text,slug,version,harness,family,model,effort,tier,enabled,created_at`,
		p.TenantID, pin.Slug, pin.Version, pin.Harness, pin.Family, pin.Model, pin.Effort, pin.Tier, pin.Enabled, string(raw), pin.Source, pin.Note, pin.RegisteredEffortLevel).
		Scan(&out.ID, &out.Slug, &out.Version, &out.Harness, &out.Family, &out.Model, &out.Effort, &out.Tier, &out.Enabled, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	err = sub.Commit(ctx)
	return out, err
}
