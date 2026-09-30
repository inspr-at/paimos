// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/inspr-at/paimos/internal/hookcap"
)

// HookInstaller is an explicit, disabled-by-default local setup choice. It
// never grants messages or inherits an enable flag from the model environment.
type HookInstaller struct {
	Enabled      bool
	Executable   string
	Scope        string
	Harness      string
	qualify      func(string, string, string) string
	authenticate func(context.Context, string, string) error
	beforeCommit func()
}

func hookOwnerHome() (string, error) {
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return "", errors.New("config_provenance")
	}
	home, err := filepath.EvalSymlinks(u.HomeDir)
	if err != nil {
		return "", errors.New("config_provenance")
	}
	return home, nil
}

func hookEnvironmentBlocker(home string, environment []string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" {
			continue
		}
		switch key {
		case "HOME":
			if value != home {
				return "config_environment"
			}
		case "CLAUDE_CONFIG_DIR":
			if value != filepath.Join(home, ".claude") {
				return "config_environment"
			}
		case "CODEX_HOME":
			if value != filepath.Join(home, ".codex") {
				return "config_environment"
			}
		case "XDG_CONFIG_HOME", "XDG_STATE_HOME":
			return "config_environment"
		default:
			if strings.HasPrefix(key, "AEON_") || strings.HasPrefix(key, "PAIMOS_") {
				return "config_environment"
			}
		}
	}
	return ""
}

func (h *HookInstaller) apply(ctx context.Context, home, workspace, computer, harness, version, goos string, uninstall bool, environment []string) hookcap.Capability {
	c := hookcap.Capability{Harness: harness, Version: version, OS: goos, Blocker: "feature_disabled"}
	// Never serialize arbitrary harness version output into diagnostics.
	if !hookcap.Valid(c) {
		c.Version = ""
		c.Blocker = "unsupported_version"
		return c
	}
	fail := func(reason string) hookcap.Capability { c.Blocker = reason; return c }
	if h == nil || !h.Enabled {
		return c
	}
	if h.Scope != "" && h.Scope != "user" {
		return fail("project_scope")
	}
	if reason := hookEnvironmentBlocker(home, environment); reason != "" {
		return fail(reason)
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || repositoryPath(home) || within(workspace, home) {
		return fail("project_scope")
	}
	if reason := hookProjectBlocker(workspace, home); reason != "" && !uninstall {
		return fail(reason)
	}
	if harness != "claude" && harness != "codex" {
		return fail("unsupported_harness")
	}
	if !uninstall {
		qualify := h.qualify
		if qualify == nil {
			qualify = hookcap.Qualified
		}
		if reason := qualify(harness, version, goos); reason != "" {
			return fail(reason)
		}
	}
	root := filepath.Join(home, ".local", "share", "aeon", "hooks")
	if ValidateStateLocation(root, workspace) != nil {
		return fail("project_scope")
	}
	store, err := OpenStore(root, !uninstall)
	if errors.Is(err, os.ErrNotExist) && uninstall {
		return fail("repair_required")
	}
	if err != nil {
		return fail("config_provenance")
	}
	defer store.Close()
	lock, err := store.LockNamed("hooks.lock")
	if err != nil {
		return fail("settings_changed")
	}
	defer lock.Close()
	name := harness + ".json"
	receipt := HookReceipt{Schema: "aeon.user-hook.v2", ComputerID: computer, Harness: harness, Home: home, Groups: map[string][]json.RawMessage{}}
	raw, err := store.Read(name, 64<<10)
	if err == nil {
		if !uniqueHookJSON(raw) || json.Unmarshal(raw, &receipt) != nil || receipt.Schema != "aeon.user-hook.v2" || receipt.ComputerID != computer || receipt.Harness != harness || receipt.Home != home {
			return fail("ownership_unknown")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail("ownership_unknown")
	} else if uninstall {
		return fail("repair_required")
	}
	settings, err := openDirectory(filepath.Join(home, "."+harness), !uninstall, false)
	if errors.Is(err, os.ErrNotExist) && uninstall {
		return fail("repair_required")
	}
	if err != nil {
		return fail("config_provenance")
	}
	defer settings.Close()
	lockSettings, err := settings.LockNamed("aeon-hooks.lock")
	if err != nil {
		return fail("settings_changed")
	}
	defer lockSettings.Close()
	file := "settings.json"
	if harness == "codex" {
		file = "hooks.json"
	}
	before, mode, err := settings.readHookSettings(file, 4<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail("config_provenance")
	}
	if before == nil && uninstall {
		return fail("repair_required")
	}
	pin := receipt.Pin
	if !uninstall {
		authenticate := h.authenticate
		if authenticate == nil {
			authenticate = authenticateHook
		}
		pin, err = pinHook(ctx, h.Executable, store, authenticate)
		if err != nil {
			if err.Error() == "artifact_changed" {
				return fail("artifact_changed")
			}
			return fail("artifact_untrusted")
		}
	}
	after, err := mergePairedHooks(before, receipt, pin, uninstall)
	if err != nil {
		return fail(err.Error())
	}
	if !uninstall {
		// Save ownership before commit. A crash can leave known old and new
		// groups, but never makes an unknown group eligible for removal.
		if receipt.Groups == nil {
			receipt.Groups = map[string][]json.RawMessage{}
		}
		for _, event := range hookEvents {
			group := hookGroup(pin, harness, event)
			known := false
			for _, old := range receipt.Groups[event] {
				if sameHookJSON(old, group) {
					known = true
				}
			}
			if !known {
				receipt.Groups[event] = append(receipt.Groups[event], group)
			}
		}
		receipt.Pin = pin
		raw, _ = json.Marshal(receipt)
		if len(raw) > 64<<10 || store.Write(name, raw, false) != nil {
			return fail("settings_changed")
		}
		if pin.Check() != nil {
			return fail("artifact_changed")
		}
	}
	if !sameHookJSON(before, after) {
		if settings.commitHookSettings(file, before, after, mode, h.beforeCommit) != nil {
			return fail("settings_changed")
		}
	}
	if uninstall {
		return fail("repair_required")
	}
	if pin.Check() != nil {
		return fail("artifact_changed")
	}
	current, _, err := settings.readHookSettings(file, 4<<20)
	if err != nil || !sameHookJSON(current, after) {
		return fail("settings_changed")
	}
	c.Verified, c.Blocker = true, ""
	return c
}

// RepairHooks is the same installation path used after pairing approval. It
// requires a connected saved computer and never changes its messaging consent.
func (e *Engine) RepairHooks(ctx context.Context, uninstall bool) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.View.ComputerState != "connected" || s.DisconnectAll {
		return e.progress(s), errors.New("connected pairing required for hook repair")
	}
	if e.Hooks != nil && e.Hooks.Harness != "" {
		found := false
		for _, a := range s.View.Enrollments {
			if a.Harness == e.Hooks.Harness && a.State == "connected" && !s.Removed[a.AccountID] {
				found = true
			}
		}
		if !found {
			return e.progress(s), errors.New("enrolled harness required for hook repair")
		}
	}
	if err = e.setupHooks(ctx, s, uninstall); err != nil {
		return e.progress(s), err
	}
	proof := e.proof(s)
	if _, err = e.API.Reconcile(ctx, proof); err != nil {
		return e.progress(s), err
	}
	return e.progress(s), nil
}

func refreshHookCapabilities(s *snapshot) {
	for i, c := range s.HookCapabilities {
		if !c.Verified {
			continue
		}
		c = hookcap.Project(c)
		if c.Verified {
			if _, _, err := ReadUserHookIdentity(s.View.ComputerID, c.Harness, s.Request.Workspace); err != nil {
				c.Verified, c.Blocker = false, "repair_required"
			}
		}
		s.HookCapabilities[i] = c
	}
}

func (e *Engine) setupHooks(ctx context.Context, s *snapshot, uninstall bool) error {
	home := ""
	if e.Hooks != nil && e.Hooks.Enabled {
		var err error
		home, err = hookOwnerHome()
		if err != nil {
			return err
		}
		if s.HookHome != "" && s.HookHome != home {
			return errors.New("config_environment")
		}
	}
	previous := s.HookCapabilities
	s.HookCapabilities = nil
	if e.Hooks != nil && e.Hooks.Harness != "" {
		for _, c := range previous {
			if c.Harness != e.Hooks.Harness {
				s.HookCapabilities = append(s.HookCapabilities, c)
			}
		}
	}
	for _, c := range s.Candidates {
		if e.Hooks != nil && e.Hooks.Harness != "" && e.Hooks.Harness != c.Candidate.Harness {
			continue
		}
		selected := false
		for _, a := range s.View.Enrollments {
			if a.Harness == c.Candidate.Harness && a.State == "connected" && !s.Removed[a.AccountID] {
				selected = true
			}
		}
		if !selected {
			continue
		}
		capability := e.Hooks.apply(ctx, home, s.Request.Workspace, s.View.ComputerID, c.Candidate.Harness, c.Version, runtime.GOOS, uninstall, os.Environ())
		if capability.Verified {
			s.HookHome = home
		}
		s.HookCapabilities = append(s.HookCapabilities, capability)
	}
	return e.save(s, false)
}

// Disconnect removes only its exact unchanged entries. Local configuration
// conflicts never delay server revocation or credential cleanup.
func (e *Engine) removeUserHooks(ctx context.Context, s *snapshot) {
	for _, c := range s.Candidates {
		e.removeUserHook(ctx, s, c.Candidate.Harness)
	}
}

func (e *Engine) removeUserHook(ctx context.Context, s *snapshot, harness string) {
	if s.HookHome == "" {
		return
	}
	home, err := hookOwnerHome()
	if err != nil || home != s.HookHome {
		return
	}
	installer := &HookInstaller{Enabled: true, Scope: "user"}
	for _, c := range s.Candidates {
		if c.Candidate.Harness != harness {
			continue
		}
		capability := installer.apply(ctx, home, s.Request.Workspace, s.View.ComputerID, harness, c.Version, runtime.GOOS, true, nil)
		kept := s.HookCapabilities[:0]
		for _, old := range s.HookCapabilities {
			if old.Harness != harness {
				kept = append(kept, old)
			}
		}
		s.HookCapabilities = append(kept, capability)
		break
	}
}

// ReadUserHookIdentity gives the future peer-only runtime the public pin and
// owned configuration digest, without opening pairing tokens/API config. It
// does not establish harness qualification, launch ancestry or a message grant.
// Callers must still match the observed loaded image with MatchesLoadedImage.
func ReadUserHookIdentity(computer, harness, workspace string) (HookPin, string, error) {
	home, err := hookOwnerHome()
	if err != nil {
		return HookPin{}, "", err
	}
	if reason := hookEnvironmentBlocker(home, os.Environ()); reason != "" {
		return HookPin{}, "", errors.New(reason)
	}
	if reason := hookProjectBlocker(workspace, home); reason != "" {
		return HookPin{}, "", errors.New(reason)
	}
	return readUserHookIdentity(home, computer, harness)
}

func readUserHookIdentity(home, computer, harness string) (HookPin, string, error) {
	fail := func() (HookPin, string, error) { return HookPin{}, "", errors.New("repair_required") }
	if harness != "claude" && harness != "codex" {
		return fail()
	}
	store, err := OpenStore(filepath.Join(home, ".local", "share", "aeon", "hooks"), false)
	if err != nil {
		return fail()
	}
	defer store.Close()
	raw, err := store.Read(harness+".json", 64<<10)
	if err != nil {
		return fail()
	}
	var receipt HookReceipt
	if !uniqueHookJSON(raw) || json.Unmarshal(raw, &receipt) != nil || receipt.Schema != "aeon.user-hook.v2" || receipt.ComputerID != computer || receipt.Home != home || receipt.Harness != harness || receipt.Pin.Check() != nil {
		return fail()
	}
	settings, err := openDirectory(filepath.Join(home, "."+harness), false, false)
	if err != nil {
		return fail()
	}
	defer settings.Close()
	file := "settings.json"
	if harness == "codex" {
		file = "hooks.json"
	}
	raw, _, err = settings.readHookSettings(file, 4<<20)
	if err != nil {
		return fail()
	}
	// Re-merging should be exactly idempotent. Missing, changed, extra or
	// duplicate delivery groups cannot establish the active configuration pin.
	expected, err := mergePairedHooks(raw, receipt, receipt.Pin, false)
	if err != nil || !sameHookJSON(raw, expected) {
		return fail()
	}
	var groups []json.RawMessage
	for _, event := range hookEvents {
		groups = append(groups, hookGroup(receipt.Pin, harness, event))
	}
	owned, _ := json.Marshal(groups)
	return receipt.Pin, Hash(owned), nil
}
