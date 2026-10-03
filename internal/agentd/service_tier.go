// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/servicetier"
)

type serviceTierAdapter interface {
	ServiceTiers(context.Context, StartRequest) (servicetier.Report, error)
}
type serviceTierAPI interface {
	ReportServiceTiers(context.Context, HarnessSession, []servicetier.Report, *string) error
}

func (r *Remote) ReportServiceTiers(ctx context.Context, s HarnessSession, reports []servicetier.Report, active *string) error {
	return r.harnessWorker(ctx, s, "/tier/report", map[string]any{"reports": reports, "active_tier": active}, nil)
}
func (*PiAdapter) ServiceTiers(_ context.Context, r StartRequest) (servicetier.Report, error) {
	return servicetier.Advertised(Pi, r.Profile.Model, "unknown"), nil
}
func (*CursorAdapter) ServiceTiers(_ context.Context, r StartRequest) (servicetier.Report, error) {
	return servicetier.Advertised(Cursor, r.Profile.Model, "unknown"), nil
}
func (a *CodexAdapter) ServiceTiers(ctx context.Context, r StartRequest) (servicetier.Report, error) {
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return servicetier.Report{}, err
	}
	raw, code, err := probeRun(ctx, a.Path, harnesslaunch.Environment(withEnv("CODEX_HOME", home), a.Nodes[r.AccountKey].Path), "--version")
	version := strings.TrimSpace(string(raw))
	if err != nil || code != 0 || !validHarnessValue(version, 80) {
		version = "unknown"
	}
	return servicetier.Advertised(Codex, r.Profile.Model, version), nil
}
func (a *ClaudeAdapter) ServiceTiers(ctx context.Context, r StartRequest) (servicetier.Report, error) {
	resolved, err := a.resolved(r.Workspace)
	if err != nil {
		return servicetier.Report{}, err
	}
	home, err := localHome(a.Homes, r.AccountKey)
	if err != nil {
		return servicetier.Report{}, err
	}
	raw, code, err := probeRun(ctx, resolved.ClaudePath, claudeEnvironment(home, resolved.NodePath, resolved.ClaudePath), "--version")
	version := strings.TrimSpace(string(raw))
	if err != nil || code != 0 || !validHarnessValue(version, 80) {
		version = "unknown"
	}
	// Claude prints e.g. "3.2.0 (Claude Code)"; extract the version alone.
	for _, word := range strings.Fields(version) {
		if word != "unknown" && word[0] >= '0' && word[0] <= '9' {
			version = word
			break
		}
	}
	return servicetier.Advertised(Claude, r.Profile.Model, version), nil
}

// A Codex tier is applied by a new thread resume at an idle boundary. Existing
// work is never interrupted or repriced. The native acknowledgement must name
// this exact thread; a failed or ambiguous resume never confirms the change.
func (p *codexProcess) changeTier(ctx context.Context, tier string) error {
	if !servicetier.Valid(tier) {
		return ErrSettingRejected
	}
	p.eventMu.Lock()
	idle := p.persistent && p.terminalSeen && p.acknowledged && p.terminal != nil && p.terminal.Clean && !p.invalid && !p.finishing && !p.sealed
	thread := p.threadID
	p.eventMu.Unlock()
	if !idle || thread == "" {
		return ErrSettingRejected
	}
	value := map[string]string{"default": "default", "fast": "fast", "fastest": "ultrafast"}[tier]
	raw, err := p.request(ctx, "jsonrpc", "thread/resume", map[string]any{"threadId": thread, "config": map[string]any{"service_tier": value}})
	if err != nil {
		return err
	}
	var result struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err = json.Unmarshal(raw, &result); err != nil || result.Thread.ID != thread {
		return errors.New("Codex tier acknowledgement mismatch")
	}
	p.eventMu.Lock()
	p.serviceTier = tier
	p.eventMu.Unlock()
	p.observe(AdapterEvent{HarnessTier: tier})
	return nil
}

func (a *ACPAdapter) ServiceTiers(_ context.Context, r StartRequest) (servicetier.Report, error) {
	return servicetier.Advertised(a.Harness, r.Profile.Model, "unknown"), nil
}
func (*GrokAdapter) ServiceTiers(_ context.Context, r StartRequest) (servicetier.Report, error) {
	return servicetier.Advertised(Grok, r.Profile.Model, "unknown"), nil
}
