// SPDX-License-Identifier: AGPL-3.0-only

// Package consent implements current native consent anchored in person-decided
// approvals. A stored intent is a pin, never a replacement for these reads.
package consent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/nativequalification"
	"github.com/inspr-at/paimos/internal/workorders"
)

const SessionScope = "harness.control.native.session"
const RetentionScope = "harness.control.native.retention"
const SettingsProfile = "lead-confined-v1"

// Facts is the bounded proposal shown in the existing approval rationale. The
// server derives policy digests; neither a supplied hash nor a label grants tools.
type Facts struct {
	ComputerID            string   `json:"computer_id"`
	AccountID             string   `json:"account_id"`
	WorkspaceID           string   `json:"workspace_id"`
	WorkspaceRoot         string   `json:"workspace_root"`
	ModelProfileID        string   `json:"model_profile_id"`
	ToolSet               []string `json:"tool_set"`
	SettingsProfile       string   `json:"settings_profile"`
	LocalHistory          string   `json:"local_history"`
	RetainFinals          bool     `json:"retain_finals"`
	Revision              int64    `json:"revision"`
	MaximumRuntimeSeconds int64    `json:"maximum_runtime_seconds"`
	MaximumTurns          int64    `json:"maximum_turns"`
	MaximumTokens         int64    `json:"maximum_tokens"`
}

func Scope(scope string) bool { return scope == SessionScope || scope == RetentionScope }

func (f *Facts) Validate() error {
	for _, id := range []string{f.ComputerID, f.AccountID, f.ModelProfileID} {
		if !workorders.UUID(id) {
			return errors.New("native consent requires exact computer, account and model profile")
		}
	}
	if f.WorkspaceID == "" || len(f.WorkspaceID) > 128 || strings.ContainsAny(f.WorkspaceID, "\x00\r\n") ||
		len(f.WorkspaceRoot) > 4096 || !path.IsAbs(f.WorkspaceRoot) || path.Clean(f.WorkspaceRoot) != f.WorkspaceRoot || strings.ContainsAny(f.WorkspaceRoot, "\x00\r\n") ||
		f.SettingsProfile != SettingsProfile || f.LocalHistory != "vendor_native" ||
		f.Revision != 1 || f.MaximumRuntimeSeconds < 1 || f.MaximumRuntimeSeconds > 86400 ||
		f.MaximumTurns < 1 || f.MaximumTurns > 10000 || f.MaximumTokens < 1 || f.MaximumTokens > 10000000 || f.ToolSet == nil {
		return errors.New("invalid native consent facts")
	}
	if _, err := nativequalification.SessionOverlay(f.ToolSet); err != nil {
		return errors.New("invalid native consent tool set")
	}
	f.ToolSet = slices.Clone(f.ToolSet)
	slices.Sort(f.ToolSet)
	return nil
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (f Facts) Digests() (tools, settings string) {
	overlay, _ := nativequalification.SessionOverlay(f.ToolSet)
	return digest(f.ToolSet), digest(overlay)
}

// Summary is appended by the server, so the existing Desk/phone approval card
// displays every consent choice even if the agent's own rationale omits it.
func (f Facts) Summary(scope string) string {
	return fmt.Sprintf("\n\nNative consent (%s):\nComputer: %s\nAccount: %s\nWorkspace: %s\nWorkspace root: %s\nModel profile: %s\nTools: %s\nSettings: %s\nLocal history: %s\nRetain explicit finals in Aeon: %t\nRevision: %d\nLimits: %d seconds, %d turns, %d tokens", scope, f.ComputerID, f.AccountID, f.WorkspaceID, f.WorkspaceRoot, f.ModelProfileID, strings.Join(f.ToolSet, ", "), f.SettingsProfile, f.LocalHistory, f.RetainFinals, f.Revision, f.MaximumRuntimeSeconds, f.MaximumTurns, f.MaximumTokens)
}
