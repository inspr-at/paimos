// SPDX-License-Identifier: AGPL-3.0-only

package agentaccounts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/hostcapacity"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Risk: an advisory route becomes launch authority after account/context/model
// revocation, or identity changes silently reuse a native conversation.
func TestNativeSessionFinalAdmissionRechecksIdentityContextAndCapacity(t *testing.T) {
	d := dbtest.Open(t)
	uid := func(n int) string { return fmt.Sprintf("51000000-0000-4000-8000-%012d", n) }
	person := tenant.Principal{ID: uid(1), TenantID: uid(2), Kind: tenant.Person}
	paired := tenant.Principal{ID: uid(3), TenantID: person.TenantID, Kind: tenant.Agent,
		Scopes: []string{"harness.worker", "harness.control"}, KeyCreatorID: person.ID}
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	in := NativeAdmission{TenantID: person.TenantID, OwnerPersonID: person.ID, PrincipalID: paired.ID,
		ProjectID: uid(4), AccountID: uid(5), ComputerID: uid(6), ModelProfileID: uid(7),
		OwnedSessionID: uid(8), Harness: "claude", DaemonID: "native-test", DaemonGeneration: "generation-1", LedgerGeneration: uid(9)}
	seedCtx := dbtest.Seed(t.Context())
	seed := func(fn func(pgx.Tx) error) {
		t.Helper()
		if err := db.InTenant(seedCtx, d.App, person.TenantID, fn); err != nil {
			t.Fatal(err)
		}
	}
	seed(func(tx pgx.Tx) error {
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants(id,slug,name) VALUES($1,'native-admission','native')`, []any{person.TenantID}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Owner'),($1,$3,'agent','Paired')`, []any{person.TenantID, person.ID, paired.ID}},
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'NATIVE-1',id,'Project' FROM node_kinds WHERE slug='project'`, []any{person.TenantID, in.ProjectID}},
			{`INSERT INTO model_profiles(tenant_id,id,slug,version,harness,family,model,effort,tier) VALUES($1,$2,'native-admission','1','claude','anthropic','claude-opus-4-6','high','strong')`, []any{person.TenantID, in.ModelProfileID}},
		} {
			if _, err := tx.Exec(seedCtx, q.sql, q.args...); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(seedCtx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id) VALUES($1,$2,'fixture','native_admission_fixture',$3,$4,$5) RETURNING id::text`, person.TenantID, paired.ID, strings.Repeat("a", 64), paired.Scopes, person.ID).Scan(&paired.KeyID); err != nil {
			return err
		}
		policy := hostcapacity.Default()
		policy.MaximumAgents = 1
		policyRaw, _ := json.Marshal(policy)
		load := 1.0
		signals, _ := json.Marshal(hostcapacity.Signals{Cores: 8, Load: &load, MemoryPressure: "normal", Power: "plugged_in", Thermal: "normal"})
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO agent_pairing_requests(tenant_id,id,user_code,device_hash,runtime_hash,lifecycle_hash,details,request_digest,state,approved_by)
 VALUES($1,$2,'123456789',$3,$4,$5,'{"platform":"darwin","arch":"arm64"}','fixture','redeemed',$6)`, []any{person.TenantID, uid(10), strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), person.ID}},
			{`INSERT INTO agent_pairing_computers(tenant_id,id,request_id,principal_id,key_id,daemon_id,lifecycle_hash,ledger_generation,capacity_policy,capacity_signals,capacity_reported_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, []any{person.TenantID, in.ComputerID, uid(10), paired.ID, paired.KeyID, in.DaemonID, strings.Repeat("c", 64), in.LedgerGeneration, policyRaw, signals, now}},
			{`INSERT INTO agent_accounts(tenant_id,id,account_key,harness,daemon_id,registered_by_principal_id,label,owner_person_id,linked_at,link_revision,allowed_model_profile_ids,last_probe_at,last_probe_ok,last_daemon_generation,max_parallel_runs)
 VALUES($1,$2,'enrolled','claude',$3,$4,'Own',$5,$7,1,ARRAY[$6::uuid],$7,true,$8,1)`, []any{person.TenantID, in.AccountID, in.DaemonID, paired.ID, person.ID, in.ModelProfileID, now, in.DaemonGeneration}},
			{`INSERT INTO agent_pairing_enrollments(tenant_id,account_id,computer_id,request_id,model_profile_id,ongoing_approved_at,verification_expires_at)
 VALUES($1,$2,$3,$4,$5,$6::timestamptz,$6::timestamptz+interval '1 hour')`, []any{person.TenantID, in.AccountID, in.ComputerID, uid(10), in.ModelProfileID, now}},
			{`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model) VALUES($1,$2,$3::timestamptz-interval '1 hour',$3::timestamptz+interval '1 hour','requests',100,'unrestricted')`, []any{person.TenantID, in.AccountID, now}},
		} {
			if _, err := tx.Exec(seedCtx, q.sql, q.args...); err != nil {
				return err
			}
		}
		schedule := capacity.DefaultSchedule("UTC")
		schedule.Reserve = capacity.ReserveOff
		schedule.Nights = true
		for i := range schedule.Week {
			schedule.Week[i] = capacity.Day{On: true, Start: 0, End: 24}
		}
		scheduleRaw, _ := json.Marshal(schedule)
		_, err := tx.Exec(seedCtx, `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule) VALUES($1,$2,'account',$3,$3,$4)`, person.TenantID, person.ID, in.AccountID, scheduleRaw)
		return err
	})
	var binding NativeAccountBinding
	seed(func(tx pgx.Tx) error {
		if err := agentpairing.Lock(seedCtx, tx); err != nil {
			return err
		}
		var err error
		binding, err = BindNativeAccountTx(seedCtx, tx, in)
		return err
	})
	finalCtx := tenant.WithPrincipal(dbtest.Seed(t.Context()), paired)
	check := func(want nativecontract.Reason) {
		t.Helper()
		err := db.InTenant(finalCtx, d.App, person.TenantID, func(tx pgx.Tx) error { return RequireNativeSessionTx(finalCtx, tx, paired, in, binding, "", now) })
		if want == "" {
			if err != nil {
				t.Fatal(err)
			}
			return
		}
		var denied *nativecontract.ValidationError
		if !errors.As(err, &denied) || denied.Reason != want {
			t.Fatalf("got %v; want %s", err, want)
		}
	}
	check("") // Establish the successful route before changing ONE gate.
	var contexts []string
	seed(func(tx pgx.Tx) error {
		rows, err := tx.Query(seedCtx, `SELECT context_id::text FROM account_use_cells WHERE account_id=$1`, in.AccountID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			contexts = append(contexts, id)
		}
		return rows.Err()
	})
	if len(contexts) == 0 {
		t.Fatal("fixture never granted account context")
	}
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `DELETE FROM account_use_cells WHERE account_id=$1`, in.AccountID)
		return err
	})
	check(nativecontract.ReasonPermissionRevoked)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `INSERT INTO account_use_cells(tenant_id,account_id,context_id,source,set_by) SELECT $1,$2,c,'person',$4 FROM unnest($3::uuid[]) c`, person.TenantID, in.AccountID, contexts, person.ID)
		return err
	})
	check("")
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE model_profiles SET enabled=false WHERE id=$1`, in.ModelProfileID)
		return err
	})
	check(nativecontract.ReasonQualificationMissing)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE model_profiles SET enabled=true WHERE id=$1`, in.ModelProfileID)
		return err
	})
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE agent_accounts SET link_revision=link_revision+1 WHERE id=$1`, in.AccountID)
		return err
	})
	check(nativecontract.ReasonIdentityMismatch)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE agent_accounts SET link_revision=$2 WHERE id=$1`, in.AccountID, binding.LinkRevision)
		return err
	})
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `INSERT INTO native_session_roots(tenant_id,id,owned_session_id,project_id,owner_person_id,account_id,computer_id,runtime_epoch,hold_state) VALUES($1,gen_random_uuid(),$2,$3,$4,$5,$6,$7,'idle')`, person.TenantID, uid(11), in.ProjectID, person.ID, in.AccountID, in.ComputerID, uid(12))
		return err
	})
	check(nativecontract.ReasonAccountFull)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE agent_accounts SET max_parallel_runs=2 WHERE id=$1`, in.AccountID)
		return err
	})
	check(nativecontract.ReasonHostFull)
	// A fresh daemon generation and route cannot make an idle/taken root free.
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE native_session_roots SET hold_state='taken' WHERE owned_session_id=$1`, uid(11))
		return err
	})
	check(nativecontract.ReasonHostFull)
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE native_session_roots SET released_at=$2 WHERE owned_session_id=$1`, uid(11), now)
		return err
	})
	check("")
	seed(func(tx pgx.Tx) error {
		_, err := tx.Exec(seedCtx, `UPDATE agent_keys SET revoked_at=$2 WHERE id=$1`, paired.KeyID, now)
		return err
	})
	check(nativecontract.ReasonPermissionRevoked)
}
