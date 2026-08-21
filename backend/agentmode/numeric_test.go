// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package agentmode

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/deliverytrust"
	_ "modernc.org/sqlite"
)

func TestPermissionFingerprintRejectsInvalidSignedIdentities(t *testing.T) {
	for _, tc := range []struct {
		name   string
		userID int64
		epoch  int64
	}{
		{name: "zero user", userID: 0, epoch: 0},
		{name: "negative user", userID: -1, epoch: 0},
		{name: "negative epoch", userID: 1, epoch: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := permissionFingerprint(tc.userID, tc.epoch, "basis"); !errors.Is(err, ErrInvariant) {
				t.Fatalf("permissionFingerprint(%d,%d) error=%v", tc.userID, tc.epoch, err)
			}
		})
	}
	first, err := permissionFingerprint(math.MaxInt64, math.MaxInt64, "basis")
	if err != nil {
		t.Fatal(err)
	}
	second, err := permissionFingerprint(math.MaxInt64, math.MaxInt64, "basis")
	if err != nil || first != second || first == ([32]byte{}) {
		t.Fatalf("maximum permission identity digest=%x repeat=%x err=%v", first, second, err)
	}
}

func TestEstimateFactRejectsNonPositiveDatabaseRevisions(t *testing.T) {
	received := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	stage := &trustStageFact{ReporterType: "external"}
	valid := sql.NullInt64{Int64: 1, Valid: true}
	confidence := sql.NullFloat64{Float64: 0.5, Valid: true}
	for _, tc := range []struct {
		name     string
		revision sql.NullInt64
		sequence sql.NullInt64
	}{
		{name: "zero revision", revision: sql.NullInt64{Valid: true}, sequence: valid},
		{name: "negative revision", revision: sql.NullInt64{Int64: -1, Valid: true}, sequence: valid},
		{name: "zero sequence", revision: valid, sequence: sql.NullInt64{Valid: true}},
		{name: "negative sequence", revision: valid, sequence: sql.NullInt64{Int64: -1, Valid: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := estimateFactFromRow(stage, "estimate:1", tc.revision, tc.sequence, "agent", received,
				confidence, "basis", sql.NullFloat64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}); !errors.Is(err, ErrInvariant) {
				t.Fatalf("estimateFactFromRow() error=%v", err)
			}
		})
	}
	maximum := sql.NullInt64{Int64: math.MaxInt64, Valid: true}
	fact, err := estimateFactFromRow(stage, "estimate:max", maximum, maximum, "agent", received,
		confidence, "basis", sql.NullFloat64{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{})
	if err != nil || fact.Revision != math.MaxInt64 || fact.Sequence != math.MaxInt64 {
		t.Fatalf("maximum estimate fact=%+v err=%v", fact, err)
	}
}

func TestDurationHistoryRejectsNonPositiveExecutionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name        string
		executionID int64
		wantError   bool
	}{
		{name: "negative", executionID: -1, wantError: true},
		{name: "zero", executionID: 0, wantError: true},
		{name: "maximum", executionID: math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			database.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = database.Close() })
			if _, err := database.Exec(`CREATE TABLE delivery_stage_durations (
				stage_execution_id INTEGER, project_id_at_completion INTEGER, stage_key TEXT,
				estimator_policy_version INTEGER, completed_at TEXT, full_lead_seconds INTEGER,
				active_seconds INTEGER, blocked_seconds INTEGER, human_wait_seconds INTEGER)`); err != nil {
				t.Fatal(err)
			}
			completed := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
			if _, err := database.Exec(`INSERT INTO delivery_stage_durations VALUES(?,?,?,?,?,?,?,?,?)`,
				tc.executionID, 99, string(deliverytrust.StageQA), deliverytrust.EstimatorPolicyVersion,
				completed, 10, 10, 0, 0); err != nil {
				t.Fatal(err)
			}
			history, err := loadDurationHistory(context.Background(), database, []catalogEntry{{ProjectID: 99}})
			if tc.wantError {
				if !errors.Is(err, ErrInvariant) {
					t.Fatalf("loadDurationHistory() error=%v", err)
				}
				return
			}
			samples := history[durationKey{ProjectID: 99, Stage: string(deliverytrust.StageQA)}]
			if err != nil || len(samples) != 1 || samples[0].StageExecutionID != math.MaxInt64 {
				t.Fatalf("maximum duration history=%+v err=%v", history, err)
			}
		})
	}
}
