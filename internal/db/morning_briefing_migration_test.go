// SPDX-License-Identifier: AGPL-3.0-only
package db

import (
	"os"
	"strings"
	"testing"
)

func TestBriefingMigrationDoesNotBlockLogWritersRegression(t *testing.T) {
	content, err := os.ReadFile("migrations/1086_morning_briefing_ranges.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	if !strings.HasPrefix(sql, "-- aeon:no-transaction\n") || !strings.Contains(sql, "CREATE INDEX CONCURRENTLY IF NOT EXISTS") {
		t.Fatal("briefing index blocks writers during the build")
	}
	if strings.Contains(sql, "ON events ") {
		t.Fatal("duplicates the existing events_at_idx")
	}
}
