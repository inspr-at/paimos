// SPDX-License-Identifier: AGPL-3.0-only

package public

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
)

func TestPublicLimitSharedWindowTenantIsolationAndCleanup(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	tenantB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	key := hash("read:192.0.2.5")
	first := &Module{pool: database.App}
	second := &Module{pool: database.App}

	for _, module := range []*Module{first, second} {
		allowed, retry, err := module.allowPublic(ctx, tenantA, key, 2)
		if err != nil || !allowed || retry != 0 {
			t.Fatalf("shared first two: allowed=%t retry=%d err=%v", allowed, retry, err)
		}
	}
	allowed, retry, err := first.allowPublic(ctx, tenantA, key, 2)
	if err != nil || allowed || retry != 60 {
		t.Fatalf("shared denial: allowed=%t retry=%d err=%v", allowed, retry, err)
	}
	allowed, _, err = second.allowPublic(ctx, tenantB, key, 2)
	if err != nil || !allowed {
		t.Fatalf("second tenant: allowed=%t err=%v", allowed, err)
	}
	var count int
	if err := db.InTenant(ctx, database.App, tenantB, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_rate_limits WHERE bucket_key=$1`, key).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("tenant B visibility: count=%d err=%v", count, err)
	}
	if err := database.App.QueryRow(ctx, `SELECT count(*) FROM quote_public_rate_limits`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unscoped visibility: count=%d err=%v", count, err)
	}

	// Advance the stored attempts without waiting a minute. The next call
	// prunes them and admits a new attempt in the same locked transaction.
	if err := db.InTenant(ctx, database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE quote_public_rate_limits SET attempts=ARRAY[clock_timestamp()-interval '61 seconds'] WHERE bucket_key=$1`, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	allowed, _, err = second.allowPublic(ctx, tenantA, key, 2)
	if err != nil || !allowed {
		t.Fatalf("rolled window: allowed=%t err=%v", allowed, err)
	}
	if err := db.InTenant(ctx, database.App, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT cardinality(attempts) FROM quote_public_rate_limits WHERE bucket_key=$1`, key).Scan(&count)
	}); err != nil || count != 1 {
		t.Fatalf("rolled attempts: count=%d err=%v", count, err)
	}

	oldKey := hash("old")
	if err := db.InTenant(ctx, database.App, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO quote_public_rate_limits(tenant_id,bucket_key,attempts,updated_at) VALUES($1::uuid,$2,ARRAY[clock_timestamp()-interval '3 minutes'],clock_timestamp()-interval '3 minutes')`, tenantA, oldKey)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	first.limitCalls.Store(63)
	if allowed, _, err := first.allowPublic(ctx, tenantA, hash("cleanup-trigger"), 2); err != nil || !allowed {
		t.Fatalf("cleanup trigger: allowed=%t err=%v", allowed, err)
	}
	if err := db.InTenant(ctx, database.App, tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM quote_public_rate_limits WHERE bucket_key=$1`, oldKey).Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("cleanup: count=%d err=%v", count, err)
	}
}

func TestPublicLimitRetryAfterUsesOldestAttempt(t *testing.T) {
	database := dbtest.Open(t)
	ctx := t.Context()
	tenantID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	key := hash("accept:192.0.2.6")
	m := &Module{pool: database.App}
	if err := db.InTenant(ctx, database.App, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO quote_public_rate_limits(tenant_id,bucket_key,attempts) VALUES($1::uuid,$2,ARRAY[clock_timestamp()-interval '55 seconds'])`, tenantID, key)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	allowed, retry, err := m.allowPublic(ctx, tenantID, key, 1)
	if err != nil || allowed || retry < 4 || retry > 5 {
		t.Fatalf("retry after: allowed=%t retry=%d err=%v", allowed, retry, err)
	}

	// The HTTP route sends Retry-After as seconds without exposing a bucket key.
	req := httptest.NewRequest(http.MethodPost, "/api/public/quotes/invalid/token/accept", nil)
	req.RemoteAddr = "192.0.2.6:40000"
	rec := httptest.NewRecorder()
	for i := 0; i < 11; i++ {
		rec = httptest.NewRecorder()
		m.limitPublic(rec, req, "accept", 10)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("HTTP status = %d", rec.Code)
	}
	value, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || value < 1 || value > int(time.Minute.Seconds()) {
		t.Fatalf("HTTP Retry-After = %q", rec.Header().Get("Retry-After"))
	}
}

func TestPublicLimitConcurrentInstancesAdmitOnlyLimit(t *testing.T) {
	database := dbtest.Open(t)
	instances := [2]*Module{{pool: database.App}, {pool: database.App}}
	const limit = 10
	var wg sync.WaitGroup
	results := make(chan bool, 24)
	errors := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(module *Module) {
			defer wg.Done()
			allowed, _, err := module.allowPublic(t.Context(), zeroTenant, hash("read:203.0.113.7"), limit)
			results <- allowed
			errors <- err
		}(instances[i%2])
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	admitted := 0
	for allowed := range results {
		if allowed {
			admitted++
		}
	}
	if admitted != limit {
		t.Fatalf("admitted %d concurrent requests, want %d", admitted, limit)
	}
}
