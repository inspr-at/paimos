// SPDX-License-Identifier: AGPL-3.0-only
// Package hostcapacity evaluates content-free host signals for new starts only.
package hostcapacity

import (
	"errors"
	"math"
	"time"
)

type Policy struct {
	Mode             string  `json:"mode"`
	MaximumAgents    int     `json:"maximum_agents"`
	MaximumLoad      float64 `json:"maximum_load"`
	WaitWhenBusy     bool    `json:"wait_when_busy"`
	EaseOnBattery    bool    `json:"ease_on_battery"`
	EaseWhenHot      bool    `json:"ease_when_hot"`
	ConsiderActivity bool    `json:"consider_activity"`
}

func Default() Policy {
	return Policy{Mode: "off", MaximumLoad: 30, WaitWhenBusy: true, EaseOnBattery: true, EaseWhenHot: true}
}
func (p Policy) Validate() error {
	if (p.Mode != "off" && p.Mode != "smart" && p.Mode != "fixed") || p.MaximumAgents < 0 || p.MaximumAgents > 64 || !finite(p.MaximumLoad) || p.MaximumLoad < 1 || p.MaximumLoad > 200 {
		return errors.New("invalid host capacity policy")
	}
	return nil
}

type Signals struct {
	Load           *float64 `json:"load"`
	Cores          int      `json:"cores"`
	MemoryPressure string   `json:"memory_pressure"`
	MemoryUsedGB   *float64 `json:"memory_used_gb,omitempty"`
	MemoryTotalGB  *float64 `json:"memory_total_gb,omitempty"`
	Power          string   `json:"power"`
	Thermal        string   `json:"thermal"`
	InputActive    *bool    `json:"input_active,omitempty"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (s Signals) Validate() error {
	if s.Cores < 1 || s.Cores > 4096 || s.Load != nil && (!finite(*s.Load) || *s.Load < 0 || *s.Load > 10000) {
		return errors.New("invalid host load")
	}
	for _, n := range []*float64{s.MemoryUsedGB, s.MemoryTotalGB} {
		if n != nil && (!finite(*n) || *n < 0 || *n > 1048576) {
			return errors.New("invalid host memory")
		}
	}
	if s.MemoryPressure != "unknown" && s.MemoryPressure != "normal" && s.MemoryPressure != "high" || s.Power != "unknown" && s.Power != "plugged_in" && s.Power != "battery" || s.Thermal != "unknown" && s.Thermal != "normal" && s.Thermal != "hot" {
		return errors.New("invalid host signals")
	}
	return nil
}

type Point struct {
	At   time.Time `json:"at"`
	Load float64   `json:"load"`
}
type View struct {
	Policy     Policy     `json:"policy"`
	Signals    *Signals   `json:"signals"`
	ReportedAt *time.Time `json:"reported_at"`
	Running    int        `json:"running"`
	Queued     int        `json:"queued"`
	Reason     string     `json:"reason"`
	LoadLimit  float64    `json:"load_limit"`
	History    []Point    `json:"history"`
}

// Evaluate uses an injected clock. Missing or old signals cannot authorize an
// enabled policy; Off preserves existing start behavior and ignores telemetry.
func Evaluate(p Policy, s *Signals, at *time.Time, running int, now time.Time) (string, float64) {
	if p.MaximumAgents > 0 && running >= p.MaximumAgents {
		return "maximum_agents", 0
	}
	if p.Mode == "off" {
		return "", 0
	}
	if s == nil || at == nil || at.After(now) || now.Sub(*at) > time.Minute {
		return "host_signals_stale", 0
	}
	limit := p.MaximumLoad
	if p.Mode == "smart" {
		limit = math.Max(1, float64(s.Cores)*5/3)
		if p.ConsiderActivity && s.InputActive == nil {
			return "host_activity_unknown", limit
		}
		if p.EaseOnBattery && s.Power == "battery" {
			limit *= .5
		}
		if p.EaseWhenHot && s.Thermal == "hot" {
			limit *= .5
		}
		if p.ConsiderActivity && s.InputActive != nil && *s.InputActive {
			limit *= .5
		}
		if p.WaitWhenBusy && s.MemoryPressure == "high" {
			return "memory_pressure", limit
		}
		if !p.WaitWhenBusy && !(p.EaseOnBattery && s.Power == "battery" || p.EaseWhenHot && s.Thermal == "hot" || p.ConsiderActivity && s.InputActive != nil && *s.InputActive) {
			return "", limit
		}
	}
	if s.Load == nil {
		return "host_load_unknown", limit
	}
	if *s.Load > limit {
		return "host_load", limit
	}
	return "", limit
}
