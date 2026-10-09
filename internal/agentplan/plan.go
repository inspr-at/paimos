// SPDX-License-Identifier: AGPL-3.0-only

// Package agentplan validates the person's start ceiling. It does not decide
// account capacity, reserve starts, or interrupt already-running work.
package agentplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const PreferenceKey = "agents.working"
const ReadScope = "agents.plan.read"
const DefaultTotal = 15
const MaxTotal = 30

type Mode string

const (
	NoLimit Mode = "no_limit"
	Off     Mode = "off"
	AtMost  Mode = "at_most"
)

// Limit uses a discriminant in Go and a string or integer on the wire. In
// particular, AtMost with Value=0 never means NoLimit.
type Limit struct {
	Mode  Mode
	Value int
}

func (l Limit) Validate() error {
	switch l.Mode {
	case NoLimit, Off:
		if l.Value == 0 {
			return nil
		}
	case AtMost:
		if l.Value >= 0 && l.Value <= MaxTotal {
			return nil
		}
	}
	return errors.New("harness limit must be no_limit, off or an integer from 0 to 30")
}

func (l Limit) MarshalJSON() ([]byte, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	if l.Mode == AtMost {
		return json.Marshal(l.Value)
	}
	return json.Marshal(l.Mode)
}

func (l *Limit) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	var next Limit
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &next.Mode); err != nil {
			return err
		}
		if next.Mode != NoLimit && next.Mode != Off {
			return errors.New("harness limit string must be no_limit or off")
		}
	} else {
		next.Mode = AtMost
		if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &next.Value) != nil {
			return errors.New("harness limit must be no_limit, off or an integer from 0 to 30")
		}
	}
	if err := next.Validate(); err != nil {
		return err
	}
	*l = next
	return nil
}

type Plan struct {
	Total  int                      `json:"total"`
	Limits map[string]Limit         `json:"limits"`
	Daily  map[string]DailySettings `json:"daily,omitempty"`
}

func Default() Plan {
	return Plan{Total: DefaultTotal, Limits: map[string]Limit{}}
}

func HarnessLabel(harness string) string {
	switch harness {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "cursor":
		return "Cursor"
	case "gemini":
		return "Gemini CLI"
	case "grok":
		return "Grok"
	case "opencode":
		return "OpenCode"
	case "pi":
		return "Pi"
	default:
		return ""
	}
}

func (p Plan) Validate() error {
	if p.Total < 0 || p.Total > MaxTotal {
		return errors.New("planned total must be an integer from 0 to 30")
	}
	for harness, limit := range p.Limits {
		if HarnessLabel(harness) == "" {
			return errors.New("unknown harness in plan")
		}
		if err := limit.Validate(); err != nil {
			return err
		}
	}
	for harness, settings := range p.Daily {
		if HarnessLabel(harness) == "" {
			return errors.New("unknown harness in daily plan")
		}
		if err := settings.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Decode accepts the canonical plan and the existing AEON-499 preference.
// Legacy cap becomes total; area/model reservations are deliberately ignored,
// since minimum reservations cannot be interpreted as maximum limits.
// A missing preference or a legacy preference without cap keeps the launcher's
// existing ceiling of 15 until the person explicitly chooses a total.
func Decode(raw []byte) (Plan, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Default(), "default", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return Plan{}, "", errors.New("plan must be a JSON object")
	}
	_, total := fields["total"]
	_, limits := fields["limits"]
	_, daily := fields["daily"]
	_, legacyCap := fields["cap"]
	_, legacyView := fields["view"]
	_, legacyArea := fields["area"]
	_, legacyModel := fields["model"]
	if total || limits || daily && !legacyCap && !legacyView && !legacyArea && !legacyModel {
		var in struct {
			Total  *int                     `json:"total"`
			Limits map[string]Limit         `json:"limits"`
			Daily  map[string]DailySettings `json:"daily"`
		}
		if err := strictJSON(raw, &in); err != nil {
			return Plan{}, "", err
		}
		if in.Total == nil {
			return Plan{}, "", errors.New("planned total is required")
		}
		if value, exists := fields["limits"]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Plan{}, "", errors.New("limits must be a JSON object")
		}
		if value, exists := fields["daily"]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Plan{}, "", errors.New("daily must be a JSON object")
		}
		p := Plan{Total: *in.Total, Limits: in.Limits, Daily: in.Daily}
		if p.Limits == nil {
			p.Limits = map[string]Limit{}
		}
		return p, "plan", p.Validate()
	}
	var old struct {
		Cap   *int                     `json:"cap"`
		View  string                   `json:"view"`
		Area  map[string]float64       `json:"area"`
		Model map[string]float64       `json:"model"`
		Daily map[string]DailySettings `json:"daily"`
	}
	if err := strictJSON(raw, &old); err != nil {
		return Plan{}, "", err
	}
	if old.View != "" && old.View != "area" && old.View != "model" {
		return Plan{}, "", errors.New("invalid legacy working view")
	}
	p := Default()
	p.Daily = old.Daily
	if value, exists := fields["daily"]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return Plan{}, "", errors.New("daily must be a JSON object")
	}
	if value, exists := fields["cap"]; exists {
		if old.Cap == nil || *old.Cap < 1 || *old.Cap > 12 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return Plan{}, "", errors.New("legacy cap must be an integer from 1 to 12")
		}
		p.Total = *old.Cap
	}
	return p, "legacy", p.Validate()
}

func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return errors.New("invalid plan JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return errors.New("expected one plan object")
	}
	return nil
}

// CanStart checks a snapshot, including counts above a newly lowered plan.
// Callers must serialize competing starts and check account room separately.
// Every harness contributes to the total, even when that harness is now Off.
func CanStart(plan Plan, running map[string]int, harness string) (bool, string) {
	if err := plan.Validate(); err != nil {
		return false, "invalid plan"
	}
	label := HarnessLabel(harness)
	if label == "" {
		return false, "unknown harness"
	}
	total := 0
	for _, count := range running {
		if count < 0 {
			return false, "invalid running count"
		}
		// Saturating at 31 avoids overflow without changing the decision.
		total += min(count, MaxTotal+1)
		total = min(total, MaxTotal+1)
	}
	if total >= plan.Total {
		return false, "planned total reached"
	}
	limit, exists := plan.Limits[harness]
	if exists {
		switch limit.Mode {
		case Off:
			return false, label + " is off"
		case AtMost:
			if running[harness] >= limit.Value {
				return false, fmt.Sprintf("%s at its limit", label)
			}
		}
	}
	return true, ""
}
