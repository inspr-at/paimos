// SPDX-License-Identifier: AGPL-3.0-only

package nativequota

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

type patch struct {
	scope    string
	reading  *capacity.Reading
	allowed  *bool
	reset    *time.Time
	complete bool
}

type claudeWindow struct {
	Utilization *float64 `json:"utilization"`
	Reset       *int64   `json:"resetsAt"`
}

func decodeClaude(raw []byte, now time.Time, session string) ([]patch, error) {
	var frame struct {
		Type      string `json:"type"`
		SessionID string `json:"session_id"`
		Info      struct {
			Status string `json:"status"`
			Kind   string `json:"rateLimitType"`
			claudeWindow
			Windows map[string]claudeWindow `json:"unifiedWindows"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Type != "rate_limit_event" {
		return nil, ErrProtocol
	}
	if frame.SessionID != "" && frame.SessionID != session {
		return nil, ErrRuntime
	}
	if frame.Info.Status != "allowed" && frame.Info.Status != "allowed_warning" && frame.Info.Status != "rejected" {
		return nil, ErrProtocol
	}
	if len(frame.Info.Windows) > 8 {
		return nil, ErrBounds
	}
	windows := frame.Info.Windows
	if windows == nil {
		windows = map[string]claudeWindow{}
	}
	if frame.Info.Kind != "" {
		if claudeMinutes(frame.Info.Kind) == 0 {
			return nil, ErrScope
		}
		if frame.Info.Utilization != nil || frame.Info.Reset != nil {
			if old, duplicate := windows[frame.Info.Kind]; duplicate && !reflect.DeepEqual(old, frame.Info.claudeWindow) {
				return nil, ErrProtocol
			}
			windows[frame.Info.Kind] = frame.Info.claudeWindow
		} else if frame.Info.Status == "rejected" {
			if _, present := windows[frame.Info.Kind]; !present {
				windows[frame.Info.Kind] = claudeWindow{}
			}
		}
	}
	keys := sortedKeys(windows)
	out := make([]patch, 0, len(keys))
	complete := false
	for _, bucket := range keys {
		minutes := claudeMinutes(bucket)
		if minutes == 0 {
			return nil, ErrScope
		}
		w := windows[bucket]
		var used *float64
		if w.Utilization != nil {
			if math.IsNaN(*w.Utilization) || math.IsInf(*w.Utilization, 0) || *w.Utilization < 0 || *w.Utilization > 1 {
				return nil, ErrProtocol
			}
			n := *w.Utilization * 100
			used = &n
		}
		p, err := makePatch(bucket, minutes, used, w.Reset, now)
		if err != nil {
			return nil, err
		}
		if frame.Info.Status == "rejected" {
			if bucket == frame.Info.Kind {
				no := false
				p.allowed = &no
			}
		} else if p.complete {
			yes := true
			p.allowed = &yes
			complete = true
		}
		out = append(out, p)
	}
	if frame.Info.Status == "rejected" && frame.Info.Kind == "" {
		// No named window means an account stop, never 100% in every bucket.
		no := false
		out = append(out, patch{scope: "account", allowed: &no})
	} else if complete {
		yes := true
		out = append(out, patch{scope: "account", allowed: &yes, complete: true})
	}
	if len(out) == 0 {
		return nil, ErrNoReadings
	}
	return out, nil
}

func claudeMinutes(kind string) int {
	switch kind {
	case "five_hour":
		return 300
	case "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_overage_included":
		return 10080
	default:
		return 0
	}
}

type codexWindow struct {
	Used    *float64 `json:"usedPercent"`
	Minutes *int     `json:"windowDurationMins"`
	Reset   *int64   `json:"resetsAt"`
}
type codexSnapshot struct {
	LimitID   string       `json:"limitId"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

func decodeCodex(raw []byte, now time.Time, thread string, buckets []string) ([]patch, error) {
	var frame struct {
		Method string `json:"method"`
		Params struct {
			ThreadID   string                   `json:"threadId"`
			RateLimits *codexSnapshot           `json:"rateLimits"`
			ByID       map[string]codexSnapshot `json:"rateLimitsByLimitId"`
			Allowed    *bool                    `json:"ordinaryUsageAllowed"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Method != "account/rateLimits/updated" && frame.Method != "rateLimits/updated" {
		return nil, ErrProtocol
	}
	if frame.Params.ThreadID != "" && frame.Params.ThreadID != thread {
		return nil, ErrRuntime
	}
	if len(frame.Params.ByID) > 16 {
		return nil, ErrBounds
	}
	snapshots := frame.Params.ByID
	if snapshots == nil {
		snapshots = map[string]codexSnapshot{}
	}
	if frame.Params.RateLimits != nil {
		bucket := frame.Params.RateLimits.LimitID
		if bucket == "" {
			bucket = "codex"
		}
		if old, duplicate := snapshots[bucket]; duplicate {
			old.LimitID = bucket
			next := *frame.Params.RateLimits
			next.LimitID = bucket
			if !reflect.DeepEqual(old, next) {
				return nil, ErrProtocol
			}
		}
		snapshots[bucket] = *frame.Params.RateLimits
	}
	out := make([]patch, 0, MaxWindows)
	seen := map[string]bool{}
	complete := false
	for _, bucket := range sortedKeys(snapshots) {
		if !codexBucketAllowed(bucket, buckets) {
			return nil, ErrScope
		}
		s := snapshots[bucket]
		if s.LimitID != "" && s.LimitID != bucket {
			return nil, ErrProtocol
		}
		for _, w := range []*codexWindow{s.Primary, s.Secondary} {
			if w == nil {
				continue
			}
			// A duration-free partial notification supplies no usable scope.
			// Do not rebuild a "fresh" reading from remembered numeric fields.
			if w.Minutes == nil {
				continue
			}
			p, err := makePatch(bucket, *w.Minutes, w.Used, w.Reset, now)
			if err != nil {
				return nil, err
			}
			if seen[p.scope] {
				return nil, ErrProtocol
			}
			seen[p.scope] = true
			if p.complete {
				complete = true
				p.allowed = frame.Params.Allowed
			}
			out = append(out, p)
		}
	}
	if frame.Params.Allowed != nil {
		out = append(out, patch{scope: "account", allowed: frame.Params.Allowed, complete: complete})
	}
	if len(out) == 0 {
		return nil, ErrNoReadings
	}
	return out, nil
}

func codexBucketAllowed(bucket string, allowed []string) bool {
	if bucket == "codex" {
		return true
	}
	for _, b := range allowed {
		if bucket == b {
			return true
		}
	}
	return false
}

func makePatch(bucket string, minutes int, used *float64, reset *int64, now time.Time) (patch, error) {
	kind := "other"
	switch minutes {
	case 300:
		kind = "5h"
	case 10080:
		kind = "weekly"
	default:
		if minutes >= 28*24*60 && minutes <= 31*24*60 {
			kind = "monthly"
		}
		// The existing ledger identifies windows by kind/bucket. Include the
		// duration for other/monthly windows to avoid conflating two scopes.
		bucket = fmt.Sprintf("%s.w%d", bucket, minutes)
	}
	p := patch{scope: kind + "/" + bucket}
	if minutes < 1 || minutes > 527040 || !regexpScope.MatchString(bucket) || len(bucket) > 64 {
		return patch{}, ErrProtocol
	}
	if used != nil && (math.IsNaN(*used) || math.IsInf(*used, 0) || *used < 0 || *used > 100) {
		return patch{}, ErrProtocol
	}
	if reset != nil {
		at := time.Unix(*reset, 0).UTC()
		if at.Year() < 2000 || at.Year() > 2099 || !at.After(now) || at.After(now.Add(time.Duration(minutes)*time.Minute)) {
			return patch{}, ErrProtocol
		}
		p.reset = &at
	}
	if used != nil && p.reset != nil {
		r := capacity.Reading{WindowKind: kind, Bucket: bucket, WindowMinutes: minutes, UsedPercent: *used,
			ResetsAt: *p.reset, ReadAt: now, Source: "agentd"}
		if r.Validate(now) != nil {
			return patch{}, ErrProtocol
		}
		p.reading, p.complete = &r, true
	}
	return p, nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func scopeValid(scope string) bool {
	if scope == "account" {
		return true
	}
	parts := strings.Split(scope, "/")
	if len(parts) != 2 || len(parts[1]) > 64 || !regexpScope.MatchString(parts[1]) {
		return false
	}
	return parts[0] == "5h" || parts[0] == "weekly" || parts[0] == "monthly" || parts[0] == "other"
}
