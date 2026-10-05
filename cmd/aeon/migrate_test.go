// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/db"
)

func TestMigrationBlockedExitCode(t *testing.T) {
	err := fmt.Errorf("migrate 1215: %w", &db.BusyWorkParentsError{Parents: []db.BusyWorkParent{{Key: "AEON-648", Reason: "bound session"}}})
	if serverErrorExitCode(err) != 78 || serverErrorExitCode(errors.New("ordinary failure")) != 1 {
		t.Fatal("boot must distinguish blocked migration from ordinary failure")
	}
	for _, text := range []string{"AEON-648", "bound session", "old container running", "graceful handovers", "migrate --check"} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("missing %q in diagnostic", text)
		}
	}
}

func TestMigrationCommandRequiresCheckBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{nil, {"--apply"}, {"--check", "extra"}} {
		if err := migrateCommand(args, io.Discard); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("invalid command must fail before connecting: %v", err)
		}
	}
}
