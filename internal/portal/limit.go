// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const publicLimitWindow = time.Minute

// allow locks one tenant and bucket in quote_public_rate_limits, the sliding
// window AEON-133 shares across replicas. Portal operations use their own
// bucket names so they do not consume a quote link's budget. The stored key
// is a hash; the address itself is not written.
func (m *Module) allow(ctx context.Context, tenantID, key string, limit int) (bool, int, error) {
	if m.pool == nil || limit < 1 || limit > 120 || !uuidPattern.MatchString(tenantID) {
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
		if m.limitCalls.Add(1)%64 == 0 {
			_, err := tx.Exec(ctx, `DELETE FROM quote_public_rate_limits WHERE ctid IN (
				SELECT ctid FROM quote_public_rate_limits
				WHERE tenant_id=$1::uuid AND updated_at < clock_timestamp()-interval '2 minutes'
				ORDER BY updated_at LIMIT 128
			)`, tenantID)
			return err
		}
		return nil
	})
	return allowed, retryAfter, err
}

func (m *Module) limit(w http.ResponseWriter, r *http.Request, tenantID, operation string, limit int) bool {
	if tenantID == "" {
		tenantID = zeroTenant
	}
	allowed, retryAfter, err := m.allow(r.Context(), tenantID, hash(operation+":"+remoteIP(r)), limit)
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return false
	}
	if allowed {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	fail(w, http.StatusTooManyRequests, "too many attempts")
	return false
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		host = r.RemoteAddr
	}
	if host == "" {
		host = "unknown"
	}
	if len(host) > 256 {
		host = host[:256]
	}
	return host
}
