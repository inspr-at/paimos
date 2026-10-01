// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"sync"

	"github.com/inspr-at/paimos/internal/agentverification"
	"github.com/inspr-at/paimos/internal/grokprobe"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

const (
	grokModel         = "grok-4.7"
	grokEffort        = "xhigh"
	grokConfigSHA256  = "3b9f1cefb4672eed5856debd6173bb82009546ad9b10f28d8372749d1bb8e259"
	grokProfileSHA256 = "9cd1990054adc092e004e649da746e4bb2844ae6debae4a8aa5495df5ecfb231"
)

//go:embed grokassets/config.toml grokassets/conversation.txt
var grokAssets embed.FS

// GrokBinding is operator-local. AEON receives only its opaque account key.
// The native adapter executes an isolated conversation without workspace I/O.
type GrokBinding = grokprobe.Binding

type GrokAdapter struct {
	quotaIDs sync.Map
	Bindings map[string]GrokBinding
	Homes    map[string]string
	billing  *grokBillingCapability
}

func NewGrokAdapter(bindings ...map[string]GrokBinding) *GrokAdapter {
	a := &GrokAdapter{Bindings: map[string]GrokBinding{}}
	if len(bindings) > 0 {
		a.Bindings = bindings[0]
	}
	return a
}
func (*GrokAdapter) Name() string { return Grok }

// VerificationSupported matches the server's native Grok qualification. The
// account-specific binary, subject and confinement checks remain in startNative.
func (*GrokAdapter) VerificationSupported() bool {
	return grokVerificationSupported(runtime.GOOS, runtime.GOARCH)
}

func grokVerificationSupported(goos, goarch string) bool {
	return agentverification.For(Grok, goos, goarch).Supported
}

func (a *GrokAdapter) Start(ctx context.Context, r StartRequest, observe func(AdapterEvent)) (Process, error) {
	if err := validExecutionMode(r.Run, a); err != nil {
		return nil, err
	}
	if r.Profile.Harness != Grok || r.Profile.Model != grokModel || r.Profile.Effort != grokEffort || r.AccountKey == "" {
		return nil, errors.New("native Grok profile or account unavailable")
	}
	b, ok := a.Bindings[r.AccountKey]
	if !ok {
		return nil, errors.New("native Grok account unavailable")
	}
	return a.startNative(ctx, r, b, observe)
}

func sha256Hex(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

type grokUsageTracker struct {
	models  map[string]sessionusage.UsageReport
	created map[string]int64
	cost    int64
}

// ACP cost and token totals are separate observations. used/size is context
// occupancy, never token throughput. No usage counters are inferred from it.
func (s *grokUsageTracker) event(raw json.RawMessage, model string) (AdapterEvent, bool) {
	var params struct {
		Update struct {
			Kind string `json:"sessionUpdate"`
			Cost *struct {
				Amount   json.RawMessage `json:"amount"`
				Currency string          `json:"currency"`
			} `json:"cost"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Update.Kind != "usage_update" {
		return AdapterEvent{}, false
	}
	ev := AdapterEvent{Kind: "usage"}
	if cost := params.Update.Cost; cost != nil && cost.Currency == "USD" {
		if current, ok := usdMicros(cost.Amount); ok {
			ev.CostMicrosDelta = cumulativeDelta(current, &s.cost)
		}
	}
	if report, created, ok := s.grokNativeUsage(raw, model); ok {
		prev, exists := s.models[report.Model]
		if (!exists && len(s.models) < 128) || exists && created >= s.created[report.Model] && monotonicGrokUsage(prev, report) {
			if s.models == nil {
				s.models = map[string]sessionusage.UsageReport{}
				s.created = map[string]int64{}
			}
			// Missing optional counters retain the last known observation.
			if report.CachedInputTokens == nil {
				report.CachedInputTokens = cloneCount(prev.CachedInputTokens)
			}
			if report.ReasoningTokens == nil {
				report.ReasoningTokens = cloneCount(prev.ReasoningTokens)
			}
			ev.InputTokensDelta = usageCount(report.InputTokens) - usageCount(prev.InputTokens)
			ev.OutputTokensDelta = usageCount(report.OutputTokens) - usageCount(prev.OutputTokens)
			ev.CachedInputTokensDelta = usageCount(report.CachedInputTokens) - usageCount(prev.CachedInputTokens)
			ev.ReasoningTokensDelta = usageCount(report.ReasoningTokens) - usageCount(prev.ReasoningTokens)
			s.models[report.Model] = report
			s.created[report.Model] = created
			ev.SessionUsage = &report
		}
	}
	return ev, ev.SessionUsage != nil || ev.CostMicrosDelta > 0
}

func usageCount(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func monotonicGrokUsage(prev, next sessionusage.UsageReport) bool {
	for _, pair := range [][2]*int64{{prev.InputTokens, next.InputTokens}, {prev.OutputTokens, next.OutputTokens}, {prev.CachedInputTokens, next.CachedInputTokens}, {prev.ReasoningTokens, next.ReasoningTokens}} {
		if pair[0] != nil && pair[1] != nil && *pair[1] < *pair[0] {
			return false
		}
	}
	return next.CachedInputTokens == nil || prev.CachedInputTokens == nil || usageCount(next.InputTokens)-*next.CachedInputTokens >= usageCount(prev.InputTokens)-*prev.CachedInputTokens
}

func (s *grokUsageTracker) grokNativeUsage(raw json.RawMessage, model string) (sessionusage.UsageReport, int64, bool) {
	var params struct {
		Update struct {
			Input     *int64 `json:"inputTokens"`
			Output    *int64 `json:"outputTokens"`
			Cached    *int64 `json:"cachedReadTokens"`
			Created   *int64 `json:"cacheCreationTokens"`
			Reasoning *int64 `json:"reasoningTokens"`
			Model     string `json:"model"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil || params.Update.Input == nil || params.Update.Output == nil {
		return sessionusage.UsageReport{}, 0, false
	}
	u := params.Update
	name := firstNonempty(u.Model, model)
	if u.Cached == nil {
		u.Cached = s.models[name].CachedInputTokens
	}
	cached := usageCount(u.Cached)
	created := s.created[name]
	if u.Created != nil {
		created = *u.Created
	}
	// Grok inputTokens excludes cache reads and creation, as in usage.json.
	// Retain missing optional cumulative counters before normalizing so a
	// later update cannot silently remove them from inclusive input.
	for _, n := range []int64{*u.Input, *u.Output, cached, created} {
		if n < 0 || n > managedUsageMax {
			return sessionusage.UsageReport{}, 0, false
		}
	}
	if *u.Input > managedUsageMax-cached || *u.Input+cached > managedUsageMax-created {
		return sessionusage.UsageReport{}, 0, false
	}
	report, ok := sessionusage.CountReport(name, *u.Input+cached+created, *u.Output, cached, u.Cached != nil)
	if !ok {
		return sessionusage.UsageReport{}, 0, false
	}
	if u.Reasoning != nil {
		if *u.Reasoning < 0 || *u.Reasoning > *u.Output {
			return sessionusage.UsageReport{}, 0, false
		}
		report.ReasoningTokens = cloneCount(u.Reasoning)
	}
	return report, created, true
}
