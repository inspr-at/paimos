// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"sort"
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
	switch vendor {
	case "codex":
		return codexLimit(raw, known, at)
	case "claude":
		return claudeLimit(raw, known, at)
	case "grok":
		return grokLimit(raw)
	case "pi":
		return piLimit(raw)
	case "cursor":
		// cursor-agent 2026.09.18 mentions quota_exceeded only inside
		// agent-store sync (storage), and no ACP 429 quota signal was found.
		// Leave Cursor unmapped until a model-quota frame is evidenced.
		return nil
	default:
		return nil
	}
}

// Codex 0.159.0 publishes these rateLimitReachedType values. A snapshot-level
// value stops the run and does not say whether the 5-hour or weekly window
// was the one that filled. Only a value on that window object names it.
func codexRateReached(v string) bool {
	switch v {
	case "rate_limit_reached", "workspace_owner_credits_depleted", "workspace_member_credits_depleted", "workspace_owner_usage_limit_reached", "workspace_member_usage_limit_reached":
		return true
	default:
		return false
	}
}

type codexNamed struct {
	bucket  string
	minutes int
	reset   *int64
}

type codexWalk struct {
	stop  bool
	reset *int64
	named []codexNamed
}

func codexLimit(raw []byte, known []Reading, at time.Time) *LimitHit {
	st := &codexWalk{}
	walkCodexQuota(unwrapMethod(raw), "", 0, st)
	if !st.stop {
		return nil
	}
	hit := &LimitHit{}
	if len(st.named) == 0 {
		if st.reset != nil {
			hit.ResetsAt = validReset(*st.reset, at)
		}
		return hit
	}
	no := false
	for _, n := range st.named {
		reset := (*time.Time)(nil)
		if n.reset != nil {
			reset = validReset(*n.reset, at)
		}
		matched := false
		for _, r := range known {
			if !namedWindow(r, n) || !r.ResetsAt.After(at) {
				continue
			}
			matched = true
			if reset != nil {
				r.ResetsAt = *reset
			}
			r.UsedPercent = 100
			r.OrdinaryUsageAllowed = &no
			r.ReadAt = at
			r.Source = "harness"
			r.Phase = "update"
			addNamedReading(hit, r, at)
		}
		if matched || n.minutes < 1 || reset == nil {
			continue
		}
		addNamedReading(hit, Reading{WindowKind: windowKind(n.minutes), Bucket: safeBucket(n.bucket), WindowMinutes: n.minutes, UsedPercent: 100, ResetsAt: *reset, ReadAt: at, Source: "harness", Phase: "update", OrdinaryUsageAllowed: &no}, at)
	}
	if len(hit.Readings) == 0 {
		hit.Window = ""
		if st.reset != nil {
			hit.ResetsAt = validReset(*st.reset, at)
		}
	}
	return hit
}

func namedWindow(r Reading, n codexNamed) bool {
	if n.minutes > 0 && r.WindowMinutes != n.minutes {
		return false
	}
	if n.bucket != "" && r.Bucket != "" && r.Bucket != n.bucket {
		return false
	}
	return n.minutes > 0 || n.bucket != ""
}

func addNamedReading(hit *LimitHit, r Reading, at time.Time) {
	if r.Validate(at) != nil {
		return
	}
	for _, old := range hit.Readings {
		if old.WindowKind == r.WindowKind && old.Bucket == r.Bucket {
			return
		}
	}
	hit.Readings = append(hit.Readings, r)
	if hit.ResetsAt == nil || r.ResetsAt.After(*hit.ResetsAt) {
		t := r.ResetsAt
		hit.ResetsAt = &t
		hit.Window = r.WindowKind
	}
}

func walkCodexQuota(raw json.RawMessage, bucket string, depth int, st *codexWalk) {
	if depth > 8 || len(raw) == 0 || string(raw) == "null" {
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	if info, ok := obj["codexErrorInfo"]; ok {
		if hit, reset := codexUsageLimit(info); hit {
			st.stop = true
			if reset != nil {
				st.reset = reset
			}
		}
	}
	windowShape := obj["windowDurationMins"] != nil || obj["usedPercent"] != nil
	container := obj["primary"] != nil || obj["secondary"] != nil || obj["rateLimits"] != nil || obj["rateLimitsByLimitId"] != nil
	if allowed := jsonBool(obj["ordinaryUsageAllowed"]); allowed != nil && !*allowed {
		st.stop = true
		if !windowShape && st.reset == nil {
			st.reset = jsonUnix(obj["resetsAt"], obj["resets_at"])
		}
	}
	if reached, ok := jsonString(obj["rateLimitReachedType"]); ok && codexRateReached(reached) {
		st.stop = true
		if windowShape && !container {
			mins := 0
			if n := jsonInt(obj["windowDurationMins"]); n != nil {
				mins = int(*n)
			}
			st.named = append(st.named, codexNamed{bucket: bucket, minutes: mins, reset: jsonUnix(obj["resetsAt"], obj["resets_at"])})
		} else if !windowShape && st.reset == nil {
			st.reset = jsonUnix(obj["resetsAt"], obj["resets_at"])
		}
	}
	if next, ok := obj["rateLimits"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
	if next, ok := obj["primary"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
	if next, ok := obj["secondary"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
	if next, ok := obj["rateLimitsByLimitId"]; ok {
		var by map[string]json.RawMessage
		if json.Unmarshal(next, &by) == nil {
			keys := make([]string, 0, len(by))
			for k := range by {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walkCodexQuota(by[k], k, depth+1, st)
			}
		}
	}
	if next, ok := obj["error"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
	if next, ok := obj["data"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
	if next, ok := obj["turn"]; ok {
		walkCodexQuota(next, bucket, depth+1, st)
	}
}

// usageLimitExceeded is a Codex error variant. 0.159.0 sends codexErrorInfo as
// the plain string; an object form may also carry limitId and resetsAt.
// limitId does not choose a window.
func codexUsageLimit(raw json.RawMessage) (bool, *int64) {
	if s, ok := jsonString(raw); ok {
		return s == "usageLimitExceeded", nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return false, nil
	}
	if s, ok := jsonString(obj["type"]); ok && s == "usageLimitExceeded" {
		return true, jsonUnix(obj["resetsAt"], obj["resets_at"])
	}
	if inner, ok := obj["usageLimitExceeded"]; ok {
		var nested map[string]json.RawMessage
		if json.Unmarshal(inner, &nested) == nil {
			return true, jsonUnix(nested["resetsAt"], nested["resets_at"])
		}
		return true, nil
	}
	return false, nil
}

func claudeLimit(raw []byte, known []Reading, at time.Time) *LimitHit {
	var f struct {
		Type string `json:"type"`
		Info struct {
			Status string `json:"status"`
			Kind   string `json:"rateLimitType"`
			Reset  *int64 `json:"resetsAt"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &f) != nil || f.Type != "rate_limit_event" || f.Info.Status != "rejected" {
		return nil
	}
	minutes := 0
	switch f.Info.Kind {
	case "five_hour":
		minutes = 300
	case "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included":
		minutes = 10080
	}
	hit := &LimitHit{}
	if f.Info.Reset != nil {
		hit.ResetsAt = validReset(*f.Info.Reset, at)
	}
	if minutes == 0 {
		// A rejected event without rateLimitType names no window. Keep an
		// explicit reset and leave every stored percentage alone.
		return hit
	}
	no := false
	matched := false
	for _, r := range known {
		if r.Bucket != f.Info.Kind || !r.ResetsAt.After(at) {
			continue
		}
		matched = true
		if hit.ResetsAt != nil {
			r.ResetsAt = *hit.ResetsAt
		}
		r.UsedPercent = 100
		r.OrdinaryUsageAllowed = &no
		r.ReadAt = at
		r.Source = "harness"
		r.Phase = "update"
		r.WindowMinutes = minutes
		r.WindowKind = windowKind(minutes)
		addNamedReading(hit, r, at)
	}
	if !matched && hit.ResetsAt != nil {
		addNamedReading(hit, Reading{WindowKind: windowKind(minutes), Bucket: f.Info.Kind, WindowMinutes: minutes, UsedPercent: 100, ResetsAt: *hit.ResetsAt, ReadAt: at, Source: "harness", Phase: "update", OrdinaryUsageAllowed: &no}, at)
	}
	if len(hit.Readings) == 0 {
		hit.Window = ""
	}
	return hit
}

// Grok 1.0.44 publishes error_type. The ticket names usage_limit_reached,
// rate_limited and usage_pool_exhausted; global_rate_limit is the same enum
// and is also a quota stop. concurrency_limit and storage quota_exceeded are not.
// Nothing in that enum supplies a window or a reset, so none is recorded.
func grokLimit(raw []byte) *LimitHit {
	for _, value := range grokErrorTypes(unwrapMethod(raw)) {
		switch value {
		case "usage_limit_reached", "rate_limited", "usage_pool_exhausted", "global_rate_limit":
			return &LimitHit{}
		}
	}
	return nil
}

func grokErrorTypes(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	out := []string{}
	if s, ok := jsonString(obj["error_type"]); ok {
		out = append(out, s)
	}
	if errRaw, ok := obj["error"]; ok {
		var e map[string]json.RawMessage
		if json.Unmarshal(errRaw, &e) == nil {
			if s, ok := jsonString(e["error_type"]); ok {
				out = append(out, s)
			}
			if data, ok := e["data"]; ok {
				var d map[string]json.RawMessage
				if json.Unmarshal(data, &d) == nil {
					if s, ok := jsonString(d["error_type"]); ok {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

func piLimit(raw []byte) *LimitHit {
	for _, code := range piCodes(unwrapMethod(raw)) {
		if code == "rate_limit_exceeded" {
			return &LimitHit{}
		}
	}
	return nil
}

func piCodes(raw json.RawMessage) []string {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	out := []string{}
	if s, ok := jsonString(obj["code"]); ok {
		out = append(out, s)
	}
	if errRaw, ok := obj["error"]; ok {
		var e map[string]json.RawMessage
		if json.Unmarshal(errRaw, &e) == nil {
			if s, ok := jsonString(e["code"]); ok {
				out = append(out, s)
			}
			if data, ok := e["data"]; ok {
				var d map[string]json.RawMessage
				if json.Unmarshal(data, &d) == nil {
					if s, ok := jsonString(d["code"]); ok {
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

func unwrapMethod(raw []byte) json.RawMessage {
	var root struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(raw, &root) == nil && root.Method != "" && len(root.Params) > 0 {
		return root.Params
	}
	return raw
}

func validReset(unix int64, at time.Time) *time.Time {
	t := time.Unix(unix, 0).UTC()
	if t.After(at) && t.Before(at.Add(366*24*time.Hour)) {
		return &t
	}
	return nil
}

func safeBucket(s string) string {
	if s == "" || len(s) > 64 || !label.MatchString(s) {
		return ""
	}
	return s
}

func jsonString(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

func jsonBool(raw json.RawMessage) *bool {
	if len(raw) == 0 {
		return nil
	}
	var v bool
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func jsonInt(raw json.RawMessage) *int64 {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var n int64
	if json.Unmarshal(raw, &n) == nil {
		return &n
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil {
		return nil
	}
	n = int64(f)
	return &n
}

func jsonUnix(first, second json.RawMessage) *int64 {
	if n := jsonInt(first); n != nil {
		return n
	}
	return jsonInt(second)
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
