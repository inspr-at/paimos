// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/inspr-at/paimos/backend/db"
	"github.com/inspr-at/paimos/backend/handlers/testschema"

	_ "modernc.org/sqlite"
)

func prepareIsolatedMigratedDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := testschema.WriteClone(dir); err != nil {
		t.Fatalf("write handler schema clone: %v", err)
	}
}

func closeHandlerDB() error {
	if db.DB == nil {
		return nil
	}
	err := db.DB.Close()
	db.DB = nil
	return err
}

func openClonedHandlerDB(t *testing.T) *sql.DB {
	t.Helper()
	prepareIsolatedMigratedDir(t)
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeHandlerDB()
	})
	return db.DB
}

func openFreshMigratedHandlerDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeHandlerDB()
	})
	return db.DB
}

type handlerSchemaSnapshot struct {
	version     int
	schemaCount int
	tables      string
	users       int
	projects    int
	foreignKeys int
}

func readHandlerSchemaSnapshot(t *testing.T, database *sql.DB) handlerSchemaSnapshot {
	t.Helper()
	var snap handlerSchemaSnapshot
	version, err := db.CurrentSchemaVersion(database)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	snap.version = version
	if err := database.QueryRow(`SELECT COUNT(*) FROM schema_versions`).Scan(&snap.schemaCount); err != nil {
		t.Fatal(err)
	}
	rows, err := database.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []byte
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if len(names) > 0 {
			names = append(names, ',')
		}
		names = append(names, name...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	snap.tables = string(names)
	for _, check := range []struct {
		query string
		got   *int
	}{
		{`SELECT COUNT(*) FROM users`, &snap.users},
		{`SELECT COUNT(*) FROM projects`, &snap.projects},
		{`PRAGMA foreign_keys`, &snap.foreignKeys},
	} {
		if err := database.QueryRow(check.query).Scan(check.got); err != nil {
			t.Fatalf("%s: %v", check.query, err)
		}
	}
	return snap
}

func TestHandlerSchemaTemplateMatchesFreshOpen(t *testing.T) {
	var clone handlerSchemaSnapshot
	t.Run("clone", func(t *testing.T) {
		clone = readHandlerSchemaSnapshot(t, openClonedHandlerDB(t))
	})
	if db.DB != nil {
		t.Fatal("clone fixture retained its connection")
	}
	t.Run("fresh migrate", func(t *testing.T) {
		fresh := readHandlerSchemaSnapshot(t, openFreshMigratedHandlerDB(t))
		if clone != fresh {
			t.Fatalf("cloned schema diverged from a current-source db.Open migrate:\nclone=%+v\nfresh=%+v", clone, fresh)
		}
		if clone.version == 0 || clone.schemaCount == 0 || clone.tables == "" {
			t.Fatalf("cloned schema is empty: %+v", clone)
		}
		if clone.users != 0 || clone.projects != 0 {
			t.Fatalf("cloned fixture carried seed rows: %+v", clone)
		}
		if clone.foreignKeys != 1 {
			t.Fatalf("cloned fixture foreign_keys=%d", clone.foreignKeys)
		}
	})
}

func TestHandlerSchemaClonesAreIsolated(t *testing.T) {
	var firstPath string
	var schemaVersion int
	t.Run("mutate first copy", func(t *testing.T) {
		database := openClonedHandlerDB(t)
		firstPath = os.Getenv("DATA_DIR")
		if _, err := database.Exec(`INSERT INTO projects(name,key) VALUES('First clone','C1')`); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`CREATE TABLE fixture_only_marker (value TEXT); INSERT INTO fixture_only_marker VALUES ('first fixture')`); err != nil {
			t.Fatal(err)
		}
		if err := database.QueryRow(`SELECT MAX(version) FROM schema_versions`).Scan(&schemaVersion); err != nil {
			t.Fatal(err)
		}
	})
	if db.DB != nil {
		t.Fatal("fixture cleanup retained its connection")
	}
	t.Run("independent second copy", func(t *testing.T) {
		database := openClonedHandlerDB(t)
		if os.Getenv("DATA_DIR") == firstPath {
			t.Fatal("fixtures reused a mutable database directory")
		}
		var projects, marker, version, foreignKeys int
		for _, check := range []struct {
			query string
			got   *int
			want  int
		}{
			{`SELECT COUNT(*) FROM projects`, &projects, 0},
			{`SELECT COUNT(*) FROM sqlite_master WHERE name='fixture_only_marker'`, &marker, 0},
			{`SELECT MAX(version) FROM schema_versions`, &version, schemaVersion},
			{`PRAGMA foreign_keys`, &foreignKeys, 1},
		} {
			if err := database.QueryRow(check.query).Scan(check.got); err != nil || *check.got != check.want {
				t.Fatalf("isolated schema check %q: got=%d want=%d err=%v", check.query, *check.got, check.want, err)
			}
		}
	})
	t.Run("concurrent file clones", func(t *testing.T) {
		dirA, dirB := t.TempDir(), t.TempDir()
		if err := testschema.WriteClone(dirA); err != nil {
			t.Fatal(err)
		}
		if err := testschema.WriteClone(dirB); err != nil {
			t.Fatal(err)
		}
		paths := []string{testschema.ClonePath(dirA), testschema.ClonePath(dirB)}
		type cloneResult struct {
			id    int64
			other int
			err   error
		}
		results := make([]cloneResult, len(paths))
		var wg sync.WaitGroup
		for i, path := range paths {
			wg.Add(1)
			go func(i int, path string) {
				defer wg.Done()
				conn, err := sql.Open("sqlite", path+"?_txlock=immediate")
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				defer conn.Close()
				key := fmt.Sprintf("HX%d", i)
				res, err := conn.Exec(`INSERT INTO projects(name,key) VALUES(?,?)`, fmt.Sprintf("Concurrent %d", i), key)
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				id, err := res.LastInsertId()
				if err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				otherKey := fmt.Sprintf("HX%d", 1-i)
				var other int
				if err := conn.QueryRow(`SELECT COUNT(*) FROM projects WHERE key=?`, otherKey).Scan(&other); err != nil {
					results[i] = cloneResult{err: err}
					return
				}
				results[i] = cloneResult{id: id, other: other}
			}(i, path)
		}
		wg.Wait()
		for i, result := range results {
			if result.err != nil {
				t.Fatalf("concurrent clone %d: %v", i, result.err)
			}
			if result.id <= 0 {
				t.Fatalf("concurrent clone %d inserted id=%d", i, result.id)
			}
			if result.other != 0 {
				t.Fatalf("concurrent clone %d observed the other clone's row", i)
			}
		}
		if results[0].id != results[1].id {
			t.Fatalf("clones shared sqlite_sequence: ids=%d,%d", results[0].id, results[1].id)
		}
	})
}
