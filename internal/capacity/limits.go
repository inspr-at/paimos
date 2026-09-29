// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"time"
)

// LimitHit contains only vendor-established window bounds. An unbounded hit
// still stops the run, but cannot manufacture a percentage window or reset.
type LimitHit struct {
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	Readings []Reading  `json:"-"`
}

// VendorLimit recognizes structured protocol signals, never free-text errors.
// The caller must bind the frame to its owned account/session first.
func VendorLimit(vendor string, raw []byte, known []Reading, at time.Time) *LimitHit {
	if len(raw) > 1<<20 {
		return nil
	}
	type signal struct {
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Reached string          `json:"rateLimitReachedType"`
		Allowed *bool           `json:"ordinaryUsageAllowed"`
		Window  string          `json:"window"`
		Minutes int             `json:"window_minutes"`
		Reset   *int64          `json:"resets_at"`
	}
	var f struct {
		signal
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Error  json.RawMessage `json:"error"`
		Turn   struct {
			Error json.RawMessage `json:"error"`
		} `json:"turn"`
		Info struct {
			Status string `json:"status"`
			Kind   string `json:"rateLimitType"`
			Reset  *int64 `json:"resetsAt"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	if f.Method != "" {
		if json.Unmarshal(f.Params, &f) != nil {
			return nil
		}
	}
	if len(f.Turn.Error) > 0 {
		f.Error = f.Turn.Error
	}
	var data signal
	if len(f.Error) > 0 && string(f.Error) != "null" {
		var e struct {
			signal
			Data signal `json:"data"`
			Info signal `json:"codexErrorInfo"`
		}
		if json.Unmarshal(f.Error, &e) != nil {
			return nil
		}
		f.signal = e.signal
		data = e.Data
		if data.Reached == "" {
			data.Reached = e.Info.Reached
		}
	}
	code := func(v signal) string {
		var c string
		_ = json.Unmarshal(v.Code, &c)
		if c == "" {
			c = v.Type
		}
		return c
	}
	limited := false
	minutes := f.Minutes
	reset := f.Reset
	bucket := ""
	switch vendor {
	case "codex":
		limited = f.Reached != "" || data.Reached != "" || f.Allowed != nil && !*f.Allowed
	case "claude":
		limited = f.Type == "rate_limit_event" && f.Info.Status == "rejected"
		reset = f.Info.Reset
		bucket = f.Info.Kind
		switch f.Info.Kind {
		case "five_hour":
			minutes = 300
		case "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included":
			minutes = 10080
		}
	case "grok", "cursor", "pi":
		for _, c := range []string{code(f.signal), code(data)} {
			switch c {
			case "usage_limit_reached", "rate_limited", "usage_pool_exhausted", "rate_limit_exceeded", "quota_exceeded":
				limited = true
			}
		}
		// ACP structured HTTP status. Other errors remain ordinary failures.
		if vendor == "cursor" && string(f.Code) == "429" {
			limited = true
		}
	}
	if !limited {
		return nil
	}
	if minutes == 0 {
		minutes = data.Minutes
	}
	if reset == nil {
		reset = data.Reset
	}
	hit := &LimitHit{}
	if reset != nil {
		t := time.Unix(*reset, 0).UTC()
		if t.After(at) && t.Before(at.Add(366*24*time.Hour)) {
			hit.ResetsAt = &t
		}
	}
	if minutes > 0 {
		hit.Window = windowKind(minutes)
	}
	no := false
	if minutes > 0 && hit.ResetsAt != nil {
		r := Reading{WindowKind: hit.Window, Bucket: bucket, WindowMinutes: minutes, UsedPercent: 100, ResetsAt: *hit.ResetsAt, ReadAt: at, Source: "harness", Phase: "update", OrdinaryUsageAllowed: &no}
		if r.Validate(at) == nil {
			hit.Readings = append(hit.Readings, r)
		}
	}
	// Only vendor-denied/identified windows are made full. Do not turn an
	// unrelated weekly allowance into 100% when a five-hour limit is reached.
	for _, r := range known {
		if !r.ResetsAt.After(at) || (bucket != "" && r.Bucket != bucket) {
			continue
		}
		if bucket == "" && (r.OrdinaryUsageAllowed == nil || *r.OrdinaryUsageAllowed) && r.UsedPercent < 100 {
			continue
		}
		r.UsedPercent = 100
		r.OrdinaryUsageAllowed = &no
		r.ReadAt = at
		r.Source = "harness"
		r.Phase = "update"
		if r.Validate(at) != nil {
			continue
		}
		if len(hit.Readings) == 0 {
			hit.Readings = append(hit.Readings, r)
		} else {
			found := false
			for _, old := range hit.Readings {
				found = found || old.WindowKind == r.WindowKind && old.Bucket == r.Bucket
			}
			if !found {
				hit.Readings = append(hit.Readings, r)
			}
		}
		if hit.ResetsAt == nil || r.ResetsAt.After(*hit.ResetsAt) {
			t := r.ResetsAt
			hit.ResetsAt = &t
			hit.Window = r.WindowKind
		}
	}
	return hit
}

// ClaudeStatusline discards every field except the two documented quota
// windows. No transcript/config reader or vendor process is involved.
func ClaudeStatusline(raw []byte, at time.Time) []Reading {
	if len(raw) > 64<<10 {
		return nil
	}
	var in struct {
		RateLimits map[string]struct {
			Used  *float64 `json:"used_percentage"`
			Reset *int64   `json:"resets_at"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	out := []Reading{}
	for _, k := range []string{"five_hour", "seven_day"} {
		w, ok := in.RateLimits[k]
		if !ok || w.Used == nil || w.Reset == nil {
			continue
		}
		mins := 300
		if k == "seven_day" {
			mins = 10080
		}
		r := Reading{WindowKind: windowKind(mins), Bucket: k, WindowMinutes: mins, UsedPercent: *w.Used, ResetsAt: time.Unix(*w.Reset, 0).UTC(), ReadAt: at, Source: "harness", Phase: "update"}
		if r.Validate(at) == nil {
			out = append(out, r)
		}
	}
	return out
}
