// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"time"

	"github.com/inspr-at/paimos/internal/rules"
)

const managedControlCapability = "managed_control_v1"
const defaultTokenBudget int64 = 100000
const defaultTurnBudget int64 = 16

var ErrBudgetExhausted = errors.New("managed run budget exhausted")
var ErrControlExpired = errors.New("control authorization expired")

// ManagedControlAdapter is deliberately opt-in. Codex's app-server cannot bind
// an immutable tool ceiling (see verification.go), and Cursor inherits MCP
// configuration. Neither qualifies merely because it has a cancellation API.
// Claude's restricted SDK inventory uses the bounded macOS terminal tool.
type ManagedControlAdapter interface{ ManagedControlSupported() bool }

func (*ClaudeAdapter) ManagedControlSupported() bool { return runtime.GOOS == "darwin" }

type managedContextAPI interface {
	ManagedContext(context.Context, HarnessSession) (rules.Merged, error)
	RecordManagedRules(context.Context, HarnessSession, rules.Merged) error
}

func (r *Remote) ManagedContext(ctx context.Context, s HarnessSession) (rules.Merged, error) {
	var out rules.Merged
	err := r.harnessWorker(ctx, s, "/managed-context", struct{}{}, &out)
	return out, err
}
func (r *Remote) RecordManagedRules(ctx context.Context, s HarnessSession, m rules.Merged) error {
	id, err := randomID()
	if err != nil {
		return err
	}
	id = id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
	return r.harnessWorker(ctx, s, "/rules-receipts", map[string]any{"request_id": id, "expected_revision": 0, "context": m.Context, "body_sha256": m.SHA256, "version": m.Version, "byte_size": m.ByteSize, "source": "online"}, nil)
}
func (s *Supervisor) managedRules(ctx context.Context, entry *owned, run Run, profile Profile) (string, error) {
	api, ok := s.api.(managedContextAPI)
	if !ok {
		return "", errors.New("managed rules API unavailable")
	}
	m, err := api.ManagedContext(ctx, entry.harness)
	if err != nil {
		return "", err
	}
	c := m.Context
	h := profile.Harness
	if h == Claude {
		h = "claude-code"
	}
	if c.TenantID != s.tenantID || c.ProjectID != entry.harness.ProjectID || c.AgentID != s.principalID || c.TaskID != run.WorkOrderID || c.Role != "builder" || c.Harness != h {
		return "", errors.New("managed rules scope mismatch")
	}
	if err = rules.ValidateMerged(m, c, time.Now()); err != nil {
		return "", err
	}
	if err = api.RecordManagedRules(ctx, entry.harness, m); err != nil {
		return "", err
	}
	return m.Body, nil
}

// Local counters update before telemetry I/O, including before Start returns.
// Reporting boundaries may overshoot the token limit; they are not a provider
// billing cap. A single graceful stop is scheduled; failure escalates through
// Process.Stop, whose owned lifetime fences every process-group signal.
func (s *Supervisor) observeBudget(entry *owned, ev AdapterEvent) {
	entry.budgetMu.Lock()
	if entry.tokenBudget <= 0 || entry.turnBudget <= 0 {
		entry.budgetMu.Unlock()
		return
	}
	add := func(current *int64, delta, limit int64) {
		if delta > 0 {
			if delta >= limit-*current {
				*current = limit
			} else {
				*current += delta
			}
		}
	}
	add(&entry.tokensUsed, ev.InputTokensDelta, entry.tokenBudget)
	add(&entry.tokensUsed, ev.OutputTokensDelta, entry.tokenBudget)
	add(&entry.turnsUsed, ev.BudgetTurnsDelta, entry.turnBudget)
	if entry.budgetReason == "" {
		if ev.BudgetExhausted == "turn_budget_exhausted" || ev.BudgetExhausted == "token_budget_exhausted" {
			entry.budgetReason = ev.BudgetExhausted
		}
		if entry.tokensUsed >= entry.tokenBudget {
			entry.budgetReason = "token_budget_exhausted"
		} else if entry.turnsUsed >= entry.turnBudget {
			entry.budgetReason = "turn_budget_exhausted"
		}
	}
	ready := entry.budgetReason != "" && entry.budgetProcess != nil && !entry.budgetStopStarted
	if ready {
		entry.budgetStopStarted = true
	}
	entry.budgetMu.Unlock()
	if ready {
		go s.stopForBudget(entry)
	}
}
func (s *Supervisor) stopForBudget(entry *owned) {
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	defer func() {
		entry.mu.Lock()
		defer entry.mu.Unlock()
		entry.budgetMu.Lock()
		entry.record.BudgetStopReason = entry.budgetReason
		entry.record.BudgetStopUnconfirmed = entry.budgetStopUnconfirmed && !entry.record.ExitObserved
		entry.budgetMu.Unlock()
		if err := s.journal.Put(entry.record); err != nil {
			slog.Warn("managed budget receipt not persisted")
		}
	}()
	entry.budgetMu.Lock()
	proc := entry.budgetProcess
	tools := entry.budgetTools
	entry.budgetMu.Unlock()
	_ = tools.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if graceful, ok := proc.(GracefulProcess); ok {
		if err := graceful.GracefulStop(ctx); err == nil {
			return
		}
	}
	// Exhaustion is a local lifetime limit, independent of human controls.
	// Stop uses the same verified, unreaped group fence as the run deadline.
	// A successful signal is not an exit receipt: only monitor may confirm it.
	entry.budgetMu.Lock()
	entry.budgetStopUnconfirmed = true
	entry.budgetMu.Unlock()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	_ = proc.Stop(stopCtx)
}

// Claude's stop is Query.close through the bridge, followed by observed child
// exit. It never silently turns a rejected native command into an OS signal.
func (p *claudeProcess) GracefulStop(ctx context.Context) error {
	if err := p.Control(ctx, "stop", ""); err != nil {
		return err
	}
	select {
	case <-p.waitDone:
		return nil
	case <-ctx.Done():
		return ErrGracefulTimeout
	}
}

func (entry *owned) budgetStopReason() string {
	entry.budgetMu.Lock()
	defer entry.budgetMu.Unlock()
	return entry.budgetReason
}
