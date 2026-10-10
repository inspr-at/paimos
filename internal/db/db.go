// SPDX-License-Identifier: AGPL-3.0-only

// Package db owns the Postgres pool, migrations and tenant-scoped transactions.
// Shared contract between P0.2 (implements) and P0.3 (uses); extend, do not rename.
package db

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TenantSetting is the Postgres setting row-level security policies read.
const TenantSetting = "aeon.tenant_id"

// Open connects the pool and runs pending migrations.
//
// Sessions run with JIT compilation off. Aeon's reads are short OLTP queries,
// but recursive tree walks carry large row estimates, so Postgres would JIT
// compile them: on the production copy (AEON-140) that cost about 310 ms of a
// 350 ms /api/projects query that runs in 26 ms without it. A URL that sets
// jit itself (options=-c jit=on) keeps its choice.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := ParsePoolConfig(url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if _, set := cfg.ConnConfig.RuntimeParams["jit"]; !set && !strings.Contains(cfg.ConnConfig.RuntimeParams["options"], "jit") {
		cfg.ConnConfig.RuntimeParams["jit"] = "off"
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	if err := CheckRequiredCapabilities(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// InTenant runs fn in one transaction with TenantSetting set to tenantID
// (set_config(..., true)), so every RLS policy applies. The same statement
// sets the transaction's project visibility (ADR-003 P2): an explicit service
// visibility from AllProjects or OnlyProjects, else the visibility of the
// principal in ctx, else none (fail closed).
// Carry the authenticating principal in ctx: keyed agents acquire their usage
// admission fence before fn can lock resources. WithKeyScopeUse adds target
// keys to the sorted admission batch for operations that touch several keys.
func InTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	return InTenantWithBootstrap(ctx, ctx, pool, tenantID, fn)
}

// InTenantWithBootstrap bounds acquisition, BEGIN and authorization setup with
// bootstrap. Established transactions use ctx, including COMMIT and ROLLBACK;
// callers can retain an uncanceled cleanup context with server SQL timeouts.
func InTenantWithBootstrap(ctx, bootstrap context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	return inTenant(ctx, bootstrap, pool, tenantID, pgx.TxOptions{}, true, fn)
}

// InTenantReadSnapshot reads permissions, canonical identity and response data
// from one snapshot after any independently committed preparation. Guarded and
// keyed reads allow authorization row locks and scope-use evidence writes;
// ordinary reads remain read-only. Neither runs work-status derivation.
func InTenantReadSnapshot(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	if HasTransaction(ctx) {
		return fmt.Errorf("tenant read snapshot requires an independent transaction")
	}
	options := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	_, guarded := ctx.Value(tenantGuardKey{}).(TenantGuard)
	p, keyed := tenant.PrincipalFrom(ctx)
	if guarded || keyed && p.Kind == tenant.Agent && p.KeyID != "" {
		options.AccessMode = pgx.ReadWrite
	}
	return inTenant(ctx, ctx, pool, tenantID, options, false, fn)
}

func inTenant(ctx, bootstrap context.Context, pool *pgxpool.Pool, tenantID string, options pgx.TxOptions, deriveWorkStatus bool, fn func(pgx.Tx) error) error {
	var tx pgx.Tx
	var err error
	if parent, ok := ctx.Value(transactionContextKey{}).(pgx.Tx); ok {
		tx, err = parent.Begin(bootstrap)
	} else {
		tx, err = beginTx(bootstrap, pool, options)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Set only tenant isolation until keyed admission validates the captured
	// creator. Project visibility must never be built from a stale owner.
	p, hasPrincipal := tenant.PrincipalFrom(ctx)
	keyed := hasPrincipal && p.TenantID == tenantID && p.Kind == tenant.Agent && p.KeyID != ""
	if keyed {
		if _, err := tx.Exec(bootstrap, `SELECT set_config($1,$2,true)`, TenantSetting, tenantID); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
	} else if err := enterTenant(bootstrap, tx, tenantID); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}
	if limit, ok := ctx.Value(readLimitKey{}).(*readLimit); ok {
		if err := SetLocalStatementTimeout(bootstrap, tx, limit.duration); err != nil {
			return fmt.Errorf("set read statement timeout: %w", err)
		}
	}
	if err := lockAgentScopeUse(bootstrap, tx, tenantID); err != nil {
		return err
	}
	if keyed {
		if err := ValidateKeyCreatorTx(bootstrap, tx, p); err != nil {
			return err
		}
		if err := enterTenant(bootstrap, tx, tenantID); err != nil {
			return fmt.Errorf("set tenant visibility: %w", err)
		}
	}
	var active bool
	if deriveWorkStatus {
		active, err = beginWorkStatus(bootstrap, tx)
		if err != nil {
			return err
		}
	}
	if guard, ok := ctx.Value(tenantGuardKey{}).(TenantGuard); ok {
		if err := guard(bootstrap, tx, tenantID); err != nil {
			return err
		}
		// The guard may have waited behind an access change. Recompute project
		// visibility under its fence rather than retaining the pre-lock snapshot.
		if err := enterTenant(bootstrap, tx, tenantID); err != nil {
			return fmt.Errorf("refresh guarded tenant: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		if limit, ok := ctx.Value(readLimitKey{}).(*readLimit); ok && IsStatementTimeout(err) {
			limit.timedOut.Store(true)
		}
		return err
	}
	if active {
		if group, ok := ctx.Value(workStatusGroupKey{}).(map[string]bool); ok {
			group[tenantID] = true
		}
	}
	if active {
		if _, nested := ctx.Value(transactionContextKey{}).(pgx.Tx); !nested {
			if err := flushWorkStatus(ctx, tx); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// TenantGuard fences a request's authorization at the start of each transaction,
// before a handler can take resource locks. It must only use the supplied tx.
type TenantGuard func(context.Context, pgx.Tx, string) error
type tenantGuardKey struct{}

func WithTenantGuard(ctx context.Context, guard TenantGuard) context.Context {
	return context.WithValue(ctx, tenantGuardKey{}, guard)
}

// WithAdditionalTenantGuard preserves an authentication guard while adding a
// handler fence. Both run before target reads and the visibility refresh.
func WithAdditionalTenantGuard(ctx context.Context, guard TenantGuard) context.Context {
	prior, _ := ctx.Value(tenantGuardKey{}).(TenantGuard)
	return WithTenantGuard(ctx, func(ctx context.Context, tx pgx.Tx, tid string) error {
		if prior != nil {
			if err := prior(ctx, tx, tid); err != nil {
				return err
			}
		}
		return guard(ctx, tx, tid)
	})
}

type transactionContextKey struct{}

// HasTransaction identifies a retained outer transaction. Independent preparation
// must refuse such a context instead of hiding a nested savepoint commit in it.
func HasTransaction(ctx context.Context) bool {
	_, ok := ctx.Value(transactionContextKey{}).(pgx.Tx)
	return ok
}

// InTransaction groups sequential InTenant calls into one atomic unit. Each
// tenant operation still enters its own savepoint and sets its RLS context.
// Callers must pass the supplied context to every operation in the unit.
func InTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context) error) error {
	tx, err := beginTx(ctx, pool, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	group := map[string]bool{}
	grouped := context.WithValue(context.WithValue(ctx, transactionContextKey{}, tx), workStatusGroupKey{}, group)
	if err := fn(grouped); err != nil {
		return err
	}
	// Coalesce every nested tenant operation at the outer commit boundary.
	tenants := make([]string, 0, len(group))
	for id := range group {
		tenants = append(tenants, id)
	}
	slices.Sort(tenants)
	for _, id := range tenants {
		if err := enterTenant(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.work_status_entered',$1,true)`, id); err != nil {
			return err
		}
		if err := flushWorkStatus(ctx, tx); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
