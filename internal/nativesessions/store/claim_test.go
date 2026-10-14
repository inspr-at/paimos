// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentaccounts"
	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/harness"
	"github.com/inspr-at/paimos/internal/nativeadmission"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativeintegration"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// This fake native-grant registry is deliberately separate from role/key
// grants. Both adapters read its CURRENT state in the supplied transaction.
type pairedGrantAuthority struct{ Authorization }

func (a pairedGrantAuthority) AuthorizeTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, project string, intent nativecontract.StartIntent) (Authorization, error) {
	var revision int64
	var revoked bool
	err := tx.QueryRow(ctx, `SELECT revision,revoked FROM fixture_native_grants
 WHERE tenant_id=$1 AND id=$2 AND actor_id=$3`, actor.TenantID, a.Policy.GrantID, actor.PrincipalID).Scan(&revision, &revoked)
	if err != nil {
		return Authorization{}, err
	}
	if revoked {
		return Authorization{}, fail(403, nativecontract.ReasonPermissionRevoked)
	}
	if revision != a.Policy.GrantRevision || actor.GrantID != "" && (actor.GrantID != a.Policy.GrantID || actor.GrantRevision != revision) ||
		project != a.ProjectID || intent.ModelProfileID != a.Launch.ModelProfileID {
		return Authorization{}, fail(403, nativecontract.ReasonIdentityMismatch)
	}
	return a.Authorization, nil
}

func (a pairedGrantAuthority) RequireNativeGrantTx(ctx context.Context, tx pgx.Tx, actor nativecontract.Actor, intent nativecontract.StartIntent, pinned nativecontract.Policy) error {
	current, err := a.AuthorizeTx(ctx, tx, actor, a.ProjectID, intent)
	if err != nil {
		return err
	}
	if actor.OwnerPersonID != current.OwnerPersonID || !samePolicy(current.Policy, pinned) {
		return fail(403, nativecontract.ReasonIdentityMismatch)
	}
	return nil
}

func newPairedStartFixture(t *testing.T) (*startFixture, pairedGrantAuthority) {
	t.Helper()
	f := newStartFixture(t)
	mux := http.NewServeMux()
	agentpairing.New(f.d.App, "https://native.test", "").Mount(mux)
	agentaccounts.New(f.d.App).Mount(mux)
	call := func(path string, body any, person bool, out any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		if person {
			r = r.WithContext(f.personCtx(t))
			r.Header.Set("Origin", "https://native.test")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		want := http.StatusOK
		if strings.HasSuffix(path, "/capacity/approve") {
			want = http.StatusNoContent
		}
		if w.Code != want {
			t.Fatalf("pairing %s: %d %s", path, w.Code, w.Body.String())
		}
		if out != nil {
			if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
				t.Fatal(err)
			}
		}
	}
	request, err := uuid()
	if err != nil {
		t.Fatal(err)
	}
	device := strings.Repeat("d", 64)
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	var code struct {
		Code string `json:"user_code"`
	}
	call("/api/agent-pairing/device", map[string]any{"request_id": request, "tenant_id": f.person.TenantID,
		"device_hash": hash(device), "runtime_hash": hash(strings.Repeat("r", 64)), "lifecycle_hash": hash(strings.Repeat("l", 64)),
		"computer_name": "Native real pairing", "platform": "darwin", "arch": "arm64", "workspace_path": "/fixture/native",
		"capabilities": []string{"managed_runs"}, "accounts": []agentpairing.Choice{{AccountKey: "real-own-account", Harness: "claude", Label: "Own", ProfileID: f.intent.ModelProfileID}}}, false, &code)
	var review, paired agentpairing.View
	call("/api/agent-pairing/lookup", map[string]string{"user_code": code.Code}, true, &review)
	call("/api/agent-pairing/requests/"+request+"/approve", map[string]any{"request_digest": review.Digest, "verification": "connect_only", "selected_account_keys": []string{"real-own-account"}}, true, nil)
	call("/api/agent-pairing/redeem", map[string]string{"tenant_id": f.person.TenantID, "request_id": request, "device_secret": device}, false, &paired)
	if paired.PrincipalID == nil || paired.ComputerID == nil || paired.DaemonID == nil || len(paired.Enrollments) != 1 {
		t.Fatal("pairing did not issue a runtime/enrollment")
	}
	f.agent = tenant.Principal{TenantID: f.person.TenantID, ID: *paired.PrincipalID, Kind: tenant.Agent}
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT k.id::text,k.created_by_principal_id::text,k.scopes
 FROM agent_pairing_computers c JOIN agent_keys k ON k.tenant_id=c.tenant_id AND k.id=c.key_id WHERE c.id=$1`, *paired.ComputerID).Scan(&f.agent.KeyID, &f.agent.KeyCreatorID, &f.agent.Scopes); err != nil {
		t.Fatal(err)
	}
	f.agent.AuthKeyID = f.agent.KeyID
	if !slices.Equal(f.agent.Scopes, agentpairing.RuntimePermissions) || slices.Contains(f.agent.Scopes, "harness.control") || slices.Contains(f.agent.Scopes, "chat.receive") {
		t.Fatal("fixture expanded the issued runtime key ceiling")
	}
	f.intent.ComputerID, f.intent.AccountID = *paired.ComputerID, paired.Enrollments[0].AccountID
	// Connect-only issuance deliberately grants no ongoing account work. Use
	// the real separate PERSON approval before seeding daemon readiness facts.
	call("/api/agent-accounts/"+f.intent.AccountID+"/capacity/approve", nil, true, nil)
	f.daemon.Actor = nativecontract.Actor{TenantID: f.agent.TenantID, PrincipalID: f.agent.ID, PrincipalKind: "agent", OwnerPersonID: f.person.ID, KeyCeiling: f.agent.Scopes}
	f.daemon.ComputerID, f.daemon.DaemonID = f.intent.ComputerID, *paired.DaemonID
	f.observer.value.PrincipalID, f.observer.value.ComputerID = f.agent.ID, f.intent.ComputerID
	f.observer.value.AccountID, f.observer.value.DaemonID = f.intent.AccountID, f.daemon.DaemonID
	f.seed(t, func(tx pgx.Tx) error {
		// Only readiness/ledger observations change after actual provision;
		// neither the provisioned role nor its key permissions are touched.
		if _, err := tx.Exec(t.Context(), `UPDATE agent_pairing_computers SET ledger_generation=$2,
 capacity_policy=(SELECT capacity_policy FROM agent_pairing_computers WHERE daemon_id='daemon-native'),
 capacity_signals=(SELECT capacity_signals FROM agent_pairing_computers WHERE daemon_id='daemon-native'),capacity_reported_at=$3 WHERE id=$1`, f.intent.ComputerID, f.daemon.LedgerGeneration, f.now); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE agent_accounts SET last_probe_at=$2,last_probe_ok=true,last_daemon_generation=$3,linked_at=$2,owner_person_id=$4,link_revision=1 WHERE id=$1`, f.intent.AccountID, f.now, f.daemon.DaemonGeneration, f.person.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `DELETE FROM account_capacity_schedules WHERE account_id=$1`, f.intent.AccountID); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO account_capacity_schedules(tenant_id,principal_id,scope,scope_key,account_id,schedule)
 SELECT tenant_id,principal_id,scope,$1::text,$1::uuid,schedule FROM account_capacity_schedules WHERE account_id<>$1::uuid
 ON CONFLICT DO NOTHING`, f.intent.AccountID)
		return err
	})
	a := pairedGrantAuthority{f.s.authority.(fixedAuthority).authorization}
	f.seed(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `CREATE TABLE fixture_native_grants(tenant_id uuid,id uuid,actor_id uuid,revision bigint,revoked boolean);
 ALTER TABLE fixture_native_grants ENABLE ROW LEVEL SECURITY;
 ALTER TABLE fixture_native_grants FORCE ROW LEVEL SECURITY;
 CREATE POLICY fixture_native_grants_tenant ON fixture_native_grants USING(tenant_id=NULLIF(current_setting('aeon.tenant_id',true),'')::uuid)`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO fixture_native_grants VALUES($1,$2,$3,$4,false)`, f.person.TenantID, a.Policy.GrantID, f.person.ID, a.Policy.GrantRevision)
		return err
	})
	f.s.authority = a
	return f, a
}

// Risk: a real paired runtime cannot execute an approved native start, or the
// integration widens that key instead of checking the ORIGINAL actor again.
func TestPairedNativeStartAndRevalidationKeepSeparateAuthorities(t *testing.T) {
	for _, change := range []string{"none", "actor_control", "native_grant", "pairing", "different_key"} {
		t.Run(change, func(t *testing.T) {
			f, authority := newPairedStartFixture(t)
			accepted := f.begin(t)
			claimed, err := f.s.Claim(f.daemonCtx(t), f.daemon, f.claim(accepted))
			if err != nil || claimed.ExpectedBinding == nil || claimed.ExpectedBinding.RunID != nil || claimed.RootReservationID == nil {
				t.Fatalf("real paired key failed native coordinator claim: %v", err)
			}
			in := agentaccounts.NativeAdmission{TenantID: f.person.TenantID, ProjectID: f.project, OwnerPersonID: f.person.ID, PrincipalID: f.agent.ID,
				OwnedSessionID: accepted.Session.OwnedSessionID, AccountID: f.intent.AccountID, ComputerID: f.intent.ComputerID,
				Harness: f.intent.Harness, ModelProfileID: f.intent.ModelProfileID, DaemonID: f.daemon.DaemonID,
				DaemonGeneration: f.daemon.DaemonGeneration, LedgerGeneration: f.daemon.LedgerGeneration}
			var binding agentaccounts.NativeAccountBinding
			f.seed(t, func(tx pgx.Tx) error {
				var err error
				binding, err = agentaccounts.BindNativeAccountTx(dbtest.Seed(t.Context()), tx, in)
				return err
			})
			check := func() error {
				ctx := f.daemonCtx(t)
				return db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
					return agentaccounts.RequireNativeSessionTx(ctx, tx, f.agent, in, binding, in.OwnedSessionID, f.now, authority)
				})
			}
			if err := check(); err != nil {
				t.Fatalf("real paired key failed exact-root runtime validation: %v", err)
			}
			root := nativeintegration.RootAuthority{Pool: f.d.App, Grants: authority, Now: func() time.Time { return f.now }}
			runtimeRequest := nativeadmission.Request{Binding: *claimed.ExpectedBinding, AccountBindingDigest: binding.Digest(),
				Profile: nativecontract.ProfileRequest{TenantID: f.person.TenantID, OwnerPersonID: f.person.ID,
					AccountID: f.intent.AccountID, AccountKey: binding.AccountKey, Harness: f.intent.Harness,
					WorkspaceID: f.intent.WorkspaceID, BindingRevision: claimed.ExpectedBinding.BindingRevision, Policy: claimed.Policy}, Launch: claimed.LaunchConfig}
			if err := root.Revalidate(f.daemonCtx(t), runtimeRequest); err != nil {
				t.Fatalf("native admission port failed exact-root runtime validation: %v", err)
			}
			if change == "none" {
				stale := runtimeRequest
				stale.Binding.RuntimeEpoch = "30000000-0000-4000-8000-000000000001"
				var refusal *nativecontract.ValidationError
				if err := root.Revalidate(f.daemonCtx(t), stale); !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonWrongGeneration {
					t.Fatalf("native admission accepted another runtime epoch: %v", err)
				}
				stale = runtimeRequest
				stale.AccountBindingDigest = strings.Repeat("0", 64)
				if err := root.Revalidate(f.daemonCtx(t), stale); !errors.As(err, &refusal) || refusal.Reason != nativecontract.ReasonIdentityMismatch {
					t.Fatalf("native admission accepted another account enrollment: %v", err)
				}
				// Check an actual control handler with this authenticated runtime.
				mux := http.NewServeMux()
				harness.New(f.d.App).Mount(mux)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mux.ServeHTTP(w, r.WithContext(tenant.WithPrincipal(r.Context(), f.agent)))
				}))
				t.Cleanup(server.Close)
				var prefix string
				if err := f.d.Admin.QueryRow(t.Context(), `SELECT prefix FROM agent_keys WHERE id=$1`, f.agent.KeyID).Scan(&prefix); err != nil {
					t.Fatal(err)
				}
				r, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/api/projects/"+f.project+"/harness-sessions/"+claimed.ExpectedBinding.HarnessSessionID+"/controls/interrupt", strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				r.Header.Set("Authorization", "Bearer aeon_"+prefix+"_"+strings.Repeat("r", 64))
				response, err := server.Client().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(io.LimitReader(response.Body, 8193))
				var denial struct {
					Error string `json:"error"`
				}
				if err != nil || len(raw) > 8192 || json.Unmarshal(raw, &denial) != nil ||
					response.StatusCode != http.StatusForbidden || denial.Error != "key scope required: harness.control" {
					t.Fatalf("ordinary control route did not refuse its exact key ceiling: HTTP %d %s (%v)", response.StatusCode, raw, err)
				}
				return
			}
			var claimDone chan error
			if change != "different_key" {
				f.observer.entered, f.observer.release = make(chan struct{}), make(chan struct{})
				claimDone = make(chan error, 1)
				go func() { _, err := f.s.Claim(f.daemonCtx(t), f.daemon, f.claim(accepted)); claimDone <- err }()
				select {
				case <-f.observer.entered:
				case err := <-claimDone:
					t.Fatalf("claim refused before the observation barrier: %v", err)
				case <-t.Context().Done():
					t.Fatal("claim did not reach the observation barrier")
				}
			}
			f.seed(t, func(tx pgx.Tx) error {
				ctx := dbtest.Seed(t.Context())
				if err := agentpairing.Lock(ctx, tx); err != nil {
					return err
				}
				switch change {
				case "actor_control":
					permissions, _ := authz.BuiltinPermissions("admin")
					kept := slices.DeleteFunc(slices.Clone(permissions), func(p string) bool { return p == "harness.control" })
					var role string
					if err := tx.QueryRow(ctx, `INSERT INTO roles(tenant_id,key,name) VALUES($1,'no_native_control','Current actor without control') RETURNING id::text`, f.person.TenantID).Scan(&role); err != nil {
						return err
					}
					if _, err := tx.Exec(ctx, `INSERT INTO role_permissions(tenant_id,role_id,permission) SELECT $1,$2,unnest($3::text[])`, f.person.TenantID, role, kept); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE role_bindings SET role_id=$2 WHERE principal_id=$1`, f.person.ID, role)
					return err
				case "native_grant":
					_, err := tx.Exec(ctx, `UPDATE fixture_native_grants SET revoked=true WHERE tenant_id=$1 AND id=$2`, f.person.TenantID, authority.Policy.GrantID)
					return err
				case "pairing":
					_, err := tx.Exec(ctx, `UPDATE agent_pairing_enrollments SET state='revoked' WHERE account_id=$1`, f.intent.AccountID)
					return err
				case "different_key":
					return tx.QueryRow(ctx, `INSERT INTO agent_keys(tenant_id,principal_id,name,prefix,hash,scopes,created_by_principal_id,person_owner_required)
 VALUES($1,$2,'other runtime key','native_other_runtime',$3,$4,$5,true) RETURNING id::text`, f.person.TenantID, f.agent.ID, strings.Repeat("f", 64), f.agent.Scopes, f.person.ID).Scan(&f.agent.KeyID)
				}
				return nil
			})
			if change == "different_key" {
				f.agent.AuthKeyID = f.agent.KeyID
			}
			// Establish that each mutation preserved the daemon's worker authority;
			// otherwise the negative could pass for an unrelated role/key failure.
			ctx := f.daemonCtx(t)
			if err := db.InTenant(ctx, f.d.App, f.person.TenantID, func(tx pgx.Tx) error {
				return authz.RequireTx(ctx, tx, f.agent, "harness.worker", authz.Scope{ProjectID: f.project})
			}); err != nil {
				t.Fatalf("negative fixture lost ordinary worker authority: %v", err)
			}
			assertRefusal := func(err error) {
				t.Helper()
				var claimFailure *Failure
				var nativeFailure *nativecontract.ValidationError
				if errors.As(err, &claimFailure) && claimFailure.Reason == nativecontract.ReasonPermissionRevoked ||
					errors.As(err, &nativeFailure) && nativeFailure.Reason == nativecontract.ReasonPermissionRevoked {
					return
				}
				t.Fatalf("%s did not refuse with permission_revoked: %v", change, err)
			}
			if claimDone != nil {
				close(f.observer.release)
				select {
				case err = <-claimDone:
				case <-t.Context().Done():
					t.Fatal("claim did not leave the observation barrier")
				}
				f.observer.entered = nil
			} else {
				_, err = f.s.Claim(f.daemonCtx(t), f.daemon, f.claim(accepted))
			}
			assertRefusal(err)
			assertRefusal(check())
			assertRefusal(root.Revalidate(f.daemonCtx(t), runtimeRequest))
		})
	}
}

var _ Authority = pairedGrantAuthority{}
var _ agentaccounts.NativeSessionAuthority = pairedGrantAuthority{}
