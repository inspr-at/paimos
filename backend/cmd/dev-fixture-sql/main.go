// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
// AGPL-3.0-only — see LICENSE.

// Command dev-fixture-sql applies one bounded SQL document from stdin through
// the application's initialized SQLite connection. It exists only for local
// synthetic marketing fixtures and is not part of the production binary.
package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/inspr-at/paimos/backend/db"
)

const maxDocumentBytes = 1 << 20

func isProductionEnv(value string) bool {
	env := strings.ToLower(strings.TrimSpace(value))
	return env == "production" || env == "prod"
}

func apply(database *sql.DB, input io.Reader) error {
	body, err := io.ReadAll(io.LimitReader(input, maxDocumentBytes+1))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(body) == 0 {
		return fmt.Errorf("stdin is empty")
	}
	if len(body) > maxDocumentBytes {
		return fmt.Errorf("stdin exceeds %d bytes", maxDocumentBytes)
	}

	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(string(body)); err != nil {
		return fmt.Errorf("execute: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func main() {
	if isProductionEnv(os.Getenv("PAIMOS_ENV")) {
		log.Fatal("dev-fixture-sql: refused in production")
	}
	if err := db.Open(); err != nil {
		log.Fatalf("dev-fixture-sql: db.Open: %v", err)
	}
	if err := apply(db.DB, os.Stdin); err != nil {
		log.Fatalf("dev-fixture-sql: %v", err)
	}
}
