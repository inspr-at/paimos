// SPDX-License-Identifier: AGPL-3.0-only

package authz

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
)

// Risk: a person-bound key or run reads another project or workspace knowledge,
// a deactivated person or a removed grant keeps working, or a narrow key scope
// is widened by the person's role.
func TestPersonCeiling(t *testing.T) {
	d := dbtest.Open(t)
	ctx := t.Context()
	var tid, owner, person, limited, inactive, wide, legacy, both, crossed, hidden string
	if err := d.Admin.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('person-ceiling','Person ceiling') RETURNING id::text`).Scan(&tid); err != nil {
		t.Fatal(err)
	}
	ids := []struct {
		kind string
		dest *string
	}{
		{"person", &owner}, {"person", &person}, {"person", &limited}, {"person", &inactive},
		{"agent", &wide}, {"agent", &legacy}, {"agent", &both}, {"agent", &crossed}, {"agent", &hidden},
	}
	for _, row := range ids {
		if err := d.Admin.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,$2,'Ceiling') RETURNING id::text`, tid, row.kind).Scan(row.dest); err != nil {
			t.Fatal(err)
		}
	}
	dbtest.BindRole(t, d, tid, owner, "owner")
	dbtest.BindRole(t, d, tid, wide, "member")
	dbtest.BindRole(t, d, tid, legacy, "member")
	dbtest.BindRole(t, d, tid, crossed, "member")
	var projectM, projectL, knowledge, runPerson, runNull, runInactive, runAgent string
	err := db.InTenant(dbtest.Seed(ctx), d.App, tid, func(tx pgx.Tx) error {
		node := func(slug, title, parent string) (string, error) {
			var id string
			var parentID any
			if parent != "" {
				parentID = parent
			}
			err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),$2,$3 FROM node_kinds k WHERE k.tenant_id=$1 AND k.slug=$4 RETURNING id::text`, tid, title, parentID, slug).Scan(&id)
			return id, err
		}
		var err error
		if projectM, err = node("project", "M", ""); err != nil {
			return err
		}
		if projectL, err = node("project", "L", ""); err != nil {
			return err
		}
		if knowledge, err = node("memory", "workspace knowledge", ""); err != nil {
			return err
		}
		order := func(parent string) (string, error) {
			id, err := node("work_order", "Order", parent)
			if err != nil {
				return "", err
			}
			_, err = tx.Exec(ctx, `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id,status) VALUES($1,$2,$3,'ready')`, tid, id, owner)
			return id, err
		}
		orderM, err := order(projectM)
		if err != nil {
			return err
		}
		orderL, err := order(projectL)
		if err != nil {
			return err
		}
		run := func(agent, orderID string, acting any) (string, error) {
			var id string
			err := tx.QueryRow(ctx, `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,acting_person_id) VALUES($1,$2,$3,$4) RETURNING id::text`, tid, orderID, agent, acting).Scan(&id)
			return id, err
		}
		if runPerson, err = run(legacy, orderM, person); err != nil {
			return err
		}
		if runNull, err = run(legacy, orderM, nil); err != nil {
			return err
		}
		if runInactive, err = run(hidden, orderL, inactive); err != nil {
			return err
		}
		if runAgent, err = run(legacy, orderM, wide); err != nil {
			return err
		}
		session := func(project, agent, lease, runID string, nonce string) error {
			sum := sha256.Sum256([]byte("aeon.harness.lease\x00" + lease))
			ref := sha256.Sum256([]byte(lease + "\x00" + project + "\x00" + nonce))
			var run any
			if runID != "" {
				run = runID
			}
			_, err := tx.Exec(ctx, `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,run_id,harness,host,management,role,work_shape,ref_digest,lease_digest) VALUES($1,$2,$3,$4,$5,'grok','ceiling-host','unmanaged','worker','unknown',$6,$7)`, tid, project, agent, owner, run, ref[:], sum[:])
			return err
		}
		if err = session(projectL, legacy, "lease-positive", runPerson, "a"); err != nil {
			return err
		}
		if err = session(projectL, hidden, "lease-hidden", runInactive, "b"); err != nil {
			return err
		}
		if err = session(projectM, legacy, "lease-ambiguous", "", "c"); err != nil {
			return err
		}
		return session(projectL, legacy, "lease-ambiguous", "", "d")
	})
	if err != nil {
		t.Fatal(err)
	}
	bindProject := func(principal, project string) {
		t.Helper()
		if _, err := d.Admin.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) SELECT $1,$2,id,'project',$3 FROM roles WHERE tenant_id=$1 AND key='member'`, tid, principal, project); err != nil {
			t.Fatal(err)
		}
	}
	bindProject(person, projectM)
	bindProject(limited, projectL)
	bindProject(both, projectM)
	bindProject(both, projectL)
	bindProject(hidden, projectM)
	if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, inactive); err != nil {
		t.Fatal(err)
	}

	wideP := tenant.Principal{ID: wide, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, FullAccess: true}
	legacyP := tenant.Principal{ID: legacy, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, FullAccess: true}
	bothP := tenant.Principal{ID: both, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, KeyPersonID: person, FullAccess: true}
	crossedP := tenant.Principal{ID: crossed, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: limited, KeyPersonID: person, FullAccess: true}
	hiddenP := tenant.Principal{ID: hidden, TenantID: tid, Kind: tenant.Agent, KeyCreatorID: owner, FullAccess: true}

	t.Run("admin key sees only the person", func(t *testing.T) {
		inCeiling(t, d, wideP, func(tx pgx.Tx) {
			vis := mustEnter(t, ctx, tx, tid, wideP)
			if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
				t.Fatalf("sql visibility %s", vis)
			}
			if !nodeVisible(t, ctx, tx, projectM) || nodeVisible(t, ctx, tx, projectL) || nodeVisible(t, ctx, tx, knowledge) {
				t.Fatalf("nodes leaked under %s", vis)
			}
			if !allows(t, ctx, tx, wideP, projectM) || allows(t, ctx, tx, wideP, projectL) || allows(t, ctx, tx, wideP, "") {
				t.Fatal("go project set diverges from sql")
			}
			if err := RequireTx(ctx, tx, wideP, "nodes.read", Scope{ProjectID: projectM}); err != nil {
				t.Fatal(err)
			}
			if err := RequireTx(ctx, tx, wideP, "nodes.read", Scope{ProjectID: projectL}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("project L: %v", err)
			}
		})
	})

	t.Run("narrow key scope wins", func(t *testing.T) {
		narrow := wideP
		narrow.FullAccess = false
		narrow.Scopes = []string{"events.read"}
		inCeiling(t, d, narrow, func(tx pgx.Tx) {
			if allows(t, ctx, tx, narrow, projectM) {
				t.Fatal("narrow key still reads nodes")
			}
			if err := RequireTx(ctx, tx, narrow, "nodes.read", Scope{ProjectID: projectM}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("require: %v", err)
			}
			vis := mustEnter(t, ctx, tx, tid, narrow)
			if !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
				t.Fatalf("sql followed the key scope: %s", vis)
			}
		})
	})

	t.Run("legacy three argument stays", func(t *testing.T) {
		inCeiling(t, d, legacyP, func(tx pgx.Tx) {
			var three, four string
			if err := tx.QueryRow(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid)`, tid, legacy, owner).Scan(&three); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,NULL::uuid)`, tid, legacy, owner).Scan(&four); err != nil {
				t.Fatal(err)
			}
			if three != "*" || four != three {
				t.Fatalf("legacy entry %q vs %q", three, four)
			}
			if !allows(t, ctx, tx, legacyP, "") || !allows(t, ctx, tx, legacyP, projectM) || !allows(t, ctx, tx, legacyP, projectL) {
				t.Fatal("legacy go ceiling narrowed")
			}
			if !nodeVisible(t, ctx, tx, knowledge) {
				t.Fatal("legacy key lost workspace knowledge")
			}
		})
	})

	t.Run("parity matrix", func(t *testing.T) {
		inCeiling(t, d, bothP, func(tx pgx.Tx) {
			vis := mustEnter(t, ctx, tx, tid, bothP)
			if !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) || vis == "*" {
				t.Fatalf("both projects sql %s", vis)
			}
			if !allows(t, ctx, tx, bothP, projectM) || allows(t, ctx, tx, bothP, projectL) {
				t.Fatal("both projects go")
			}
			ids, err := GrantedProjectIDsTx(ctx, tx, bothP, "nodes.read")
			if err != nil || len(ids) != 1 || ids[0] != projectM {
				t.Fatalf("granted %v %v", ids, err)
			}
		})
		inCeiling(t, d, crossedP, func(tx pgx.Tx) {
			vis := mustEnter(t, ctx, tx, tid, crossedP)
			if vis != "{}" && vis != "" {
				t.Fatalf("crossed sql %s", vis)
			}
			if allows(t, ctx, tx, crossedP, projectM) || allows(t, ctx, tx, crossedP, projectL) || allows(t, ctx, tx, crossedP, "") {
				t.Fatal("crossed go")
			}
		})
	})

	t.Run("missing or non-person acting id", func(t *testing.T) {
		inCeiling(t, d, wideP, func(tx pgx.Tx) {
			for _, id := range []string{"00000000-0000-0000-0000-000000000000", wide, inactive} {
				var vis string
				if err := tx.QueryRow(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, tid, wide, owner, id).Scan(&vis); err != nil {
					t.Fatal(err)
				}
				if vis != "" {
					t.Fatalf("acting %s visibility %s", id, vis)
				}
			}
			bad := wideP
			bad.KeyPersonID = inactive
			if _, err := loadTx(ctx, tx, bad, projectM); !errors.Is(err, ErrForbidden) {
				t.Fatalf("inactive go %v", err)
			}
			if allows(t, ctx, tx, bad, projectM) {
				t.Fatal("inactive go check")
			}
		})
	})

	t.Run("deactivate then restore", func(t *testing.T) {
		if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='deactivated' WHERE id=$1`, person); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := d.Admin.Exec(context.Background(), `UPDATE principals SET status='active' WHERE id=$1`, person); err != nil {
				t.Error(err)
			}
		})
		inCeiling(t, d, wideP, func(tx pgx.Tx) {
			vis := mustEnter(t, ctx, tx, tid, wideP)
			if vis != "" || nodeVisible(t, ctx, tx, projectM) {
				t.Fatalf("deactivated still sees %s", vis)
			}
			if err := RequireTx(ctx, tx, wideP, "nodes.read", Scope{ProjectID: projectM}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("deactivated require %v", err)
			}
		})
		if _, err := d.Admin.Exec(ctx, `UPDATE principals SET status='active' WHERE id=$1`, person); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("run person", func(t *testing.T) {
		inCeiling(t, d, legacyP, func(tx pgx.Tx) {
			got, err := db.RunPrincipalTx(ctx, tx, legacyP, runPerson)
			if err != nil || got.KeyPersonID != person {
				t.Fatalf("run person %+v %v", got, err)
			}
			bound := tenant.WithPrincipal(ctx, got)
			var vis string
			if err := tx.QueryRow(bound, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, tid, got.ID, owner, db.ActingPersonID(bound, got)).Scan(&vis); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) || vis == "*" || !nodeVisible(t, bound, tx, projectM) || nodeVisible(t, bound, tx, knowledge) {
				t.Fatalf("run visibility %s", vis)
			}
			unchanged, err := db.RunPrincipalTx(ctx, tx, legacyP, runNull)
			if err != nil || unchanged.KeyPersonID != "" {
				t.Fatalf("null run %+v %v", unchanged, err)
			}
			conflict := legacyP
			conflict.KeyPersonID = limited
			if _, err := db.RunPrincipalTx(ctx, tx, conflict, runPerson); err == nil || !strings.Contains(err.Error(), "conflicting run person") {
				t.Fatalf("conflict %v", err)
			}
			agentPerson, err := db.RunPrincipalTx(ctx, tx, legacyP, runAgent)
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, tid, agentPerson.ID, owner, agentPerson.KeyPersonID).Scan(&vis); err != nil {
				t.Fatal(err)
			}
			if vis != "" {
				t.Fatalf("agent acting person %s", vis)
			}
			if _, err := loadTx(tenant.WithPrincipal(ctx, agentPerson), tx, agentPerson, projectM); !errors.Is(err, ErrForbidden) {
				t.Fatalf("agent acting go %v", err)
			}
		})
	})

	t.Run("worker lease", func(t *testing.T) {
		inCeiling(t, d, legacyP, func(tx pgx.Tx) {
			got, err := db.WorkerRunPrincipalTx(ctx, tx, legacyP, "lease-positive")
			if err != nil || got.KeyPersonID != person {
				t.Fatalf("positive %+v %v", got, err)
			}
			// The lookup widens for the session read, then re-enters this
			// principal before it returns. The setting itself must be the cap.
			vis := setting(t, ctx, tx)
			if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) || !nodeVisible(t, ctx, tx, projectM) || nodeVisible(t, ctx, tx, projectL) || nodeVisible(t, ctx, tx, knowledge) {
				t.Fatalf("positive visibility %s", vis)
			}
		})
		inCeiling(t, d, hiddenP, func(tx pgx.Tx) {
			unknown, err := db.WorkerRunPrincipalTx(ctx, tx, hiddenP, "lease-unknown")
			if err != nil || unknown.KeyPersonID != "" {
				t.Fatalf("unknown %+v %v", unknown, err)
			}
			vis := setting(t, ctx, tx)
			if vis == "*" || !strings.Contains(vis, projectM) || strings.Contains(vis, projectL) {
				t.Fatalf("unknown lease left %s", vis)
			}
			got, err := db.WorkerRunPrincipalTx(ctx, tx, hiddenP, "lease-hidden")
			if err != nil || got.KeyPersonID != inactive {
				t.Fatalf("hidden %+v %v", got, err)
			}
			vis = setting(t, ctx, tx)
			if vis != "" || nodeVisible(t, ctx, tx, projectM) {
				t.Fatalf("hidden visibility %s", vis)
			}
		})
		err := db.InTenant(tenant.WithPrincipal(ctx, legacyP), d.App, tid, func(tx pgx.Tx) error {
			_, err := db.WorkerRunPrincipalTx(ctx, tx, legacyP, "lease-ambiguous")
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "ambiguous worker generation") {
			t.Fatalf("ambiguous %v", err)
		}
	})

	t.Run("removed grant", func(t *testing.T) {
		if _, err := d.Admin.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2 AND scope_type='project'`, tid, person); err != nil {
			t.Fatal(err)
		}
		inCeiling(t, d, wideP, func(tx pgx.Tx) {
			vis := mustEnter(t, ctx, tx, tid, wideP)
			if (vis != "{}" && vis != "") || nodeVisible(t, ctx, tx, projectM) || allows(t, ctx, tx, wideP, projectM) {
				t.Fatalf("removed grant still sees %s", vis)
			}
		})
	})
}

func inCeiling(t *testing.T, d *dbtest.DB, p tenant.Principal, fn func(pgx.Tx)) {
	t.Helper()
	ctx := tenant.WithPrincipal(t.Context(), p)
	err := db.InTenant(ctx, d.App, p.TenantID, func(tx pgx.Tx) error {
		fn(tx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mustEnter(t *testing.T, ctx context.Context, tx pgx.Tx, tid string, p tenant.Principal) string {
	t.Helper()
	var creator any
	if p.KeyCreatorID != "" {
		creator = p.KeyCreatorID
	}
	var vis string
	if err := tx.QueryRow(ctx, `SELECT aeon_enter_principal($1::uuid,$2::uuid,$3::uuid,$4::uuid)`, tid, p.ID, creator, db.ActingPersonID(ctx, p)).Scan(&vis); err != nil {
		t.Fatal(err)
	}
	return vis
}

func setting(t *testing.T, ctx context.Context, tx pgx.Tx) string {
	t.Helper()
	var vis string
	if err := tx.QueryRow(ctx, `SELECT coalesce(current_setting('aeon.visible_projects',true),'')`).Scan(&vis); err != nil {
		t.Fatal(err)
	}
	return vis
}

func allows(t *testing.T, ctx context.Context, tx pgx.Tx, p tenant.Principal, project string) bool {
	t.Helper()
	check, err := ProjectsTx(ctx, tx, p)
	if err != nil {
		t.Fatal(err)
	}
	return check("nodes.read", project)
}

func nodeVisible(t *testing.T, ctx context.Context, tx pgx.Tx, id string) bool {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM nodes WHERE id=$1::uuid`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}
