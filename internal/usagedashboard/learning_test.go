// SPDX-License-Identifier: AGPL-3.0-only
package usagedashboard

import (
	"testing"

	"github.com/inspr-at/paimos/internal/modelregistry"
)

func TestLearningHistoryThresholdWindowAndVersion(t *testing.T) {
	c := CellFor(modelregistry.Profile{ID: "p", Harness: "codex", Family: "openai", Model: "gpt-6-astra", Effort: "xhigh"}, "backend", "complex")
	if c.Version != "6" || c.Line != "astra" {
		t.Fatal(c)
	}
	estimate := 2.0
	var samples []LearningSample
	for i := range 4 {
		samples = append(samples, LearningSample{Cell: c, Hours: float64(i + 1), Tokens: float64(i+1) * 1_000_000, EstimateHours: &estimate})
	}
	h := History(samples, c)
	if h.State != "uncalibrated" || h.Tickets != 4 || h.Hours != nil || h.Tokens != nil || h.SpeedFactor != nil || h.SpeedTickets != 4 {
		t.Fatal(h)
	}
	samples = append(samples, LearningSample{Cell: c, Hours: 5, Tokens: 5_000_000, EstimateHours: &estimate})
	h = History(samples, c)
	if h.State != "calibrated" || *h.Hours != 3 || *h.Tokens != 3_000_000 || *h.SpeedFactor != 1.5 {
		t.Fatal(h)
	}
	newer := c
	newer.Version = "6.1"
	newer.ProfileID = "new"
	h = History(samples, newer)
	if h.State != "calibrated" || h.Tickets != 5 || h.SpeedFactor != nil || h.SpeedTickets != 0 {
		t.Fatal("latest borrowed a speed factor", h)
	}
	wrong := c
	wrong.Bucket = "normal"
	if History(samples, wrong).State != "uncalibrated" {
		t.Fatal("cross-bucket history")
	}
	for range 40 {
		samples = append(samples, LearningSample{Cell: c, Hours: 99, Tokens: 99})
	}
	if h = History(samples, c); h.Tickets != 30 {
		t.Fatal("unbounded window", h)
	}
	// An unversioned moving alias cannot claim an exact-cell speed.
	alias := CellFor(modelregistry.Profile{ID: "a", Harness: "claude", Family: "anthropic", Model: "opus", Effort: "high"}, "backend", "normal")
	if alias.Exact(alias) {
		t.Fatal("alias invented a concrete version")
	}
}
