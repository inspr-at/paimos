// SPDX-License-Identifier: AGPL-3.0-only

// Package servicetier describes serving speed independently of reasoning effort.
// Vendor facts are pinned, never fetched at runtime. Missing prices fail closed.
package servicetier

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const Capability = "service_tier_v1"

func Valid(t string) bool { return t == "default" || t == "fast" || t == "fastest" }

type Tier struct {
	Tier            string   `json:"tier"`
	Name            string   `json:"name"`
	SpeedFactor     *float64 `json:"speed_factor"`
	PriceMultiplier *float64 `json:"price_multiplier"`
	UsageMultiplier *float64 `json:"usage_multiplier"`
	Mechanism       string   `json:"mechanism"`
	Offered         bool     `json:"offered"`
	Reason          string   `json:"reason,omitempty"`
}

type Report struct {
	Harness            string    `json:"harness"`
	Model              string    `json:"model"`
	HarnessVersion     string    `json:"harness_version"`
	AdapterVersion     string    `json:"adapter_version"`
	CheckedAt          time.Time `json:"checked_at"`
	Source             string    `json:"source"`
	Applies            string    `json:"applies"`
	ChangeInstructions string    `json:"change_instructions"`
	Tiers              []Tier    `json:"tiers"`
}

func number(n float64) *float64 { return &n }

// Advertised reports only exact models with published prices. Version checks
// qualify the launch mechanism; they do not assert account entitlement.
func Advertised(harness, model, version string) Report {
	r := Report{Harness: harness, Model: model, HarnessVersion: version, AdapterVersion: "aeon-service-tier-v1",
		CheckedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), Source: "adapter default-only report", Applies: "next_run"}
	r.Tiers = []Tier{{Tier: "default", Name: "Default", Offered: true, SpeedFactor: number(1), PriceMultiplier: number(1), UsageMultiplier: number(1), Mechanism: "default"},
		{Tier: "fast", Name: "Fast", Reason: "No published price and supported mechanism for this harness/model"},
		{Tier: "fastest", Name: "Fastest", Reason: "No published price and supported mechanism for this harness/model"}}
	switch harness {
	case "codex":
		r.Source = "https://learn.chatgpt.com/docs/agent-configuration/speed"
		r.ChangeInstructions = "Change it in its terminal with /fast or the vendor service_tier setting"
		r.Tiers[0].Mechanism = "service_tier=default"
		if atLeast(version, 0, 159, 2) && (model == "gpt-6.1-sol" || model == "gpt-6-astra" || model == "gpt-6-sol" || model == "gpt-6-luna") {
			r.Tiers[1] = Tier{Tier: "fast", Name: "Fast", Offered: true, PriceMultiplier: number(2), UsageMultiplier: number(2.5), Mechanism: "service_tier=fast"}
		}
		// Ultrafast access is account-controlled, so advertising model support
		// alone cannot make it selectable. A qualified reporter may supply it.
		r.Tiers[2].Reason = "Ultrafast requires advertised model support, a published price and account access"
	case "claude":
		r.Source = "https://code.claude.com/docs/en/fast-mode"
		r.Applies = "next_turn"
		r.ChangeInstructions = "Change it in its terminal with /fast"
		r.Tiers[0].Mechanism = "fastMode=false"
		if atLeast(version, 2, 1, 205) && (model == "claude-opus-5-5" || model == "claude-opus-5" || model == "claude-opus-4-8") {
			r.Tiers[1] = Tier{Tier: "fast", Name: "Fast", Offered: true, SpeedFactor: number(2.5), PriceMultiplier: number(2), Mechanism: "fastMode=true"}
		}
		r.Tiers[2].Reason = "Claude Code advertises no tier above fast mode"
	}
	return r
}

func atLeast(version string, major, minor, patch int) bool {
	fields := strings.Fields(version)
	if len(fields) == 0 {
		return false
	}
	v := strings.Split(strings.TrimPrefix(fields[len(fields)-1], "v"), ".")
	if len(v) != 3 {
		return false
	}
	want := []int{major, minor, patch}
	parsed := make([]int, len(v))
	for i, part := range v {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return false
		}
		parsed[i] = n
	}
	for i := range v {
		n, err := strconv.Atoi(v[i])
		if err != nil || n < 0 {
			return false
		}
		if parsed[i] != want[i] {
			return parsed[i] > want[i]
		}
	}
	return true
}

func (r Report) Find(tier string) (Tier, bool) {
	for _, t := range r.Tiers {
		if t.Tier == tier {
			return t, t.Offered && t.PriceMultiplier != nil
		}
	}
	return Tier{}, false
}

func (r Report) Validate() error {
	if r.Model == "" || len(r.Model) > 128 || r.HarnessVersion == "" || len(r.HarnessVersion) > 80 || r.AdapterVersion == "" || len(r.AdapterVersion) > 80 || r.CheckedAt.IsZero() || len(r.Source) > 256 || (r.Applies != "next_run" && r.Applies != "next_turn") || len(r.ChangeInstructions) > 256 || len(r.Tiers) != 3 {
		return errors.New("invalid tier report provenance")
	}
	seen := map[string]bool{}
	for _, t := range r.Tiers {
		if !Valid(t.Tier) || seen[t.Tier] || t.Name != map[string]string{"default": "Default", "fast": "Fast", "fastest": "Fastest"}[t.Tier] {
			return errors.New("invalid or duplicate tier")
		}
		seen[t.Tier] = true
		for _, n := range []*float64{t.SpeedFactor, t.PriceMultiplier, t.UsageMultiplier} {
			if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n <= 0 || *n > 1000) {
				return errors.New("invalid tier factor")
			}
		}
		if t.Offered != (t.PriceMultiplier != nil) || (t.Offered && t.Mechanism == "") || (!t.Offered && (t.Reason == "" || len(t.Reason) > 256)) {
			return errors.New("offered tier requires price and mechanism; unavailable tier requires reason")
		}
		if t.Offered {
			mechanism := "default"
			switch r.Harness {
			case "codex":
				mechanism = "service_tier=" + map[string]string{"default": "default", "fast": "fast", "fastest": "ultrafast"}[t.Tier]
			case "claude":
				if t.Tier == "fastest" {
					return errors.New("Claude fastest unavailable")
				}
				mechanism = "fastMode=" + strconv.FormatBool(t.Tier == "fast")
			default:
				if t.Tier != "default" {
					return errors.New("adapter supports Default only")
				}
			}
			if t.Mechanism != mechanism {
				return fmt.Errorf("invalid %s mechanism", t.Tier)
			}
		}
	}
	d, ok := r.Find("default")
	if !ok || *d.PriceMultiplier != 1 {
		return errors.New("Default price must be one")
	}
	return nil
}
