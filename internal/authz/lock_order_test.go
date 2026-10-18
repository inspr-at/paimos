// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/operatoractor"
	"github.com/inspr-at/paimos/internal/tenant"
)

func TestInviteAcceptTenantBeforeAdvisory(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, alias, identity string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('invite-lock-order','Invite lock order') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Owner') RETURNING id::text`, tid).Scan(&owner); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,email) VALUES($1,'person','Alias','invite@example.test') RETURNING id::text`, tid).Scan(&alias); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO identities(issuer,subject,email) VALUES('https://id.example.test','invited','invite@example.test') RETURNING id::text`).Scan(&identity); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO invites(tenant_id,email,token_hash,workspace_role_id,created_by,expires_at)
			SELECT $1,'invite@example.test',decode(repeat('00',32),'hex'),id,$2,now()+interval '1 day'
			FROM roles WHERE tenant_id=$1 AND key='member'`, tid, owner)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, alias, "member")
	var person tenant.Principal
	dbtest.TenantBeforeAdvisory(t, d, tid, tid, 532, func(ctx context.Context) error {
		return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
			var err error
			person, err = AcceptInvite(ctx, tx, tid, identity, "invite@example.test", "Invited person", "")
			return err
		})
	})
	var linked string
	var bindings int
	if err := d.Admin.QueryRow(ctx, `SELECT linked_to::text,
		(SELECT count(*) FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2)
		FROM principals WHERE tenant_id=$1 AND id=$2`, tid, alias).Scan(&linked, &bindings); err != nil {
		t.Fatal(err)
	}
	if person.ID == "" || linked != person.ID || bindings != 0 {
		t.Fatalf("linked=%s want %s; alias bindings=%d", linked, person.ID, bindings)
	}
}

func TestAccessMutationTenantBeforeTree(t *testing.T) {
	for _, name := range []string{"project membership", "project write", "operator actor"} {
		t.Run(name, func(t *testing.T) {
			d := dbtest.Open(t)
			var tid string
			if err := d.Admin.QueryRow(t.Context(), `INSERT INTO tenants(slug,name) VALUES('lock-order','Lock order') RETURNING id::text`).Scan(&tid); err != nil {
				t.Fatal(err)
			}
			dbtest.TenantBeforeTree(t, d, tid, func(ctx context.Context) error {
				return db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
					if name == "operator actor" {
						_, err := operatoractor.Ensure(ctx, tx, tid)
						return err
					}
					if name == "project write" {
						return LockProjectWrite(ctx, tx, tid)
					}
					return LockProjectMutation(ctx, tx, tid)
				})
			})
		})
	}
}
