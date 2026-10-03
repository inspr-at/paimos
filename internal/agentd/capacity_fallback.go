// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sort"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

// CapacityAccountStatus enumerates approved local account bindings, never a
// scan of vendor credential directories. Paths, keys and provider IDs stay local.
// Fallback describes capability, not whether an account has quota remaining.
type CapacityAccountStatus struct {
	AccountID  string     `json:"account_id"`
	Harness    string     `json:"harness"`
	Label      string     `json:"label,omitempty"`
	Plan       string     `json:"plan,omitempty"`
	Fallback   string     `json:"fallback"`
	LastReadAt *time.Time `json:"last_read_at,omitempty"`
}

// CapacityAccounts is a display-only projection of all enrolled config homes.
// Multiple accounts of one vendor remain separate even when labels coincide.
func (s *Supervisor) CapacityAccounts(accountID string) []CapacityAccountStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CapacityAccountStatus{}
	for _, a := range s.accounts {
		if accountID != "" && a.ID != accountID {
			continue
		}
		v := CapacityAccountStatus{AccountID: a.ID, Harness: a.Harness, Fallback: "not available"}
		if a.Metadata != nil {
			if validHarnessValue(a.Metadata.Label, 128) {
				v.Label = a.Metadata.Label
			}
			if validHarnessValue(a.Metadata.Plan, 128) {
				v.Plan = a.Metadata.Plan
			}
		}
		if adapter, ok := s.adapters[a.Harness].(interface{ CapacitySupport(string) string }); ok {
			v.Fallback = adapter.CapacitySupport(a.Key)
		}
		if at := s.capacityLast[a.ID]; !at.IsZero() {
			v.LastReadAt = &at
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

func (a *CodexAdapter) CapacitySupport(key string) string {
	if _, err := localHome(a.Homes, key); err != nil || a.Emails[key] == "" {
		return "not available: account binding missing"
	}
	if !a.CanCaptureCapacity(key) {
		return "not available idle: reading starts with a run"
	}
	return "account/rateLimits/read"
}
func (a *ClaudeAdapter) CapacitySupport(key string) string {
	if _, err := localHome(a.Homes, key); err != nil {
		return "not available: account binding missing"
	}
	if a.CanCaptureCapacity(key) {
		return "get_usage"
	}
	return "not available idle: reading starts with a run"
}
func (*CursorAdapter) CapacitySupport(string) string                              { return "not available headless" }
func (*CursorAdapter) CanCaptureCapacity(string) bool                             { return false }
func (*CursorAdapter) CaptureCapacity(context.Context, string) []capacity.Reading { return nil }

// A billing capability requires both a quota-neutral protocol and a verified
// identity/quota decoder for one exact executable. The installed Grok scout
// verified only struct names, not fields or a no-quota initialize path. There
// are intentionally no production capabilities yet; no configuration flag can
// turn an unverified schema into a quota reading.
type grokBillingCapability struct {
	binaryPath   string
	binarySHA256 string
	version      string
	decode       func(json.RawMessage, time.Time, string) []capacity.Reading
}

func (a *GrokAdapter) CapacitySupport(key string) string {
	if !a.billingSupported(key) {
		return "not available: Grok billing capability unverified"
	}
	if _, err := localHome(a.Homes, key); err != nil {
		return "not available: account binding missing"
	}
	return "x.ai/billing"
}
func (a *GrokAdapter) billingSupported(key string) bool {
	b, ok := a.Bindings[key]
	return ok && b.PrincipalSHA256 != "" && a.billing != nil && a.billing.decode != nil && a.billing.binaryPath == b.BinaryPath && a.billing.version != "" && exactCapacityBinary(b.BinaryPath, a.billing.binarySHA256)
}
func (a *GrokAdapter) CanCaptureCapacity(key string) bool {
	if !a.billingSupported(key) {
		return false
	}
	_, err := localHome(a.Homes, key)
	return err == nil
}
func (a *GrokAdapter) CaptureCapacity(ctx context.Context, key string) []capacity.Reading {
	// Check before launching: unsupported versions never run a prompt, TUI,
	// credential probe or even initialize on behalf of quota collection.
	if ctx.Err() != nil || !a.billingSupported(key) {
		return nil
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return nil
	}
	b := a.Bindings[key]
	p, err := launchWire(b.BinaryPath, []string{"agent", "stdio"}, home, capacityEnvironment("GROK_HOME", home, b.BinaryPath), "jsonrpc", func(AdapterEvent) {})
	if err != nil {
		return nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Stop(cleanup)
		_ = p.finishReader()
	}()
	op, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err = p.request(op, "jsonrpc", "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "aeon-capacity", "version": "1"}}); err != nil {
		return nil
	}
	raw, err := p.request(op, "jsonrpc", "x.ai/billing", map[string]any{})
	if err != nil {
		return nil
	}
	now := time.Now().UTC()
	readings := a.billing.decode(raw, now, b.PrincipalSHA256)
	if len(readings) > 32 {
		return nil
	}
	for i := range readings {
		readings[i].Source = "agentd"
		readings[i].ReadAt = now
		readings[i].RunID = ""
		readings[i].Phase = ""
		if readings[i].Validate(now) != nil {
			return nil
		}
	}
	return readings
}

// Vendor-owned authentication comes exclusively from the selected config home.
// Do not inherit API keys, alternate auth paths or loader/provider overrides.
func capacityEnvironment(name, home, path string) []string {
	return []string{"HOME=" + home, name + "=" + home, "PATH=" + filepath.Dir(path) + string(filepath.ListSeparator) + "/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
}

// Cursor legacy enrollments without a home retain their existing behavior.
// Once any homes are configured, every selected key must be explicitly bound.
func (a *CursorAdapter) accountEnvironment(key string) ([]string, error) {
	if a.Homes == nil {
		return nil, nil
	}
	home, err := localHome(a.Homes, key)
	if err != nil {
		return nil, err
	}
	return capacityEnvironment("CURSOR_CONFIG_DIR", home, a.Path), nil
}
