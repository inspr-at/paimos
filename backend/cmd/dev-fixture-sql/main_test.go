// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// AGPL-3.0-only — see LICENSE.

package main

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestApplyDocument(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	document := `CREATE TABLE fixture (value TEXT NOT NULL); INSERT INTO fixture VALUES ('ready');`
	if err := apply(database, strings.NewReader(document)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var value string
	if err := database.QueryRow(`SELECT value FROM fixture`).Scan(&value); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if value != "ready" {
		t.Fatalf("value = %q, want ready", value)
	}
}

func TestApplyRejectsEmptyAndOversizedInput(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if err := apply(database, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty input error = %v", err)
	}
	oversized := strings.NewReader(strings.Repeat("x", maxDocumentBytes+1))
	if err := apply(database, oversized); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized input error = %v", err)
	}
}

func TestIsProductionEnv(t *testing.T) {
	for _, env := range []string{"production", "PRODUCTION", " prod "} {
		if !isProductionEnv(env) {
			t.Errorf("isProductionEnv(%q) = false, want true", env)
		}
	}
	for _, env := range []string{"", "development", "staging"} {
		if isProductionEnv(env) {
			t.Errorf("isProductionEnv(%q) = true, want false", env)
		}
	}
}
