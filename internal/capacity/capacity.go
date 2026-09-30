// SPDX-License-Identifier: AGPL-3.0-only

// Package capacity holds credential-free quota observations and pure pacing.
package capacity

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"time"
	_ "time/tzdata" // Schedules must work in the minimal runtime image too.
)

type Reading struct {
	PlusMinus            float64   `json:"plus_minus,omitempty"`
	Evidence             *Evidence `json:"evidence,omitempty"`
	WindowKind           string    `json:"window_kind"`
	Bucket               string    `json:"bucket,omitempty"`
	WindowMinutes        int       `json:"window_minutes"`
	UsedPercent          float64   `json:"used_percent"`
	ResetsAt             time.Time `json:"resets_at"`
	Plan                 string    `json:"plan,omitempty"`
	Source               string    `json:"source"`
	ReadAt               time.Time `json:"read_at"`
	OrdinaryUsageAllowed *bool     `json:"ordinary_usage_allowed,omitempty"`
	RunID                string    `json:"run_id,omitempty"`
	Phase                string    `json:"phase,omitempty"`
}

var label = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._ -]*$`)

func (r Reading) Validate(now time.Time) error {
	if math.IsNaN(r.PlusMinus) || math.IsInf(r.PlusMinus, 0) || r.PlusMinus < 0 || r.PlusMinus > 100 {
		return errors.New("invalid estimate uncertainty")
	}
	if r.Evidence != nil {
		if r.Source != "estimate" || r.Evidence.Samples < 1 || r.Evidence.Samples > 100000 {
			return errors.New("invalid estimate evidence")
		}
		switch r.Evidence.Kind {
		case "runs", "tokens", "limit_hits", "drift":
		default:
			return errors.New("invalid estimate evidence")
		}
	}

	if r.WindowMinutes < 1 || r.WindowMinutes > 527040 || math.IsNaN(r.UsedPercent) || math.IsInf(r.UsedPercent, 0) || r.UsedPercent < 0 || r.UsedPercent > 100 || r.ReadAt.IsZero() || r.ResetsAt.IsZero() || r.ReadAt.After(now.Add(time.Minute)) || !r.ResetsAt.After(r.ReadAt) || r.ReadAt.Before(r.StartsAt()) {
		return errors.New("invalid capacity reading")
	}
	switch r.WindowKind {
	case "5h":
		if r.WindowMinutes != 300 {
			return errors.New("invalid 5h duration")
		}
	case "weekly":
		if r.WindowMinutes != 10080 {
			return errors.New("invalid weekly duration")
		}
	case "monthly", "other":
	default:
		return errors.New("invalid window kind")
	}
	switch r.Source {
	case "harness", "agentd", "estimate":
	default:
		return errors.New("invalid source")
	}
	switch r.Phase {
	case "", "start", "update", "end":
	default:
		return errors.New("invalid phase")
	}
	if len(r.Bucket) > 64 || (r.Bucket != "" && !label.MatchString(r.Bucket)) || len(r.Plan) > 128 || (r.Plan != "" && !label.MatchString(r.Plan)) {
		return errors.New("invalid display metadata")
	}
	return nil
}

func (r Reading) StartsAt() time.Time {
	return r.ResetsAt.Add(-time.Duration(r.WindowMinutes) * time.Minute)
}
func (r Reading) Freshness(now time.Time) string {
	if !now.Before(r.ResetsAt) {
		return "expired"
	}
	age := now.Sub(r.ReadAt)
	if age <= 10*time.Minute {
		return "fresh"
	}
	if age <= time.Duration(r.WindowMinutes)*time.Minute/6 {
		return "aging"
	}
	return "stale"
}
func (r Reading) Routable(now time.Time) bool {
	return r.Freshness(now) == "fresh" && r.Source != "estimate" && (r.OrdinaryUsageAllowed == nil || *r.OrdinaryUsageAllowed)
}

// Enforce presence separately from numeric validation: missing utilization is
// unknown, never an observation of zero usage. Explicit zero remains valid.
func (r *Reading) UnmarshalJSON(raw []byte) error {
	type wire Reading
	var v wire
	if err := requiredJSON(raw, &v, "window_kind", "window_minutes", "used_percent", "resets_at", "source", "read_at"); err != nil {
		return err
	}
	*r = Reading(v)
	return nil
}
func requiredJSON(raw []byte, dst any, fields ...string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	for _, f := range fields {
		v, ok := obj[f]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return errors.New("missing required capacity field")
		}
	}
	return nil
}
