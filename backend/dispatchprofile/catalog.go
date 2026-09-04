// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package dispatchprofile owns the immutable, typed profiles used when the
// execution-options authority dispatches an operator-local agentd child. It
// contains no credentials, paths, process handles, or account identities.
package dispatchprofile

import (
	"errors"
	"regexp"
	"sort"
)

const CatalogVersion = "1"

const (
	MachineAuthenticatedReporter = "authenticated_reporter"
	AccountLocalProbe            = "local_probe"
)

var stableValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// Profile is an immutable execution choice. Machine and Account are source
// selectors: their runtime values come from the authenticated reporter and a
// fixed-argv local probe respectively, never from a start request.
type Profile struct {
	ID            string `json:"id"`
	Version       string `json:"version"`
	Harness       string `json:"harness"`
	Model         string `json:"model"`
	Effort        string `json:"effort"`
	MachineSource string `json:"machine_source"`
	AccountSource string `json:"account_source"`
	WorkspaceMode string `json:"workspace_mode"`
}

var catalog = []Profile{
	{ID: "codex-luna-medium", Version: CatalogVersion, Harness: "codex", Model: "gpt-5.6-luna", Effort: "medium", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "codex-sol-high", Version: CatalogVersion, Harness: "codex", Model: "gpt-5.6-sol", Effort: "high", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "codex-sol-xhigh", Version: CatalogVersion, Harness: "codex", Model: "gpt-5.6-sol", Effort: "xhigh", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "codex-terra-high", Version: CatalogVersion, Harness: "codex", Model: "gpt-5.6-terra", Effort: "high", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "claude-sonnet-high", Version: CatalogVersion, Harness: "claude", Model: "sonnet", Effort: "high", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "claude-opus-xhigh", Version: CatalogVersion, Harness: "claude", Model: "opus", Effort: "xhigh", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
	{ID: "claude-fable-xhigh", Version: CatalogVersion, Harness: "claude", Model: "fable", Effort: "xhigh", MachineSource: MachineAuthenticatedReporter, AccountSource: AccountLocalProbe, WorkspaceMode: "exclusive"},
}

// List returns a detached, stable-order catalog for the execution-options API.
func List() []Profile {
	out := append([]Profile(nil), catalog...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Resolve requires the exact immutable version and expected harness.
func Resolve(id, version, harness string) (Profile, error) {
	for _, profile := range catalog {
		if profile.ID != id || profile.Version != version {
			continue
		}
		if profile.Harness != harness {
			return Profile{}, errors.New("dispatch profile does not support the requested harness")
		}
		if err := Validate(profile); err != nil {
			return Profile{}, err
		}
		return profile, nil
	}
	return Profile{}, errors.New("dispatch profile id and version are unavailable")
}

// Validate rejects malformed or widened catalog entries before they can reach
// a vendor adapter. Supported pairs are deliberately closed by Resolve.
func Validate(profile Profile) error {
	if err := ValidateSnapshot(profile); err != nil {
		return err
	}
	if profile.WorkspaceMode != "exclusive" {
		return errors.New("dispatch profile workspace mode is unsupported")
	}
	return nil
}

// ValidateSnapshot validates a durable profile without consulting the live
// catalog. Retired profile versions must remain readable after a binary
// upgrade; their immutable model and effort are the historical authority.
func ValidateSnapshot(profile Profile) error {
	for _, value := range []string{profile.ID, profile.Version, profile.Harness, profile.Model, profile.Effort} {
		if len(value) == 0 || len(value) > 128 || !stableValue.MatchString(value) {
			return errors.New("dispatch profile contains an invalid stable value")
		}
	}
	if profile.Harness != "codex" && profile.Harness != "claude" {
		return errors.New("dispatch profile harness is unsupported")
	}
	if profile.MachineSource != MachineAuthenticatedReporter || profile.AccountSource != AccountLocalProbe {
		return errors.New("dispatch profile provenance source is unsupported")
	}
	if profile.WorkspaceMode != "exclusive" && profile.WorkspaceMode != "shared" {
		return errors.New("dispatch profile workspace mode is unsupported")
	}
	switch profile.Effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return errors.New("dispatch profile effort is unsupported")
	}
	return nil
}
