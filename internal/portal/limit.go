// SPDX-License-Identifier: AGPL-3.0-only

package portal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
)

const publicLimitWindow = time.Minute

// allow locks one bucket in quote_public_rate_limits, the sliding window
// AEON-133 shares across replicas. Portal operations use their own bucket
// names so they do not consume a quote link's budget. Public portal calls
// pass the zero tenant: the bucket is the client and the operation, never
// the slug, so an unknown address and a closed portal share one window.
// The stored key is an HMAC; the address itself is not written.
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
		return nil
	})
	return allowed, retryAfter, err
}

// ExpireLimits deletes portal limiter rows older than the sliding window.
// It does not wait for another request: RunLimitSweep calls it on a timer,
// including once at startup. Only the zero-tenant partition is visible here,
// which is where public portal buckets are stored.
func (m *Module) ExpireLimits(ctx context.Context) error {
	if m.pool == nil {
		return errors.New("portal limiter unavailable")
	}
	return db.InTenant(ctx, m.pool, zeroTenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM quote_public_rate_limits WHERE updated_at < clock_timestamp() - make_interval(secs => $1)`, int(publicLimitWindow.Seconds()))
		return err
	})
}

// RunLimitSweep expires idle limiter rows until ctx is cancelled.
func (m *Module) RunLimitSweep(ctx context.Context) {
	ticker := time.NewTicker(publicLimitWindow)
	defer ticker.Stop()
	for {
		if err := m.ExpireLimits(ctx); err != nil && ctx.Err() == nil {
			slog.Error("portal limit expiry", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Module) limit(w http.ResponseWriter, r *http.Request, operation string, limit int) bool {
	key, err := m.bucketKey(operation, remoteIP(r))
	if err != nil {
		fail(w, http.StatusServiceUnavailable, "portal unavailable")
		return false
	}
	allowed, retryAfter, err := m.allow(r.Context(), zeroTenant, key, limit)
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

// bucketKey is HMAC-SHA256 under the server session key, the same keyed-hash
// mechanism as the session cookie. A plain digest of the address would let
// someone with a database copy recover it offline. The key is never logged.
func (m *Module) bucketKey(operation, ip string) (string, error) {
	if len(m.macKey) < 32 {
		return "", errors.New("portal limiter key unavailable")
	}
	mac := hmac.New(sha256.New, m.macKey)
	mac.Write([]byte("portal-limit"))
	mac.Write([]byte{0})
	mac.Write([]byte(operation))
	mac.Write([]byte{0})
	mac.Write([]byte(ip))
	return hex.EncodeToString(mac.Sum(nil)), nil
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
