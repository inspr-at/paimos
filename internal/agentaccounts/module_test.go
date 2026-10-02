// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
)

var (
	adminPool *pgxpool.Pool
	appPool   *pgxpool.Pool
	testDB    *dbtest.DB
	setupOnce sync.Once
	setupErr  error
	keySeq    uint64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testDB != nil {
		if err := testDB.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "dbtest cleanup: %v\n", err)
			if code == 0 {
				code = 1
			}
		}
	}
	os.Exit(code)
}

func TestIntegratedGroupAndLimitDeleteRoutes(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/agent-accounts/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/limit", http.StatusUnauthorized},
		{"/api/agent-accounts/groups/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", http.StatusUnauthorized},
		{"/api/agent-accounts/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa/unsupported", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func useDB(t *testing.T) {
	t.Helper()
	setupOnce.Do(func() { setupErr = setupDB() })
	if setupErr != nil {
		t.Fatalf("database: %v", setupErr)
	}
}

func setupDB() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	handle, err := dbtest.New(ctx)
	if err != nil {
		return err
	}
	testDB = handle
	adminPool = handle.Admin
	appPool = handle.App
	return nil
}

func reset(t *testing.T) {
	t.Helper()
	useDB(t)
	if _, err := adminPool.Exec(t.Context(), `TRUNCATE TABLE tenants CASCADE`); err != nil {
		t.Fatalf("reset: %v", err)
	}
}

func makePrincipal(t *testing.T, slug, kind, name string, roles []string) tenant.Principal {
	t.Helper()
	var tenantID string
	if err := appPool.QueryRow(t.Context(), `INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`, slug).Scan(&tenantID); err != nil {
		t.Fatalf("tenant: %v", err)
	}
	return addPrincipal(t, tenantID, kind, name, roles)
}

func addPrincipal(t *testing.T, tenantID, kind, name string, roles []string) tenant.Principal {
	t.Helper()
	if roles == nil {
		roles = []string{}
	}
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			INSERT INTO principals (tenant_id, kind, name, roles)
			VALUES ($1::uuid, $2, $3, $4) RETURNING id::text`, tenantID, kind, name, roles).Scan(&id)
	})
	if err != nil {
		t.Fatalf("principal: %v", err)
	}
	dbtest.BindLegacy(t, testDB, tenantID, id)
	k := tenant.Person
	if kind == "agent" {
		k = tenant.Agent
	}
	return tenant.Principal{ID: id, TenantID: tenantID, Kind: k, Name: name, Roles: roles}
}

func issueKey(t *testing.T, p tenant.Principal, scopes []string) string {
	t.Helper()
	if scopes == nil {
		scopes = []string{}
	}
	secret := hex.EncodeToString([]byte("secret-for-" + p.Name + strings.Join(scopes, ",")))
	sum := sha256.Sum256([]byte(secret))
	prefix := strings.ReplaceAll(p.TenantID, "-", "") + fmt.Sprintf("%016x", atomic.AddUint64(&keySeq, 1))
	if len(prefix) < 48 {
		t.Fatalf("short prefix %s", prefix)
	}
	prefix = prefix[:48]
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_keys (tenant_id, principal_id, name, prefix, hash, scopes)
			VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)`,
			p.TenantID, p.ID, p.Name, prefix, hex.EncodeToString(sum[:]), scopes)
		return err
	})
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	return "aeon_" + prefix + "_" + secret
}

func call(t *testing.T, mod httpapi.Module, p *tenant.Principal, token, method, path, body string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	mod.Mount(mux)
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if p != nil {
		r = r.WithContext(tenant.WithPrincipal(r.Context(), *p))
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec.Code, rec.Body.Bytes()
}

func codexProfile(t *testing.T, admin tenant.Principal) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `
			INSERT INTO model_profiles (tenant_id, slug, version, harness, family, model, effort, tier)
			VALUES ($1::uuid, 'codex-luna-medium', '2', 'codex', 'openai', 'gpt-6-luna', 'medium', 'fast')
			RETURNING id::text`, admin.TenantID).Scan(&id)
	})
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	return id
}

func insertRun(t *testing.T, person, agent tenant.Principal, profileID string) string {
	t.Helper()
	var id string
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		var nodeID string
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO nodes (tenant_id, key, kind_id, title)
			SELECT $1::uuid, aeon_next_node_key($1::uuid, 'WOR'), k.id, 'Work'
			FROM node_kinds k WHERE k.slug = 'work_order'
			RETURNING id::text`, person.TenantID).Scan(&nodeID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO work_orders (tenant_id, node_id, requested_by_principal_id, assignee_principal_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'ready')`,
			person.TenantID, nodeID, person.ID, agent.ID); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `
			INSERT INTO agent_runs (tenant_id, work_order_id, agent_principal_id, model_profile_id, status)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'queued')
			RETURNING id::text`, person.TenantID, nodeID, agent.ID, profileID).Scan(&id)
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return id
}

func scalar(t *testing.T, p tenant.Principal, query string, args ...any) int64 {
	t.Helper()
	var n int64
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, p.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), query, args...).Scan(&n)
	})
	if err != nil {
		t.Fatalf("scalar: %v", err)
	}
	return n
}

// Quota-detail fixtures explicitly own their account; administrator status
// alone no longer grants another person's usage after decision 8.
func ownFixtureAccount(t *testing.T, owner tenant.Principal, a *Account) {
	t.Helper()
	if _, err := adminPool.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=now() WHERE id=$1`, a.ID, owner.ID); err != nil {
		t.Fatal(err)
	}
	a.OwnerPersonID = &owner.ID
}

func accountsMod() httpapi.Module { return New(appPool) }

func windowBody(start, end time.Time, unit string, allowance int64, pace string) string {
	return fmt.Sprintf(`{"starts_at":%q,"ends_at":%q,"unit":%q,"allowance":%d,"pace_model":%q,"burst_ratio":0}`,
		start.Format(time.RFC3339), end.Format(time.RFC3339), unit, allowance, pace)
}

func callStatus(t *testing.T, mod httpapi.Module, p *tenant.Principal, token, method, path, body string, want int, dst any) {
	t.Helper()
	status, raw := call(t, mod, p, token, method, path, body)
	if status != want {
		t.Fatalf("%s %s status %d, want %d: %s", method, path, status, want, raw)
	}
	if dst != nil && want != http.StatusNoContent {
		if err := json.Unmarshal(raw, dst); err != nil {
			t.Fatalf("json: %v body %s", err, raw)
		}
	}
}

func routeBody(t *testing.T, runID, daemonID string, accounts []Account, estimates map[string]int64) string {
	t.Helper()
	ids := make([]string, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	raw, err := json.Marshal(map[string]any{"run_id": runID, "daemon_id": daemonID, "account_ids": ids, "estimated_units": estimates})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func mustRoute(t *testing.T, mod httpapi.Module, p tenant.Principal, token, runID, daemonID string, accounts []Account, estimates map[string]int64) RouteResult {
	t.Helper()
	var out RouteResult
	callStatus(t, mod, &p, token, http.MethodPost, "/api/agent-accounts/route", routeBody(t, runID, daemonID, accounts, estimates), http.StatusOK, &out)
	return out
}

func TestAccountPoolRoutingAndLedger(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	member := addPrincipal(t, admin.TenantID, "person", "Mo", []string{"member"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	otherAgent := addPrincipal(t, admin.TenantID, "agent", "other", nil)
	profileID := codexProfile(t, admin)
	token := issueKey(t, runner, []string{"account.manage", "run.claim"})
	mod := accountsMod()
	start := time.Now().Add(-time.Minute).UTC()
	end := time.Now().Add(time.Hour).UTC()

	callStatus(t, mod, &member, "", http.MethodGet, "/api/agent-accounts", "", http.StatusForbidden, nil)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", `{"account_key":"sk-live","harness":"codex","daemon_id":"daemon-a","label":"Codex"}`, http.StatusBadRequest, nil)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", `{"account_key":"local-codex","harness":"codex","daemon_id":"daemon-a","label":"Codex","token":"x"}`, http.StatusBadRequest, nil)
	callStatus(t, mod, &runner, issueKey(t, runner, nil), http.MethodPost, "/api/agent-accounts", `{"account_key":"local-codex","harness":"codex","daemon_id":"daemon-a","label":"Codex"}`, http.StatusForbidden, nil)

	var account Account
	const register = `{"account_key":"local-codex","harness":"codex","daemon_id":"daemon-a","label":"Codex","max_parallel_runs":2}`
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", register, http.StatusCreated, &account)
	var again Account
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", register, http.StatusCreated, &again)
	if again.ID != account.ID || account.MaxParallel != 2 || account.RegisteredBy != runner.ID {
		t.Fatalf("register %+v", account)
	}
	callStatus(t, mod, &otherAgent, issueKey(t, otherAgent, []string{"account.manage"}), http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, http.StatusForbidden, nil)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", `{"daemon_id":"other","daemon_generation":"g1","available":true}`, http.StatusForbidden, nil)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, http.StatusOK, &account)
	if account.LastProbeOK == nil || !*account.LastProbeOK {
		t.Fatal("probe was not recorded")
	}
	fixtureAlwaysOn(t, mod, admin, account.ID)

	var requests Window
	callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(start, end, "requests", 100, "unrestricted"), http.StatusCreated, &requests)
	callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(start, end.Add(time.Minute), "requests", 50, "unrestricted"), http.StatusConflict, nil)
	var tokens Window
	callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(end, end.Add(time.Hour), "tokens", 100, "unrestricted"), http.StatusCreated, &tokens)
	callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(start, end, "tokens", 100, "unrestricted"), http.StatusCreated, &tokens)

	runID := insertRun(t, admin, runner, profileID)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route", routeBody(t, runID, "daemon-a", []Account{account}, map[string]int64{"requests": 10}), http.StatusConflict, nil)
	if scalar(t, admin, `SELECT coalesce(sum(reserved),0) FROM account_allowance_windows`) != 0 {
		t.Fatal("partial reservation was committed")
	}
	routed := mustRoute(t, mod, runner, token, runID, "daemon-a", []Account{account}, map[string]int64{"requests": 10, "tokens": 10})
	if routed.AccountID != account.ID || routed.DaemonID != "daemon-a" || len(routed.Reservations) != 2 {
		t.Fatalf("route %+v", routed)
	}
	replay := mustRoute(t, mod, runner, token, runID, "daemon-a", []Account{account}, map[string]int64{"requests": 10, "tokens": 10})
	if replay.Reservations[0].ReservationID != routed.Reservations[0].ReservationID {
		t.Fatal("retry did not replay the reservation")
	}
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type = 'account.reserved'`) != 1 {
		t.Fatal("reservation event repeated")
	}

	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `
			INSERT INTO run_telemetry (tenant_id, run_id, sequence, kind, turn_count_delta, input_tokens_delta, output_tokens_delta, cost_micros_delta)
			VALUES ($1::uuid, $2::uuid, 1, 'usage', 2, 3, 2, 7)`, admin.TenantID, runID)
		if err != nil {
			return err
		}
		if err := Settle(t.Context(), tx, runner, runID); err != nil {
			return err
		}
		return Settle(t.Context(), tx, runner, runID)
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if scalar(t, admin, `SELECT coalesce(sum(used),0) FROM account_allowance_windows WHERE unit = 'requests'`) != 2 {
		t.Fatal("requests were not settled from telemetry")
	}
	if scalar(t, admin, `SELECT coalesce(sum(used),0) FROM account_allowance_windows WHERE unit = 'tokens'`) != 5 {
		t.Fatal("tokens were not settled")
	}
	if scalar(t, admin, `SELECT coalesce(sum(reserved),0) FROM account_allowance_windows`) != 0 {
		t.Fatal("reserved units remained after settle")
	}
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type = 'account.settled'`) != 1 {
		t.Fatal("settle was not idempotent")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO run_telemetry (tenant_id, run_id, sequence, kind, turn_count_delta)
			VALUES ($1::uuid, $2::uuid, 2, 'usage', 1)`, admin.TenantID, runID); err != nil {
			return err
		}
		if err := Settle(t.Context(), tx, otherAgent, runID); err == nil {
			t.Fatal("unassigned agent settled")
		}
		return Settle(t.Context(), tx, runner, runID)
	})
	if err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if scalar(t, admin, `SELECT coalesce(sum(used),0) FROM account_allowance_windows WHERE unit = 'requests'`) != 3 {
		t.Fatal("monotonic settle did not add the new turn")
	}

	live := insertRun(t, admin, runner, profileID)
	// The settled account is still available and under its parallel cap.
	liveRoute := mustRoute(t, mod, runner, token, live, "daemon-a", []Account{account}, map[string]int64{"requests": 4, "tokens": 4})
	if liveRoute.AccountID != account.ID {
		t.Fatal("second run was not routed")
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status = 'running', daemon_id = 'daemon-a', daemon_generation = 'g9' WHERE id = $1::uuid`, live); err != nil {
			return err
		}
		if err := Release(t.Context(), tx, runner, live, "daemon-a", "g9"); err == nil {
			t.Fatal("live run released its reservation")
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status = 'cancelled' WHERE id = $1::uuid`, live); err != nil {
			return err
		}
		if err := Release(t.Context(), tx, runner, live, "daemon-a", "wrong"); err == nil {
			t.Fatal("fence mismatch released")
		}
		return Release(t.Context(), tx, runner, live, "daemon-a", "g9")
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id = $1::uuid AND state = 'released'`, live) != 2 {
		t.Fatal("cancelled run did not release")
	}
	if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id = $1::uuid AND account_id IS NOT NULL`, live) != 1 {
		t.Fatal("terminal run lost its account history")
	}
}

func TestAllowanceProvisionalWithoutMeasuredUnit(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	token := issueKey(t, runner, []string{"account.manage", "run.claim"})
	mod := accountsMod()
	var account Account
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", `{"account_key":"local","harness":"codex","daemon_id":"daemon","label":"Codex"}`, http.StatusCreated, &account)
	ownFixtureAccount(t, admin, &account)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", `{"daemon_id":"daemon","daemon_generation":"g1","available":true}`, http.StatusOK, nil)
	fixtureAlwaysOn(t, mod, admin, account.ID)
	start, end := time.Now().Add(-time.Minute).UTC(), time.Now().Add(time.Hour).UTC()
	for _, unit := range []string{"requests", "cost_micros"} {
		var window Window
		callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(start, end, unit, 100, "unrestricted"), http.StatusCreated, &window)
		if !window.Provisional {
			t.Fatalf("new %s window claimed measured usage", unit)
		}
	}
	runID := insertRun(t, admin, runner, codexProfile(t, admin))
	mustRoute(t, mod, runner, token, runID, "daemon", []Account{account}, map[string]int64{"requests": 1, "cost_micros": 10})
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry (tenant_id, run_id, sequence, kind, turn_count_delta)
			VALUES ($1::uuid, $2::uuid, 1, 'turn', 1)`, admin.TenantID, runID); err != nil {
			return err
		}
		return Settle(t.Context(), tx, runner, runID)
	})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	var accounts []Account
	callStatus(t, mod, &admin, "", http.MethodGet, "/api/agent-accounts", "", http.StatusOK, &accounts)
	if len(accounts) != 1 || len(accounts[0].Windows) != 2 {
		t.Fatalf("account windows: %+v", accounts)
	}
	for _, w := range accounts[0].Windows {
		switch w.Unit {
		case "requests":
			if w.Provisional || w.Used != 1 {
				t.Fatalf("measured request window: %+v", w)
			}
		case "cost_micros":
			if !w.Provisional || w.Used != 0 {
				t.Fatalf("unmeasured cost window: %+v", w)
			}
		}
	}
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `INSERT INTO run_telemetry (tenant_id, run_id, sequence, kind, cost_micros_delta)
			VALUES ($1::uuid, $2::uuid, 2, 'usage', 7)`, admin.TenantID, runID); err != nil {
			return err
		}
		return Settle(t.Context(), tx, runner, runID)
	})
	if err != nil {
		t.Fatalf("late measured cost: %v", err)
	}
	callStatus(t, mod, &admin, "", http.MethodGet, "/api/agent-accounts", "", http.StatusOK, &accounts)
	for _, w := range accounts[0].Windows {
		if w.Unit == "cost_micros" && (w.Provisional || w.Used != 7) {
			t.Fatalf("measured cost window: %+v", w)
		}
	}
}

func TestRankDrainGrantAndStaleProbe(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	claimer := addPrincipal(t, admin.TenantID, "agent", "claimer", nil)
	stranger := addPrincipal(t, admin.TenantID, "agent", "stranger", nil)
	profileID := codexProfile(t, admin)
	token := issueKey(t, runner, []string{"account.manage"})
	mod := accountsMod()
	start := time.Now().Add(-time.Minute).UTC()
	end := time.Now().Add(time.Hour).UTC()

	var low, high Account
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", `{"account_key":"low","harness":"codex","daemon_id":"daemon-low","label":"Low","max_parallel_runs":1}`, http.StatusCreated, &low)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts", `{"account_key":"high","harness":"codex","daemon_id":"daemon-low","label":"High","max_parallel_runs":1}`, http.StatusCreated, &high)
	for _, account := range []Account{low, high} {
		callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe", fmt.Sprintf(`{"daemon_id":%q,"daemon_generation":"g1","available":true}`, account.DaemonID), http.StatusOK, nil)
		callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows", windowBody(start, end, "requests", 100, "unrestricted"), http.StatusCreated, nil)
		fixtureAlwaysOn(t, mod, admin, account.ID)
	}
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows w SET used = 40 FROM agent_accounts a WHERE a.id = w.account_id AND a.account_key = 'high'`)
		return err
	})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	runLow := insertRun(t, admin, runner, profileID)
	got := mustRoute(t, mod, runner, token, runLow, "daemon-low", []Account{low, high}, map[string]int64{"requests": 10})
	if got.AccountID != low.ID {
		t.Fatalf("ranked %s, want low usage %s", got.AccountID, low.ID)
	}
	// The low account is now at its parallel cap. The next run uses the fuller account.
	runNext := insertRun(t, admin, runner, profileID)
	got = mustRoute(t, mod, runner, token, runNext, "daemon-low", []Account{low, high}, map[string]int64{"requests": 10})
	if got.AccountID != high.ID {
		t.Fatalf("parallel occupancy did not move the route, got %s", got.AccountID)
	}
	callStatus(t, mod, &admin, "", http.MethodPatch, "/api/agent-accounts/"+high.ID, `{"state":"draining"}`, http.StatusOK, nil)
	blocked := insertRun(t, admin, runner, profileID)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route", routeBody(t, blocked, "daemon-low", []Account{low, high}, map[string]int64{"requests": 10}), http.StatusConflict, nil)
	// Draining does not drop the run already reserved on that account.
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE state = 'active'`) != 2 {
		t.Fatal("drain released an owned reservation")
	}

	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at = now() - interval '5 minutes' WHERE id = $1::uuid`, low.ID)
		return err
	})
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	// low is full anyway; refresh high by reactivating and probing, then make its probe stale.
	callStatus(t, mod, &admin, "", http.MethodPatch, "/api/agent-accounts/"+high.ID, `{"state":"available"}`, http.StatusOK, nil)
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at = now() - interval '5 minutes'`)
		return err
	})
	if err != nil {
		t.Fatalf("stale all: %v", err)
	}
	// Free a slot by completing the low run so staleness, not occupancy, is the cause.
	err = db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status = 'completed' WHERE id = $1::uuid`, runLow); err != nil {
			return err
		}
		return Release(t.Context(), tx, runner, runLow, "", "")
	})
	if err != nil {
		t.Fatalf("complete low: %v", err)
	}
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route", routeBody(t, blocked, "daemon-low", []Account{low, high}, map[string]int64{"requests": 10}), http.StatusConflict, nil)

	fresh := insertRun(t, admin, runner, profileID)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/"+low.ID+"/probe", `{"daemon_id":"daemon-low","daemon_generation":"g2","available":true}`, http.StatusOK, nil)
	grantClaim(t, admin, claimer, fresh)
	callStatus(t, mod, &stranger, issueKey(t, stranger, []string{"run.claim"}), http.MethodPost, "/api/agent-accounts/route", routeBody(t, fresh, "daemon-low", []Account{low}, map[string]int64{"requests": 1}), http.StatusForbidden, nil)
	callStatus(t, mod, &claimer, issueKey(t, claimer, nil), http.MethodPost, "/api/agent-accounts/route", routeBody(t, fresh, "daemon-low", []Account{low}, map[string]int64{"requests": 1}), http.StatusForbidden, nil)
	var claimerAccount Account
	callStatus(t, mod, &claimer, issueKey(t, claimer, []string{"account.manage"}), http.MethodPost, "/api/agent-accounts", `{"account_key":"claimer","harness":"codex","daemon_id":"claimer-daemon","label":"Claimer"}`, http.StatusCreated, &claimerAccount)
	callStatus(t, mod, &claimer, issueKey(t, claimer, []string{"account.probe"}), http.MethodPost, "/api/agent-accounts/"+claimerAccount.ID+"/probe", `{"daemon_id":"claimer-daemon","daemon_generation":"g1","available":true}`, http.StatusOK, nil)
	callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+claimerAccount.ID+"/windows", windowBody(start, end, "requests", 100, "unrestricted"), http.StatusCreated, nil)
	claimed := mustRoute(t, mod, claimer, issueKey(t, claimer, []string{"run.claim"}), fresh, "claimer-daemon", []Account{claimerAccount}, map[string]int64{"requests": 1})
	if claimed.AccountID != claimerAccount.ID {
		t.Fatalf("grant route picked %s", claimed.AccountID)
	}

	other := makePrincipal(t, "beta", "person", "Bea", []string{"admin"})
	var listed []Account
	callStatus(t, mod, &other, "", http.MethodGet, "/api/agent-accounts", "", http.StatusOK, &listed)
	if len(listed) != 0 {
		t.Fatal("accounts leaked across tenants")
	}
}

func TestRouteOnlyClaimsLocalDaemonEnrollment(t *testing.T) {
	reset(t)
	admin := makePrincipal(t, "alpha", "person", "Ada", []string{"admin"})
	runner := addPrincipal(t, admin.TenantID, "agent", "runner", nil)
	otherAgent := addPrincipal(t, admin.TenantID, "agent", "other", nil)
	profileID := codexProfile(t, admin)
	token := issueKey(t, runner, []string{"account.manage"})
	mod := accountsMod()
	start := time.Now().Add(-time.Minute).UTC()
	end := time.Now().Add(time.Hour).UTC()

	register := func(p tenant.Principal, key, daemon string) Account {
		t.Helper()
		var account Account
		callStatus(t, mod, &p, issueKey(t, p, []string{"account.manage"}), http.MethodPost, "/api/agent-accounts",
			fmt.Sprintf(`{"account_key":%q,"harness":"codex","daemon_id":%q,"label":"Codex"}`, key, daemon), http.StatusCreated, &account)
		callStatus(t, mod, &p, issueKey(t, p, []string{"account.probe"}), http.MethodPost, "/api/agent-accounts/"+account.ID+"/probe",
			fmt.Sprintf(`{"daemon_id":%q,"daemon_generation":"g1","available":true}`, daemon), http.StatusOK, nil)
		callStatus(t, mod, &admin, "", http.MethodPost, "/api/agent-accounts/"+account.ID+"/windows",
			windowBody(start, end, "requests", 100, "unrestricted"), http.StatusCreated, nil)
		fixtureAlwaysOn(t, mod, admin, account.ID)
		return account
	}
	local := register(runner, "local", "daemon-a")
	foreignDaemon := register(runner, "foreign-daemon", "daemon-b")
	unenrolled := register(runner, "unenrolled", "daemon-a")
	foreignOwner := register(otherAgent, "foreign-owner", "daemon-a")

	// All three competing accounts have lower projected usage, but none is
	// enrolled by this daemon. The caller's selected account must still win.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, admin.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE account_allowance_windows SET used = 50 WHERE account_id = $1::uuid`, local.ID)
		return err
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	run := insertRun(t, admin, runner, profileID)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route",
		routeBody(t, run, "daemon-a", nil, map[string]int64{"requests": 1}), http.StatusBadRequest, nil)
	selected := mustRoute(t, mod, runner, token, run, "daemon-a", []Account{local}, map[string]int64{"requests": 1})
	if selected.AccountID != local.ID {
		t.Fatalf("route picked %s, want local %s", selected.AccountID, local.ID)
	}
	for _, wrong := range []struct {
		daemon   string
		accounts []Account
	}{
		{"daemon-b", []Account{foreignDaemon}},
		{"daemon-a", []Account{unenrolled}},
	} {
		callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route",
			routeBody(t, run, wrong.daemon, wrong.accounts, map[string]int64{"requests": 1}), http.StatusConflict, nil)
	}
	if scalar(t, admin, `SELECT count(*) FROM events WHERE type = 'account.reserved'`) != 1 {
		t.Fatal("rejected replay wrote an event")
	}

	// The local account is occupied. Neither a different daemon nor another
	// account on this daemon may be used as an implicit fallback.
	blocked := insertRun(t, admin, runner, profileID)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route",
		routeBody(t, blocked, "daemon-a", []Account{local}, map[string]int64{"requests": 1}), http.StatusConflict, nil)
	callStatus(t, mod, &runner, token, http.MethodPost, "/api/agent-accounts/route",
		routeBody(t, blocked, "daemon-a", []Account{foreignOwner}, map[string]int64{"requests": 1}), http.StatusConflict, nil)
	if scalar(t, admin, `SELECT count(*) FROM agent_runs WHERE id = $1::uuid AND account_id IS NOT NULL`, blocked) != 0 {
		t.Fatal("failed route bound the queued run")
	}
	if scalar(t, admin, `SELECT count(*) FROM account_reservations WHERE run_id = $1::uuid`, blocked) != 0 {
		t.Fatal("failed route reserved allowance")
	}
	if got := mustRoute(t, mod, runner, token, blocked, "daemon-b", []Account{foreignDaemon}, map[string]int64{"requests": 1}); got.AccountID != foreignDaemon.ID {
		t.Fatalf("second daemon route picked %s", got.AccountID)
	}

	otherTenant := makePrincipal(t, "beta", "person", "Bea", []string{"admin"})
	otherRunner := addPrincipal(t, otherTenant.TenantID, "agent", "beta-runner", nil)
	otherProfile := codexProfile(t, otherTenant)
	otherRun := insertRun(t, otherTenant, otherRunner, otherProfile)
	callStatus(t, mod, &otherRunner, issueKey(t, otherRunner, nil), http.MethodPost, "/api/agent-accounts/route",
		routeBody(t, otherRun, "daemon-a", []Account{unenrolled}, map[string]int64{"requests": 1}), http.StatusConflict, nil)
}

func grantClaim(t *testing.T, person, agent tenant.Principal, runID string) {
	t.Helper()
	err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		var requestID string
		var expires time.Time
		if err := tx.QueryRow(t.Context(), `
			INSERT INTO approval_requests
				(tenant_id, proposed_by_principal_id, agent_principal_id, run_id, scope, resource_kind, resource_id, rationale, expires_at)
			VALUES ($1::uuid, $2::uuid, $2::uuid, $3::uuid, 'run.claim', 'run', $3::uuid, 'dispatch', now() + interval '1 day')
			RETURNING id::text, expires_at`, person.TenantID, agent.ID, runID).Scan(&requestID, &expires); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `
			INSERT INTO approval_decisions (tenant_id, request_id, decided_by_principal_id, decision)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'approved')`, person.TenantID, requestID, person.ID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `
			INSERT INTO agent_permission_grants
				(tenant_id, approval_request_id, agent_principal_id, scope, resource_kind, resource_id, valid_until)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 'run.claim', 'run', $4::uuid, $5)`,
			person.TenantID, requestID, agent.ID, runID, expires)
		return err
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
}
