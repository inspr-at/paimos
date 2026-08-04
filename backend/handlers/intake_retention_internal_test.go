// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"database/sql"
	"testing"

	"github.com/inspr-at/paimos/backend/db"

	_ "modernc.org/sqlite"
)

// TestRetentionSweep_IntakeSessions covers the PAI-704 sweeper rules:
// stale active sessions flip to abandoned after the idle window, and
// finished sessions age out entirely after the retention window.
func TestRetentionSweep_IntakeSessions(t *testing.T) {
	oldDB := db.DB
	sqlDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.DB = sqlDB
	t.Cleanup(func() {
		sqlDB.Close()
		db.DB = oldDB
	})

	if _, err := db.DB.Exec(`CREATE TABLE intake_sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL,
		status TEXT NOT NULL DEFAULT 'active',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	seed := func(status, updatedAt string) int64 {
		res, err := db.DB.Exec(
			`INSERT INTO intake_sessions (user_id, status, updated_at) VALUES (1, ?, ?)`,
			status, updatedAt)
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	freshActive := seed("active", "datetime-now") // fixed below
	if _, err := db.DB.Exec(`UPDATE intake_sessions SET updated_at=datetime('now') WHERE id=?`, freshActive); err != nil {
		t.Fatal(err)
	}
	staleActive := seed("active", "2020-01-01 00:00:00")
	oldCompleted := seed("completed", "2020-01-01 00:00:00")
	freshAbandoned := seed("abandoned", "datetime-now")
	if _, err := db.DB.Exec(`UPDATE intake_sessions SET updated_at=datetime('now') WHERE id=?`, freshAbandoned); err != nil {
		t.Fatal(err)
	}

	// The sweep logs (and ignores) errors for the unrelated tables that
	// don't exist in this minimal fixture DB; only intake rules matter here.
	runRetentionSweep()

	status := func(id int64) string {
		var s string
		err := db.DB.QueryRow(`SELECT status FROM intake_sessions WHERE id=?`, id).Scan(&s)
		if err == sql.ErrNoRows {
			return "<deleted>"
		}
		if err != nil {
			t.Fatalf("status %d: %v", id, err)
		}
		return s
	}

	if got := status(freshActive); got != "active" {
		t.Errorf("fresh active session: got %s, want active", got)
	}
	if got := status(staleActive); got != "abandoned" {
		t.Errorf("stale active session: got %s, want abandoned", got)
	}
	if got := status(oldCompleted); got != "<deleted>" {
		t.Errorf("old completed session: got %s, want deleted", got)
	}
	if got := status(freshAbandoned); got != "abandoned" {
		t.Errorf("fresh abandoned session must survive the retention window: got %s", got)
	}
}
