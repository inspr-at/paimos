// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package flowhost

import (
	"github.com/inspr-at/paimos/backend/baselinebatch"
	"testing"
	"time"
)

func TestCurrentBatchUsesItsOnlyForecastWithoutInventingTaskProgress(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	eta := int64(180)
	for _, observed := range []bool{true, false} {
		batch := &baselinebatch.Batch{Status: baselinebatch.BatchActive, Forecasts: []baselinebatch.Forecast{{Subject: "overall", Percent: 90, ETASeconds: &eta, Observed: observed, Fresh: observed, Kind: baselinebatch.ForecastMeasured, AsOf: now.Format(time.RFC3339Nano)}}}
		label, task, overall, _ := mapProgress(nil, batch, now, now.Add(10*time.Minute))
		if label != "Current batch" || task.Forecast.PercentComplete != 90 || task.Forecast.EstimatedFinish != overall.Forecast.EstimatedFinish {
			t.Fatalf("contradictory current batch forecast: %s %+v / %+v", label, task, overall)
		}
		if observed {
			if task.Progress["percent_complete"] != float64(90) || task.Progress["freshness"] != "fresh" {
				t.Fatalf("lost measurement: %+v", task)
			}
		} else if task.Progress["percent_complete"] != nil || task.Forecast.Kind != "educated_guess" {
			t.Fatalf("guess became observation: %+v", task)
		}
		batch.Status = baselinebatch.BatchPaused
		_, task, _, freshness := mapProgress(nil, batch, now, now.Add(10*time.Minute))
		if task.Progress["status"] != "pending" || freshness != "Paused activity · not live work" {
			t.Fatal("paused batch shown as live")
		}
	}
}

func TestCurrentBatchDoesNotReplaceExplicitTaskOrMissingEvidence(t *testing.T) {
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	overall := baselinebatch.Forecast{Subject: "overall", Percent: 90}
	task := baselinebatch.Forecast{Subject: "verification", Percent: 20}
	for _, forecasts := range [][]baselinebatch.Forecast{{overall, task}, {task, overall}} {
		label, got, all, _ := mapProgress(nil, &baselinebatch.Batch{Forecasts: forecasts}, now, now.Add(time.Minute))
		if label != "verification" || got.Forecast.PercentComplete != 20 || all.Forecast.PercentComplete != 90 {
			t.Fatalf("explicit task replaced: %s %+v", label, got)
		}
	}
	_, got, _, _ := mapProgress(nil, &baselinebatch.Batch{}, now, now.Add(time.Minute))
	if got.Progress["percent_complete"] != nil || got.Forecast.Kind != "educated_guess" {
		t.Fatal("missing forecast became observed")
	}
}
