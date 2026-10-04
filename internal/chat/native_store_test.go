// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"context"
	"crypto/sha256"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
	"github.com/inspr-at/paimos/internal/dbtest"
	"github.com/inspr-at/paimos/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Deliberately seed a historical registration that predates the database guard.
// Only this trigger is disabled, in this isolated fixture transaction; every
// original denial assertion still exercises the production handler unchanged.
func (f *fixture) preGuardMutation(t *testing.T, sql string, args ...any) {
	t.Helper()
	tx, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `ALTER TABLE harness_sessions DISABLE TRIGGER chat_registration_store`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), sql, args...); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `ALTER TABLE harness_sessions ENABLE TRIGGER chat_registration_store`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) replyObligation(t *testing.T, session string) string {
	t.Helper()
	var event int64
	if err := f.d.Admin.QueryRow(t.Context(), `INSERT INTO events(tenant_id,actor_principal_id,type,after) VALUES($1,$2,'inbox.sent','{}') RETURNING id`, f.alice.TenantID, f.alice.ID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	message := uid()
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO inbox_compat_messages(tenant_id,id,project_id,sender_principal_id,recipient_principal_id,recipient_session_id,recipient_address,body,key_digest,request_digest,sent_event_id,is_action_request,expects_reply,delivery_level)
        VALUES($1,$2::text::uuid,$3,$4,$5,$6,'paimos:fixture','Synthetic obligation',$2::text,$2::text,$7,true,true,'simple')`, f.alice.TenantID, message, f.project, f.alice.ID, f.agent.ID, session, event); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admin.Exec(t.Context(), `INSERT INTO inbox_reply_obligations(tenant_id,message_id) VALUES($1,$2)`, f.alice.TenantID, message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestNativeStorePrebindingTransitiveAliases(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, conflict := range []string{"person", "role", "project"} {
			t.Run(strconv.FormatBool(reverse)+"/"+conflict, func(t *testing.T) {
				f := newFixture(t)
				thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
				refs := []string{"chain-a-" + uid(), "chain-b-" + uid(), "chain-c-" + uid()}
				for i := 0; i < 2; i++ {
					ref, vendor := refs[i], refs[i+1]
					if reverse {
						ref, vendor = vendor, ref
					}
					id, _ := f.registerNative(t, f.alice, f.project, ref, vendor)
					f.stopNative(t, id)
				}
				first, _ := f.registerNative(t, f.alice, f.project, refs[0], "")
				bound := f.bind(t, f.alice, thread, first, "0")
				f.nativeClaims(t, thread, 3) // No earlier generation is replayed.
				owner, project := f.alice, f.project
				if conflict == "person" {
					owner = f.bob
				}
				if conflict == "project" {
					project = f.secondProject
				}
				if conflict == "role" {
					target := f.thread(t, owner, f.role(t, owner, project, "worker", "other"))
					next, _ := f.registerNative(t, owner, project, refs[2], "")
					w := f.call(owner, "POST", "/api/chat-threads/"+target.ID+"/binding", map[string]string{"session_id": next, "expected_epoch": "0"}, "")
					expect(t, w, http.StatusNotFound)
					if !strings.Contains(w.Body.String(), "chat binding unavailable") {
						t.Fatal("wrong denial reason")
					}
					f.stopNative(t, next)
				} else {
					w := f.replayNative(owner, project, refs[2], "conflict-lease-"+uid(), "")
					expect(t, w, http.StatusConflict)
					if !strings.Contains(w.Body.String(), "chat binding unavailable") {
						t.Fatal("wrong denial reason")
					}
				}
				next, lease := f.registerNative(t, f.alice, f.project, refs[2], "")
				resumed := f.bind(t, f.alice, bound, next, "1")
				if resumed.ID != thread.ID || resumed.BindingEpoch != "2" {
					t.Fatal("legitimate continuation lost history")
				}
				decode[Thread](t, f.call(f.agent, "POST", "/api/chat-deliveries/binding/resolve", WorkerBindingRequest{thread.ID, next, "2"}, lease))
			})
		}
	}
}

func nativeDigest(ref string) []byte {
	sum := sha256.Sum256([]byte("aeon.harness.ref\x00" + ref))
	return sum[:]
}

func TestNativeStoreDatabaseGuardDeniesBypass(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	ref := "raw-native-" + uid()
	first, _ := f.registerNative(t, f.alice, f.project, ref, "")
	f.bind(t, f.alice, thread, first, "0")
	foreign, _ := f.registerNative(t, f.bob, f.project, "foreign-native-"+uid(), "")
	f.stopNative(t, first)
	tests := []struct {
		name, sql string
		args      []any
	}{
		{"create", `INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,lease_digest) VALUES($1,$2,$3,$4,'claude','fixture','unmanaged','worker',$5,$6)`, []any{f.alice.TenantID, f.project, f.agent.ID, f.bob.ID, nativeDigest(ref), nativeDigest(uid())}},
		{"replay_vendor", `UPDATE harness_sessions SET vendor_ref_digest=$2 WHERE id=$1`, []any{foreign, nativeDigest(ref)}},
		{"rename_ref", `UPDATE harness_sessions SET ref_digest=$2 WHERE id=$1`, []any{foreign, nativeDigest(ref)}},
		{"rename_through_observation_encoding", `UPDATE harness_sessions SET ref_digest=$2 WHERE id=$1`, []any{first, []byte(strings.Repeat("0", 64))}},
		{"malformed_vendor_encoding", `UPDATE harness_sessions SET vendor_ref_digest=$2 WHERE id=$1`, []any{first, []byte(strings.Repeat("0", 64))}},
		{"transfer_person", `UPDATE harness_sessions SET owner_principal_id=$2 WHERE id=$1`, []any{first, f.bob.ID}},
		{"transfer_project", `UPDATE harness_sessions SET project_id=$2 WHERE id=$1`, []any{first, f.secondProject}},
		{"rename_harness", `UPDATE harness_sessions SET harness='codex' WHERE id=$1`, []any{first}},
		{"raw_claim", `INSERT INTO chat_native_contexts(tenant_id,harness,ref_digest,role_id,owner_person_id,project_id) VALUES($1,'claude',$2,$3,$4,$5)`, []any{f.alice.TenantID, nativeDigest(uid()), thread.Role.ID, f.alice.ID, f.project}},
		{"raw_link", `INSERT INTO chat_native_aliases(tenant_id,harness,ref_digest,alias_digest) VALUES($1,'claude',$2,$2)`, []any{f.alice.TenantID, nativeDigest(uid())}},
		{"snapshot_bind", `INSERT INTO chat_session_contexts(tenant_id,session_id,role_id,owner_person_id,project_id,harness,ref_digest) VALUES($1,$2,$3,$4,$5,'claude',$6)`, []any{f.alice.TenantID, foreign, thread.Role.ID, f.alice.ID, f.project, nativeDigest(ref)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := db.InTenant(tenant.WithPrincipal(t.Context(), f.alice), f.d.App, f.alice.TenantID, func(tx pgx.Tx) error {
				// Deliberately skip every Go application store call. SQL must guard it.
				_, err := tx.Exec(t.Context(), test.sql, test.args...)
				return err
			})
			var pg *pgconn.PgError
			if !errors.As(err, &pg) || pg.Code != "23514" || pg.ConstraintName != "chat_native_owner" {
				t.Fatalf("wrong guard error: %v", err)
			}
		})
	}
	f.nativeClaims(t, thread, 1)
}

func TestNativeStoreRenameRetainsAllHistoricalAliases(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	refs := []string{"rename-a-" + uid(), "rename-b-" + uid(), "rename-c-" + uid()}
	id, _ := f.registerNative(t, f.alice, f.project, refs[0], refs[1])
	f.bind(t, f.alice, thread, id, "0")
	if _, err := f.d.Admin.Exec(t.Context(), `UPDATE harness_sessions SET ref_digest=$2,vendor_ref_digest=NULL WHERE id=$1`, id, nativeDigest(refs[2])); err != nil {
		t.Fatal(err)
	}
	f.nativeClaims(t, thread, 3)
	f.stopNative(t, id)
	for _, ref := range refs {
		w := f.replayNative(f.bob, f.project, ref, "rename-conflict-"+uid(), "")
		expect(t, w, http.StatusConflict)
		if !strings.Contains(w.Body.String(), "chat binding unavailable") {
			t.Fatal("wrong denial reason")
		}
	}
}

// Exhaustive writer inventory for the requested roots, plus chat itself. A new
// SQL writer requires explicit review here; even an unlisted writer is guarded
// by the database tests above. This inspects Go AST literals, not line layout.
func TestNativeStoreWriterInventory(t *testing.T) {
	pattern := regexp.MustCompile(`(?is)(insert\s+into\s+(harness_sessions|chat_native_contexts|chat_native_aliases|chat_session_contexts|chat_session_bindings)\b|update\s+harness_sessions\s+set\s+[^;\x60]*?\b(ref_digest|vendor_ref_digest|owner_principal_id|project_id|harness)\s*=)`)
	found := map[string]bool{}
	for _, root := range []string{"harness", "inbox", "agentpairing", "agentd", "chat"} {
		err := filepath.WalkDir(filepath.Join("..", root), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				ast.Inspect(fn, func(node ast.Node) bool {
					lit, ok := node.(*ast.BasicLit)
					if ok && lit.Kind == token.STRING {
						value, e := strconv.Unquote(lit.Value)
						if e == nil && pattern.MatchString(value) {
							found[root+"/"+filepath.Base(path)+":"+fn.Name.Name] = true
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	actual := []string{}
	for path := range found {
		actual = append(actual, path)
	}
	sort.Strings(actual)
	expected := []string{"agentpairing/watch.go:attachDevice", "chat/module.go:bindThread", "harness/module.go:fillVendorRef", "harness/module.go:registerBeforeEvents"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("native writer inventory changed; audit store/trigger coverage: got %v want %v", actual, expected)
	}
}

func TestNativeStoreComponentBoundAndVisibilityRestore(t *testing.T) {
	f := newFixture(t)
	thread := f.thread(t, f.alice, f.role(t, f.alice, f.project, "worker", "original"))
	ref := "bounded-native-" + uid()
	id, _ := f.registerNative(t, f.alice, f.project, ref, "")
	// Simulate a large pre-chat migration graph in one isolated fixture write.
	tx, err := f.d.Admin.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), `SELECT set_config('aeon.chat_native_store','on',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(t.Context(), `INSERT INTO chat_native_aliases(tenant_id,harness,ref_digest,alias_digest)
        SELECT $1,'claude',least($2::bytea,decode(md5(i::text)||md5(i::text),'hex')),greatest($2::bytea,decode(md5(i::text)||md5(i::text),'hex')) FROM generate_series(1,1024) i`, f.alice.TenantID, nativeDigest(ref)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	w := f.call(f.alice, "POST", "/api/chat-threads/"+thread.ID+"/binding", map[string]string{"session_id": id, "expected_epoch": "0"}, "")
	expect(t, w, http.StatusNotFound)
	if !strings.Contains(w.Body.String(), "chat binding unavailable") {
		t.Fatal("wrong bound failure")
	}
	f.nativeClaims(t, thread, 0)
	// The store may open claim visibility only during its internal operation.
	otherRef := "private-native-" + uid()
	otherThread := f.thread(t, f.bob, f.role(t, f.bob, f.project, "worker", "other"))
	other, _ := f.registerNative(t, f.bob, f.project, otherRef, "")
	f.bind(t, f.bob, otherThread, other, "0")
	err = db.InTenant(tenant.WithPrincipal(t.Context(), f.alice), f.d.App, f.alice.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(t.Context(), `SELECT aeon_store_chat_native($1,'claude',ARRAY[$2::bytea],$3,$4)`, f.alice.TenantID, nativeDigest(uid()), f.alice.ID, f.project); err != nil {
			return err
		}
		var visible int
		if err := tx.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts WHERE role_id=$1`, otherThread.Role.ID).Scan(&visible); err != nil {
			return err
		}
		if visible != 0 {
			t.Fatal("store leaked another person's native claims")
		}
		var store string
		if err := tx.QueryRow(t.Context(), `SELECT coalesce(current_setting('aeon.chat_native_store',true),'')`).Scan(&store); err != nil {
			return err
		}
		if store != "" {
			t.Fatal("store scope not restored")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeStoreMigrationPreservesPreChatLinks(t *testing.T) {
	d, err := dbtest.NewUnmigrated(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Error(err)
		}
	})
	tid, person, agent, project, role := uid(), uid(), uid(), uid(), uid()
	a, b := nativeDigest("before-chat-a"), nativeDigest("before-chat-b")
	err = db.MigrateWithHook(t.Context(), d.App, func(name string) error {
		if name != "1141_chat_identity.sql" {
			return nil
		}
		queries := []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO tenants(id,slug,name) VALUES($1::uuid,$1::uuid::text,'Pre-chat fixture')`, []any{tid}},
			{`INSERT INTO principals(tenant_id,id,kind,name) VALUES($1,$2,'person','Person'),($1,$3,'agent','Agent')`, []any{tid, person, agent}},
			{`INSERT INTO nodes(tenant_id,id,key,kind_id,title) SELECT $1,$2,'BEFORE-1',id,'Project' FROM node_kinds WHERE tenant_id=$1 AND slug='project'`, []any{tid, project}},
			{`INSERT INTO harness_sessions(tenant_id,project_id,agent_principal_id,owner_principal_id,harness,host,management,role,ref_digest,vendor_ref_digest,lease_digest) VALUES($1,$2,$3,$4,'claude','fixture','unmanaged','worker',$5,$6,$5)`, []any{tid, project, agent, person, a, b}},
		}
		for _, q := range queries {
			if _, err := d.Admin.Exec(t.Context(), q.sql, q.args...); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Admin.Exec(t.Context(), `INSERT INTO chat_roles(tenant_id,id,owner_person_id,project_id,kind,slot_key) VALUES($1,$2,$3,$4,'worker','original')`, tid, role, person, project); err != nil {
		t.Fatal(err)
	}
	err = db.InTenant(dbtest.Seed(t.Context()), d.App, tid, func(tx pgx.Tx) error {
		_, err := tx.Exec(t.Context(), `SELECT aeon_store_chat_native($1,'claude',ARRAY[$2::bytea],$3,$4,$5)`, tid, a, person, project, role)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts WHERE tenant_id=$1 AND role_id=$2 AND ref_digest=ANY($3::bytea[])`, tid, role, [][]byte{a, b}).Scan(&count); err != nil || count != 2 {
		t.Fatalf("pre-chat alias ownership count=%d: %v", count, err)
	}
}

func TestNativeStoreUnownedHarnessChangesRemainCompatible(t *testing.T) {
	f := newFixture(t)
	id, _ := f.registerNative(t, f.alice, f.project, "unowned-adapter-"+uid(), "")
	for _, adapter := range []string{"codex", "cursor", "pi", "grok", "claude"} {
		err := db.InTenant(tenant.WithPrincipal(t.Context(), f.alice), f.d.App, f.alice.TenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(t.Context(), `UPDATE harness_sessions SET harness=$2 WHERE id=$1`, id, adapter)
			return err
		})
		if err != nil {
			t.Fatalf("unowned adapter %s: %v", adapter, err)
		}
	}
	var claims int
	if err := f.d.Admin.QueryRow(t.Context(), `SELECT count(*) FROM chat_native_contexts`).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("unbound adapter changes invented ownership: %d %v", claims, err)
	}
}
