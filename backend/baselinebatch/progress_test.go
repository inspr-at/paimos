// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/backend/delivery"
)

func planningForecast(eta int64) Forecast {
	return Forecast{
		Subject: "overall", Percent: 0, ETASeconds: &eta, Kind: ForecastGuess,
		Basis: "requirement count at confirmation; nothing has been observed yet",
		AsOf:  "2026-09-07T12:00:00Z", Label: forecastLabel(ForecastGuess),
	}
}

func TestAttachEducatedETAFallbacksMeasured(t *testing.T) {
	planning := planningForecast(3600)
	forecasts := []Forecast{
		planning,
		{
			Subject: "overall", Percent: 25, Kind: ForecastMeasured,
			Basis: "satisfied canonical delivery stage weight", Label: forecastLabel(ForecastMeasured),
		},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, Progress{}, Scope{RequirementRefs: []string{"req.a"}}, time.Time{})
	measured := out[1]
	if measured.ETASeconds != nil {
		t.Fatalf("measured must not invent an observed ETA: %+v", measured)
	}
	if measured.EducatedETASeconds == nil || *measured.EducatedETASeconds != 2700 {
		t.Fatalf("educated ETA=%v want 2700", measured.EducatedETASeconds)
	}
	if measured.EducatedETALabel != "guessed" {
		t.Fatalf("label=%q", measured.EducatedETALabel)
	}
	if measured.EducatedETABasis == "" || measured.EducatedETAAsOf != planning.AsOf {
		t.Fatalf("educated provenance=%q as_of=%q", measured.EducatedETABasis, measured.EducatedETAAsOf)
	}
	if out[0].EducatedETASeconds != nil {
		t.Fatal("planning forecast must keep its own ETA only")
	}
}

func TestAttachEducatedETAFallbacksWorkerMissingETA(t *testing.T) {
	planning := planningForecast(1800)
	forecasts := []Forecast{
		planning,
		{
			Subject: "implementation", Percent: 50, Kind: ForecastWorker,
			Basis: "worker report", Label: forecastLabel(ForecastWorker), Observed: true,
		},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, Progress{}, Scope{RequirementRefs: []string{"req.a"}}, time.Time{})
	worker := out[1]
	if worker.EducatedETASeconds == nil || *worker.EducatedETASeconds != 900 {
		t.Fatalf("worker educated ETA=%v want 900", worker.EducatedETASeconds)
	}
}

func TestAttachEducatedETAFallbacksWorkerKeepsOwnETA(t *testing.T) {
	workerETA := int64(600)
	forecasts := []Forecast{
		planningForecast(1800),
		{
			Subject: "implementation", Percent: 50, Kind: ForecastWorker, ETASeconds: &workerETA,
			Basis: "worker report", Label: forecastLabel(ForecastWorker),
		},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, Progress{}, Scope{RequirementRefs: []string{"req.a"}}, time.Time{})
	if out[1].EducatedETASeconds != nil {
		t.Fatalf("worker with ETA must not get educated fallback: %+v", out[1])
	}
}

func TestAttachEducatedETAFallbacksCompleted(t *testing.T) {
	forecasts := []Forecast{
		planningForecast(3600),
		{Subject: "overall", Percent: 100, Kind: ForecastMeasured, Label: forecastLabel(ForecastMeasured)},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchCompleted, Progress{FreshnessAsOf: "2026-09-07T13:00:00Z"},
		Scope{RequirementRefs: []string{"req.a"}}, time.Time{})
	if out[1].EducatedETASeconds == nil || *out[1].EducatedETASeconds != 0 {
		t.Fatalf("completed educated ETA=%v want 0", out[1].EducatedETASeconds)
	}
	if out[1].EducatedETABasis == "" {
		t.Fatal("completed educated basis missing")
	}
}

func TestAttachEducatedETAFallbacksWorkerCompleteBatchIncomplete(t *testing.T) {
	planning := planningForecast(1800)
	forecasts := []Forecast{
		planning,
		{
			Subject: "implementation", Percent: 100, Kind: ForecastWorker,
			Basis: "worker report", Label: forecastLabel(ForecastWorker), Observed: true,
		},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, Progress{EvidenceObserved: true, EvidenceFresh: true},
		Scope{RequirementRefs: []string{"req.a", "req.b"}}, time.Time{})
	worker := out[1]
	if worker.EducatedETASeconds == nil || *worker.EducatedETASeconds != 1800 {
		t.Fatalf("worker at 100%% on an active batch must keep planning ETA: %+v", worker)
	}
	if strings.Contains(worker.EducatedETABasis, "all required delivery stages satisfied") {
		t.Fatalf("worker fallback must not claim batch completion: %q", worker.EducatedETABasis)
	}
}

func TestAttachEducatedETAFallbacksGuessMissingETA(t *testing.T) {
	forecasts := []Forecast{
		{Subject: "overall", Percent: 0, Kind: ForecastGuess, Label: forecastLabel(ForecastGuess)},
	}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, Progress{}, Scope{RequirementRefs: []string{"req.a", "req.b"}}, time.Time{})
	guess := out[0]
	if guess.ETASeconds == nil || *guess.ETASeconds != 3600 {
		t.Fatalf("guess row ETA=%v want planning fallback 3600", guess.ETASeconds)
	}
	if guess.Basis == "" {
		t.Fatal("guess row planning basis missing")
	}
}

func TestAttachEducatedETAFallbacksStaleAssumption(t *testing.T) {
	forecasts := []Forecast{
		planningForecast(1800),
		{Subject: "overall", Percent: 0, Kind: ForecastMeasured, Label: forecastLabel(ForecastMeasured)},
	}
	progress := Progress{EvidenceObserved: true, EvidenceFresh: false}
	out := attachEducatedETAFallbacks(forecasts, BatchActive, progress, Scope{RequirementRefs: []string{"req.a"}}, time.Time{})
	if out[1].EducatedETABasis == "" {
		t.Fatal("stale educated basis missing")
	}
	if !strings.Contains(out[1].EducatedETABasis, "stale") {
		t.Fatalf("stale basis=%q", out[1].EducatedETABasis)
	}
}

func deliverySnapshotWithEstimate(progress float64, eta *int64) delivery.Snapshot {
	return delivery.Snapshot{
		Stages: []delivery.StageSnapshot{{
			StageKey: "implementation",
			LatestEstimate: &delivery.EstimateSnapshot{
				Progress:         &progress,
				ETASeconds:       eta,
				ServerReceivedAt: "2026-09-07T12:05:00Z",
			},
		}},
	}
}

func TestWorkerForecastWithoutETA(t *testing.T) {
	progress := float64(0.4)
	snapshot := deliverySnapshotWithEstimate(progress, nil)
	forecast, ok := workerForecast(snapshot)
	if !ok {
		t.Fatal("expected worker forecast")
	}
	if forecast.ETASeconds != nil {
		t.Fatalf("worker ETA must stay absent: %+v", forecast)
	}
	if forecast.Percent != 40 {
		t.Fatalf("percent=%v want 40", forecast.Percent)
	}
}
