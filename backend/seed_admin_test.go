// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inspr-at/paimos/backend/auth"
	"github.com/inspr-at/paimos/backend/db"
)

// PAI-739: the first-run seed must produce a super-admin — granting
// super_admin requires being one, so an admin-only seed makes the role
// permanently unreachable on a fresh install.

func openSeedTestDB(t *testing.T) {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := db.Open(); err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() {
		if db.DB != nil {
			db.DB.Close()
			db.DB = nil
		}
	})
}

func TestSeedAdmin_CreatesSuperAdmin(t *testing.T) {
	openSeedTestDB(t)
	t.Setenv("ADMIN_PASSWORD", "bootstrap-pass-123")

	if err := seedAdmin(); err != nil {
		t.Fatal(err)
	}

	var role, roleKey string
	var isSuper int
	if err := db.DB.QueryRow(
		`SELECT role, role_key, is_super_admin FROM users WHERE username='admin'`,
	).Scan(&role, &roleKey, &isSuper); err != nil {
		t.Fatalf("seeded user missing: %v", err)
	}
	if roleKey != "super_admin" || isSuper != 1 {
		t.Fatalf("seeded user = (role=%s, role_key=%s, is_super_admin=%d), want super_admin bootstrap", role, roleKey, isSuper)
	}
	if role != "admin" {
		t.Fatalf("legacy role shim = %q, want admin (LegacyRoleForPublicRole contract)", role)
	}
}

func TestSeedAdmin_CreatesSuperAdminFromFile(t *testing.T) {
	openSeedTestDB(t)
	path := filepath.Join(t.TempDir(), "admin-password")
	if err := os.WriteFile(path, []byte("bootstrap-pass-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_PASSWORD", "ignored-direct-password")
	t.Setenv("ADMIN_PASSWORD_FILE", path)

	if err := seedAdmin(); err != nil {
		t.Fatal(err)
	}

	var passwordHash string
	if err := db.DB.QueryRow(
		`SELECT password FROM users WHERE username='admin'`,
	).Scan(&passwordHash); err != nil {
		t.Fatalf("seeded user missing: %v", err)
	}
	if !auth.CheckPassword(passwordHash, "bootstrap-pass-123") {
		t.Fatal("seeded password did not come from ADMIN_PASSWORD_FILE")
	}
}

func TestSeedAdmin_NoopWhenUsersExist(t *testing.T) {
	openSeedTestDB(t)
	t.Setenv("ADMIN_PASSWORD", "bootstrap-pass-123")
	if _, err := db.DB.Exec(
		`INSERT INTO users(username,password,role) VALUES('existing','x','member')`); err != nil {
		t.Fatal(err)
	}

	if err := seedAdmin(); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE username='admin'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("seed ran on a non-empty users table")
	}
}
