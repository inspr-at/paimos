// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/sessionusage"
)

// notification is serialized with binding changes by wireProcess.eventMu.
func (p *codexProcess) notification(raw json.RawMessage) {
	if p.sealed || p.abandoned.Load() {
		return
	}
	method, _, model := eventProbe(raw)
	if method == "thread/settings/updated" {
		var frame struct {
			Params struct {
				ThreadID string `json:"threadId"`
				Settings struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				} `json:"threadSettings"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &frame) == nil && p.threadID != "" && frame.Params.ThreadID == p.threadID {
			p.observe(AdapterEvent{HarnessModel: frame.Params.Settings.Model, HarnessEffort: frame.Params.Settings.Effort})
		}
	}
	if method == "item/started" {
		p.observe(AdapterEvent{Kind: "tool"})
	}
	if method == "thread/tokenUsage/updated" || method == "model/rerouted" {
		// Any usage/reroute after a terminal contradicts this single-turn
		// capture, including an identical replay. Continue existing run accounting
		// and normalized provisional snapshots, but never upgrade them to final.
		if p.terminalSeen {
			p.invalid = true
			p.signalTerminal()
		}
		// The normalized session path shares UP1's strict schemas. Run accounting
		// keeps its existing independent thread cumulative delta settlement.
		if p.usage != nil {
			if report, err := p.usage.Observe(raw); err == nil && report != nil {
				ev := AdapterEvent{SessionUsage: report}
				// Only an exact, parser-validated usage model is vendor evidence.
				// Arbitrary model-like text on other notifications is not metadata.
				if model != "" && model == report.Model {
					ev.Kind, ev.EffectiveModel, ev.ModelEvidence = "usage", report.Model, "vendor_reported"
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
		terminal, err := sessionusage.ParseCodexTerminal(raw)
		if p.terminalSeen || err != nil || terminal.ThreadID != p.threadID || p.threadID == "" {
			p.invalid = true
		} else {
			// One bounded normalized candidate, even before turn/start replies.
			p.terminal = &terminal
		}
		p.terminalSeen = true
		p.signalTerminal()
	}
}

// Called under eventMu. A notification alone never acknowledges turn/start.
func (p *codexProcess) signalTerminal() {
	if !p.acknowledged {
		return
	}
	if p.terminal != nil && p.terminal.TurnID != p.turnID {
		p.invalid = true
	}
	if p.terminalSeen || p.invalid {
		p.once.Do(func() { p.done <- true })
	}
}

func (p *codexProcess) startTurn(ctx context.Context, r StartRequest) error {
	// This connection owns exactly one fresh thread and one turn/start RPC.
	thread := p.threadID
	raw, err := p.request(ctx, "jsonrpc", "turn/start", map[string]any{"threadId": thread, "input": []map[string]string{{"type": "text", "text": r.Prompt}}, "model": r.Profile.Model, "effort": r.Profile.Effort})
	turn, parseErr := sessionusage.CodexStartedTurn(raw)
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	if err != nil || parseErr != nil || thread == "" || p.threadID != thread {
		p.invalid = true
		p.sealUsage(false)
		return errors.New("Codex turn start failed")
	}
	p.turnID, p.acknowledged = turn, true
	if p.usage != nil {
		// Usage with an explicit pre-ack turnId must agree with this result.
		if err := p.usage.BindTurn(turn); err != nil {
			p.invalid = true
		}
	}
	p.signalTerminal()
	return nil
}

// waitForTurn defines the irreversible finality boundary. After an acknowledged
// candidate (or child/stream exit), stop the owned child and join BOTH its exit
// and the reader's actual EOF within one bounded interval. The reader remains
// active through stop, including buffered frames; no quiet-time heuristic or
// detached goroutine may upgrade a receipt later. Stop/drain errors fail closed.
func (p *codexProcess) waitForTurn(stop func(context.Context) error, timeout time.Duration) error {
	if p.abandoned.Load() {
		return errors.New("Codex turn completion unconfirmed")
	}
	select {
	case <-p.done:
	case <-p.waitDone:
	case <-p.readDone:
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stopErr := stop(ctx)
	cleanDrain := true
	for _, done := range []chan struct{}{p.waitDone, p.readDone} {
		select {
		case <-done:
		case <-ctx.Done():
			cleanDrain = false
		}
	}
	if cleanDrain {
		cleanDrain = p.readErr == nil
	} else {
		// Publish failure without eventMu: an admitted observer may still hold
		// it past both deadlines. Neither its eventual return nor another Wait
		// may turn this lost completion boundary into a final receipt.
		p.abandoned.Store(true)
		// A descendant holding stdout open cannot keep a detached reader alive.
		// Closing locally is loss of evidence, never equivalent to source EOF.
		p.closeReader()
		if err := p.finishReader(); err != nil {
			return errors.New("Codex turn completion unconfirmed")
		}
	}
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	clean := !p.abandoned.Load() && stopErr == nil && ctx.Err() == nil && cleanDrain && p.acknowledged && !p.invalid && p.terminal != nil && p.terminal.Clean && p.terminal.ThreadID == p.threadID && p.terminal.TurnID == p.turnID
	p.sealUsage(clean)
	if !clean {
		return errors.New("Codex turn completion unconfirmed")
	}
	return nil
}

// Called under eventMu only after the boundary, or when turn/start failed.
func (p *codexProcess) sealUsage(clean bool) {
	if p.sealed {
		return
	}
	p.sealed = true
	clean = clean && !p.abandoned.Load()
	p.invalid = p.invalid || !clean
	if p.usage != nil {
		for _, report := range p.usage.Finish(clean) {
			p.observe(AdapterEvent{SessionUsage: &report})
		}
	}
}
