// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// Parser retains only allowlisted quota fields, never raw frames, identities,
// credit balances, prompts, paths or vendor authentication material.
// Codex notifications are sparse; omitted fields do not erase a vendor denial.
type Parser struct {
	codex         map[string]codexSnapshot
	claude        map[string]Reading
	allowed       *bool
	codexReadings map[string]Reading
}
type codexWindow struct {
	Used    *float64 `json:"usedPercent"`
	Minutes *int     `json:"windowDurationMins"`
	Reset   *int64   `json:"resetsAt"`
}
type codexSnapshot struct {
	LimitID   string       `json:"limitId"`
	Plan      string       `json:"planType"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

func mergeWindow(old, next *codexWindow) *codexWindow {
	if next == nil {
		return old
	}
	if old == nil {
		old = &codexWindow{}
	}
	if next.Used != nil {
		old.Used = next.Used
	}
	if next.Minutes != nil {
		old.Minutes = next.Minutes
	}
	if next.Reset != nil {
		old.Reset = next.Reset
	}
	return old
}

func (p *Parser) Codex(raw []byte, at time.Time) []Reading {
	if len(raw) > 1<<20 {
		return nil
	}
	var frame struct {
		Method    string          `json:"method"`
		Params    json.RawMessage `json:"params"`
		Result    json.RawMessage `json:"result"`
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(raw, &frame) != nil {
		return nil
	}
	if frame.Method != "" {
		if frame.Method != "account/rateLimits/updated" && frame.Method != "rateLimits/updated" {
			return nil
		}
		raw = frame.Params
	} else if len(frame.Result) > 0 {
		raw = frame.Result
	}
	if frame.Type == "event_msg" {
		var log struct {
			Type   string `json:"type"`
			Limits struct {
				ID      string `json:"limit_id"`
				Plan    string `json:"plan_type"`
				Primary *struct {
					Used    *float64 `json:"used_percent"`
					Minutes *int     `json:"window_minutes"`
					Reset   *int64   `json:"resets_at"`
				} `json:"primary"`
				Secondary *struct {
					Used    *float64 `json:"used_percent"`
					Minutes *int     `json:"window_minutes"`
					Reset   *int64   `json:"resets_at"`
				} `json:"secondary"`
			} `json:"rate_limits"`
		}
		if json.Unmarshal(frame.Payload, &log) != nil || log.Type != "token_count" || frame.Timestamp.IsZero() {
			return nil
		}
		at = frame.Timestamp
		snapshot := codexSnapshot{LimitID: log.Limits.ID, Plan: log.Limits.Plan}
		if log.Limits.Primary != nil {
			w := log.Limits.Primary
			snapshot.Primary = &codexWindow{w.Used, w.Minutes, w.Reset}
		}
		if log.Limits.Secondary != nil {
			w := log.Limits.Secondary
			snapshot.Secondary = &codexWindow{w.Used, w.Minutes, w.Reset}
		}
		data, _ := json.Marshal(struct {
			RateLimits codexSnapshot `json:"rateLimits"`
		}{snapshot})
		raw = data
	}
	var in struct {
		RateLimits *codexSnapshot           `json:"rateLimits"`
		ByID       map[string]codexSnapshot `json:"rateLimitsByLimitId"`
		Allowed    *bool                    `json:"ordinaryUsageAllowed"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	if in.Allowed != nil {
		v := *in.Allowed
		p.allowed = &v
	}
	if p.codex == nil {
		p.codex = map[string]codexSnapshot{}
	}
	snapshots := map[string]codexSnapshot{}
	if in.RateLimits != nil {
		key := in.RateLimits.LimitID
		if key == "" {
			key = "codex"
		}
		snapshots[key] = *in.RateLimits
	}
	for k, v := range in.ByID {
		snapshots[k] = v
	}
	keys := []string{}
	for k := range snapshots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []Reading{}
	for _, k := range keys {
		if len(k) > 64 || !label.MatchString(k) {
			continue
		}
		n := snapshots[k]
		old := p.codex[k]
		if len(p.codex) >= 32 && old.LimitID == "" {
			continue
		}
		old.LimitID = k
		if n.Plan != "" {
			old.Plan = n.Plan
		}
		old.Primary = mergeWindow(old.Primary, n.Primary)
		old.Secondary = mergeWindow(old.Secondary, n.Secondary)
		p.codex[k] = old
		for i, w := range []*codexWindow{old.Primary, old.Secondary} {
			if in.Allowed == nil && ((i == 0 && n.Primary == nil) || (i == 1 && n.Secondary == nil)) {
				continue
			}
			if w == nil || w.Used == nil || w.Minutes == nil || w.Reset == nil {
				continue
			}
			r := Reading{WindowKind: windowKind(*w.Minutes), Bucket: k, WindowMinutes: *w.Minutes, UsedPercent: *w.Used, ResetsAt: time.Unix(*w.Reset, 0).UTC(), ReadAt: at, Source: "harness", Plan: old.Plan, OrdinaryUsageAllowed: p.allowed}
			if r.Validate(at) == nil {
				out = append(out, r)
			}
		}
	}
	// A sparse authority-only update must apply to every known window too.
	if len(snapshots) == 0 && in.Allowed != nil {
		for _, s := range p.codex {
			for _, w := range []*codexWindow{s.Primary, s.Secondary} {
				if w == nil || w.Used == nil || w.Minutes == nil || w.Reset == nil {
					continue
				}
				r := Reading{WindowKind: windowKind(*w.Minutes), Bucket: s.LimitID, WindowMinutes: *w.Minutes, UsedPercent: *w.Used, ResetsAt: time.Unix(*w.Reset, 0).UTC(), ReadAt: at, Source: "harness", Plan: s.Plan, OrdinaryUsageAllowed: p.allowed}
				if r.Validate(at) == nil {
					out = append(out, r)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	if p.codexReadings == nil {
		p.codexReadings = map[string]Reading{}
	}
	for _, r := range out {
		p.codexReadings[r.WindowKind+"/"+r.Bucket] = r
	}
	out = out[:0]
	for k, r := range p.codexReadings {
		if r.ResetsAt.After(at) {
			out = append(out, r)
		} else {
			delete(p.codexReadings, k)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WindowKind+out[i].Bucket < out[j].WindowKind+out[j].Bucket })
	return out
}

// CodexSnapshot consumes a full account/rateLimits/read response. Notification
// patches use Codex instead and preserve fields the vendor did not update.
func (p *Parser) CodexSnapshot(raw []byte, at time.Time) []Reading {
	p.codex = nil
	p.codexReadings = nil
	return p.Codex(raw, at)
}
func windowKind(minutes int) string {
	switch minutes {
	case 300:
		return "5h"
	case 10080:
		return "weekly"
	default:
		if minutes >= 28*24*60 && minutes <= 31*24*60 {
			return "monthly"
		}
		return "other"
	}
}

func claudeWindowMinutes(kind string) int {
	switch kind {
	case "five_hour":
		return 300
	case "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included":
		return 10080
	default:
		return 0
	}
}

// ClaudeUnnamedStop is a rejected rate_limit_event that names no window.
// The caller records a vendor stop; stored windows stay as they were.
func ClaudeUnnamedStop(raw []byte) bool {
	if len(raw) > 1<<20 {
		return false
	}
	var in struct {
		Type string `json:"type"`
		Info struct {
			Status string `json:"status"`
			Kind   string `json:"rateLimitType"`
		} `json:"rate_limit_info"`
	}
	return json.Unmarshal(raw, &in) == nil && in.Type == "rate_limit_event" && in.Info.Status == "rejected" && claudeWindowMinutes(in.Info.Kind) == 0
}

func Claude(raw []byte, at time.Time) []Reading {
	if len(raw) > 1<<20 {
		return nil
	}
	type window struct {
		Utilization *float64 `json:"utilization"`
		Reset       *int64   `json:"resetsAt"`
	}
	var in struct {
		Type string `json:"type"`
		Info struct {
			Status      string            `json:"status"`
			Kind        string            `json:"rateLimitType"`
			Utilization *float64          `json:"utilization"`
			Reset       *int64            `json:"resetsAt"`
			Windows     map[string]window `json:"unifiedWindows"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &in) != nil || in.Type != "rate_limit_event" {
		return nil
	}
	// A rejection that names no bucket is a vendor stop, not a window update.
	if in.Info.Status == "rejected" && claudeWindowMinutes(in.Info.Kind) == 0 {
		return nil
	}
	ws := in.Info.Windows
	if ws == nil {
		ws = map[string]window{}
	}
	if in.Info.Kind != "" && in.Info.Utilization != nil && in.Info.Reset != nil {
		ws[in.Info.Kind] = window{in.Info.Utilization, in.Info.Reset}
	}
	keys := []string{}
	for k := range ws {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []Reading{}
	for _, k := range keys {
		w := ws[k]
		minutes := claudeWindowMinutes(k)
		if minutes == 0 {
			continue
		}
		if w.Utilization == nil || w.Reset == nil || math.IsNaN(*w.Utilization) || *w.Utilization < 0 {
			continue
		}
		var allowed *bool
		switch in.Info.Status {
		case "allowed", "allowed_warning":
			v := true
			allowed = &v
		case "rejected":
			// Only the named bucket is denied. A sibling utilization is an
			// observation and must not clear or spread that denial.
			if k == in.Info.Kind {
				v := false
				allowed = &v
			}
		default:
			continue
		}
		r := Reading{WindowKind: windowKind(minutes), Bucket: k, WindowMinutes: minutes, UsedPercent: math.Min(100, *w.Utilization*100), ResetsAt: time.Unix(*w.Reset, 0).UTC(), ReadAt: at, Source: "harness", OrdinaryUsageAllowed: allowed}
		if r.Validate(at) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Claude remembers normalized observations so a sparse rejection can fence the
// named window even when the event contains no utilization. A rejection that
// names no window leaves that memory unchanged. An allowance-only event never
// freshens old usage; a complete new reading establishes recovery.
func (p *Parser) Claude(raw []byte, at time.Time) []Reading {
	out := Claude(raw, at)
	if p.claude == nil {
		p.claude = map[string]Reading{}
	}
	kind := ""
	namedReject := false
	if len(raw) <= 1<<20 {
		var event struct {
			Type string `json:"type"`
			Info struct {
				Status string `json:"status"`
				Kind   string `json:"rateLimitType"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(raw, &event) == nil && event.Type == "rate_limit_event" && event.Info.Status == "rejected" && claudeWindowMinutes(event.Info.Kind) > 0 {
			kind = event.Info.Kind
			namedReject = true
		}
	}
	if len(out) == 0 && !namedReject {
		return nil
	}
	seen := map[string]bool{}
	for _, r := range out {
		if prev, ok := p.claude[r.Bucket]; ok && r.OrdinaryUsageAllowed == nil {
			r.OrdinaryUsageAllowed = prev.OrdinaryUsageAllowed
			r.ReadAt = prev.ReadAt
			if !r.ResetsAt.After(r.ReadAt) {
				r.ResetsAt = prev.ResetsAt
			}
		}
		p.claude[r.Bucket] = r
		seen[r.Bucket] = true
	}
	if namedReject {
		if r, ok := p.claude[kind]; ok && !seen[kind] && r.ResetsAt.After(at) {
			no := false
			r.OrdinaryUsageAllowed = &no
			r.ReadAt = at
			p.claude[kind] = r
		}
	}
	keys := make([]string, 0, len(p.claude))
	for k, r := range p.claude {
		if !r.ResetsAt.After(at) {
			delete(p.claude, k)
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	out = out[:0]
	for _, k := range keys {
		out = append(out, p.claude[k])
	}
	return out
}
