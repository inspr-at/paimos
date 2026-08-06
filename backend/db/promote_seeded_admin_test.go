// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// PAI-739 / M138: promote the seeded 'admin' account to super_admin,
// but only on instances that have no super-admin at all.

func openPromoteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "promote.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Exec(`CREATE TABLE users (
		id             INTEGER PRIMARY KEY,
		username       TEXT NOT NULL,
		role           TEXT NOT NULL DEFAULT 'member',
		role_key       TEXT NOT NULL DEFAULT 'member',
		is_super_admin INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	return d
}

func promoteState(t *testing.T, d *sql.DB, username string) (roleKey string, isSuper int) {
	t.Helper()
	if err := d.QueryRow(
		`SELECT role_key, is_super_admin FROM users WHERE username=?`, username,
	).Scan(&roleKey, &isSuper); err != nil {
		t.Fatalf("read %s: %v", username, err)
	}
	return roleKey, isSuper
}

// TestPromoteSeededAdmin_BreaksDeadlock: the legacy admin-only seed on
// an instance with no super-admin gets promoted; re-running is a no-op.
func TestPromoteSeededAdmin_BreaksDeadlock(t *testing.T) {
	d := openPromoteTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO users(username, role) VALUES ('admin', 'admin'), ('worker', 'member')`); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Exec(PromoteSeededAdminSQL); err != nil {
		t.Fatalf("promote: %v", err)
	}
	roleKey, isSuper := promoteState(t, d, "admin")
	if roleKey != "super_admin" || isSuper != 1 {
		t.Fatalf("seeded admin = (%s, %d), want promoted", roleKey, isSuper)
	}
	if rk, s := promoteState(t, d, "worker"); rk != "member" || s != 0 {
		t.Fatalf("bystander user touched: (%s, %d)", rk, s)
	}

	// Idempotent on re-run (migrations must be safe to replay).
	if _, err := d.Exec(PromoteSeededAdminSQL); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var superCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM users WHERE is_super_admin=1`).Scan(&superCount); err != nil {
		t.Fatal(err)
	}
	if superCount != 1 {
		t.Fatalf("super-admin count after re-run = %d, want 1", superCount)
	}
}

// TestPromoteSeededAdmin_NoopWithExistingSuperAdmin: instances that
// already solved bootstrap (e.g. ppm's mba) are left untouched.
func TestPromoteSeededAdmin_NoopWithExistingSuperAdmin(t *testing.T) {
	d := openPromoteTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO users(username, role, is_super_admin) VALUES ('mba', 'admin', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(
		`INSERT INTO users(username, role) VALUES ('admin', 'admin')`); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Exec(PromoteSeededAdminSQL); err != nil {
		t.Fatalf("promote: %v", err)
	}
	roleKey, isSuper := promoteState(t, d, "admin")
	if roleKey != "member" || isSuper != 0 {
		t.Fatalf("seeded admin promoted despite existing super-admin: (%s, %d)", roleKey, isSuper)
	}
}
