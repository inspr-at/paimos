// SPDX-License-Identifier: AGPL-3.0-only
package usagedashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type learningLimitRows struct {
	pgx.Rows
	count, index int
	profile      bool
}

func (r *learningLimitRows) Next() bool { r.index++; return r.index <= r.count }
func (r *learningLimitRows) Close()     {}
func (r *learningLimitRows) Err() error { return nil }
func (r *learningLimitRows) Scan(dest ...any) error {
	if r.profile {
		values := []string{"00000000-0000-0000-0000-000000000001", "codex", "openai", "gpt-6-astra", "xhigh"}
		for i, d := range dest {
			*d.(*string) = values[i]
		}
		return nil
	}
	for _, d := range dest {
		*d.(*string) = fmt.Sprintf("00000000-0000-0000-0000-%012d", r.index)
	}
	return nil
}

type learningLimitTx struct {
	pgx.Tx
	aggregated   bool
	profileCount int
	candidates   bool
}

func (tx *learningLimitTx) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "FROM model_profiles"):
		count := tx.profileCount
		if count == 0 {
			count = 1
		}
		return &learningLimitRows{count: count, profile: true}, nil
	case strings.Contains(sql, "SELECT DISTINCT project_id"):
		return &learningLimitRows{count: 4097}, nil // reviewed baseline
	case strings.Contains(sql, "FROM outcome_events") && !strings.Contains(sql, "WITH done"):
		tx.candidates = true
		if !strings.Contains(sql, "LIMIT 12001") || !strings.Contains(sql, "s.model_profile_id=ANY") {
			return nil, fmt.Errorf("candidate history is not bounded/targeted")
		}
		return &learningLimitRows{count: 12001}, nil
	default:
		tx.aggregated = true
		return nil, fmt.Errorf("aggregation should not run after truncation")
	}
}
func TestLearningCapDegradesWithoutAggregation(t *testing.T) {
	tx := &learningLimitTx{}
	samples, err := LoadLearningSamples(t.Context(), tx, []LearningCell{{ProfileID: "00000000-0000-0000-0000-000000000001", Family: "openai", Line: "astra", Effort: "xhigh", Kind: "backend", Bucket: "complex"}}, "", func(string) bool { return true })
	if err != nil || len(samples) != 0 || tx.aggregated {
		t.Fatalf("hint cap must degrade before aggregation: samples=%v error=%v aggregated=%v", samples, err, tx.aggregated)
	}
}

func TestLearningHistoryCapIsExplicit(t *testing.T) {
	samples, truncated, err := LoadLearningHistory(t.Context(), &learningLimitTx{}, []LearningCell{{ProfileID: "00000000-0000-0000-0000-000000000001", Family: "openai", Line: "astra", Effort: "xhigh", Kind: "backend", Bucket: "complex"}}, "", func(string) bool { return true })
	if err != nil || !truncated || len(samples) != 0 {
		t.Fatalf("history truncation not explicit: %+v %v %v", samples, truncated, err)
	}
}

func TestLearningProfileCapStopsBeforeCandidates(t *testing.T) {
	tx := &learningLimitTx{profileCount: 4097}
	samples, truncated, err := LoadLearningHistory(t.Context(), tx, []LearningCell{{Family: "openai", Line: "astra", Effort: "xhigh"}}, "", func(string) bool { t.Fatal("visibility must not run at the profile cap"); return false })
	if err != nil || !truncated || len(samples) != 0 || tx.candidates || tx.aggregated {
		t.Fatalf("profile cap must stop before candidate/aggregation work: samples=%v truncated=%v err=%v tx=%+v", samples, truncated, err, tx)
	}
}
