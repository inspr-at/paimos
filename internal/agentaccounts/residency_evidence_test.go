// SPDX-License-Identifier: AGPL-3.0-only
package agentaccounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/httpapi"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

func evidenceAt(profile string, now time.Time) ResidencyEvidence {
	local, training, retention := false, true, 30
	return ResidencyEvidence{
		ProfileIDs: []string{profile}, InferenceCountries: []string{"AT", "DE"},
		StorageCountries: []string{"FR"}, LogCountries: []string{"IE"},
		LocalExecution: &local, TrainingOptOut: &training, RetentionDays: &retention,
		VerifiedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ProofRef: "document:residency-proof-v1",
	}
}

func TestStoredResidencyClassification(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	profile := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for _, tc := range []struct {
		name, want string
		change     func(*ResidencyEvidence)
	}{
		{"valid EU", "eu", func(*ResidencyEvidence) {}},
		{"expired at boundary", "any", func(e *ResidencyEvidence) { e.ExpiresAt = now }},
		{"expired", "any", func(e *ResidencyEvidence) { e.ExpiresAt = now.Add(-time.Minute) }},
		{"future verification", "any", func(e *ResidencyEvidence) { e.VerifiedAt = now.Add(time.Minute) }},
		{"missing proof", "any", func(e *ResidencyEvidence) { e.ProofRef = "" }},
		{"wrong inference", "any", func(e *ResidencyEvidence) { e.InferenceCountries = []string{"US"} }},
		{"mixed storage", "any", func(e *ResidencyEvidence) { e.StorageCountries = []string{"AT", "US"} }},
		{"wrong logs", "any", func(e *ResidencyEvidence) { e.LogCountries = []string{"GB"} }},
		{"EEA is not EU", "any", func(e *ResidencyEvidence) { e.LogCountries = []string{"NO"} }},
		{"unknown country", "any", func(e *ResidencyEvidence) { e.LogCountries = []string{"ZZ"} }},
		{"empty storage", "any", func(e *ResidencyEvidence) { e.StorageCountries = []string{} }},
		{"missing countries", "any", func(e *ResidencyEvidence) { e.LogCountries = nil }},
		{"uncovered profile", "any", func(e *ResidencyEvidence) { e.ProfileIDs = []string{"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"} }},
		{"local execution", "local", func(e *ResidencyEvidence) {
			*e.LocalExecution = true
		}},
		{"expired local", "any", func(e *ResidencyEvidence) { *e.LocalExecution = true; e.ExpiresAt = now }},
		{"not explicitly local", "any", func(e *ResidencyEvidence) { e.LocalExecution = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := evidenceAt(profile, now)
			tc.change(&e)
			got, err := ResidencyClass(t.Context(), nil, Account{ID: "test", residencyEvidence: &e}, profile, now)
			if err != nil || got != tc.want {
				t.Fatalf("class=%s err=%v; want %s", got, err, tc.want)
			}
		})
	}
}

func callEvidence(t *testing.T, mod httpapi.Module, p tenant.Principal, token, method, id string, now time.Time, body any, want int) residencyEvidenceRecord {
	t.Helper()
	r := httptest.NewRequest(method, "/api/agent-accounts/"+id+"/residency-evidence", strings.NewReader(encoded(t, body)))
	r = r.WithContext(context.WithValue(tenant.WithPrincipal(r.Context(), p), clockKey{}, now))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	mux := http.NewServeMux()
	mod.Mount(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("evidence %s: status %d want %d: %s", method, w.Code, want, w.Body.String())
	}
	var out residencyEvidenceRecord
	if want == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func ownEvidenceAccount(t *testing.T, owner tenant.Principal, a Account) {
	t.Helper()
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET owner_person_id=$2,linked_at=now(),link_revision=link_revision+1 WHERE id=$1`, a.ID, owner.ID)
		return err
	})
}

func bindEvidenceHost(t *testing.T, owner, host tenant.Principal, a Account, profile, token string) {
	t.Helper()
	parts := strings.Split(token, "_")
	seed(t, owner, func(tx pgx.Tx) error {
		var request, computer string
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,gen_random_uuid(),'123456789',$2,$2,$2,'{}','evidence-fixture','redeemed',$3) RETURNING id::text`, owner.TenantID, strings.Repeat("ab", 32), owner.ID).Scan(&request); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash)
 SELECT $1,gen_random_uuid(),$2,$3,id,$4,$5 FROM agent_keys WHERE prefix=$6 RETURNING id::text`, owner.TenantID, request, host.ID, a.DaemonID, strings.Repeat("ab", 32), parts[1]).Scan(&computer); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,verification_expires_at,ongoing_approved_at)
 VALUES($1,$2,$3,$4,$5,now()+interval '1 day',now())`, owner.TenantID, a.ID, computer, request, profile)
		return err
	})
}

func evidenceEventCount(t *testing.T, p tenant.Principal) int {
	t.Helper()
	var count int
	seed(t, p, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM events WHERE type='account.residency_evidence_updated'`).Scan(&count)
	})
	return count
}

func TestResidencyEvidenceOwnershipAndTenantIsolation(t *testing.T) {
	reset(t)
	owner := makePrincipal(t, "evidence", "person", "Owner", []string{"admin"})
	host := addPrincipal(t, owner.TenantID, "agent", "Host", []string{"admin"})
	otherHost := addPrincipal(t, owner.TenantID, "agent", "Other host", []string{"admin"})
	// Legacy fixture binding intentionally applies only to people. Give both
	// hosts real grants so ownership refusals cannot pass for missing roles.
	dbtest.BindRole(t, testDB, owner.TenantID, host.ID, "admin")
	dbtest.BindRole(t, testDB, owner.TenantID, otherHost.ID, "admin")
	admin := addPrincipal(t, owner.TenantID, "person", "Unrelated admin", []string{"admin"})
	profile := codexProfile(t, owner)
	token := issueKey(t, host, []string{"account.manage", "account.probe", "account.read"})
	otherToken := issueKey(t, otherHost, []string{"account.manage", "account.probe", "account.read"})
	mod := accountsMod()
	a := groupAccount(t, mod, owner, host, token, "evidence", "daemon-evidence", "Evidence", "test")
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	e := evidenceAt(profile, now)
	callEvidence(t, mod, owner, "", "GET", a.ID, now, nil, 404)
	callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 403)   // unlinked, even for an admin
	callEvidence(t, mod, host, token, "PUT", a.ID, now, e, 403) // unpaired registering key
	ownEvidenceAccount(t, owner, a)
	out := callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 200)
	if !out.BindingCurrent || out.AccountID != a.ID || out.RecordedBy != owner.ID || !out.RecordedAt.Equal(now) || *out.Evidence.RetentionDays != 30 || !*out.Evidence.TrainingOptOut {
		t.Fatalf("saved record lost fields: %+v", out)
	}
	bindEvidenceHost(t, owner, host, a, profile, token)
	callEvidence(t, mod, host, token, "PUT", a.ID, now, e, 200)
	callEvidence(t, mod, host, token, "GET", a.ID, now, nil, 200)
	if got := evidenceEventCount(t, owner); got != 2 {
		t.Fatalf("events=%d want 2", got)
	}
	callEvidence(t, mod, admin, "", "PUT", a.ID, now, e, 403)
	otherHost.KeyCreatorID = owner.ID
	callEvidence(t, mod, otherHost, otherToken, "PUT", a.ID, now, e, 403)
	callEvidence(t, mod, otherHost, otherToken, "GET", a.ID, now, nil, 403)
	// Even another key for the same principal is not the bound computer key.
	otherKey := issueKey(t, host, []string{"account.probe"})
	callEvidence(t, mod, host, otherKey, "PUT", a.ID, now, e, 403)
	readKey := issueKey(t, host, []string{"account.read"})
	callEvidence(t, mod, host, readKey, "PUT", a.ID, now, e, 403)
	foreign := makePrincipal(t, "foreign-evidence", "person", "Foreign", []string{"admin"})
	callEvidence(t, mod, foreign, "", "PUT", a.ID, now, e, 404)
	callEvidence(t, mod, foreign, "", "GET", a.ID, now, nil, 404)
	seed(t, foreign, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_account_residency_evidence`).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			t.Fatal("RLS exposed foreign evidence")
		}
		tag, err := tx.Exec(t.Context(), `UPDATE agent_account_residency_evidence SET recorded_at=now() WHERE account_id=$1`, a.ID)
		if err == nil && tag.RowsAffected() != 0 {
			t.Fatal("RLS allowed foreign update")
		}
		return err
	})
	err := db.InTenant(t.Context(), appPool, foreign.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO agent_account_residency_evidence(tenant_id,account_id,evidence,binding,recorded_by,recorded_at) VALUES($1,$2,'{}','{}',$3,now())`, owner.TenantID, a.ID, owner.ID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("foreign insert must fail RLS: %v", err)
	}
	// The bound key is checked live even with a previously authenticated principal.
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_keys SET revoked_at=now() WHERE prefix=$1`, strings.Split(token, "_")[1])
		return err
	})
	callEvidence(t, mod, host, token, "PUT", a.ID, now, e, 403)
	if got := evidenceEventCount(t, owner); got != 2 {
		t.Fatalf("refused writes emitted events: %d", got)
	}
}

func TestResidencyEvidenceFenceUsesStoredEvidenceAndInjectedClock(t *testing.T) {
	reset(t)
	owner, host, profile, token, mod := groupFixture(t)
	a := groupAccount(t, mod, owner, host, token, "eu", "daemon-eu", "EU", "test")
	ownEvidenceAccount(t, owner, a)
	_, _, runID := insertTicketRun(t, owner, host, profile)
	now := time.Now().UTC().Truncate(time.Second)
	e := evidenceAt(profile, now)
	// Keep the probe and allowance fresh across the deterministic expiry step.
	e.ExpiresAt = now.Add(time.Second)
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET residency='eu' WHERE id=$1`, runID)
		return err
	})
	check := func(at time.Time, wantKept int, wantEmpty bool, wantWait string) {
		t.Helper()
		seed(t, owner, func(tx pgx.Tx) error {
			ctx := context.WithValue(t.Context(), clockKey{}, at)
			accounts, err := listAccounts(ctx, tx)
			if err != nil {
				return err
			}
			run, err := loadWaitRun(ctx, tx, runID)
			if err != nil {
				return err
			}
			kept, empty, err := narrowCandidates(ctx, tx, run, "codex", accounts)
			if err != nil {
				return err
			}
			if len(kept) != wantKept || empty != wantEmpty {
				t.Fatalf("fence kept=%d empty=%v; want %d %v", len(kept), empty, wantKept, wantEmpty)
			}
			wait, err := WaitForRun(ctx, tx, runID)
			if err != nil {
				return err
			}
			if wantWait == "" && wait != nil || wantWait != "" && (wait == nil || wait.Code != wantWait) {
				t.Fatalf("wait=%+v want %q", wait, wantWait)
			}
			return nil
		})
	}
	check(now, 0, true, "residency") // missing
	callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 200)
	check(now, 1, false, "")
	check(e.ExpiresAt, 0, true, "residency") // boundary, no sleeps
	e.ExpiresAt = now.Add(time.Hour)
	e.LogCountries = []string{"US"}
	callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 200)
	check(now, 0, true, "residency")
	*e.LocalExecution = true
	e.LogCountries = []string{"IE"}
	callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 200)
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET residency='local' WHERE id=$1`, runID)
		return err
	})
	check(now, 1, false, "")
	// A surviving route emptied by a later pin is offline, not residency.
	other := groupAccount(t, mod, owner, host, token, "other", "daemon-eu", "Other", "test")
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET requested_account_id=$2 WHERE id=$1`, runID, other.ID)
		return err
	})
	check(now, 0, false, "offline")
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET requested_account_id=NULL WHERE id=$1`, runID)
		return err
	})
	// Changed host binding makes even valid evidence nonqualifying.
	seed(t, owner, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET daemon_id='new-host' WHERE id=$1`, a.ID)
		return err
	})
	check(now, 0, true, "residency")
	out := callEvidence(t, mod, owner, "", "GET", a.ID, now, nil, 200)
	if out.BindingCurrent {
		t.Fatal("changed binding retained validity")
	}
}

func TestResidencyEvidenceRejectsInvalidBoundedInputs(t *testing.T) {
	reset(t)
	owner, host, profile, token, mod := groupFixture(t)
	a := groupAccount(t, mod, owner, host, token, "invalid", "daemon", "Invalid", "test")
	ownEvidenceAccount(t, owner, a)
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		change func(*ResidencyEvidence)
	}{
		{"future", func(e *ResidencyEvidence) { e.VerifiedAt = now.Add(time.Minute) }},
		{"backwards", func(e *ResidencyEvidence) { e.ExpiresAt = e.VerifiedAt }},
		{"overlong validity", func(e *ResidencyEvidence) { e.ExpiresAt = e.VerifiedAt.Add(401 * 24 * time.Hour) }},
		{"missing proof", func(e *ResidencyEvidence) { e.ProofRef = " " }},
		{"large proof", func(e *ResidencyEvidence) { e.ProofRef = strings.Repeat("x", 1025) }},
		{"missing set", func(e *ResidencyEvidence) { e.LogCountries = nil }},
		{"duplicate country", func(e *ResidencyEvidence) { e.LogCountries = []string{"AT", "AT"} }},
		{"lowercase country", func(e *ResidencyEvidence) { e.LogCountries = []string{"at"} }},
		{"duplicate profile", func(e *ResidencyEvidence) { e.ProfileIDs = []string{profile, profile} }},
		{"missing local", func(e *ResidencyEvidence) { e.LocalExecution = nil }},
		{"unknown profile", func(e *ResidencyEvidence) { e.ProfileIDs = []string{"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"} }},
		{"too many profiles", func(e *ResidencyEvidence) { e.ProfileIDs = make([]string, 257) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := evidenceAt(profile, now)
			tc.change(&e)
			callEvidence(t, mod, owner, "", "PUT", a.ID, now, e, 400)
		})
	}
	if got := evidenceEventCount(t, owner); got != 0 {
		t.Fatalf("invalid writes emitted %d events", got)
	}
	// Body limit applies before decoding even when the JSON otherwise fits.
	status, _ := call(t, mod, &owner, "", "PUT", "/api/agent-accounts/"+a.ID+"/residency-evidence", fmt.Sprintf(`{"proof_ref":%q}`, strings.Repeat("x", 32769)))
	if status != 400 && status != 413 {
		t.Fatalf("oversized body status=%d", status)
	}
}

func TestResidencyEvidenceRechecksAfterConcurrentAccessChange(t *testing.T) {
	for _, change := range []string{"permission", "ownership"} {
		t.Run(change, func(t *testing.T) {
			reset(t)
			owner, host, profile, token, mod := groupFixture(t)
			a := groupAccount(t, mod, owner, host, token, "race", "daemon", "Race", "test")
			ownEvidenceAccount(t, owner, a)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			blocker, err := adminPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			var blockerPID int
			if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, owner.TenantID).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			r := httptest.NewRequest("PUT", "/api/agent-accounts/"+a.ID+"/residency-evidence", strings.NewReader(encoded(t, evidenceAt(profile, now))))
			r = r.WithContext(context.WithValue(tenant.WithPrincipal(ctx, owner), clockKey{}, now))
			mux := http.NewServeMux()
			mod.Mount(mux)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); mux.ServeHTTP(w, r); done <- w }()
			// Observe the actual DB lock wait. No sleeps or timing guesses: the
			// request must be pending at the write fence before revocation commits.
			for {
				var waiting bool
				if err := adminPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
 WHERE datname=current_database() AND query='SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE'
 AND $1=ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-done:
					t.Fatalf("write crossed held authorization fence: status %d", w.Code)
				default:
				}
			}
			if change == "permission" {
				_, err = blocker.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, owner.TenantID, owner.ID)
			} else {
				_, err = blocker.Exec(ctx, `UPDATE agent_accounts SET owner_person_id=NULL,linked_at=NULL,link_revision=link_revision+1 WHERE tenant_id=$1 AND id=$2`, owner.TenantID, a.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				if w.Code != 403 {
					t.Fatalf("stale authorization survived %s change: %d %s", change, w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if n := evidenceEventCount(t, owner); n != 0 {
				t.Fatalf("refused write emitted %d events", n)
			}
		})
	}
}
