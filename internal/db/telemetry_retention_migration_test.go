// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"os"
	"strings"
	"testing"
)

// Risk: the retention function stores aeon.telemetry_prune in its configuration.
// That placeholder is superuser-only at CREATE FUNCTION time, so the NOSUPERUSER
// migration role fails the whole template with SQLSTATE 42501. The flag has to
// be assigned and restored in the body instead.
func TestTelemetryRetentionMigrationRoleCanInstallPruneFlag(t *testing.T) {
	content, err := os.ReadFile("migrations/1369_telemetry_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	if strings.Contains(sql, "SET aeon.telemetry_prune") {
		t.Fatal("function configuration stores aeon.telemetry_prune; the NOSUPERUSER migration role cannot set that placeholder")
	}
	if !strings.Contains(sql, "set_config('aeon.telemetry_prune','on',true)") {
		t.Fatal("retention function does not enable the prune flag for its body")
	}
	if !strings.Contains(sql, "set_config('aeon.telemetry_prune',coalesce(prior_prune,''),true)") {
		t.Fatal("retention function does not restore the prune flag before returning")
	}
}
