// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"encoding/json"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

// notification is serialized with binding changes by wireProcess.eventMu.
func (p *codexProcess) notification(raw json.RawMessage) {
	method, _, model := eventProbe(raw)
	if method == "item/started" {
		p.observe(AdapterEvent{Kind: "tool"})
	}
	if method == "thread/tokenUsage/updated" || method == "model/rerouted" {
		// The normalized session path shares UP1's strict schemas. Run accounting
		// keeps its existing independent thread cumulative delta settlement.
		if p.usage != nil {
			if report, err := p.usage.Observe(raw); err == nil && report != nil {
				ev := AdapterEvent{SessionUsage: report}
				// Only an exact, parser-validated usage model is vendor evidence.
				// Arbitrary model-like text on other notifications is not metadata.
				if model != "" && model == report.Model {
					ev.Kind, ev.EffectiveModel, ev.ModelEvidence = "status", report.Model, "vendor_reported"
				}
				p.observe(ev)
			}
		}
		if method == "model/rerouted" {
			return
		}
		var frame struct {
			Params struct {
				ThreadID   string `json:"threadId"`
				TokenUsage struct {
					Total struct {
						Input  *int64 `json:"inputTokens"`
						Output *int64 `json:"outputTokens"`
					} `json:"total"`
				} `json:"tokenUsage"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &frame) == nil && frame.Params.ThreadID == p.threadID {
			total := frame.Params.TokenUsage.Total
			if total.Input != nil && total.Output != nil && *total.Input >= p.inputTokens && *total.Output >= p.outputTokens {
				ev := AdapterEvent{Kind: "usage", InputTokensDelta: cumulativeDelta(*total.Input, &p.inputTokens), OutputTokensDelta: cumulativeDelta(*total.Output, &p.outputTokens)}
				if ev.InputTokensDelta != 0 || ev.OutputTokensDelta != 0 {
					p.observe(ev)
				}
			}
		}
	}
	if method == "turn/completed" || method == "turn/failed" {
		var frame struct {
			Params struct {
				ThreadID string `json:"threadId"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &frame) != nil || p.threadID == "" || frame.Params.ThreadID != p.threadID {
			return
		}
		p.once.Do(func() {
			_, clean, err := sessionusage.CodexTerminalStatus(raw)
			clean = clean && err == nil
			if p.usage != nil {
				for _, report := range p.usage.Finish(clean) {
					p.observe(AdapterEvent{SessionUsage: &report})
				}
			}
			p.done <- !clean
		})
	}
}
