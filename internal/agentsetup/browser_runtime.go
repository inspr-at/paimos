// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"errors"
	"slices"
	"time"
)

const RoutineBrowserCapability = "routine_native_browser_v1"

var ErrNativeBrowserUnavailable = errors.New("native_browser_qualification_unavailable")

// NativeBrowserRuntime is trusted, owner-local launcher evidence. It is never
// accepted from a tool request, prompt or a daemon's unauthenticated claim.
// OPS qualification binds all pins into CapabilityDigest/HostMappingDigest;
// a matching server qualification alone cannot supply a missing launcher.
type NativeBrowserRuntime struct {
	TenantID, OwnerID, ProjectID, AccountID, ComputerID  string
	DaemonID, DaemonGeneration                           string
	DaemonDigest, CapabilityDigest, HostMappingDigest    string
	ChromeDigest, InterpreterDigest, LauncherDigest      string
	Platform, Adapter                                    string
	Capabilities                                         []string
	ReportedAt                                           time.Time
	OwnerApproved, LauncherReady, Qualified              bool
	Native, Sandboxed, ReadOnlyNetwork, EphemeralProfile bool
}

func (r NativeBrowserRuntime) Require(g RoutineGrant, now time.Time) error {
	if g.Validate() != nil || g.RuntimeCapability != RoutineBrowserCapability ||
		r.TenantID != g.TenantID || r.OwnerID != g.OwnerID || r.ProjectID != g.ProjectID ||
		r.AccountID != g.AccountID || r.ComputerID != g.ComputerID ||
		r.DaemonID != g.DaemonID || r.DaemonGeneration != g.DaemonGeneration ||
		r.DaemonDigest != g.DaemonDigest || r.CapabilityDigest != g.CapabilityDigest || r.HostMappingDigest != g.HostMappingDigest ||
		r.Platform != "darwin" || r.Adapter != "claude" || !r.Native || r.Sandboxed ||
		!r.OwnerApproved || !r.LauncherReady || !r.Qualified || !r.ReadOnlyNetwork || !r.EphemeralProfile ||
		!RoutineDigest(r.ChromeDigest) || !RoutineDigest(r.InterpreterDigest) || !RoutineDigest(r.LauncherDigest) ||
		len(r.Capabilities) > 32 || !slices.Contains(r.Capabilities, RoutineExecutionCapability) || !slices.Contains(r.Capabilities, RoutineBrowserCapability) ||
		now.IsZero() || r.ReportedAt.IsZero() || r.ReportedAt.After(now) || now.Sub(r.ReportedAt) > 2*time.Minute ||
		!now.Before(g.ExpiresAt) || g.ExpiresAt.Sub(now) > 2*time.Minute {
		return ErrNativeBrowserUnavailable
	}
	return nil
}
