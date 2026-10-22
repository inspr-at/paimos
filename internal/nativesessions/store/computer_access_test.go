// SPDX-License-Identifier: AGPL-3.0-only
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/jackc/pgx/v5"
)

// Risk: an observer/network gap keeps revoked computer authority alive in the
// final native claim transaction. The barrier proves the change occurs in that
// gap; legacy NULL actors and workspace admins retain their existing admission.
func TestComputerStartNativeClaimRechecksAfterObservation(t *testing.T) {
	for _, mode := range []string{"granted", "revoked", "project_revoked", "person_deactivated", "legacy", "workspace_admin"} {
		t.Run(mode, func(t *testing.T) {
			f := newStartFixture(t)
			uid := func() string {
				id, err := uuid()
				if err != nil {
					t.Fatal(err)
				}
				return id
			}
			leaf, order, run, parent := uid(), uid(), uid(), uid()
			person := f.person.ID
			ref, lease := sha256.Sum256([]byte("computer start parent ref")), sha256.Sum256([]byte("computer start parent lease"))
			f.seed(t, func(tx pgx.Tx) error {
				for _, q := range []struct {
					sql  string
					args []any
				}{
					{`INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,computer_id) SELECT $1,$2,id,'computer',$3 FROM roles WHERE key='admin'`, []any{f.person.TenantID, person, f.intent.ComputerID}},
					{`INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'NS-2',id,'Computer work',$3 FROM node_kinds WHERE slug='work'`, []any{f.person.TenantID, leaf, f.project}},
					{`INSERT INTO nodes(tenant_id,id,key,kind_id,title,parent_id) SELECT $1,$2,'NS-3',id,'Computer order',$3 FROM node_kinds WHERE slug='work_order'`, []any{f.person.TenantID, order, leaf}},
					{`INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'ready')`, []any{f.person.TenantID, order, f.person.ID}},
					{`INSERT INTO agent_runs(tenant_id,id,work_order_id,agent_principal_id,model_profile_id,account_id) VALUES($1,$2,$3,$4,$5,$6)`, []any{f.person.TenantID, run, order, f.agent.ID, f.intent.ModelProfileID, f.intent.AccountID}},
					{`INSERT INTO harness_sessions(tenant_id,id,project_id,agent_principal_id,harness,host,management,role,ref_digest,lease_digest,owner_principal_id) VALUES($1,$2,$3,$4,'claude','local','unmanaged','coordinator',$5,$6,$7)`, []any{f.person.TenantID, parent, f.project, f.agent.ID, ref[:], lease[:], f.person.ID}},
					{`INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,reserved,pace_model) VALUES($1,$2,$3::timestamptz-interval '1 hour',$3::timestamptz+interval '1 hour','requests',8,1,'unrestricted')`, []any{f.person.TenantID, f.intent.AccountID, f.now}},
					{`INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) SELECT $1,$2,id,1 FROM account_allowance_windows WHERE account_id=$3`, []any{f.person.TenantID, run, f.intent.AccountID}},
				} {
					if _, err := tx.Exec(t.Context(), q.sql, q.args...); err != nil {
						return err
					}
				}
				// The native intent and run serve the same person. A project Admin
				// grant permits work without supplying a workspace computer grant.
				if mode != "legacy" && mode != "workspace_admin" {
					if _, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='workspace'`, person); err != nil {
						return err
					}
					if _, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE key='admin'`, f.person.TenantID, person, f.project); err != nil {
						return err
					}
				}
				if mode != "legacy" {
					acting := person
					if mode == "workspace_admin" {
						acting = f.person.ID
					}
					_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET acting_person_id=$2 WHERE id=$1`, run, acting)
					return err
				}
				return nil
			})
			authority := f.s.authority.(fixedAuthority)
			authority.authorization.RunID = &run
			f.s.authority = authority
			f.intent.Role, f.intent.WorkID, f.intent.ParentHarnessSessionID = "worker", &leaf, &parent
			accepted := f.begin(t)
			f.observer.entered, f.observer.release = make(chan struct{}), make(chan struct{})
			ctx, cancel := context.WithTimeout(f.daemonCtx(t), 15*time.Second)
			defer cancel()
			defer func() {
				select {
				case <-f.observer.release:
				default:
					close(f.observer.release)
				}
			}()
			done := make(chan error, 1)
			go func() { _, err := f.s.Claim(ctx, f.daemon, f.claim(accepted)); done <- err }()
			select {
			case <-f.observer.entered:
			case err := <-done:
				t.Fatalf("claim did not reach observation: %v", err)
			case <-ctx.Done():
				t.Fatal("claim did not reach observation barrier")
			}
			if mode != "granted" {
				f.seed(t, func(tx pgx.Tx) error {
					if err := agentpairing.Lock(t.Context(), tx); err != nil {
						return err
					}
					switch mode {
					case "project_revoked":
						_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='project'`, person)
						return err
					case "person_deactivated":
						_, err := tx.Exec(t.Context(), `UPDATE principals SET status='deactivated' WHERE id=$1`, person)
						return err
					default:
						_, err := tx.Exec(t.Context(), `DELETE FROM role_bindings WHERE principal_id=$1 AND scope_type='computer'`, person)
						return err
					}
				})
			}
			close(f.observer.release)
			var err error
			select {
			case err = <-done:
			case <-ctx.Done():
				t.Fatal("claim did not complete after observation")
			}
			if mode == "revoked" || mode == "project_revoked" || mode == "person_deactivated" {
				var failure *Failure
				if !errors.As(err, &failure) || failure.Status != 403 || failure.Reason != nativecontract.ReasonPermissionRevoked {
					t.Fatalf("revoked computer start: %v", err)
				}
			} else if err != nil {
				t.Fatalf("%s computer start: %v", mode, err)
			}
			f.seed(t, func(tx pgx.Tx) error {
				var state string
				if err := tx.QueryRow(t.Context(), `SELECT status FROM agent_runs WHERE id=$1`, run).Scan(&state); err != nil {
					return err
				}
				want := "starting"
				if mode == "revoked" || mode == "project_revoked" || mode == "person_deactivated" {
					want = "queued"
				}
				if state != want {
					t.Fatalf("claim state %s, want %s", state, want)
				}
				return nil
			})
		})
	}
}
