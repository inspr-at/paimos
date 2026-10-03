// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func evidenceHTTP(mod httpapi.Module, ctx context.Context, p tenant.Principal, token, method, path, body string, now time.Time) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r = r.WithContext(context.WithValue(tenant.WithPrincipal(ctx, p), clockKey{}, now))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	mux := http.NewServeMux()
	mod.Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func TestResidencyEvidenceReservationUsesStoredEvidence(t *testing.T) {
	for _, requirement := range []string{"eu", "local"} {
		t.Run(requirement, func(t *testing.T) {
			reset(t)
			owner, host, profile, token, mod := groupFixture(t)
			a := groupAccount(t, mod, owner, host, token, "route-evidence", "daemon-eu", "EU", "test")
			ownEvidenceAccount(t, owner, a)
			_, _, run := insertTicketRun(t, owner, host, profile)
			seed(t, owner, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET residency=$2 WHERE id=$1`, run, requirement)
				return err
			})
			now := time.Now().UTC().Truncate(time.Second)
			e := evidenceAt(profile, now)
			*e.LocalExecution = requirement == "local"
			e.ExpiresAt = now.Add(time.Second)
			body := routeBody(t, run, a.DaemonID, []Account{a}, map[string]int64{"requests": 1})
			check := func(at time.Time, want int) *httptest.ResponseRecorder {
				t.Helper()
				w := evidenceHTTP(mod, t.Context(), host, token, "POST", "/api/agent-accounts/route", body, at)
				if w.Code != want {
					t.Fatalf("reservation: status %d want %d: %s", w.Code, want, w.Body.String())
				}
				return w
			}
			check(now, 409) // No evidence.
			callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 200)
			w := check(now, 200)
			var out RouteResult
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out.AccountID != a.ID || len(out.Reservations) != 1 {
				t.Fatalf("wrong reservation: %+v", out)
			}
			check(e.ExpiresAt, 409) // Queued reservation replay fails at expiry.
			seed(t, owner, func(tx pgx.Tx) error {
				return Release(t.Context(), tx, host, run, "", "")
			})
			_, _, next := insertTicketRun(t, owner, host, profile)
			seed(t, owner, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET residency=$2 WHERE id=$1`, next, requirement)
				return err
			})
			body = routeBody(t, next, a.DaemonID, []Account{a}, map[string]int64{"requests": 1})
			check(e.ExpiresAt, 409) // Fresh reservation also fails closed.
			if scalar(t, owner, `SELECT count(*) FROM account_reservations WHERE run_id=$1`, next) != 0 {
				t.Fatal("expired evidence created a reservation")
			}
			// Identical clock, account and capacity qualify after renewal, so
			// the rejection above cannot pass because of a parallel-slot limit.
			at := e.ExpiresAt
			e.ExpiresAt = now.Add(time.Hour)
			callEvidence(t, mod, owner, "", "PUT", a.ID, at, e, 200)
			check(at, 200)
		})
	}
}

func TestResidencyEvidenceLocalRequiresEUCountries(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	profile := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, field := range []string{"inference", "storage", "logs"} {
		for _, countries := range [][]string{{"US"}, {}} {
			t.Run(field+strings.Join(countries, ""), func(t *testing.T) {
				e := evidenceAt(profile, now)
				*e.LocalExecution = true
				switch field {
				case "inference":
					e.InferenceCountries = countries
				case "storage":
					e.StorageCountries = countries
				case "logs":
					e.LogCountries = countries
				}
				kept, empty, err := applyResidency(t.Context(), nil, []Account{{residencyEvidence: &e}}, profile, "eu", now)
				if err != nil || !empty || len(kept) != 0 {
					t.Fatalf("local evidence widened EU fence: kept=%v empty=%v err=%v", kept, empty, err)
				}
			})
		}
	}
}

func TestResidencyEvidenceValidityBound(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	profile := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	e := evidenceAt(profile, now)
	e.ExpiresAt = e.VerifiedAt.Add(400 * 24 * time.Hour)
	if err := e.validate(now); err != nil || e.class(profile, now) != "eu" {
		t.Fatalf("400-day boundary rejected: %v", err)
	}
	e.ExpiresAt = e.ExpiresAt.Add(time.Nanosecond)
	if err := e.validate(now); err == nil || e.class(profile, now) != "any" {
		t.Fatal("overlong validity accepted")
	}
}

// Wait on a PostgreSQL-observed lock barrier or a completed request. The
// deadline is a hang guard, never evidence that an operation is blocked.
func evidenceBlockedOrDone(t *testing.T, ctx context.Context, blockerPID int, done <-chan *httptest.ResponseRecorder) (*httptest.ResponseRecorder, string) {
	t.Helper()
	for {
		select {
		case w := <-done:
			return w, ""
		default:
		}
		var query string
		err := adminPool.QueryRow(ctx, `SELECT query FROM pg_stat_activity
 WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) LIMIT 1`, blockerPID).Scan(&query)
		if err == nil {
			return nil, query
		}
		if err != pgx.ErrNoRows {
			t.Fatal(err)
		}
	}
}

func startEvidenceRequest(t *testing.T, ctx context.Context, mod httpapi.Module, p tenant.Principal, token, method, id string, e ResidencyEvidence) <-chan *httptest.ResponseRecorder {
	t.Helper()
	body := encoded(t, e)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- evidenceHTTP(mod, ctx, p, token, method, "/api/agent-accounts/"+id+"/residency-evidence", body, e.VerifiedAt.Add(time.Hour))
	}()
	return done
}

func evidenceBlocker(t *testing.T, ctx context.Context) (pgx.Tx, int) {
	t.Helper()
	tx, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

func TestResidencyEvidenceTenantFencePrecedesPairingLock(t *testing.T) {
	reset(t)
	owner, host, profile, token, mod := groupFixture(t)
	a := groupAccount(t, mod, owner, host, token, "lock-order", "daemon", "Order", "test")
	ownEvidenceAccount(t, owner, a)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, pid := evidenceBlocker(t, ctx)
	if _, err := blocker.Exec(ctx, `SELECT set_config('aeon.tenant_id',$1,true)`, owner.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := agentpairing.Lock(ctx, blocker); err != nil {
		t.Fatal(err)
	}
	done := startEvidenceRequest(t, ctx, mod, owner, "", "PUT", a.ID, evidenceAt(profile, time.Now()))
	w, query := evidenceBlockedOrDone(t, ctx, pid, done)
	if w != nil || !strings.Contains(query, "FROM tenants WHERE") || !strings.Contains(query, "FOR NO KEY UPDATE") {
		t.Fatalf("write did not stop at tenant fence: response=%v query=%s", w, query)
	}
	// The blocker already holds tenant/tree/pairing. The waiting write has
	// not reached pairing, so re-entering those fences cannot deadlock.
	if _, err := blocker.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, owner.TenantID); err != nil {
		t.Fatalf("holder could not re-enter its tenant fence: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 200 {
		t.Fatalf("write failed after pairing released: %d %s", w.Code, w.Body.String())
	}
}

func TestResidencyEvidenceReadAvoidsWriteLocks(t *testing.T) {
	for _, lock := range []string{"tenant", "account"} {
		t.Run(lock, func(t *testing.T) {
			reset(t)
			owner, host, profile, token, mod := groupFixture(t)
			a := groupAccount(t, mod, owner, host, token, "read-lock", "daemon", "Read", "test")
			ownEvidenceAccount(t, owner, a)
			e := evidenceAt(profile, time.Now())
			callEvidence(t, mod, owner, "", "PUT", a.ID, e.VerifiedAt.Add(time.Hour), e, 200)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			blocker, pid := evidenceBlocker(t, ctx)
			query, id := `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner.TenantID
			if lock == "account" {
				query, id = `SELECT id FROM agent_accounts WHERE id=$1 FOR UPDATE`, a.ID
			}
			if _, err := blocker.Exec(ctx, query, id); err != nil {
				t.Fatal(err)
			}
			done := startEvidenceRequest(t, ctx, mod, owner, "", "GET", a.ID, e)
			w, query := evidenceBlockedOrDone(t, ctx, pid, done)
			if w == nil {
				t.Fatalf("GET blocked on %s write lock: %s", lock, query)
			}
			if w.Code != 200 {
				t.Fatalf("GET: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestResidencyEvidenceRejectsStructureBeforeLocks(t *testing.T) {
	reset(t)
	owner, host, profile, token, mod := groupFixture(t)
	a := groupAccount(t, mod, owner, host, token, "invalid-lock", "daemon", "Invalid", "test")
	ownEvidenceAccount(t, owner, a)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, pid := evidenceBlocker(t, ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner.TenantID); err != nil {
		t.Fatal(err)
	}
	e := evidenceAt(profile, time.Now())
	e.ProofRef = ""
	done := startEvidenceRequest(t, ctx, mod, owner, "", "PUT", a.ID, e)
	w, query := evidenceBlockedOrDone(t, ctx, pid, done)
	if w == nil {
		t.Fatalf("invalid structure reached transaction locks: %s", query)
	}
	if w.Code != 400 {
		t.Fatalf("invalid evidence: %d %s", w.Code, w.Body.String())
	}
}

func TestResidencyEvidenceSerializesBoundKeyRevocation(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "evidence-key-race", "person", "Owner", []string{"admin"})
	host := addPrincipal(t, owner.TenantID, "agent", "Host", []string{"admin"})
	dbtest.BindRole(t, testDB, owner.TenantID, host.ID, "admin")
	profile := codexProfile(t, owner)
	token := issueKey(t, host, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	a := groupAccount(t, mod, owner, host, token, "key-race", "daemon", "Host", "test")
	ownEvidenceAccount(t, owner, a)
	bindEvidenceHost(t, owner, host, a, profile, token)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	blocker, pid := evidenceBlocker(t, ctx)
	if _, err := blocker.Exec(ctx, `UPDATE agent_keys SET revoked_at=now() WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, host.ID); err != nil {
		t.Fatal(err)
	}
	done := startEvidenceRequest(t, ctx, mod, host, token, "PUT", a.ID, evidenceAt(profile, time.Now()))
	w, query := evidenceBlockedOrDone(t, ctx, pid, done)
	if w != nil || !strings.Contains(query, "agent_keys") {
		t.Fatalf("key revocation not serialized: response=%v query=%s", w, query)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 403 {
		t.Fatalf("revoked key accepted: %d %s", w.Code, w.Body.String())
	}
	if count := evidenceEventCount(t, owner); count != 0 {
		t.Fatalf("revoked host wrote %d events", count)
	}
}
