// SPDX-License-Identifier: AGPL-3.0-only
package accountuse_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/accountuse"
	"github.com/inspr-at/paimos/internal/authz"
	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/modelregistry"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fixture struct {
	d                                                                   *dbtest.DB
	p                                                                   tenant.Principal
	agent, account, defaultContext, holding, project, work, run, window string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{d: dbtest.Open(t), p: tenant.Principal{Kind: tenant.Person}}
	ctx := dbtest.Seed(t.Context())
	if err := f.d.App.QueryRow(ctx, `INSERT INTO tenants(slug,name) VALUES('matrix','Matrix') RETURNING id::text`).Scan(&f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	if err := db.InTenant(ctx, f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,roles) VALUES($1,'person','Owner','{admin}') RETURNING id::text`, f.p.TenantID).Scan(&f.p.ID); err != nil {
			return err
		}
		if err := dbtest.BindLegacyTx(ctx, tx, f.p.TenantID, f.p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'agent','Daemon') RETURNING id::text`, f.p.TenantID).Scan(&f.agent); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT (SELECT id::text FROM work_contexts WHERE kind='default'),(SELECT id::text FROM work_contexts WHERE kind='holding')`).Scan(&f.defaultContext, &f.holding)
	}); err != nil {
		t.Fatal(err)
	}
	return f
}
func (f *fixture) ctx(t *testing.T) context.Context { return tenant.WithPrincipal(t.Context(), f.p) }
func (f *fixture) in(t *testing.T, fn func(pgx.Tx) error) {
	t.Helper()
	if err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, fn); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) rules(t *testing.T, values accountuse.RuleValues) accountuse.Rules {
	t.Helper()
	var out accountuse.Rules
	if err := db.InTenant(f.ctx(t), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		before, err := accountuse.ReadRules(t.Context(), tx)
		if err != nil {
			return err
		}
		out, err = accountuse.WriteRules(t.Context(), tx, f.p, before.Revision, values)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f *fixture) addAccount(t *testing.T, key string) string {
	t.Helper()
	var id string
	f.in(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `INSERT INTO agent_accounts(tenant_id,account_key,harness,daemon_id,registered_by_principal_id,label) VALUES($1,$2,'codex','daemon',$3,'Opaque login') RETURNING id::text`, f.p.TenantID, key, f.agent).Scan(&id)
	})
	return id
}
func (f *fixture) addWork(t *testing.T, project bool) {
	t.Helper()
	f.in(t, func(tx pgx.Tx) error {
		var parent any
		if project {
			if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Project' FROM node_kinds k WHERE k.slug='project' RETURNING id::text`, f.p.TenantID).Scan(&f.project); err != nil {
				return err
			}
			parent = f.project
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Work',$2 FROM node_kinds k WHERE k.slug='work' RETURNING id::text`, f.p.TenantID, parent).Scan(&f.work); err != nil {
			return err
		}
		var order string
		if err := tx.QueryRow(t.Context(), `INSERT INTO nodes(tenant_id,kind_id,key,title,parent_id) SELECT $1,k.id,aeon_next_node_key($1,k.short_prefix),'Order',$2 FROM node_kinds k WHERE k.slug='work_order' RETURNING id::text`, f.p.TenantID, f.work).Scan(&order); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO work_orders(tenant_id,node_id,requested_by_principal_id) VALUES($1,$2,$3)`, f.p.TenantID, order, f.p.ID); err != nil {
			return err
		}
		if err := tx.QueryRow(t.Context(), `INSERT INTO agent_runs(tenant_id,work_order_id,agent_principal_id,account_id,queue_node_id) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, f.p.TenantID, order, f.agent, f.account, f.work).Scan(&f.run); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `INSERT INTO account_allowance_windows(tenant_id,account_id,starts_at,ends_at,unit,allowance,pace_model,burst_ratio) VALUES($1,$2,now(),now()+interval '1 day','requests',100,'unrestricted',1) RETURNING id::text`, f.p.TenantID, f.account).Scan(&f.window)
	})
}
func (f *fixture) call(t *testing.T, p tenant.Principal, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	accountuse.New(f.d.App).Mount(mux)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r = r.WithContext(tenant.WithPrincipal(t.Context(), p))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, w.Code, want, w.Body.String())
	}
	return w
}
func (f *fixture) save(t *testing.T, in accountuse.CellWrite, want int) accountuse.CellResult {
	t.Helper()
	w := f.call(t, f.p, "PATCH", "/api/account-use/cells", in, want)
	var out accountuse.CellResult
	if want == 200 && json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("invalid cell response")
	}
	return out
}
func checkPG(t *testing.T, err error, code, message string) {
	t.Helper()
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != code || pg.Message != message {
		t.Fatalf("expected %s %q, got %v", code, message, err)
	}
}

// Risk: new resources escape stamping, or holding/unmapped projects borrow
// project-less allowance. Every assertion uses the real NOSUPERUSER app role.
func TestAccountUseCreationRulesAndDatabaseFences(t *testing.T) {
	f := setup(t)
	f.in(t, func(tx pgx.Tx) error {
		r, err := accountuse.ReadRules(t.Context(), tx)
		if err != nil {
			return err
		}
		if r.NewAccounts != "ask" || r.NewContexts != "ask" || r.NewProjects != "default" || r.NewModels != "allow" || r.EnforcedAt != nil {
			t.Fatalf("new tenant defaults: %+v", r)
		}
		return nil
	})
	rules := f.rules(t, accountuse.RuleValues{"allow", "allow", "default", "allow"})
	f.account = f.addAccount(t, "first")
	f.addWork(t, true)
	f.in(t, func(tx pgx.Tx) error {
		var allowed bool
		if err := tx.QueryRow(t.Context(), `SELECT aeon_account_use_allowed($1,$2)`, f.account, f.run).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			t.Fatal("all-allowed tenant changed behavior")
		}
		var activated bool
		if err := tx.QueryRow(t.Context(), `SELECT enforced_at IS NOT NULL FROM account_use_rules`).Scan(&activated); err != nil {
			return err
		}
		if activated {
			t.Fatal("fully ticked creation accidentally activates rollback floor")
		}
		var stamped bool
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM account_use_cells WHERE account_id=$1 AND context_id=$2) AND EXISTS(SELECT 1 FROM project_work_contexts WHERE project_id=$3 AND context_id=$2)`, f.account, f.defaultContext, f.project).Scan(&stamped); err != nil {
			return err
		}
		if !stamped {
			t.Fatal("T3/T4 did not stamp creation")
		}
		var audit bool
		if err := tx.QueryRow(t.Context(), `SELECT count(*)=1 AND bool_and(after->>'rule'='allow') AND bool_and((after->>'rule_revision')::bigint=$2) FROM events WHERE type='account_use.changed' AND after->>'account_id'=$1`, f.account, rules.Revision).Scan(&audit); err != nil {
			return err
		}
		if !audit {
			t.Fatal("creation rule audit missing or incorrect")
		}
		return nil
	})
	rules = f.rules(t, accountuse.RuleValues{"allow", "allow", "holding", "allow"})
	f.call(t, f.p, "PUT", "/api/projects/"+f.project+"/work-context", map[string]any{"expected_revision": rules.Revision, "context_id": f.holding}, 200)
	f.in(t, func(tx pgx.Tx) error {
		var projectAllowed, projectless bool
		if err := tx.QueryRow(t.Context(), `SELECT aeon_account_use_allowed_for_project($1,$2),aeon_account_use_allowed_for_project($1,NULL)`, f.account, f.project).Scan(&projectAllowed, &projectless); err != nil {
			return err
		}
		if projectAllowed || !projectless {
			t.Fatal("holding project used project-less default")
		}
		return nil
	})
	err := db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO account_reservations(tenant_id,run_id,window_id,reserved_units) VALUES($1,$2,$3,1)`, f.p.TenantID, f.run, f.window)
		return err
	})
	checkPG(t, err, "23514", "account not allowed for work context")
	err = db.InTenant(dbtest.Seed(t.Context()), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE agent_runs SET status='starting' WHERE id=$1`, f.run)
		return err
	})
	checkPG(t, err, "23514", "account not allowed for work context")
	f.in(t, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `DELETE FROM project_work_contexts WHERE project_id=$1`, f.project); err != nil {
			return err
		}
		var allowed bool
		if err := tx.QueryRow(t.Context(), `SELECT aeon_account_use_allowed_for_project($1,$2)`, f.account, f.project).Scan(&allowed); err != nil {
			return err
		}
		if allowed {
			t.Fatal("unmapped project borrowed the default context")
		}
		return nil
	})
}

// Risk: bulk/Undo races silently overwrite another decision, or defaults
// retroactively rewrite saved cells. The HTTP response is the Undo token.
func TestAccountUseBulkAndStaleUndo(t *testing.T) {
	f := setup(t)
	rules := f.rules(t, accountuse.RuleValues{"allow", "allow", "default", "allow"})
	f.account = f.addAccount(t, "first")
	second := f.addAccount(t, "second")
	saved := f.save(t, accountuse.CellWrite{ExpectedRevision: rules.Revision, Bulk: &accountuse.Bulk{Scope: "all", Allowed: false}}, 200)
	if len(saved.Changes) != 2 || len(saved.Undo) != 2 {
		t.Fatal("bulk inverse did not capture every cell")
	}
	undo := accountuse.CellWrite{ExpectedRevision: saved.Revision, Changes: saved.Undo}
	first := f.save(t, undo, 200)
	f.save(t, undo, 409)
	f.save(t, accountuse.CellWrite{ExpectedRevision: first.Revision, Changes: []accountuse.Cell{{AccountID: second, ContextID: f.holding, Allowed: true}}}, 404)
	var before int
	f.in(t, func(tx pgx.Tx) error {
		return tx.QueryRow(t.Context(), `SELECT count(*) FROM account_use_cells`).Scan(&before)
	})
	f.rules(t, accountuse.RuleValues{"ask", "ask", "default", "allow"})
	third := f.addAccount(t, "third")
	f.in(t, func(tx pgx.Tx) error {
		var after int
		var thirdCells int
		if err := tx.QueryRow(t.Context(), `SELECT count(*),count(*) FILTER(WHERE account_id=$1) FROM account_use_cells`, third).Scan(&after, &thirdCells); err != nil {
			return err
		}
		if before != after || thirdCells != 0 {
			t.Fatal("ask-first widened new account or rewrote old decisions")
		}
		var activated bool
		if err := tx.QueryRow(t.Context(), `SELECT enforced_at IS NOT NULL FROM account_use_rules`).Scan(&activated); err != nil {
			return err
		}
		if !activated {
			t.Fatal("new account gap did not activate")
		}
		return nil
	})
	worker := tenant.Principal{ID: f.agent, TenantID: f.p.TenantID, Kind: tenant.Agent}
	f.call(t, worker, "PUT", "/api/account-use/rules", map[string]any{}, 403)
}

// Risk: a stale person retains matrix authority while waiting behind revocation.
// Real backend barriers prove the tenant fence precedes the matrix lock.
func TestAccountUseFenceOrderAndRevocationDuringBlockedWrite(t *testing.T) {
	f := setup(t)
	rules := f.rules(t, accountuse.RuleValues{"allow", "allow", "default", "allow"})
	f.account = f.addAccount(t, "first")
	in := accountuse.CellWrite{ExpectedRevision: rules.Revision, Changes: []accountuse.Cell{{AccountID: f.account, ContextID: f.defaultContext, Allowed: true}}}
	dbtest.TenantBeforeAdvisory(t, f.d, f.p.TenantID, "aeon-account-use:"+f.p.TenantID, 0, func(ctx context.Context) error {
		return db.InTenant(tenant.WithPrincipal(ctx, f.p), f.d.App, f.p.TenantID, func(tx pgx.Tx) error { _, err := accountuse.WriteCells(ctx, tx, f.p, in); return err })
	})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	holder, err := f.d.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.Background())
	var holderPID int
	if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE`, f.p.TenantID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- db.InTenant(tenant.WithPrincipal(ctx, f.p), f.d.App, f.p.TenantID, func(tx pgx.Tx) error { _, err := accountuse.WriteCells(ctx, tx, f.p, in); return err })
	}()
	pid := dbtest.WaitForLock(t, ctx, f.d, uint32(holderPID), "transactionid")
	if err := dbtest.WaitForBlocked(ctx, f.d.Admin, int(pid), holderPID, db.TenantFenceSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `DELETE FROM role_bindings WHERE tenant_id=$1 AND principal_id=$2`, f.p.TenantID, f.p.ID); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Await(t, ctx, done); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("revocation missed under fence: %v", err)
	}
}

// Risk: a models.manage-only role changes the model default before activation,
// through either the current API or a complete legacy update transaction.
func TestAccountUseCanonicalModelRuleWriterBeforeActivation(t *testing.T) {
	f := setup(t)
	f.in(t, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `INSERT INTO model_refresh_settings(tenant_id) VALUES($1)`, f.p.TenantID)
		return err
	})
	limited := f.p
	f.in(t, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `INSERT INTO principals(tenant_id,kind,name) VALUES($1,'person','Model manager') RETURNING id::text`, f.p.TenantID).Scan(&limited.ID); err != nil {
			return err
		}
		var role string
		if err := tx.QueryRow(t.Context(), `INSERT INTO roles(tenant_id,key,name) VALUES($1,'model_only','Models only') RETURNING id::text`, f.p.TenantID).Scan(&role); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `INSERT INTO role_permissions(tenant_id,role_id,permission) VALUES($1,$2,'models.manage')`, f.p.TenantID, role); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type) VALUES($1,$2,$3,'workspace')`, f.p.TenantID, limited.ID, role)
		return err
	})
	mux := http.NewServeMux()
	modelregistry.New(f.d.App).Mount(mux)
	body := `{"agent_reports_enabled":true,"auto_add_profiles":false,"api_enabled":false,"interval_minutes":1440,"account_use_revision":1}`
	r := httptest.NewRequest("PUT", "/api/models/refresh/settings", strings.NewReader(body))
	r = r.WithContext(tenant.WithPrincipal(t.Context(), limited))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("models-only current API: %d %s", w.Code, w.Body.String())
	}
	err := db.InTenant(tenant.WithPrincipal(t.Context(), limited), f.d.App, f.p.TenantID, func(tx pgx.Tx) error {
		if err := authz.RequireTx(t.Context(), tx, limited, "models.manage", authz.Scope{}); err != nil {
			return err
		}
		if _, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET auto_add_profiles=false`); err != nil {
			return err
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'legacy.settings','{}')`, f.p.TenantID, limited.ID)
		return err
	})
	checkPG(t, err, "42501", "canonical account-use rule writer required")
	f.in(t, func(tx pgx.Tx) error {
		var mirror, clean bool
		if err := tx.QueryRow(t.Context(), `SELECT (SELECT auto_add_profiles FROM model_refresh_settings),NOT EXISTS(SELECT 1 FROM events WHERE type='legacy.settings') AND (SELECT enforced_at IS NULL FROM account_use_rules)`).Scan(&mirror, &clean); err != nil {
			return err
		}
		if !mirror || !clean {
			t.Fatal("failed legacy write leaked mirror/event or activated tenant")
		}
		return nil
	})
	rules := f.rules(t, accountuse.RuleValues{"ask", "ask", "default", "shipped_only"})
	if rules.Revision != 2 || rules.EnforcedAt != nil {
		t.Fatal("canonical writer revision/activation wrong")
	}
	f.in(t, func(tx pgx.Tx) error {
		var mirror, audit, consumed bool
		if err := tx.QueryRow(t.Context(), `SELECT NOT (SELECT auto_add_profiles FROM model_refresh_settings),EXISTS(SELECT 1 FROM events WHERE type='account_use.rules_changed' AND after->>'new_models'='shipped_only' AND after->>'revision'='2'),coalesce(current_setting('aeon.account_use_rule_write',true),'')<>'on'`).Scan(&mirror, &audit, &consumed); err != nil {
			return err
		}
		if !mirror || !audit || !consumed {
			t.Fatal("canonical mirror/audit/one-shot did not persist correctly")
		}
		_, err := tx.Exec(t.Context(), `UPDATE model_refresh_settings SET auto_add_profiles=false`)
		return err
	})
}

// Risk: PostgreSQL skips a row policy for an empty pool, or a downgraded
// principal obtains executable data. The entry guard is unconditional.
func TestAccountUseLegacyEntryGuardEmptyAndPopulatedPools(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprint(populated), func(t *testing.T) {
			f := setup(t)
			if populated {
				f.rules(t, accountuse.RuleValues{"allow", "allow", "default", "allow"})
				f.account = f.addAccount(t, "first")
			}
			legacy := func() error {
				tx, err := f.d.App.Begin(t.Context())
				if err != nil {
					return err
				}
				defer tx.Rollback(t.Context())
				if _, err := tx.Exec(t.Context(), `SELECT set_config('aeon.account_use_capable','',true)`); err != nil {
					return err
				}
				if _, err := tx.Exec(t.Context(), `SELECT aeon_enter_principal($1,$2,NULL)`, f.p.TenantID, f.p.ID); err != nil {
					return err
				}
				var n int
				return tx.QueryRow(t.Context(), `SELECT count(*) FROM agent_accounts`).Scan(&n)
			}
			if err := legacy(); err != nil {
				t.Fatalf("nonactivated old entry changed behavior: %v", err)
			}
			f.rules(t, accountuse.RuleValues{"ask", "ask", "default", "deny"})
			checkPG(t, legacy(), "0A000", "account-use capability required: this binary is below the rollback floor")
			f.in(t, func(tx pgx.Tx) error {
				var flag bool
				if err := tx.QueryRow(t.Context(), `SELECT current_setting('aeon.account_use_capable')='on'`).Scan(&flag); err != nil {
					return err
				}
				if !flag {
					t.Fatal("candidate entry omitted capability")
				}
				return nil
			})
			if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO aeon_required_capabilities(capability) VALUES('future_unknown')`); err != nil {
				t.Fatal(err)
			}
			if err := db.CheckRequiredCapabilities(t.Context(), f.d.App); err == nil {
				t.Fatal("boot accepted an unsupported capability")
			}
		})
	}
}
