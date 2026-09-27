// SPDX-License-Identifier: AGPL-3.0-only

package public

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const publicLimitWindow = time.Minute

// publicLimitTenant resolves only the public selector. Unknown selectors use
// the zero-UUID RLS partition, so they retain the same IP limit as valid links.
func (m *Module) publicLimitTenant(ctx context.Context, selector string) (string, error) {
	if !selectorPattern.MatchString(selector) {
		return zeroTenant, nil
	}
	var tenantID *string
	err := db.InTenant(ctx, m.pool, zeroTenant, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('aeon.public_quote_selector',$1,true)`, selector); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT aeon_resolve_quote_public_tenant($1)::text`, selector).Scan(&tenantID)
	})
	if err != nil {
		return "", err
	}
	if tenantID == nil {
		return zeroTenant, nil
	}
	return *tenantID, nil
}

// allowPublic locks one tenant/IP/operation row. The database clock and row
// lock give every replica the same one-minute sliding window. Operational
// counters do not belong in the tenant's permanent business event history.
func (m *Module) allowPublic(ctx context.Context, tenantID, key string, limit int) (bool, int, error) {
	if m.pool == nil || limit < 1 || limit > 120 {
		return false, 0, errors.New("public rate limiter unavailable")
	}
	allowed, retryAfter := false, 0
	err := db.InTenant(ctx, m.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO quote_public_rate_limits(tenant_id,bucket_key) VALUES($1::uuid,$2) ON CONFLICT DO NOTHING`, tenantID, key); err != nil {
			return err
		}
		var now time.Time
		var attempts []time.Time
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp(),attempts FROM quote_public_rate_limits WHERE tenant_id=$1::uuid AND bucket_key=$2 FOR UPDATE`, tenantID, key).Scan(&now, &attempts); err != nil {
			return err
		}
		current := make([]time.Time, 0, len(attempts)+1)
		for _, at := range attempts {
			if now.Sub(at) < publicLimitWindow {
				current = append(current, at)
			}
		}
		if len(current) >= limit {
			wait := current[0].Add(publicLimitWindow).Sub(now)
			retryAfter = int((wait + time.Second - 1) / time.Second)
			if retryAfter < 1 {
				retryAfter = 1
			}
		} else {
			current = append(current, now)
			if _, err := tx.Exec(ctx, `UPDATE quote_public_rate_limits SET attempts=$3,updated_at=$4 WHERE tenant_id=$1::uuid AND bucket_key=$2`, tenantID, key, current, now); err != nil {
				return err
			}
			allowed = true
		}
		// One bounded sweep per 64 calls per process keeps storage finite without
		// turning every public request into a table-wide cleanup.
		if m.limitCalls.Add(1)%64 == 0 {
			return cleanupPublicLimits(ctx, tx, tenantID)
		}
		return nil
	})
	return allowed, retryAfter, err
}

func cleanupPublicLimits(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM quote_public_rate_limits WHERE ctid IN (
		SELECT ctid FROM quote_public_rate_limits
		WHERE tenant_id=$1::uuid AND updated_at < clock_timestamp()-interval '2 minutes'
		ORDER BY updated_at LIMIT 128
	)`, tenantID)
	return err
}

func (m *Module) limitPublic(w http.ResponseWriter, r *http.Request, operation string, limit int) bool {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	tenantID, err := m.publicLimitTenant(r.Context(), r.PathValue("publicTenant"))
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "quote link unavailable")
		return false
	}
	allowed, retryAfter, err := m.allowPublic(r.Context(), tenantID, hash(operation+":"+remote), limit)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "quote link unavailable")
		return false
	}
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	fail(w, http.StatusTooManyRequests, "too many attempts")
	return false
}
