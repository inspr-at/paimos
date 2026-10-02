// SPDX-License-Identifier: AGPL-3.0-only
package auth

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWorkstationPromptShowsCanonicalQueryAndBothRoles(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	var source, replacement string
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='member'`).Scan(&source); err != nil {
			return err
		}
		return tx.QueryRow(t.Context(), `SELECT id::text FROM roles WHERE key='viewer'`).Scan(&replacement)
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/roles/" + source + "?z=2&reassign_to=" + replacement + "&a=1"
	c := f.challenge(t, "DELETE", path, "")
	for _, want := range []string{"?a=1&reassign_to=" + replacement + "&z=2", `Target: "Member"`, `Reassign to: "Viewer"`} {
		if !strings.Contains(c.Summary, want) {
			t.Errorf("prompt omitted %q", want)
		}
	}
	// The display is canonical; the signature still binds the exact original URI.
	changed := strings.Replace(path, replacement, source, 1)
	if w := f.call(f.key.Token, "DELETE", changed, "", workstationProof(t, f.signer, c)); w.Code != 403 {
		t.Fatalf("changed reassignment accepted: %d", w.Code)
	}
	// Repeated query values retain their order: handlers use the first value.
	c = f.challenge(t, "DELETE", "/api/roles/"+source+"?reassign_to="+replacement+"&x=2&x=1", "")
	if !strings.Contains(c.Summary, "&x=2&x=1") {
		t.Fatal("prompt dropped or reordered repeated query values")
	}
}

func TestWorkstationPromptRejectsIncompleteTarget(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `UPDATE principals SET name=$2 WHERE id=$1`, f.owner.ID, "Stored "+strings.Repeat("界", 150))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/members/" + f.owner.ID + "/deactivate",
		"/api/roles/" + f.owner.ID + "?reassign_to=" + f.owner.ID + "&extra=" + strings.Repeat("x", 160),
		"/api/roles/" + f.owner.ID + "?reassign_to=%zz",
	} {
		w := f.call(f.key.Token, "DELETE", path, "", "")
		if strings.Contains(path, "/deactivate") {
			w = f.call(f.key.Token, "POST", path, "", "")
		}
		if w.Code != 400 {
			t.Errorf("incomplete or invalid prompt status: %d, want 400", w.Code)
		}
	}
	if err := db.InTenant(t.Context(), f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM owner_workstation_challenges`).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Error("failed prompt left a signable challenge")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWorkstationMembershipAccessNeedsConfirmation(t *testing.T) {
	f := newWorkstationFixture(t)
	f.enable(t)
	ctx := dbtest.Seed(t.Context())
	var member, project, viewer, developer string
	if err := db.InTenant(ctx, f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO principals(tenant_id,kind,name,status) VALUES($1,'person','Member','deactivated') RETURNING id::text`, f.owner.TenantID).Scan(&member); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO nodes(tenant_id,kind_id,key,title) SELECT $1,id,'R3-1','Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project' RETURNING id::text`, f.owner.TenantID).Scan(&project); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE key='viewer'`).Scan(&viewer); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT id::text FROM roles WHERE key='member'`).Scan(&developer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO role_bindings(tenant_id,principal_id,role_id,scope_type,scope_id) VALUES($1,$2,$3,'project',$4)`, f.owner.TenantID, member, viewer, project)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, method, path, body, before, after string }{
		{"reactivate", "POST", "/api/members/" + member + "/reactivate", "", "deactivated", "active"},
		{"replace project member", "PUT", "/api/projects/" + project + "/members/" + member, `{"role_id":"` + developer + `"}`, viewer, developer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func() string {
				t.Helper()
				var state string
				if err := db.InTenant(ctx, f.m.pool, f.owner.TenantID, func(tx pgx.Tx) error {
					if tc.method == "POST" {
						return tx.QueryRow(ctx, `SELECT status FROM principals WHERE id=$1`, member).Scan(&state)
					}
					return tx.QueryRow(ctx, `SELECT role_id::text FROM role_bindings WHERE principal_id=$1 AND scope_id=$2`, member, project).Scan(&state)
				}); err != nil {
					t.Fatal(err)
				}
				return state
			}
			w := f.call(f.key.Token, tc.method, tc.path, tc.body, "")
			if state := read(); state != tc.before {
				t.Errorf("unconfirmed membership change: %s", tc.name)
			}
			c := f.readChallenge(t, w)
			if w := f.call(f.key.Token, tc.method, tc.path, tc.body, workstationProof(t, f.signer, c)); w.Code != 200 {
				t.Fatalf("confirmed membership change: %d", w.Code)
			}
			if state := read(); state != tc.after {
				t.Fatal("confirmed membership change did not persist")
			}
		})
	}
}

type workstationLockProbeTx struct {
	pgx.Tx
	before func(string)
}

func (tx workstationLockProbeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	tx.before(sql)
	return tx.Tx.QueryRow(ctx, sql, args...)
}

func (tx workstationLockProbeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	tx.before(sql)
	return tx.Tx.Query(ctx, sql, args...)
}

func (tx workstationLockProbeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tx.before(sql)
	return tx.Tx.Exec(ctx, sql, args...)
}

func TestWorkstationMarkLocksPairingAndComputerBeforeKey(t *testing.T) {
	f := newWorkstationFixture(t)
	for _, mark := range []bool{true, false} {
		t.Run(map[bool]string{true: "mark", false: "unmark"}[mark], func(t *testing.T) {
			keyLocks, events := 0, 0
			f.m.inTenant = func(ctx context.Context, pool *pgxpool.Pool, tid string, fn func(pgx.Tx) error) error {
				return db.InTenant(ctx, pool, tid, func(tx pgx.Tx) error {
					return fn(workstationLockProbeTx{Tx: tx, before: func(sql string) {
						if events != 0 {
							t.Error("database work after event append")
						}
						if strings.Contains(sql, "INSERT INTO events") {
							events++
						}
						if !strings.Contains(sql, "FROM agent_keys") || !strings.Contains(sql, "FOR UPDATE") {
							return
						}
						keyLocks++
						// Independent transactions probe real locks at the key-lock
						// boundary. NOWAIT avoids timing assumptions and deadlocks.
						for _, probe := range []struct{ name, sql, id string }{
							{"tenant", `SELECT id FROM tenants WHERE id=$1 FOR NO KEY UPDATE NOWAIT`, tid},
							{"computer", `SELECT id FROM agent_pairing_computers WHERE id=$1 FOR UPDATE NOWAIT`, f.computer},
						} {
							err := db.InTenant(ctx, pool, tid, func(other pgx.Tx) error {
								_, err := other.Exec(ctx, probe.sql, probe.id)
								return err
							})
							var pgErr *pgconn.PgError
							if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
								t.Errorf("%s not locked before key: %v", probe.name, err)
							}
						}
						if err := db.InTenant(ctx, pool, tid, func(other pgx.Tx) error {
							var acquired bool
							if err := other.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('aeon-pairing:'||$1::text,0))`, tid).Scan(&acquired); err != nil {
								return err
							}
							if acquired {
								t.Error("pairing not locked before key")
							}
							return nil
						}); err != nil {
							t.Fatal(err)
						}
					}})
				})
			}
			computer := ""
			if mark {
				computer = f.computer
			}
			if w := f.mark(f.owner, f.key.ID, computer, mark); w.Code != 200 {
				t.Fatalf("mark=%v: %d", mark, w.Code)
			}
			if keyLocks != 1 || events != 1 {
				t.Fatalf("probe did not observe key lock and event: %d, %d", keyLocks, events)
			}
		})
	}
}

func TestWorkstationCanonicalQueryPreservesValueOrder(t *testing.T) {
	r := httptest.NewRequest("DELETE", "/api/roles/fixture?z=last&a=first&a=second&blank=", nil)
	r.Pattern = "DELETE /api/roles/{id}"
	_, summary := workstationAction(workstationFixture{}.owner, r, nil)
	if !strings.Contains(summary, "?a=first&a=second&blank=&z=last") {
		t.Fatal("query display is missing or not canonical")
	}
}
