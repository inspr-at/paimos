// SPDX-License-Identifier: AGPL-3.0-only
package hostcapacity

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestHostCapacityAdmission(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-time.Minute - time.Nanosecond)
	load := 32.4
	active := true
	signals := Signals{Load: &load, Cores: 18, MemoryPressure: "normal", Power: "plugged_in", Thermal: "normal", InputActive: &active}
	smart := Default()
	smart.Mode = "smart"
	cases := []struct {
		name    string
		policy  Policy
		signals *Signals
		at      *time.Time
		running int
		reason  string
		limit   float64
	}{
		{"default off ignores old load", Default(), &signals, &stale, 20, "", 0},
		{"smart eighteen cores", smart, &signals, &now, 8, "host_load", 30},
		{"fixed", func() Policy { p := smart; p.Mode = "fixed"; p.MaximumLoad = 40; return p }(), &signals, &now, 8, "", 40},
		{"stale", smart, &signals, &stale, 0, "host_signals_stale", 0},
		{"no sample", smart, nil, nil, 0, "host_signals_stale", 0},
		{"missing load", smart, &Signals{Cores: 18, MemoryPressure: "normal", Power: "unknown", Thermal: "unknown"}, &now, 0, "host_load_unknown", 30},
		{"activity opt in only", func() Policy { p := smart; p.ConsiderActivity = true; return p }(), &signals, &now, 0, "host_load", 15},
		{"missing opted-in activity", func() Policy { p := smart; p.ConsiderActivity = true; return p }(), func() *Signals { s := signals; s.InputActive = nil; return &s }(), &now, 0, "host_activity_unknown", 30},
		{"battery", smart, func() *Signals { s := signals; s.Power = "battery"; return &s }(), &now, 0, "host_load", 15},
		{"heat", smart, func() *Signals { s := signals; s.Thermal = "hot"; return &s }(), &now, 0, "host_load", 15},
		{"memory", smart, func() *Signals { s := signals; s.MemoryPressure = "high"; return &s }(), &now, 0, "memory_pressure", 30},
		{"max applies in off", func() Policy { p := Default(); p.MaximumAgents = 12; return p }(), nil, nil, 12, "maximum_agents", 0},
		{"busy switch off", func() Policy { p := smart; p.WaitWhenBusy = false; return p }(), &signals, &now, 0, "", 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reason, limit := Evaluate(c.policy, c.signals, c.at, c.running, now)
			if reason != c.reason || limit != c.limit {
				t.Fatalf("got %s %v, want %s %v", reason, limit, c.reason, c.limit)
			}
		})
	}
	load = 30
	if reason, _ := Evaluate(smart, &signals, &now, 8, now); reason != "" {
		t.Fatal("equal threshold must allow a new start", reason)
	}
	load = 29
	if reason, _ := Evaluate(smart, &signals, &now, 8, now); reason != "" {
		t.Fatal("falling load must resume starts", reason)
	}
}
func TestHostCapacityBounds(t *testing.T) {
	for _, p := range []Policy{{}, {Mode: "smart", MaximumLoad: math.Inf(1)}, {Mode: "off", MaximumLoad: 30, MaximumAgents: 65}, {Mode: "fixed", MaximumLoad: 0}} {
		if p.Validate() == nil {
			t.Fatal("accepted invalid policy", p)
		}
	}
	if Default().Validate() != nil {
		t.Fatal("default invalid")
	}
	n := math.NaN()
	if (Signals{Load: &n, Cores: 18, MemoryPressure: "normal", Power: "unknown", Thermal: "normal"}).Validate() == nil {
		t.Fatal("accepted NaN")
	}
}

// Migration 1250 has no size CHECKs; the widest valid values must still encode
// within the 2 KiB budget the columns were designed for.
func TestHostCapacityLargestEncodingsStayBounded(t *testing.T) {
	big, yes := 1048576.0, true
	load := 10000.0
	policy := Policy{Mode: "smart", MaximumAgents: 64, MaximumLoad: 199.99999999999997, WaitWhenBusy: true, EaseOnBattery: true, EaseWhenHot: true, ConsiderActivity: true}
	signals := Signals{Load: &load, Cores: 4096, MemoryPressure: "unknown", MemoryUsedGB: &big, MemoryTotalGB: &big, Power: "plugged_in", Thermal: "unknown", InputActive: &yes}
	if policy.Validate() != nil || signals.Validate() != nil {
		t.Fatal("fixture must be valid")
	}
	for _, v := range []any{policy, signals} {
		raw, err := json.Marshal(v)
		if err != nil || len(raw) > 512 {
			t.Fatal("encoding exceeds bound", len(raw), err)
		}
	}
}
