// SPDX-License-Identifier: AGPL-3.0-only
package nodes

import (
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/usagedashboard"
)

func TestPlanningLearningBackoffAndSpeed(t *testing.T) {
	cell := usagedashboard.LearningCell{ProfileID: "new", Family: "openai", Line: "astra", Version: "6.1", Effort: "xhigh", Kind: "backend", Bucket: "complex"}
	route := &planRoute{view: &planningRoute{Harness: "codex"}, cell: cell, key: routeKey{harness: "codex", model: "gpt-6.1-astra", effort: "xhigh", learning: "new|backend|complex"}}
	estimate := 2.0
	makeSamples := func(c usagedashboard.LearningCell, n int, rate float64) []usagedashboard.LearningSample {
		out := []usagedashboard.LearningSample{}
		for range n {
			out = append(out, usagedashboard.LearningSample{Cell: c, Hours: 4, Tokens: rate * 4, EstimateHours: &estimate})
		}
		return out
	}
	old := cell
	old.ProfileID = "old"
	old.Version = "6"
	other := cell
	other.Kind = "docs"
	for _, tc := range []struct {
		name, level string
		learned     []usagedashboard.LearningSample
		legacy      []calibrationSample
		want, speed float64
	}{
		{"exact wins", "cell", append(makeSamples(cell, 5, 1_000_000), makeSamples(old, 9, 2_000_000)...), nil, 1_000_000, 2},
		{"latest backoff", "line", makeSamples(old, 5, 2_000_000), nil, 2_000_000, 1},
		{"profile across kinds", "profile", makeSamples(other, 5, 3_000_000), nil, 3_000_000, 1},
		{"legacy route", "route", nil, []calibrationSample{{harness: "codex", model: "gpt-6.1-astra", effort: "xhigh", tokensPerHour: 7}, {harness: "codex", model: "gpt-6.1-astra", tokensPerHour: 7}, {harness: "codex", model: "gpt-6.1-astra", tokensPerHour: 7}, {harness: "codex", model: "gpt-6.1-astra", tokensPerHour: 7}, {harness: "codex", model: "gpt-6.1-astra", tokensPerHour: 7}}, 7, 1},
		{"below threshold", "default", makeSamples(cell, 4, 1_000_000), nil, defaultTokensPerHour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pl := &planner{learning: tc.learned, samples: tc.legacy, calibrations: map[routeKey]calibration{}}
			c := pl.calibration(route)
			if c.level != tc.level || c.tokensPerHour != tc.want || c.speed != tc.speed {
				t.Fatalf("calibration %+v", c)
			}
			if !strings.Contains(c.basisText, "n=") {
				t.Fatalf("missing basis count: %s", c.basisText)
			}
			if tc.level == "line" && !strings.Contains(c.basisText, "based on 6") {
				t.Fatal(c.basisText)
			}
			if tc.level == "default" && !strings.Contains(c.basisText, "uncalibrated") {
				t.Fatal(c.basisText)
			}
		})
	}
}

func TestPlanningLearningCacheSeparatesCells(t *testing.T) {
	cell := usagedashboard.LearningCell{ProfileID: "p", Family: "openai", Line: "astra", Version: "6", Effort: "xhigh", Kind: "backend", Bucket: "normal"}
	other := cell
	other.Bucket = "complex"
	pl := &planner{calibrations: map[routeKey]calibration{}}
	for range 5 {
		pl.learning = append(pl.learning, usagedashboard.LearningSample{Cell: cell, Hours: 1, Tokens: 1_000_000}, usagedashboard.LearningSample{Cell: other, Hours: 1, Tokens: 2_000_000})
	}
	a := &planRoute{cell: cell, key: routeKey{learning: "normal"}}
	b := &planRoute{cell: other, key: routeKey{learning: "complex"}}
	if pl.calibration(a).tokensPerHour != 1_000_000 || pl.calibration(b).tokensPerHour != 2_000_000 {
		t.Fatal("cell calibration reused across complexity buckets")
	}
}
