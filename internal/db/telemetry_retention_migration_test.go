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

// Risk: SELECT FOR UPDATE/SHARE applies UPDATE policies. events has none under
// FORCE RLS, so a locking candidate scan matches no rows and every batch
// deletes nothing while reporting success.
func TestTelemetryRetentionCandidateScanTakesNoRowLock(t *testing.T) {
	content, err := os.ReadFile("migrations/1369_telemetry_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	start := strings.Index(sql, "INTO batch_ids FROM (")
	end := strings.Index(sql, "ORDER BY e.at,e.id LIMIT batch_size")
	if start < 0 || end < start {
		t.Fatal("retention function no longer selects a bounded candidate batch")
	}
	if candidates := sql[start:end]; strings.Contains(candidates, " FOR UPDATE") || strings.Contains(candidates, " FOR SHARE") ||
		strings.Contains(candidates, " FOR NO KEY UPDATE") || strings.Contains(candidates, " FOR KEY SHARE") {
		t.Fatalf("candidate scan takes a row lock that RLS filters to zero rows on events:\n%s", candidates)
	}
}

// Risk: the batch selection, the delete policy, the append-only trigger and
// the partial index each carry their own copy of the prune rule. A copy that
// still matches every old heartbeat deletes the phase changes goal history
// replays; an index copy that differs from the selection stops matching it.
func TestTelemetryRetentionSitesShareOnePruneRule(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		content, err := os.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	sql, index := read("1369_telemetry_retention.sql"), read("1370_telemetry_retention_index.sql")
	for site, want := range map[string]string{
		"policy":    "AND aeon_telemetry_prunable(type,before,after)\n AND at < statement_timestamp()",
		"trigger":   "AND public.aeon_telemetry_prunable(OLD.type,OLD.before,OLD.after)",
		"selection": "AND public.aeon_telemetry_prunable(e.type,e.before,e.after) AND e.at<cutoff AND e.undo_of IS NULL",
		"undo scan": "OR (public.aeon_telemetry_prunable(e.type,e.before,e.after) AND e.at<cutoff)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("%s does not apply the shared prune rule: missing %q", site, want)
		}
	}
	if !strings.Contains(index, "WHERE aeon_telemetry_prunable(type,before,after);") {
		t.Error("the partial index predicate is not the shared prune rule")
	}
	// The type list may name the result rows; it must never select rows again.
	if n := strings.Count(sql, "IN ('account.probed','harness.heartbeat')"); n != 0 {
		t.Errorf("%d separate type-only prune rules remain", n)
	}
}
