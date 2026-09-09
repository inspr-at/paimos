// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

// Package testschema caches one closed current-source SQLite snapshot for
// handler tests. Callers copy those bytes into a distinct DATA_DIR and still
// call db.Open so connection hooks, pool settings, and migrate skip checks
// remain. The snapshot is unseeded; tests that need users still insert them.
package testschema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/inspr-at/paimos/backend/brand"
	appdb "github.com/inspr-at/paimos/backend/db"
)

var handlerSchema struct {
	once  sync.Once
	bytes []byte
	err   error
}

func dbPath(dir string) string {
	return filepath.Join(dir, brand.Default.DBFilename)
}

func closeDB() error {
	if appdb.DB == nil {
		return nil
	}
	err := appdb.DB.Close()
	appdb.DB = nil
	return err
}

func restoreEnv(key, prev string) {
	if prev == "" {
		_ = os.Unsetenv(key)
		return
	}
	_ = os.Setenv(key, prev)
}

func buildTemplate() error {
	dir, err := os.MkdirTemp("", "paimos-handlers-schema-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	prevDir, prevMode := os.Getenv("DATA_DIR"), os.Getenv("PAIMOS_TEST_MODE")
	if err := os.Setenv("DATA_DIR", dir); err != nil {
		return err
	}
	if err := os.Setenv("PAIMOS_TEST_MODE", "1"); err != nil {
		restoreEnv("DATA_DIR", prevDir)
		return err
	}
	defer func() {
		restoreEnv("DATA_DIR", prevDir)
		restoreEnv("PAIMOS_TEST_MODE", prevMode)
	}()
	if err := appdb.Open(); err != nil {
		_ = closeDB()
		return err
	}
	if _, err := appdb.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		_ = closeDB()
		return fmt.Errorf("checkpoint handler schema template: %w", err)
	}
	snap := filepath.Join(dir, "schema-snapshot.db")
	quoted := "'" + strings.ReplaceAll(snap, "'", "''") + "'"
	// #nosec G202 -- snap is a MkdirTemp path owned by this process; not user input.
	if _, err := appdb.DB.Exec(`VACUUM INTO ` + quoted); err != nil {
		_ = closeDB()
		return fmt.Errorf("snapshot handler schema template: %w", err)
	}
	if err := closeDB(); err != nil {
		return err
	}
	for _, sidecar := range []string{snap + "-wal", snap + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			return fmt.Errorf("handler schema template left %s after snapshot", sidecar)
		}
	}
	bytes, err := os.ReadFile(snap)
	if err != nil {
		return err
	}
	if len(bytes) == 0 {
		return fmt.Errorf("handler schema template is empty")
	}
	handlerSchema.bytes = bytes
	return nil
}

func ensure() error {
	handlerSchema.once.Do(func() {
		handlerSchema.err = buildTemplate()
	})
	return handlerSchema.err
}

// WriteClone copies the process-owned current-schema snapshot into dir as
// brand.Default.DBFilename. Callers still invoke db.Open on that DATA_DIR.
func WriteClone(dir string) error {
	if err := ensure(); err != nil {
		return err
	}
	return os.WriteFile(dbPath(dir), handlerSchema.bytes, 0o600)
}

// ClonePath is the SQLite file WriteClone creates under dir.
func ClonePath(dir string) string {
	return dbPath(dir)
}
