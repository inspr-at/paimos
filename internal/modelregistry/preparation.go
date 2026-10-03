// SPDX-License-Identifier: AGPL-3.0-only

package modelregistry

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/events"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/inspr-at/paimos/internal/workorders"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCatalogNotReady is infrastructure failure, distinct from an unavailable
// qualified reviewer. Transaction-injected resolvers never initialize a catalog.
var ErrCatalogNotReady = errors.New("model catalog is not ready")

type CatalogOperation uint8

const (
	CatalogRead CatalogOperation = iota
	CatalogManage
	CatalogReview
	CatalogCompletion
)

// CatalogPreparation describes the decoded initiating operation. Review adapters
// re-read the current target/run and authority under tenant/tree/pairing fences.
// False means authorized completion with a closed review gate, never setup.
// The callback runs again before the final event flush to catch wall-clock expiry.
type CatalogPreparation struct {
	Operation CatalogOperation
	Request   *http.Request
	Authorize func(context.Context, pgx.Tx, tenant.Principal) (bool, error)
	Clock     func(context.Context, pgx.Tx) (time.Time, error)
}

func catalogReady(ctx context.Context, tx pgx.Tx) (bool, error) {
	slugs := make([]string, 0, len(catalogProfiles()))
	for _, p := range catalogProfiles() {
		slugs = append(slugs, p.Slug)
	}
	var count int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM model_profiles WHERE slug=ANY($1::text[]) AND version=$2`, slugs, CatalogVersion).Scan(&count)
	return count == len(slugs), err
}

func requireCatalog(ctx context.Context, tx pgx.Tx) error {
	ready, err := catalogReady(ctx, tx)
	if err != nil {
		return err
	}
	if !ready {
		return ErrCatalogNotReady
	}
	return nil
}

// PrepareCatalog owns a standalone transaction. Call before beginning the final
// operation, never from a callback holding resource/event locks. Ordinary reads
// and review keys retain their existing authority; no models.manage expansion.
func PrepareCatalog(ctx context.Context, pool *pgxpool.Pool, p tenant.Principal, in CatalogPreparation) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if db.HasTransaction(ctx) {
		return errors.New("catalog preparation requires an independent transaction")
	}
	if in.Operation > CatalogCompletion {
		return errors.New("unknown catalog initiating operation")
	}
	if in.Request == nil {
		return errors.New("catalog preparation requires initiating request")
	}
	return db.InTenant(ctx, pool, p.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout','5s',true),set_config('statement_timeout','5s',true)`); err != nil {
			return err
		}
		if err := db.LockTenant(ctx, tx, p.TenantID); err != nil {
			return err
		}
		if in.Operation == CatalogReview || in.Operation == CatalogCompletion {
			if in.Authorize == nil {
				return errors.New("review preparation requires authority adapter")
			}
			if err := agentpairing.Lock(ctx, tx); err != nil {
				return err
			}
		} else if in.Authorize != nil {
			// Read adapters may need to hold current target/project placement steady.
			if err := db.LockTree(ctx, tx, p.TenantID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('aeon-model-registry:' || current_setting('aeon.tenant_id',true),0))`); err != nil {
			return err
		}
		keyPrincipal := func(scope string) (tenant.Principal, error) {
			var now time.Time
			var err error
			if in.Clock != nil {
				now, err = in.Clock(ctx, tx)
			} else {
				err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
			}
			if err != nil {
				return p, err
			}
			return workorders.CurrentKeyPrincipalAt(in.Request.WithContext(ctx), tx, p, scope, now)
		}
		authorize := func() (bool, error) {
			scope := "models.read"
			if in.Operation == CatalogManage {
				scope = "models.manage"
				if p.Kind != tenant.Person {
					return false, authz.ErrForbidden
				}
			}
			if in.Operation == CatalogReview {
				scope = "work_orders.write"
			}
			if in.Operation == CatalogCompletion {
				scope = "run.telemetry"
			}
			current, err := keyPrincipal(scope)
			if err != nil {
				return false, err
			}
			if err := workorders.RefreshPrincipal(ctx, tx, current); err != nil {
				return false, err
			}
			if in.Operation == CatalogRead || in.Operation == CatalogManage {
				if err := authz.RequireTx(ctx, tx, current, scope, authz.Scope{}); err != nil {
					return false, err
				}
			}
			if in.Authorize != nil {
				return in.Authorize(ctx, tx, current)
			}
			return true, nil
		}
		allowed, err := authorize()
		if err != nil || !allowed {
			return err
		}
		changes, err := prepareCatalogDeferred(ctx, tx, p)
		if err != nil {
			return err
		}
		allowed, err = authorize()
		if err != nil {
			return err
		}
		if !allowed {
			return authz.ErrForbidden
		} // crossed authority/expiry: roll back setup
		for _, change := range changes {
			if _, err := events.Append(ctx, tx, p, change); err != nil {
				return err
			}
		}
		// No target/run/account re-entry after the event counter. Only recheck
		// wall-clock expiry of the exact initiating key; access is still fenced.
		scope := "models.read"
		switch in.Operation {
		case CatalogManage:
			scope = "models.manage"
		case CatalogReview:
			scope = "work_orders.write"
		case CatalogCompletion:
			scope = "run.telemetry"
		}
		_, err = keyPrincipal(scope)
		return err
	})
}
