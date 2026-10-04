// SPDX-License-Identifier: AGPL-3.0-only

// Package db owns the Postgres pool, migrations and tenant-scoped transactions.
// Shared contract between P0.2 (implements) and P0.3 (uses); extend, do not rename.
package db

import (
	"context"
	"fmt"
	"strings"

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
	cfg, err := pgxpool.ParseConfig(url)
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
	var tx pgx.Tx
	var err error
	if parent, ok := ctx.Value(transactionContextKey{}).(pgx.Tx); ok {
		tx, err = parent.Begin(ctx)
	} else {
		tx, err = pool.Begin(ctx)
	}
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := enterTenant(ctx, tx, tenantID); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}
	if limit, ok := ctx.Value(readLimitKey{}).(*readLimit); ok {
		if err := SetLocalStatementTimeout(ctx, tx, limit.duration); err != nil {
			return fmt.Errorf("set read statement timeout: %w", err)
		}
	}
	if err := lockAgentScopeUse(ctx, tx, tenantID); err != nil {
		return err
	}
	if guard, ok := ctx.Value(tenantGuardKey{}).(TenantGuard); ok {
		if err := guard(ctx, tx, tenantID); err != nil {
			return err
		}
		// The guard may have waited behind an access change. Recompute project
		// visibility under its fence rather than retaining the pre-lock snapshot.
		if err := enterTenant(ctx, tx, tenantID); err != nil {
			return fmt.Errorf("refresh guarded tenant: %w", err)
		}
	}
	if err := fn(tx); err != nil {
		if limit, ok := ctx.Value(readLimitKey{}).(*readLimit); ok && IsStatementTimeout(err) {
			limit.timedOut.Store(true)
		}
		return err
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

type transactionContextKey struct{}

// InTransaction groups sequential InTenant calls into one atomic unit. Each
// tenant operation still enters its own savepoint and sets its RLS context.
// Callers must pass the supplied context to every operation in the unit.
func InTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(context.Context) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(context.WithValue(ctx, transactionContextKey{}, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
