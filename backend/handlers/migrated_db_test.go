// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, version 3.

package handlers

import (
	"testing"

	"github.com/inspr-at/paimos/backend/handlers/testschema"
)

// prepareIsolatedMigratedDir writes a closed current-schema clone into a
// distinct DATA_DIR. Callers still db.Open so hooks, pool, and migrate skip
// remain. Branding/env tests that only plant branding.json keep their own
// DATA_DIR and do not use this helper.
func prepareIsolatedMigratedDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("PAIMOS_TEST_MODE", "1")
	if err := testschema.WriteClone(dir); err != nil {
		t.Fatalf("write handler schema clone: %v", err)
	}
}
