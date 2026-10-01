// SPDX-License-Identifier: AGPL-3.0-only

package db

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSplitSQL(t *testing.T) {
	in := `
-- leading
CREATE TABLE t (id int); -- trailing
/* block
   semi; colon */
CREATE TABLE u (note text);
SELECT 'a;b', 'it''s';
SELECT $$ a; b $$;
SELECT $tag$ a; b $tag$;
`
	got := splitSQL(in)
	want := []string{
		"CREATE TABLE t (id int)",
		"CREATE TABLE u (note text)",
		"SELECT 'a;b', 'it''s'",
		"SELECT $$ a; b $$",
		"SELECT $tag$ a; b $tag$",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statements:\n%s", len(got), strings.Join(got, "\n---\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stmt %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// Compare the checker against the actual migration runner, including cases
// where its boundaries differ from PostgreSQL's parser and every shipped file.
func TestMigrationCheckerSplitParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required; migration-compat CI installs it and runs this test explicitly")
	}
	cases := []struct{ name, sql string }{
		{"empty", ""},
		{"empty statements", ";; \n ;"},
		{"statements", "SELECT 1; SELECT 2; SELECT 3"},
		{"line comments", "-- lead;\nSELECT 1; -- tail;"},
		{"crlf", "-- lead;\r\nSELECT 1; -- tail;\r\nSELECT 2;"},
		{"block comments", "/* lead; */ SELECT 1; /* tail; */ SELECT 2;"},
		{"nested looking", "ALTER TABLE nodes ADD COLUMN extra text; /* a /* b */ ALTER TABLE nodes ALTER COLUMN title TYPE text; -- */"},
		{"nested looking drop", "/* a /* b */ DROP TABLE nodes; -- */"},
		{"comment joins keyword", "DR/**/OP TABLE nodes;"},
		{"comment joins words", "SELECT/**/1; SELECT-- no newline"},
		{"comment punctuation", "SELECT /, -, $1, $tag;"},
		{"comment at eof", "SELECT 1; /* unfinished"},
		{"comment opener at eof", "SELECT 1; /*"},
		{"single quotes", "SELECT 'a;b', 'it''s;here'; SELECT 2;"},
		{"double quotes", "SELECT \"a;b\", \"it\"\"s;here\"; SELECT 2;"},
		{"comments in quotes", "SELECT '-- x; /* y */'; SELECT \"/* ; */\";"},
		{"backslash is not an escape", `INSERT INTO notes VALUES (E'escaped \' DROP TABLE nodes;');`},
		{"unfinished quote", "SELECT 'unfinished; SELECT 2;"},
		{"unfinished identifier", "SELECT \"unfinished; SELECT 2;"},
		{"dollar body", "DO $$ BEGIN SELECT 'a;b'; /* c; */ END $$; SELECT 2;"},
		{"tagged dollar body", "SELECT $tag_1$ a; b $tag_1$; SELECT 2;"},
		{"digit dollar tag", "SELECT $123$ a; b $123$; SELECT 2;"},
		{"unfinished dollar body", "SELECT $tag$ unfinished; SELECT 2;"},
		{"different dollar tags", "SELECT $a$ $b$ ; $b$ $a$; SELECT 2;"},
		{"unicode text", "SELECT 'é;🙂', \"漢字;\"; SELECT 2;"},
		{"unicode whitespace", "\u0085\u00a0\u1680\u2000\u2028\u202f\u205f\u3000SELECT 1\u0085;\ufeffSELECT 2\ufeff;"},
	}
	names, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct{ name, sql string }{name, string(body)})
	}
	sqls := make([]string, len(cases))
	for i, tc := range cases {
		sqls[i] = tc.sql
	}
	input, err := json.Marshal(sqls)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", `
import { readFileSync } from 'node:fs';
import { splitSQL } from '../../scripts/check-migrations.mjs';
process.stdout.write(JSON.stringify(JSON.parse(readFileSync(0, 'utf8')).map(splitSQL)));
`)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("checker splitter: %v\n%s", err, output)
	}
	var checked [][]string
	if err := json.Unmarshal(output, &checked); err != nil {
		t.Fatalf("checker splitter output: %v\n%s", err, output)
	}
	if len(checked) != len(cases) {
		t.Fatalf("checker returned %d cases, want %d", len(checked), len(cases))
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if executed := splitSQL(tc.sql); !slices.Equal(checked[i], executed) {
				t.Fatalf("checker statements: %q\nrunner statements: %q", checked[i], executed)
			}
		})
	}
	t.Logf("splitter parity: %d edge cases and %d embedded migrations", len(cases)-len(names), len(names))
}

func TestSplitCoreMigrations(t *testing.T) {
	names, err := migrationNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations")
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if strings.Join(sorted, "\n") != strings.Join(names, "\n") {
		t.Fatalf("migrations not sorted: %v", names)
	}
	for _, name := range names {
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if len(splitSQL(string(body))) == 0 {
			t.Fatalf("%s has no statements", name)
		}
	}
	body, err := migrationFiles.ReadFile("migrations/0003_principals.sql")
	if err != nil {
		t.Fatal(err)
	}
	stmts := splitSQL(string(body))
	if len(stmts) != 5 {
		t.Fatalf("0003 has %d statements: %q", len(stmts), stmts)
	}
	joined := strings.Join(stmts, "\n")
	for _, needle := range []string{
		"CREATE TABLE principals",
		"CREATE UNIQUE INDEX principals_tenant_identity",
		"ENABLE ROW LEVEL SECURITY",
		"FORCE ROW LEVEL SECURITY",
		"CREATE POLICY tenant_isolation",
		"aeon.tenant_id",
	} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("0003 missing %s", needle)
		}
	}
}

func TestConcurrentIndexOptOut(t *testing.T) {
	for _, tc := range []struct {
		name, body    string
		marked, valid bool
	}{
		{"index", "-- aeon:no-transaction\n-- SPDX-License-Identifier: AGPL-3.0-only\nCREATE INDEX CONCURRENTLY pending ON messages(id) WHERE id IS NOT NULL;", true, true},
		{"crlf", "-- aeon:no-transaction\r\nCREATE INDEX CONCURRENTLY pending ON messages(id);", true, true},
		{"if not exists", "-- aeon:no-transaction\nCREATE INDEX CONCURRENTLY IF NOT EXISTS pending ON messages(id);", true, true},
		{"unique", "-- aeon:no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY pending ON messages(id);", true, true},
		{"multiline unique", "-- aeon:no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY pending\n    ON messages(id);", true, true},
		{"unique if not exists", "-- aeon:no-transaction\nCREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS pending ON messages(id);", true, true},
		{"normal", "CREATE TABLE messages(id int);", false, true},
		{"marker must be first", "-- license\n-- aeon:no-transaction\nCREATE TABLE messages(id int);", false, true},
		{"empty", "-- aeon:no-transaction\n", true, false},
		{"ddl", "-- aeon:no-transaction\nALTER TABLE messages ADD COLUMN body text;", true, false},
		{"blocking index", "-- aeon:no-transaction\nCREATE INDEX pending ON messages(id);", true, false},
		{"blocking unique index", "-- aeon:no-transaction\nCREATE UNIQUE INDEX pending ON messages(id);", true, false},
		{"blocking if not exists", "-- aeon:no-transaction\nCREATE INDEX IF NOT EXISTS pending ON messages(id);", true, false},
		{"set", "-- aeon:no-transaction\nSET lock_timeout='5s'; CREATE INDEX CONCURRENTLY IF NOT EXISTS pending ON messages(id);", true, false},
		{"batch", "-- aeon:no-transaction\nCREATE INDEX CONCURRENTLY pending ON messages(id); SELECT 1;", true, false},
		{"two indexes", "-- aeon:no-transaction\nCREATE INDEX CONCURRENTLY pending ON messages(id); CREATE INDEX CONCURRENTLY other ON messages(id);", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			index, table, err := concurrentIndex(tc.body, splitSQL(tc.body))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, err)
			}
			if tc.marked && tc.valid && (index != "pending" || table != "messages") {
				t.Fatalf("wrong target %q %q", index, table)
			}
			if !tc.marked && index != "" {
				t.Fatal("unmarked file opted out")
			}
		})
	}
}
