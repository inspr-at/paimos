package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestMigration178PreservesMultilineBodyBytes(t *testing.T) {
	database, _ := openM165Fixture(t, 177)
	project, sender := seedM165ProjectAgent(t, database, "M178", "sender")
	result, err := database.Exec(`INSERT INTO project_agents(project_id,name) VALUES(?,'receiver')`, project)
	if err != nil {
		t.Fatal(err)
	}
	receiver, _ := result.LastInsertId()
	insert := func(body string) (sql.Result, error) {
		return database.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,message_id) VALUES(?,?,?,lower(hex(randomblob(16))))`, sender, receiver, body)
	}
	if _, err := insert("first\nsecond\n"); err == nil {
		t.Fatal("M177 fixture unexpectedly accepts multiline")
	}
	if err := migrateThrough(database, 178); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"first\n", "first\r\n", "  first\n\nsecond\n", "first\r\n\r\nsecond\r\n"} {
		result, err := insert(body)
		if err != nil {
			t.Fatalf("ordinary multiline body rejected: %v", err)
		}
		id, _ := result.LastInsertId()
		var got string
		if err := database.QueryRow(`SELECT body FROM agent_messages WHERE id=?`, id).Scan(&got); err != nil || got != body {
			t.Fatalf("body changed: got=%q err=%v", got, err)
		}
	}
	for _, body := range []string{"first\x00second\n", "first\x00", "Bearer\nabcdefgh1234", "api_\r\nkey: example", "sk-proj-abcdef\nghijklmnop", "-----BE\nGIN PRIVATE KEY-----"} {
		if _, err := insert(body); err == nil || !strings.Contains(err.Error(), "paimos_message_body_contains_secret_like") {
			t.Fatalf("message SQL backstop failed: err=%v", err)
		}
	}
	var scalar int
	if err := database.QueryRow(`SELECT paimos_contains_secret_like(CAST(? AS BLOB))`, "ordinary\ntext").Scan(&scalar); err != nil || scalar != 1 {
		t.Fatalf("strict scalar SQL guard changed: result=%d err=%v", scalar, err)
	}
}

func seedMigration178Ledger(t *testing.T) (*sql.DB, string) {
	t.Helper()
	database, path := openM165Fixture(t, 177)
	database.SetMaxOpenConns(1)
	project, sender := seedM165ProjectAgent(t, database, "M178", "sender")
	_, receiver := seedM165ProjectAgentInProject(t, database, project, "receiver")
	exec := func(statement string, args ...any) {
		t.Helper()
		if _, err := database.Exec(statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`PRAGMA foreign_keys=ON`)
	exec(`INSERT INTO users(id,username,password,role,status) VALUES(178,'m178-human','fixture','member','active')`)
	exec(`INSERT INTO agent_message_targets(id,instance,project_id,address,adapter,target_kind,target_ref_cipher,maximum_level,role,enabled,version)
		VALUES('target178','test',?,'codex:receiver','codex','codex_thread',zeroblob(29),'simple','primary',1,1)`, project)
	exec(`INSERT INTO agent_messages(id,from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,from_address,to_address,expects_reply,delivery_primary_target_id)
		VALUES(41,?,?,'original',1,'2026-09-01T10:00:00.000Z','message178','paimos:sender','codex:receiver',1,'target178')`, sender, receiver)
	exec(`INSERT INTO agent_messages(id,from_agent_id,to_agent_id,body,delivered,delivered_at,message_id,parent_message_id,reply_to,from_address,to_address)
		VALUES(42,?,?,'reply',1,'2026-09-01T10:00:00.000Z','reply178',41,'message178','codex:receiver','paimos:sender')`, receiver, sender)
	exec(`INSERT INTO agent_messages(id,from_agent_id,to_agent_id,body,message_id,is_action_request)
		VALUES(43,?,?,'held','held178',1)`, sender, receiver)
	exec(`INSERT INTO agent_message_deliveries(delivery_id,message_row_id,instance,primary_target_id,requested_level,state,consumer_fence)
		VALUES('delivery178',41,'test','target178','simple','pending',7)`)
	exec(`INSERT INTO agent_message_idempotency(instance,project_id,sender_agent_id,key_digest,request_digest,message_row_id)
		VALUES('test',?,?,zeroblob(32),zeroblob(32),41)`, project, sender)
	exec(`INSERT INTO agent_message_cursors(project_id,project_agent_id,address,cursor,consumer_fence) VALUES(?,?,'codex:receiver',41,7)`, project, receiver)
	exec(`INSERT INTO agent_reply_obligations(message_row_id,project_id,sender_agent_id,next_attention_at)
		VALUES(41,?,?,'2030-01-01T00:00:00.000Z')`, project, sender)
	exec(`INSERT INTO agent_message_human_resolutions(resolution_id,message_row_id,project_id,outcome,actor_user_id,actor_session_id,instance,idempotency_key_digest,request_digest)
		VALUES('11111111-1111-4111-8111-111111111178',43,?,'dismissed',178,'fixture-session','test',zeroblob(32),zeroblob(32))`, project)
	exec(`INSERT INTO lifecycle_runtimes(id,project_id,generation,machine_id,user_id,api_key_id,lease_digest,registration_json,expires_at,created_at)
		VALUES('runtime178',?,'generation178','fixture',178,1,zeroblob(32),'{}','2030-01-01','2026-09-01')`, project)
	exec(`INSERT INTO lifecycle_runtime_sessions(session_id,runtime_id,generation,created_at) VALUES('session178','runtime178','session-generation178','2026-09-01')`)
	exec(`INSERT INTO agent_consumer_streams(id,project_id,agent_id,address,kind,revision,generation,registration_json,runtime_id,runtime_generation,session_id,session_generation,user_id,api_key_id,target_id,target_version,lease_digest,expires_at)
		VALUES('stream178',?,?,'codex:receiver','fallback',7,'consumer178','{}','runtime178','generation178','session178','session-generation178',178,1,'target178',1,zeroblob(32),'2030-01-01')`, project, receiver)
	exec(`INSERT INTO agent_consumer_attempts(id,stream_id,stream_revision,request_key,nonce_digest,owner_json,consumer_digest,resource_id,cursor,state,expires_at)
		VALUES('attempt178','stream178',7,'request178',zeroblob(32),'{}',zeroblob(32),'delivery178',41,'outcome_unknown','2030-01-01')`)
	exec(`INSERT INTO agent_runtime_health(id,runtime_id,project_id,layer,sequence,state,reason,failure_count,episode,published_at,updated_at)
		VALUES('health178','runtime178',?,'fallback',1,'unhealthy','consumer_authority_unavailable',1,1,'2026-09-01','2026-09-01')`, project)
	// A removed high cursor must never be reused after the table rebuild.
	exec(`INSERT INTO agent_messages(id,from_agent_id,to_agent_id,body,message_id) VALUES(99,?,?,'removed','removed178')`, sender, receiver)
	exec(`DELETE FROM agent_messages WHERE id=99`)
	return database, path
}

func migration178Snapshot(t *testing.T, database *sql.DB) (map[string]string, map[string][]string) {
	t.Helper()
	schema := map[string]string{}
	var tables []string
	rows, err := database.Query(`SELECT type,name,sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, name, statement string
		if err := rows.Scan(&kind, &name, &statement); err != nil {
			t.Fatal(err)
		}
		schema[kind+":"+name] = statement
		if kind == "table" && name != "schema_versions" {
			tables = append(tables, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	data := map[string][]string{}
	for _, table := range tables {
		rows, err := database.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		data[table] = []string{}
		for rows.Next() {
			values, dest := make([]any, len(cols)), make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(values)
			if err != nil {
				t.Fatal(err)
			}
			data[table] = append(data[table], string(encoded))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		sort.Strings(data[table])
	}
	return schema, data
}

func assertMigration178Pragmas(t *testing.T, database *sql.DB) {
	t.Helper()
	var foreignKeys, legacy, violations int
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA legacy_alter_table`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 || legacy != 0 || violations != 0 {
		t.Fatalf("pragma state: FK=%d legacy=%d violations=%d", foreignKeys, legacy, violations)
	}
}

func TestMigration178PreservesCompleteLedgerSchemaAndReferences(t *testing.T) {
	database, path := seedMigration178Ledger(t)
	beforeSchema, beforeData := migration178Snapshot(t, database)
	if err := migrateThrough(database, 178); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 178); err != nil {
		t.Fatal(err)
	}
	afterSchema, afterData := migration178Snapshot(t, database)
	want := strings.Replace(beforeSchema["table:agent_messages"], `CREATE TABLE agent_messages (`, `CREATE TABLE "agent_messages" (`, 1)
	want = strings.Replace(want, `CHECK(NOT paimos_contains_secret_like(body))`, `CHECK(NOT paimos_message_body_contains_secret_like(CAST(body AS BLOB)))`, 1)
	beforeSchema["table:agent_messages"] = want
	if !reflect.DeepEqual(beforeSchema, afterSchema) {
		for name, sql := range beforeSchema {
			if sql != afterSchema[name] {
				t.Errorf("schema changed beyond guard: %s", name)
			}
		}
		t.Fatalf("schema object count before=%d after=%d", len(beforeSchema), len(afterSchema))
	}
	if !reflect.DeepEqual(beforeData, afterData) {
		for name, rows := range beforeData {
			if !reflect.DeepEqual(rows, afterData[name]) {
				t.Errorf("table rows changed: %s", name)
			}
		}
		t.Fatal("ledger rows changed during rebuild")
	}
	assertMigration178Pragmas(t, database)
	if _, err := database.Exec(`UPDATE agent_messages SET expects_reply=0 WHERE id=41`); err == nil {
		t.Fatal("message immutability trigger lost")
	}
	if _, err := database.Exec(`UPDATE agent_message_deliveries SET attempt_count=attempt_count+1 WHERE message_row_id=41`); err == nil {
		t.Fatal("M177 consumer fence lost")
	}
	if _, err := database.Exec(`UPDATE agent_message_cursors SET cursor=42`); err == nil {
		t.Fatal("M177 cursor fence lost")
	}
	if _, err := database.Exec(`UPDATE agent_messages SET parent_message_id=999 WHERE id=42`); err == nil {
		t.Fatal("self-reference FK lost")
	}
	if _, err := database.Exec(`INSERT INTO agent_message_deliveries(delivery_id,message_row_id,instance,requested_level,state) VALUES('invalid',999,'test','simple','pending')`); err == nil {
		t.Fatal("child FK lost")
	}
	if _, err := database.Exec(`UPDATE agent_reply_obligations SET state='closed',closing_message_row_id=42,closed_at='2026-09-01T11:00:00.000Z',next_attention_at=NULL WHERE message_row_id=41`); err != nil {
		t.Fatalf("reply closure trigger/reference broken: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := migrateThrough(reopened, 178); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.Exec(`INSERT INTO agent_messages(from_agent_id,to_agent_id,body,message_id) SELECT from_agent_id,to_agent_id,?, 'post178' FROM agent_messages WHERE id=41`, "post\nrestore\n")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	if id <= 99 {
		t.Fatalf("cursor high-water reused: %d", id)
	}
}

func TestMigration178RollsBackWholeRebuildAndRestoresPragmas(t *testing.T) {
	database, _ := seedMigration178Ledger(t)
	// Failure at the final version write occurs after copy/drop/rename and all
	// index/trigger recreation; every preceding step must be rolled back.
	if _, err := database.Exec(`CREATE TRIGGER fail_m178 BEFORE INSERT ON schema_versions WHEN NEW.version=178 BEGIN SELECT RAISE(ABORT,'injected M178 failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeSchema, beforeData := migration178Snapshot(t, database)
	if err := migrateThrough(database, 178); err == nil || !strings.Contains(err.Error(), "injected M178 failure") {
		t.Fatalf("expected injected failure, got %v", err)
	}
	afterSchema, afterData := migration178Snapshot(t, database)
	if !reflect.DeepEqual(beforeSchema, afterSchema) || !reflect.DeepEqual(beforeData, afterData) {
		t.Fatal("failed rebuild changed original schema or data")
	}
	assertMigration178Pragmas(t, database)
	if migrationRecorded(t, database, 178) {
		t.Fatal("failed migration recorded")
	}
	if _, err := database.Exec(`DROP TRIGGER fail_m178`); err != nil {
		t.Fatal(err)
	}
	if err := migrateThrough(database, 178); err != nil {
		t.Fatalf("retry after rollback: %v", err)
	}
	assertMigration178Pragmas(t, database)
	// Cancellation must not prevent restoring connection-scoped pragmas.
	conn, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := applyMessageBodyMigration178(ctx, conn); err == nil {
		t.Fatal("cancelled migration succeeded")
	}
	conn.Close()
	assertMigration178Pragmas(t, database)
}

func TestMigration178RefusesLegacyNULWithoutRewritingEvidence(t *testing.T) {
	database, _ := seedMigration178Ledger(t)
	// The legacy TEXT UDF could inspect only the prefix before NUL. M178
	// validates BLOB bytes and must stop, not silently drop or sanitize a row.
	if _, err := database.Exec(`UPDATE agent_messages SET body=? WHERE id=42`, "ordinary\x00hidden\n"); err != nil {
		t.Fatalf("legacy fixture: %v", err)
	}
	beforeSchema, beforeData := migration178Snapshot(t, database)
	if err := migrateThrough(database, 178); err == nil || !strings.Contains(err.Error(), "blocked by 1 legacy message bodies") {
		t.Fatalf("expected content-free legacy refusal, got %v", err)
	}
	afterSchema, afterData := migration178Snapshot(t, database)
	if !reflect.DeepEqual(beforeSchema, afterSchema) || !reflect.DeepEqual(beforeData, afterData) {
		t.Fatal("refused migration changed prior evidence")
	}
	if migrationRecorded(t, database, 178) {
		t.Fatal("refused migration recorded")
	}
	assertMigration178Pragmas(t, database)
}
