// SPDX-License-Identifier: AGPL-3.0-only

// Package nativequalification contains local conformance checks for the LEAD
// pilot. Passing a fake does not issue a grant or enable a production runtime.
package nativequalification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

var (
	ErrPolicy   = errors.New("native qualification policy mismatch")
	ErrPin      = errors.New("native qualification pin mismatch")
	ErrFence    = errors.New("native qualification workspace or resource mismatch")
	ErrGrant    = errors.New("native qualification grant refused")
	ErrDeadline = errors.New("native qualification deadline expired")
	ErrEvidence = errors.New("native qualification evidence mismatch")
	ErrBound    = errors.New("native qualification bound exceeded")
	ErrExit     = errors.New("native qualification exit unconfirmed")
)

// Candidate is local-only. Require must check the exact current actor and
// grant, and is never a replacement for RequireTx in the tool broker's write.
type Candidate struct {
	Spec                          nativecontract.LaunchSpec
	Actor                         nativecontract.Actor
	AllowedTools                  []string
	ResourceRoot, ResourceLeaseID string
	NativeHistoryGrantID          string
	Require                       func(context.Context, nativecontract.Actor, nativecontract.Policy) error
	RequireHistory                func(context.Context, nativecontract.Actor, string) error
	Now                           func() time.Time
}

// Overlay describes only this session. Empty setting sources and empty legacy
// hook buckets exclude personal/project hook scripts. The broker tool guard
// remains a runtime callback, separate from these settings hooks.
type Overlay struct {
	SettingSources        []string        `json:"settingSources"`
	Settings              json.RawMessage `json:"settings"`
	DisabledLegacyHooks   []string        `json:"disabledLegacyHooks"`
	AllowedTools          []string        `json:"allowedTools"`
	Tools                 []string        `json:"tools"`
	Plugins               []string        `json:"plugins"`
	AdditionalDirectories []string        `json:"additionalDirectories"`
	StrictMCPConfig       bool            `json:"strictMcpConfig"`
	PermissionMode        string          `json:"permissionMode"`
	PersistSession        bool            `json:"persistSession"`
}

// SessionOverlay never reads or edits global/personal settings. No
// disableAllHooks flag is used: it could disable the mandatory tool guard too.
func SessionOverlay(tools []string) (Overlay, error) {
	if len(tools) > 13 {
		return Overlay{}, ErrBound
	}
	seen := map[string]bool{}
	allowed := make([]string, 0, len(tools))
	for _, name := range tools {
		if !leadTool(name) || seen[name] {
			return Overlay{}, ErrPolicy
		}
		seen[name] = true
		allowed = append(allowed, "mcp__aeon__"+name)
	}
	return Overlay{
		SettingSources:      []string{},
		Settings:            json.RawMessage(`{"fastMode":false,"hooks":{"SessionStart":[],"UserPromptSubmit":[],"PostToolUse":[],"Stop":[],"SubagentStart":[],"SubagentStop":[]}}`),
		DisabledLegacyHooks: []string{"attached-inbox", "cswap", "duplicate-subagent-registration"},
		AllowedTools:        allowed, Tools: []string{}, Plugins: []string{}, AdditionalDirectories: []string{},
		StrictMCPConfig: true, PermissionMode: "dontAsk", PersistSession: true,
	}, nil
}

func leadTool(name string) bool {
	switch name {
	case "aeon_comment", "aeon_status", "aeon_check_criterion", "aeon_evidence", "aeon_request_approval", "aeon_reply", "aeon_terminal", "aeon_read", "aeon_write", "aeon_edit", "aeon_glob", "aeon_grep", "aeon_activity":
		return true
	default:
		return false
	}
}

func (Candidate) MarshalJSON() ([]byte, error) { return nil, ErrPolicy }

func (c Candidate) current(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Now == nil || c.Require == nil || c.RequireHistory == nil {
		return ErrPolicy
	}
	if !c.Now().Before(c.Spec.Policy.ExpiresAt) {
		return ErrDeadline
	}
	if err := c.Require(ctx, c.Actor, c.Spec.Policy); err != nil {
		return ErrGrant
	}
	if err := c.RequireHistory(ctx, c.Actor, c.NativeHistoryGrantID); err != nil {
		return ErrGrant
	}
	// A remote authority check may itself consume the remaining deadline.
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.Now().Before(c.Spec.Policy.ExpiresAt) {
		return ErrDeadline
	}
	return nil
}

func validateCandidate(ctx context.Context, c Candidate) error {
	if err := c.current(ctx); err != nil {
		return err
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return err
	}
	for name, value := range map[string]any{"NativeBinding": c.Spec.Binding, "NativePolicy": c.Spec.Policy} {
		data, err := json.Marshal(value)
		if err != nil || v.ValidateJSON(name, data) != nil {
			return ErrPolicy
		}
	}
	s := c.Spec
	if c.Actor.TenantID != s.Binding.TenantID || c.Actor.GrantID != s.Policy.GrantID || c.Actor.GrantRevision != s.Policy.GrantRevision ||
		c.Actor.OwnerPersonID == "" || c.Actor.PrincipalID == "" || s.Mode != "structured" || s.Binding.RunID != nil ||
		s.Policy.LocalHistory != "vendor_native" || c.NativeHistoryGrantID == "" ||
		len(s.Profile.Environment) != 0 || s.Options.ToolPolicyDigest != s.Policy.ToolPolicyDigest || s.Options.SettingsPolicyDigest != s.Policy.SettingsPolicyDigest {
		return ErrPolicy
	}
	if s.Policy.ExpiresAt.Sub(c.Now()) > 24*time.Hour {
		return ErrDeadline
	}
	if _, err := SessionOverlay(c.AllowedTools); err != nil {
		return err
	}
	if !physicalDirectory(s.Profile.WorkspaceRoot) || !physicalDirectory(s.Profile.AccountHome) || !physicalDirectory(s.Profile.UserHome) ||
		!physicalDirectory(s.Profile.ConversationRoot) || !within(s.Profile.AccountHome, s.Profile.ConversationRoot) ||
		!physicalDirectory(s.Cwd) || !within(s.Profile.WorkspaceRoot, s.Cwd) {
		return ErrFence
	}
	if c.ResourceRoot != "" && (!physicalDirectory(c.ResourceRoot) || c.ResourceLeaseID == "") {
		return ErrFence
	}
	if c.ResourceRoot == "" && c.ResourceLeaseID != "" {
		return ErrFence
	}
	if !sameBinding(s.Binding, s.Tools.Binding) || !c.Now().Before(s.Tools.ExpiresAt) ||
		s.Tools.ExpiresAt.After(s.Policy.ExpiresAt) || len(s.Tools.Token) != 32 {
		return ErrPolicy
	}
	// URL syntax is already qualified by P1a; a non-loopback endpoint cannot be
	// introduced by the qualification candidate.
	port, portErr := strconv.Atoi(strings.TrimPrefix(s.Tools.URL, "http://127.0.0.1:"))
	if !strings.HasPrefix(s.Tools.URL, "http://127.0.0.1:") || portErr != nil || port < 1 || port > 65535 {
		return ErrPolicy
	}
	for _, pin := range []nativecontract.BinaryPin{s.Profile.Binary, s.Profile.SDK} {
		if err := CheckPin(pin); err != nil {
			return err
		}
	}
	return c.current(ctx)
}

// CheckPin bounds the read before hashing and never runs the binary/SDK. It
// checks fixture identity; launch-time ownedprocess/agentsetup checks still apply.
func CheckPin(pin nativecontract.BinaryPin) error {
	if len(pin.Path) > 4096 || len(pin.SHA256) != 64 || pin.Version == "" || len(pin.Version) > 128 || pin.Protocol == "" || len(pin.Protocol) > 128 {
		return ErrPin
	}
	physical, err := filepath.EvalSymlinks(pin.Path)
	if err != nil || !filepath.IsAbs(pin.Path) || physical != pin.Path {
		return ErrPin
	}
	f, err := os.Open(pin.Path)
	if err != nil {
		return ErrPin
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 || info.Mode().Perm()&0022 != 0 {
		return ErrPin
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (32<<20)+1))
	if err != nil || n > 32<<20 || hex.EncodeToString(hash.Sum(nil)) != pin.SHA256 {
		return ErrPin
	}
	return nil
}

func physicalDirectory(path string) bool {
	if len(path) == 0 || len(path) > 4096 || !filepath.IsAbs(path) {
		return false
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil || actual != path {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func sameBinding(a, b nativecontract.Binding) bool {
	// The LEAD root always has null run_id; pointer identity is not authority.
	return a.RunID == nil && b.RunID == nil && a == b
}
