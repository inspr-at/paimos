// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

const managedUsageMax = int64(1_000_000_000_000)

type piUsageTotal struct{ input, output, cached int64 }

type piUsageTracker struct {
	totals map[string]piUsageTotal
	seen   map[string]bool
	cost   int64
}

// message_end is authoritative for one assistant message. Streaming updates,
// turn_end and agent_end can repeat its usage and must never be added again.
// Pi's input excludes both cache categories; inclusive input includes both.
func (s *piUsageTracker) event(raw json.RawMessage) (AdapterEvent, bool) {
	var frame struct {
		Type    string `json:"type"`
		Message struct {
			Role      string `json:"role"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			Timestamp int64  `json:"timestamp"`
			Usage     struct {
				Input  *int64 `json:"input"`
				Output *int64 `json:"output"`
				Read   *int64 `json:"cacheRead"`
				Write  *int64 `json:"cacheWrite"`
				Cost   struct {
					Total json.RawMessage `json:"total"`
				} `json:"cost"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Type != "message_end" || frame.Message.Role != "assistant" || frame.Message.Timestamp <= 0 || frame.Message.Provider == "" || frame.Message.Model == "" {
		return AdapterEvent{}, false
	}
	u := frame.Message.Usage
	for _, n := range []*int64{u.Input, u.Output, u.Read, u.Write} {
		if n == nil || *n < 0 || *n > managedUsageMax {
			return AdapterEvent{}, false
		}
	}
	if *u.Input > managedUsageMax-*u.Read || *u.Input+*u.Read > managedUsageMax-*u.Write {
		return AdapterEvent{}, false
	}
	model := frame.Message.Provider + "/" + frame.Message.Model
	key := fmt.Sprintf("%s/%d", model, frame.Message.Timestamp)
	if s.seen[key] || len(s.seen) >= 4096 {
		return AdapterEvent{}, false
	}
	prev, exists := s.totals[model]
	if !exists && len(s.totals) >= 128 {
		return AdapterEvent{}, false
	}
	input := *u.Input + *u.Read + *u.Write
	if prev.input > managedUsageMax-input || prev.output > managedUsageMax-*u.Output || prev.cached > managedUsageMax-*u.Read {
		return AdapterEvent{}, false
	}
	next := piUsageTotal{prev.input + input, prev.output + *u.Output, prev.cached + *u.Read}
	report, ok := sessionusage.CountReport(model, next.input, next.output, next.cached, true)
	if !ok {
		return AdapterEvent{}, false
	}
	if s.totals == nil {
		s.totals = map[string]piUsageTotal{}
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	s.totals[model], s.seen[key] = next, true
	ev := AdapterEvent{Kind: "usage", SessionUsage: &report, InputTokensDelta: input, OutputTokensDelta: *u.Output, CachedInputTokensDelta: *u.Read}
	if cost, ok := usdMicros(u.Cost.Total); ok && s.cost <= math.MaxInt64-cost {
		ev.CostMicrosDelta = cost
		s.cost += cost
	}
	return ev, true
}
