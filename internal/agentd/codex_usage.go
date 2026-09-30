// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/sessionusage"
)

var errCodexNoActiveTurn = errors.New("Codex steer rejected: no active turn")

const defaultCodexIdleTimeout = 10 * time.Minute

// notification is serialized with binding changes by wireProcess.eventMu.
func (p *codexProcess) notification(raw json.RawMessage) {
	if p.sealed || p.abandoned.Load() {
		return
	}
	method, _, model := eventProbe(raw)
	if method == "account/rateLimits/updated" || method == "rateLimits/updated" {
		p.emitCapacity(raw, "update")
		return
	}
	// Account quota notifications above belong to this authenticated connection.
	// Turn errors must additionally name its owned thread and acknowledged turn.
	var limitBinding struct {
		Params struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &limitBinding) == nil && p.threadID != "" && limitBinding.Params.ThreadID == p.threadID {
		turn := firstNonempty(limitBinding.Params.TurnID, limitBinding.Params.Turn.ID)
		if turn != "" {
			if hit := capacity.CodexLimit(raw, p.lastCapacity, time.Now().UTC(), p.capacityModel); hit != nil {
				if p.acknowledged && turn == p.turnID {
					p.emitVendorLimit(hit)
				} else if !p.acknowledged && p.pendingLimit == nil {
					p.pendingLimit, p.pendingLimitTurn = hit, turn
				}
			}
		}
	}
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
			if frame.Params.Settings.Model != "" {
				p.capacityModel = frame.Params.Settings.Model
			}
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
					p.capacityModel = report.Model
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
						Input     *int64 `json:"inputTokens"`
						Output    *int64 `json:"outputTokens"`
						Cached    *int64 `json:"cachedInputTokens"`
						Reasoning *int64 `json:"reasoningOutputTokens"`
					} `json:"total"`
				} `json:"tokenUsage"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &frame) == nil && frame.Params.ThreadID == p.threadID {
			total := frame.Params.TokenUsage.Total
			if total.Input != nil && total.Output != nil && *total.Input >= p.inputTokens && *total.Output >= p.outputTokens {
				ev := AdapterEvent{Kind: "usage", InputTokensDelta: cumulativeDelta(*total.Input, &p.inputTokens), OutputTokensDelta: cumulativeDelta(*total.Output, &p.outputTokens)}
				if total.Cached != nil && *total.Cached >= p.cachedTokens && *total.Cached <= *total.Input {
					ev.CachedInputTokensDelta = cumulativeDelta(*total.Cached, &p.cachedTokens)
				}
				if total.Reasoning != nil && *total.Reasoning >= p.reasoningTokens && *total.Reasoning <= *total.Output {
					ev.ReasoningTokensDelta = cumulativeDelta(*total.Reasoning, &p.reasoningTokens)
				}
				if ev.InputTokensDelta != 0 || ev.OutputTokensDelta != 0 || ev.CachedInputTokensDelta != 0 || ev.ReasoningTokensDelta != 0 {
					p.observe(ev)
				}
			}
		}
	}
	if method == "turn/completed" || method == "turn/failed" {
		terminal, err := sessionusage.ParseCodexTerminal(raw)
		if p.persistent && p.terminalSeen && err == nil && p.terminal != nil && terminal == *p.terminal {
			return // Repeated terminal evidence cannot end or wake a live session.
		}
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
	if p.persistent && !p.invalid && p.terminal != nil && p.terminal.Clean {
		if !p.idlePublished {
			p.idlePublished = true
			p.armIdleCompletion()
			p.observe(AdapterEvent{Activity: "idle", BudgetTurnsDelta: 1})
		}
		return
	}
	if p.terminalSeen || p.invalid {
		p.once.Do(func() { p.done <- true })
	}
}

// eventMu is held. Generation plus controlMu fences a timer that races a wake.
// A daemon context that is already cancelled completes the idle wait now.
func (p *codexProcess) armIdleCompletion() {
	if p.shutdownRequested {
		p.completeIdleLocked()
		return
	}
	delay := p.idleTimeout
	if delay <= 0 {
		delay = defaultCodexIdleTimeout
	}
	p.idleGeneration++
	generation := p.idleGeneration
	p.idleTimer = time.AfterFunc(delay, func() {
		p.controlMu.Lock()
		defer p.controlMu.Unlock()
		p.eventMu.Lock()
		defer p.eventMu.Unlock()
		if generation != p.idleGeneration || !p.idlePublished || p.invalid || p.sealed || p.abandoned.Load() || p.finishing {
			return
		}
		p.completeIdleLocked()
	})
}

// eventMu is held. Stops the wake timer and releases Wait exactly once.
func (p *codexProcess) completeIdleLocked() {
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	p.idleGeneration++
	p.finishing = true
	p.once.Do(func() { p.done <- true })
}

// bindLifetime attaches the daemon context. Wait observes its cancellation
// only while a clean idle wake window is open.
func (p *codexProcess) bindLifetime(ctx context.Context) {
	if ctx == nil || ctx.Done() == nil {
		return
	}
	p.shutdownWait.Store(&codexShutdown{done: ctx.Done()})
}

// finishIdleOnShutdown records daemon cancellation. It ends the wait only
// when the session is already in a clean idle window; a busy turn keeps running
// and the next idle arm completes immediately.
func (p *codexProcess) finishIdleOnShutdown() bool {
	p.controlMu.Lock()
	defer p.controlMu.Unlock()
	p.eventMu.Lock()
	defer p.eventMu.Unlock()
	p.shutdownRequested = true
	if !p.persistent || p.finishing || p.sealed || p.invalid || !p.idlePublished || p.abandoned.Load() {
		return false
	}
	p.completeIdleLocked()
	return true
}

// awaitCompletion blocks until the turn ends, the child exits, or a cancelled
// daemon context ends a clean idle wait. Cancellation before the turn is idle
// does not fail that turn.
func (p *codexProcess) awaitCompletion() {
	sawShutdown := false
	for {
		var shutdown <-chan struct{}
		if !sawShutdown {
			if signal := p.shutdownWait.Load(); signal != nil {
				shutdown = signal.done
			}
		}
		select {
		case <-p.done:
			return
		case <-p.waitDone:
			return
		case <-p.readDone:
			return
		case <-shutdown:
			sawShutdown = true
			if p.finishIdleOnShutdown() {
				return
			}
		}
	}
}

func (p *codexProcess) startTurn(ctx context.Context, r StartRequest) error {
	// Each request stays on the fresh thread owned by this connection.
	p.eventMu.Lock()
	thread := p.threadID
	// turn/start explicitly selects this model, including after an idle wake.
	// A previous turn's model switch must not change the new turn's quota scope.
	p.capacityModel = r.Profile.Model
	p.eventMu.Unlock()
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
	if p.pendingLimit != nil && p.pendingLimitTurn == turn {
		p.emitVendorLimit(p.pendingLimit)
	}
	p.pendingLimit, p.pendingLimitTurn = nil, ""
	if p.usage != nil {
		// Usage with an explicit pre-ack turnId must agree with this result.
		if err := p.usage.BindTurn(turn); err != nil {
			p.invalid = true
		}
	}
	p.signalTerminal()
	if !p.terminalSeen {
		p.observe(AdapterEvent{Activity: "busy"})
	}
	return nil
}

// waitForTurn defines the irreversible finality boundary. Persistent workers
// keep clean turns idle; failures and child/stream exit still end their lifetime.
// At that boundary, stop the owned child and join BOTH its exit
// and the reader's actual EOF within one bounded interval. The reader remains
// active through stop, including buffered frames; no quiet-time heuristic or
// detached goroutine may upgrade a receipt later. Stop/drain errors fail closed.
func (p *codexProcess) waitForTurn(stop func(context.Context) error, timeout time.Duration) error {
	if p.abandoned.Load() {
		return errors.New("Codex turn completion unconfirmed")
	}
	p.awaitCompletion()
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
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	clean = clean && !p.abandoned.Load()
	p.invalid = p.invalid || !clean
	if p.usage != nil {
		for _, report := range p.usage.Finish(clean) {
			p.observe(AdapterEvent{SessionUsage: &report})
		}
	}
}

// wakeTurn advances only a live inbox-enabled process, never a finished run.
// eventMu fences buffered terminal/usage notifications against the new binding.
func (p *codexProcess) wakeTurn(ctx context.Context, text string) error {
	ctx, cancel := operationContext(ctx)
	defer cancel()
	p.eventMu.Lock()
	if !p.persistent || p.finishing || p.sealed || p.invalid || !p.idlePublished || p.abandoned.Load() {
		p.eventMu.Unlock()
		return ErrNotOwned
	}
	if p.idleTimer != nil {
		p.idleTimer.Stop()
	}
	p.idleGeneration++
	if p.usage != nil {
		// Preserve thread-wide cumulative totals; never reset them per turn.
		_ = p.usage.NextTurn()
	}
	p.terminal, p.terminalSeen, p.acknowledged, p.idlePublished = nil, false, false, false
	p.eventMu.Unlock()
	if err := p.startTurn(ctx, StartRequest{Prompt: text, Profile: p.profile}); err != nil {
		p.eventMu.Lock()
		p.once.Do(func() { p.done <- true })
		p.eventMu.Unlock()
		return err
	}
	p.observe(AdapterEvent{Kind: "turn", TurnCountDelta: 1})
	return nil
}
