// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/openrouter"
)

// CapacityCapture contains only normalized facts and bounded causes. Cleanup
// evidence stays local; unconfirmed termination retains the local capture fence.
type CapacityCapture struct {
	Result             string
	Readings           []capacity.Reading
	Credits            *openrouter.Credits
	CleanupUnconfirmed bool
}

// No production entry is qualified. An exact binary, its pinned interpreter,
// startup hooks, inherited config/tools and group termination must all have
// release-owned evidence before an idle launch is enabled. A successful fake
// exchange establishes protocol tests only (agentverification/capability.go).
type codexIdleCapability struct {
	binaryPath, binarySHA256, nodePath, nodeSHA256, version string
	startupHooks, inheritedConfig, termination              bool
}

func (a *CodexAdapter) CanCaptureCapacity(key string) bool {
	c := a.idleUsage
	node := a.Nodes[key].Path
	if c == nil || c.version == "" || !c.startupHooks || !c.inheritedConfig || !c.termination ||
		c.binaryPath != a.Path || c.nodePath != node || strings.TrimSpace(a.Emails[key]) == "" ||
		!exactCapacityBinary(a.Path, c.binarySHA256) || !exactCapacityBinary(node, c.nodeSHA256) {
		return false
	}
	_, err := localHome(a.Homes, key)
	return err == nil
}

func (a *CodexAdapter) CaptureCapacity(ctx context.Context, key string) []capacity.Reading {
	return a.CaptureCapacityResult(ctx, key).Readings
}

func (a *CodexAdapter) CaptureCapacityResult(ctx context.Context, key string) CapacityCapture {
	if !a.CanCaptureCapacity(key) {
		return CapacityCapture{Result: "unsupported"}
	}
	return a.captureCapacityResult(ctx, key, func(home string) (*wireProcess, error) {
		if err := harnesslaunch.Validate(a.Path, a.Nodes[key].Path); err != nil {
			return nil, err
		}
		environment := harnesslaunch.Environment(capacityEnvironment("CODEX_HOME", home, a.Path), a.Nodes[key].Path)
		return launchWire(a.Path, []string{"app-server", "--listen", "stdio://"}, home, environment, "jsonrpc", func(AdapterEvent) {})
	})
}

// This private transport seam tests deadlines without enabling any production
// capability. Public entry points always apply the qualification before launch.
func (a *CodexAdapter) captureCapacity(ctx context.Context, key string, launch func(string) (*wireProcess, error)) []capacity.Reading {
	return a.captureCapacityResult(ctx, key, launch).Readings
}

func captureError(ctx context.Context) CapacityCapture {
	if ctx.Err() != nil {
		return CapacityCapture{Result: "timeout"}
	}
	return CapacityCapture{Result: "protocol"}
}

func (a *CodexAdapter) captureCapacityResult(ctx context.Context, key string, launch func(string) (*wireProcess, error)) (out CapacityCapture) {
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	home, err := localHome(a.Homes, key)
	if err != nil || strings.TrimSpace(a.Emails[key]) == "" {
		return CapacityCapture{Result: "launch_failed"}
	}
	if op.Err() != nil {
		return captureError(op)
	}
	p, err := launch(home)
	if err != nil {
		return CapacityCapture{Result: "launch_failed"}
	}
	defer func() {
		// Idle checks never need a graceful user turn. Kill only this retained,
		// owned group and join it before releasing the capture slot.
		p.closeReader()
		cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		if p.cmd.Process != nil {
			if p.lifetime == nil {
				out = CapacityCapture{Result: "launch_failed", CleanupUnconfirmed: true}
			} else {
				// The final waiter can revoke signaling before publishing
				// waitDone. A declined signal is not cleanup evidence: join
				// the retained waiter and use its bounded final outcome.
				_ = p.lifetime.Signal(true)
				select {
				case <-p.waitDone:
					if p.cleanupResult() != nil {
						out = CapacityCapture{Result: "launch_failed", CleanupUnconfirmed: true}
					}
				case <-cleanup.Done():
					out = CapacityCapture{Result: "timeout", CleanupUnconfirmed: true}
				}
			}
		}
		if err := p.finishReader(); err != nil {
			out = CapacityCapture{Result: "protocol", CleanupUnconfirmed: true}
		}
	}()
	raw, err := p.request(op, "jsonrpc", "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-capacity", "version": "1"}})
	if err != nil || len(raw) > 64<<10 {
		return captureError(op)
	}
	if err := p.sendContext(op, map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		return captureError(op)
	}
	raw, err = p.request(op, "jsonrpc", "account/read", map[string]any{"refreshToken": false})
	if err != nil || len(raw) > 64<<10 {
		return captureError(op)
	}
	var identity struct {
		Account *struct {
			ID    string `json:"id"`
			Type  string `json:"type"`
			Email string `json:"email"`
		} `json:"account"`
	}
	if decodeProbeJSON(raw, &identity) != nil {
		return CapacityCapture{Result: "protocol"}
	}
	if identity.Account == nil {
		return CapacityCapture{Result: "authentication_failed"}
	}
	if identity.Account.Type != "chatgpt" || !agentsetup.CodexAccountMatches(a.Emails[key], identity.Account.Email) {
		return CapacityCapture{Result: "identity_mismatch"}
	}
	raw, err = p.request(op, "jsonrpc", "account/rateLimits/read", map[string]any{})
	if err != nil || len(raw) > 64<<10 || rejectDuplicateKeys(raw) != nil {
		return captureError(op)
	}
	parser := capacity.Parser{}
	now := time.Now().UTC()
	readings := parser.CodexSnapshot(raw, now)
	if len(readings) == 0 || len(readings) > 32 {
		return CapacityCapture{Result: "protocol"}
	}
	for i := range readings {
		readings[i].Source = "agentd"
		if readings[i].Validate(now) != nil {
			return CapacityCapture{Result: "protocol"}
		}
	}
	if identity.Account.ID != "" {
		a.quotaIDs.Store(key, identity.Account.ID)
	}
	return CapacityCapture{Result: "success", Readings: readings}
}

func (a *PiAdapter) CanCaptureCapacity(key string) bool {
	if a.Providers[key] != "openrouter" {
		return false
	}
	_, err := localHome(a.Homes, key)
	return err == nil
}
func (a *PiAdapter) CapacitySupport(key string) string {
	if a.CanCaptureCapacity(key) {
		return "OpenRouter key cap; total balance unknown"
	}
	return "not available: provider measurement unsupported"
}
func (a *PiAdapter) CaptureCapacityResult(ctx context.Context, key string) CapacityCapture {
	if !a.CanCaptureCapacity(key) {
		return CapacityCapture{Result: "unsupported"}
	}
	op, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ctx = op
	release, err := a.acquireProbe(ctx, key)
	if err != nil {
		return captureError(ctx)
	}
	defer release()
	home, err := localHome(a.Homes, key)
	if err != nil {
		return CapacityCapture{Result: "launch_failed"}
	}
	credits, err := agentsetup.OpenRouterCredits(ctx, home, a.OpenRouter)
	if errors.Is(err, openrouter.ErrProfile) {
		return CapacityCapture{Result: "launch_failed"}
	}
	if errors.Is(err, openrouter.ErrKey) {
		return CapacityCapture{Result: "authentication_failed"}
	}
	if err != nil {
		return captureError(ctx)
	}
	if credits == nil || !credits.Valid() {
		return CapacityCapture{Result: "protocol"}
	}
	return CapacityCapture{Result: "success", Credits: credits}
}
