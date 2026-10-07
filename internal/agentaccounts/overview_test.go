// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: an all-account read leaks private quota/calendar data, drops independent
// windows, grants agents a new default authority, or crosses tenant boundaries.
func TestOverviewValuesMaskingExplicitScopeAndTenantIsolation(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "overview", "person", "Owner", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runtime", nil)
	otherPerson := addPrincipal(t, person.TenantID, "person", "Other owner", nil)
	foreign := makePrincipal(t, "overview-foreign", "person", "Foreign", []string{"admin"})
	profile := codexProfile(t, person)
	token := issueKey(t, runner, []string{"account.manage", "account.probe"})
	mod := accountsMod()
	now := time.Now().UTC().Add(-time.Second).Truncate(time.Microsecond)
	accounts := []Account{}
	for i, label := range []string{"First", "Private"} {
		var a Account
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts", encoded(t, map[string]any{"account_key": label, "harness": "codex", "daemon_id": "daemon-a", "label": label}), 201, &a)
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/probe", `{"daemon_id":"daemon-a","daemon_generation":"g1","available":true}`, 200, nil)
		at := now
		if i == 1 {
			at = now.Add(-time.Hour)
		}
		readings := []capacity.Reading{
			{WindowKind: "5h", WindowMinutes: 300, UsedPercent: float64(10 + i), ResetsAt: now.Add(time.Hour), ReadAt: at, Source: "harness"},
			{WindowKind: "weekly", WindowMinutes: 10080, UsedPercent: float64(20 + i), ResetsAt: now.Add(time.Duration(i+24) * time.Hour), ReadAt: at, Source: "agentd"},
		}
		callStatus(t, mod, &runner, token, "POST", "/api/agent-accounts/"+a.ID+"/readings", encoded(t, readingsWrite{readings}), 204, nil)
		owner := person.ID
		if i == 1 {
			owner = otherPerson.ID
		}
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=now() WHERE id=$1`, a.ID, owner); err != nil {
				return err
			}
			if i == 0 {
				learning := capacityLearning{Windows: []capacity.LearnedWindow{{Kind: "weekly", Minutes: 10080,
					Runs: []capacity.RunSample{{At: now, Profile: profile, Percent: 10, Tokens: 2_000_000}, {At: now, Profile: profile, Percent: 10, Tokens: 2_000_000}, {At: now, Profile: profile, Percent: 10, Tokens: 2_000_000}},
					Burn: []capacity.UseSample{{At: now, Percent: 5, Hours: 1}},
				}}}
				return saveLearning(t.Context(), tx, a.ID, learning)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, a)
	}
	var page overviewPage
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview", "", 200, &page)
	if len(page.Accounts) != 2 || page.HasMore || page.AsOf.IsZero() {
		t.Fatalf("incorrect complete overview: %+v", page)
	}
	for _, a := range page.Accounts {
		if a.AccountID == accounts[1].ID {
			if !a.DetailsRedacted || len(a.Windows) != 0 || a.Schedule != nil || a.Routing != nil || a.QuotaPool != "" {
				t.Fatalf("other owner's usage was exposed: %+v", a)
			}
		} else {
			if a.Provider != "openai" || a.OwnerPersonID == nil || *a.OwnerPersonID != person.ID || len(a.Windows) != 2 || a.DetailsRedacted {
				t.Fatalf("own account values lost: %+v", a)
			}
			for _, w := range a.Windows {
				want := 10.0
				if w.Kind == "weekly" {
					want = 20
				}
				if w.Kind == "weekly" && (w.Learned == nil || w.Learned.LimitTokens == nil || *w.Learned.LimitTokens != 20_000_000 || w.Learned.Burn != 5 || w.Learned.Samples != 3) {
					t.Fatalf("learned capacity or burn lost: %+v", w.Learned)
				}
				if w.UsedPercent == nil || *w.UsedPercent != want || w.Freshness != "fresh" || w.Source != "vendor_reported" || w.ReadAt == nil || !w.ReadAt.Equal(now) || w.ResetsAt.IsZero() || w.Pacing == nil || w.Learned == nil {
					t.Fatalf("incorrect window: %+v", w)
				}
			}
		}
	}
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview?limit=1", "", 200, &page)
	if !page.HasMore || len(page.Accounts) != 1 || page.NextCursor != page.Accounts[0].AccountID {
		t.Fatal("bounded page lost its next cursor")
	}
	first := page.Accounts[0].AccountID
	var last overviewPage
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview?limit=1&after="+page.NextCursor, "", 200, &last)
	if len(last.Accounts) != 1 || last.Accounts[0].AccountID == first || last.HasMore || last.NextCursor != "" {
		t.Fatal("keyset pagination dropped or repeated an account")
	}
	callStatus(t, mod, &foreign, "", "GET", "/api/agent-accounts/overview", "", 200, &page)
	if len(page.Accounts) != 0 {
		t.Fatal("overview crossed tenant isolation")
	}
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview?limit=101", "", 400, nil)
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview?after=not-a-uuid", "", 400, nil)
	callStatus(t, mod, &runner, token, "GET", "/api/agent-accounts/overview", "", 403, nil)
	// Even an Admin role and explicit key scope do not create a default agent
	// grant. A live custom role is required as well as the key ceiling.
	dbtest.BindRole(t, testDB, person.TenantID, runner.ID, "admin")
	overviewKey := issueKey(t, runner, []string{"account.overview.read"})
	callStatus(t, mod, &runner, overviewKey, "GET", "/api/agent-accounts/overview", "", 403, nil)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'overview_reader','Overview reader') RETURNING id::text`, person.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'account.overview.read')`, person.TenantID, role); err != nil {
			return err
		}
		var bound string
		return tx.QueryRow(t.Context(), `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1 AND scope_type='workspace' RETURNING principal_id::text`, runner.ID, role).Scan(&bound)
	}); err != nil {
		t.Fatal(err)
	}
	before := scalar(t, person, `SELECT count(*) FROM events`)
	status, raw := call(t, mod, &runner, overviewKey, "GET", "/api/agent-accounts/overview", "")
	if status != 200 {
		t.Fatalf("explicitly granted agent denied: %d %s", status, raw)
	}
	if scalar(t, person, `SELECT count(*) FROM events`) != before {
		t.Fatal("overview mutated the event log")
	}
	var agentPage overviewPage
	if err := json.Unmarshal(raw, &agentPage); err != nil {
		t.Fatal(err)
	}
	if len(agentPage.Accounts) != 2 {
		t.Fatal("explicit overview scope lost enrolled accounts")
	}
	for _, a := range agentPage.Accounts {
		if a.AccountID != accounts[1].ID {
			continue
		}
		if len(a.Windows) != 2 {
			t.Fatal("agent lost its separate stale windows")
		}
		for _, w := range a.Windows {
			want := "stale"
			if w.Kind == "weekly" {
				want = "aging"
			}
			if w.Freshness != want {
				t.Fatalf("freshness was refreshed by the overview: %s, want %s", w.Freshness, want)
			}
		}
	}
	// A confirmed shared pool cannot disclose its private member through the
	// owner's other account. Sharing the peer explicitly releases the values.
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET quota_fingerprint=repeat('ab',32),quota_pool_fingerprint=repeat('ab',32) WHERE id=ANY($1::uuid[])`, []string{accounts[0].ID, accounts[1].ID})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview", "", 200, &page)
	for _, a := range page.Accounts {
		if !a.DetailsRedacted || len(a.Windows) != 0 || a.QuotaPool != "" {
			t.Fatal("shared pool disclosed its private peer")
		}
	}
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET share_usage=true WHERE id=$1`, accounts[1].ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview", "", 200, &page)
	for _, a := range page.Accounts {
		if a.DetailsRedacted || len(a.Windows) != 2 || a.QuotaPool == "" {
			t.Fatal("explicit sharing did not expose the confirmed pool's values")
		}
	}
	// New daemon fact-only windows retain their measured precision and age,
	// including stale observations that cannot grant routing capacity.
	readAt, resetsAt, percent := now.Add(-time.Hour), now.Add(3*time.Hour), 18.125
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		resource, err := localReadinessResource(t.Context(), tx, accounts[0])
		if err != nil {
			return err
		}
		return storeReadinessFact(tenant.WithPrincipal(t.Context(), runner), tx, accounts[0], ReadinessFactWrite{ResourceID: resource, WindowKey: "daily", Source: "harness", ObservedAt: now, ReadingAt: &readAt, ResetsAt: &resetsAt, UsedPercent: &percent, CreditState: "unknown"}, now)
	}); err != nil {
		t.Fatal(err)
	}
	callStatus(t, mod, &person, "", "GET", "/api/agent-accounts/overview", "", 200, &page)
	found := false
	for _, a := range page.Accounts {
		if a.AccountID != accounts[0].ID {
			continue
		}
		for _, w := range a.Windows {
			if w.Kind == "fact" && strings.HasSuffix(w.Bucket, ":daily") {
				found = true
				if w.UsedPercent == nil || *w.UsedPercent != percent || w.Freshness != "stale" || w.ReadAt == nil || !w.ReadAt.Equal(readAt) || !w.ResetsAt.Equal(resetsAt) || w.Source != "vendor_reported" {
					t.Fatalf("stale fact-only window lost precision or age: %+v", w)
				}
			}
		}
	}
	if !found {
		t.Fatal("overview dropped a known stale fact-only window")
	}

	for _, forbidden := range []string{"account_key", "config_home", "identity", "registered_by_principal_id", "secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("values-only overview leaked %s", forbidden)
		}
	}
	permission, ok := authz.Lookup("account.overview.read")
	if !ok || !permission.AgentGrantable {
		t.Fatal("explicit overview scope is unavailable for a custom role")
	}
}

// Risk: a vendor stop loses a spare enrolled account because its registration
// came from another agent. Keep daemon, profile, person-pin and quota fences.
func TestVendorStopAdviceUsesOtherRegistrarOnSameDaemon(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "all-daemon-accounts", "person", "Owner", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	registrar := addPrincipal(t, person.TenantID, "agent", "registrar", nil)
	profile := codexProfile(t, person)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	accounts := []Account{}
	for i, agent := range []struct{ id, daemon, label string }{{runner.ID, "daemon-a", "Stopped"}, {registrar.ID, "daemon-a", "Spare"}, {registrar.ID, "daemon-b", "OtherComputer"}} {
		var a Account
		if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,'codex',$3,$4,$2,$5,true,'g1',$6) RETURNING id::text`, person.TenantID, agent.label, agent.daemon, agent.id, now, person.ID).Scan(&a.ID); err != nil {
				return err
			}
			used := 10
			if i == 0 {
				used = 100
			}
			_, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model) VALUES($1,$2,$3,$4,'requests',100,$5,'unrestricted')`, person.TenantID, a.ID, now.Add(-time.Hour), now.Add(time.Hour), used)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, a)
	}
	// The spare was registered by another agent. A connected enrollment on
	// this computer is what lets the run's agent claim it.
	enrollComputerAccount(t, person, runner, accounts[1].ID, "daemon-a", profile, issueKey(t, runner, []string{"account.probe"}))
	run := insertRun(t, person, runner, profile)
	ctx := context.WithValue(t.Context(), clockKey{}, now)
	if err := db.InTenant(ctx, appPool, person.TenantID, func(tx pgx.Tx) error {
		next, err := NextForRun(ctx, tx, run, "daemon-a", "", accounts[0].ID, now)
		if err != nil {
			return err
		}
		if len(next.Accounts) != 1 || next.Accounts[0].AccountID != accounts[1].ID || next.Accounts[0].AvailableSlots != 1 {
			t.Fatalf("vendor-stop advice lost the spare: %+v", next)
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_runs SET requested_account_id=$2 WHERE id=$1`, run, accounts[0].ID); err != nil {
			return err
		}
		next, err = NextForRun(ctx, tx, run, "daemon-a", "", accounts[0].ID, now)
		if err == nil && len(next.Accounts) != 0 {
			t.Fatal("handoff bypassed the person's original pin")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: daemon_id is a caller-chosen string visible to other principals.
// Vendor-stop advice must not pin a retry the computer cannot claim.
func TestVendorStopAdviceRejectsUnenrolledDaemonSquatter(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "squatter-daemon", "person", "Owner", []string{"admin"})
	runner := addPrincipal(t, person.TenantID, "agent", "runner", nil)
	squatter := addPrincipal(t, person.TenantID, "agent", "squatter", nil)
	profile := codexProfile(t, person)
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var stopped, intruder string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		for _, agent := range []struct {
			id, label string
			used      int
		}{{runner.ID, "Stopped", 100}, {squatter.ID, "Squatter", 10}} {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner) VALUES($1,$2,'codex','daemon-a',$3,$2,$4,true,'g1',$5) RETURNING id::text`, person.TenantID, agent.label, agent.id, now, person.ID).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model) VALUES($1,$2,$3,$4,'requests',100,$5,'unrestricted')`, person.TenantID, id, now.Add(-time.Hour), now.Add(time.Hour), agent.used); err != nil {
				return err
			}
			if agent.label == "Stopped" {
				stopped = id
			} else {
				intruder = id
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := insertRun(t, person, runner, profile)
	ctx := context.WithValue(t.Context(), clockKey{}, now)
	if err := db.InTenant(ctx, appPool, person.TenantID, func(tx pgx.Tx) error {
		next, err := NextForRun(ctx, tx, run, "daemon-a", "", stopped, now)
		if err != nil {
			return err
		}
		for _, choice := range next.Accounts {
			if choice.AccountID == intruder {
				t.Fatalf("unenrolled same-daemon account became a vendor retry: %+v", next)
			}
		}
		if len(next.Accounts) != 0 {
			t.Fatalf("vendor-stop advice kept an unbound account: %+v", next)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Risk: a redacted overview row discloses quota headroom through routable,
// or by omitting wait when slots remain. Private rows keep one constant shape.
func TestRedactedOverviewHidesRoutableHeadroom(t *testing.T) {
	reset(t)
	person := makePrincipal(t, "overview-routable", "person", "Owner", []string{"admin"})
	other := addPrincipal(t, person.TenantID, "person", "Other", nil)
	runner := addPrincipal(t, person.TenantID, "agent", "runtime", nil)
	codexProfile(t, person)
	// A manual allowance waits for the person's default schedule (weekdays,
	// 08:00-22:00 UTC), so the wall clock made this fail at night and on weekends.
	// The injected clock is a Wednesday noon, and the claim no longer depends on
	// when the suite runs.
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	var own, private string
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, person.TenantID, func(tx pgx.Tx) error {
		for _, row := range []struct{ label, owner string }{{"Own", person.ID}, {"Private", other.ID}} {
			var id string
			if err := tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label,last_probe_at,last_probe_ok,last_daemon_generation,capacity_owner,owner_person_id,linked_at) VALUES($1,$2,'codex','daemon-a',$3,$2,$4,true,'g1',$5,$6,$4) RETURNING id::text`, person.TenantID, row.label, runner.ID, now, person.ID, row.owner).Scan(&id); err != nil {
				return err
			}
			if _, err := tx.Exec(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,used,pace_model) VALUES($1,$2,$3,$4,'requests',100,10,'unrestricted')`, person.TenantID, id, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
				return err
			}
			if row.label == "Own" {
				own = id
			} else {
				private = id
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var page overviewPage
	rec := evidenceHTTP(accountsMod(), t.Context(), person, "", "GET", "/api/agent-accounts/overview", "", now)
	if rec.Code != 200 {
		t.Fatalf("overview status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("json: %v body %s", err, rec.Body.String())
	}
	seen := map[string]overviewAccount{}
	for _, a := range page.Accounts {
		seen[a.AccountID] = a
	}
	shared, privateRow := seen[own], seen[private]
	if shared.AccountID == "" || shared.DetailsRedacted || !shared.Routable || shared.Wait != nil {
		t.Fatalf("owned account with open slots was not routable: %+v", shared)
	}
	if privateRow.AccountID == "" || !privateRow.DetailsRedacted || privateRow.Routable || privateRow.Wait == nil || privateRow.Wait.Code != "state" || privateRow.Wait.Until != nil || len(privateRow.Windows) != 0 || privateRow.Routing != nil {
		t.Fatalf("redacted overview disclosed headroom: %+v", privateRow)
	}
}

func enrollComputerAccount(t *testing.T, owner, host tenant.Principal, accountID, daemonID, profile, token string) {
	t.Helper()
	parts := strings.Split(token, "_")
	if len(parts) < 2 || parts[1] == "" {
		t.Fatal("enrollment fixture needs an agent key")
	}
	hash := strings.Repeat("ab", 32)
	if err := db.InTenant(dbtest.Seed(t.Context()), appPool, owner.TenantID, func(tx pgx.Tx) error {
		var request, computer string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,gen_random_uuid(),'864864864',$2,$2,$2,'{}','daemon-spare','redeemed',$3) RETURNING id::text`, owner.TenantID, hash, owner.ID).Scan(&request); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,state)
 SELECT $1,gen_random_uuid(),$2,$3,id,$4,$5,'connected' FROM agent_keys WHERE prefix=$6 RETURNING id::text`, owner.TenantID, request, host.ID, daemonID, hash, parts[1]).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,state,verification_expires_at,ongoing_approved_at)
 VALUES($1,$2,$3,$4,$5,'connected',now()+interval '1 day',now())`, owner.TenantID, accountID, computer, request, profile)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
