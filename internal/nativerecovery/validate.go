// SPDX-License-Identifier: AGPL-3.0-only

package nativerecovery

import (
	"encoding/json"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativereceipts"
)

func validUUID(s string) bool { _, err := uuid.Parse(s); return len(s) == 36 && err == nil }

func opaque(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func cloneBinding(b nativecontract.Binding) nativecontract.Binding {
	if b.RunID != nil {
		id := *b.RunID
		b.RunID = &id
	}
	return b
}

func (c *Coordinator) validate(schema string, value any) error {
	return nativereceipts.Validate(c.validator, schema, value)
}

func (c *Coordinator) validateBinding(b nativecontract.Binding) error {
	for _, s := range []string{b.TenantID, b.ProjectID, b.OwnedSessionID, b.HarnessSessionID, b.RuntimeEpoch, b.DaemonID, b.DaemonGeneration} {
		if len(s) > 128 {
			return fail(nativecontract.ReasonInvalidInput)
		}
	}
	if b.RunID != nil && len(*b.RunID) > 36 {
		return fail(nativecontract.ReasonInvalidInput)
	}
	return c.validate("NativeBinding", b)
}

func (c *Coordinator) validateResume(a nativecontract.Actor, in nativecontract.ResumeIntent) error {
	if !validUUID(in.RequestID) || !validUUID(in.PolicyID) || len(in.Reason) > 32 ||
		!validUUID(a.TenantID) || !validUUID(a.PrincipalID) || !validUUID(a.OwnerPersonID) ||
		len(a.GrantID) > 36 || len(a.KeyCeiling) > 128 || a.GrantRevision < 0 {
		return fail(nativecontract.ReasonInvalidInput)
	}
	for _, ceiling := range a.KeyCeiling {
		if !opaque(ceiling, 128) {
			return fail(nativecontract.ReasonInvalidInput)
		}
	}
	if err := c.validateBinding(in.ExpectedBinding); err != nil {
		return err
	}
	if err := c.validate("NativeResumeIntent", in); err != nil {
		return err
	}
	if a.TenantID != in.ExpectedBinding.TenantID {
		return fail(nativecontract.ReasonWrongInstance)
	}
	if a.PrincipalKind != "person" && a.PrincipalKind != "agent" {
		return fail(nativecontract.ReasonWrongPrincipal)
	}
	if in.Reason == "hand_back" {
		return fail(nativecontract.ReasonUnsupported) // P2c owns takeover/return.
	}
	if in.Reason == "person_continue" && (a.PrincipalKind != "person" || a.PrincipalID != a.OwnerPersonID) {
		return fail(nativecontract.ReasonWrongPrincipal)
	}
	return nil
}

func validateHistory(h History) error {
	if !opaque(h.ConversationRef, 128) || !opaque(h.WorkspaceID, 128) || !opaque(h.ResumeRef, 256) ||
		!validUUID(h.AccountID) || !nativereceipts.ValidDigest(h.MappingDigest) ||
		!nativereceipts.ValidDigest(h.AccountBindingDigest) || h.ProfileRevision < 1 {
		return fail(nativecontract.ReasonQualificationMissing)
	}
	return nil
}

func (c *Coordinator) validateSnapshot(s Snapshot, b nativecontract.Binding) error {
	if err := c.validateBinding(s.Writer.Binding); err != nil {
		return err
	}
	if !s.Writer.Binding.Equal(b) {
		return fail(nativecontract.ReasonWrongGeneration)
	}
	if !validUUID(s.OwnerPersonID) || s.Revision < 0 || !opaque(s.Writer.AttemptID, 128) ||
		s.Writer.Identity.RuntimeEpoch != "" && s.Writer.Identity.RuntimeEpoch != b.RuntimeEpoch ||
		len(s.Writer.Identity.ProcessIdentityDigest) > 64 {
		return fail(nativecontract.ReasonIdentityMismatch)
	}
	return nil // Reconnection does not require an unexpired launch/history grant.
}

func (c *Coordinator) validateAbsence(w nativecontract.WriterRecord, p Absence) error {
	e := p.Evidence
	if !w.Binding.Equal(p.Binding) || w.AttemptID != p.AttemptID || !nativereceipts.ValidDigest(p.AttestationDigest) ||
		!e.RootExited || !e.GroupAbsent || e.ObservedAt.IsZero() || e.ObservedAt.After(c.cfg.Now()) {
		return fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	known := w.Identity.ProcessIdentityDigest != ""
	if known {
		if !w.PossibleFork || p.Kind != "owned_exit" || !nativereceipts.ValidDigest(w.Identity.ProcessIdentityDigest) ||
			w.Identity.PID < 1 || w.Identity.StartedAt.IsZero() || e.ProcessIdentityDigest != w.Identity.ProcessIdentityDigest ||
			e.ObservedAt.Before(w.Identity.StartedAt) {
			return fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
	} else {
		kind := "never_forked"
		if w.PossibleFork {
			kind = "attempt_absent"
		}
		if p.Kind != kind || w.Identity.PID != 0 || !w.Identity.StartedAt.IsZero() || e.ProcessIdentityDigest != "" {
			return fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
	}
	return nil
}

func (c *Coordinator) bounded(s Snapshot, a nativecontract.Actor, used bool) error {
	if used || s.Cause != DaemonLost || s.Policy.Continuation != "bounded_preapproved" ||
		a.GrantID != s.Policy.GrantID || a.GrantRevision != s.Policy.GrantRevision {
		return fail(nativecontract.ReasonGrantMissing)
	}
	if err := c.validatePolicy(s.Policy); err != nil {
		return err
	}
	if !s.Budget.UsageKnown || s.Budget.StartedAt.IsZero() || s.Budget.StartedAt.After(c.cfg.Now()) ||
		s.Budget.TurnsUsed < 0 || s.Budget.TokensUsed < 0 {
		return fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if !c.cfg.Now().Before(s.Policy.ExpiresAt) || !c.cfg.Now().Before(c.windowEnd(s)) ||
		s.Budget.TurnsUsed >= s.Policy.MaximumTurns || s.Budget.TokensUsed >= s.Policy.MaximumTokens {
		return fail(nativecontract.ReasonGrantExpired)
	}
	return nil
}

func (c *Coordinator) windowEnd(s Snapshot) time.Time {
	end := s.Budget.StartedAt.Add(time.Duration(s.Policy.MaximumRuntimeSeconds) * time.Second)
	if s.Policy.ExpiresAt.Before(end) {
		return s.Policy.ExpiresAt
	}
	return end
}

func (c *Coordinator) validatePolicy(p nativecontract.Policy) error {
	for _, s := range []string{p.GrantID, p.ToolPolicyDigest, p.SettingsPolicyDigest, p.PermissionPolicy, p.LocalHistory, p.Continuation} {
		if len(s) > 64 {
			return fail(nativecontract.ReasonInvalidInput)
		}
	}
	return c.validate("NativePolicy", p)
}

func (c *Coordinator) validateSuccessor(s Snapshot, a nativecontract.Actor, in nativecontract.ResumeIntent, next Successor, effects Effects) error {
	old, launch := in.ExpectedBinding, next.Launch
	b := launch.Binding
	if err := c.validateBinding(b); err != nil {
		return err
	}
	if b.TenantID != old.TenantID || b.ProjectID != old.ProjectID || b.OwnedSessionID != old.OwnedSessionID || b.DaemonID != old.DaemonID ||
		!sameRun(b.RunID, old.RunID) || b.RuntimeEpoch == old.RuntimeEpoch || b.HarnessSessionID == old.HarnessSessionID ||
		b.BindingRevision <= old.BindingRevision || s.Cause == DaemonLost && b.DaemonGeneration == old.DaemonGeneration {
		return fail(nativecontract.ReasonWrongGeneration)
	}
	if !next.LaunchAuthorized || next.ClaimKind != "resume" || !nativereceipts.ValidDigest(next.RuntimeProofDigest) {
		return fail(nativecontract.ReasonGrantMissing)
	}
	if next.AuthorizationDeadline.IsZero() || !c.cfg.Now().Before(next.AuthorizationDeadline) {
		return fail(nativecontract.ReasonGrantExpired)
	}
	if err := c.validatePolicy(s.Policy); err != nil {
		return err
	}
	if err := c.validatePolicy(launch.Policy); err != nil {
		return err
	}
	if err := validateHistory(s.History); err != nil {
		return err
	}
	if err := validateHistory(next.History); err != nil {
		return err
	}
	if s.Policy.LocalHistory != "vendor_native" || launch.Policy.LocalHistory != "vendor_native" ||
		next.History != s.History || launch.ResumeRef != next.History.ResumeRef || launch.Profile.AccountID != s.History.AccountID ||
		launch.Profile.ProfileBindingRevision != s.History.ProfileRevision || launch.Mode != "structured" || launch.InitialInput != nil ||
		!opaque(launch.OwnedWriterID, 128) || launch.Lifetime == nil {
		return fail(nativecontract.ReasonQualificationMissing)
	}
	if launch.Policy.GrantID != in.PolicyID || !c.cfg.Now().Before(launch.Policy.ExpiresAt) {
		return fail(nativecontract.ReasonGrantExpired)
	}
	if launch.Policy.ToolPolicyDigest != s.Policy.ToolPolicyDigest || launch.Policy.SettingsPolicyDigest != s.Policy.SettingsPolicyDigest ||
		launch.Policy.PermissionPolicy != s.Policy.PermissionPolicy || launch.Options.ToolPolicyDigest != launch.Policy.ToolPolicyDigest ||
		launch.Options.SettingsPolicyDigest != launch.Policy.SettingsPolicyDigest {
		return fail(nativecontract.ReasonIdentityMismatch)
	}
	if len(effects.Items) > 0 && next.ReconciledEffectsDigest != effects.Digest {
		return fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if in.Reason == "approved_restart" {
		if err := c.bounded(s, a, false); err != nil {
			return err
		}
		p := launch.Policy
		if p.GrantID != s.Policy.GrantID || p.GrantRevision != s.Policy.GrantRevision || p.Continuation != s.Policy.Continuation ||
			p.ExpiresAt.After(c.windowEnd(s)) || p.MaximumRuntimeSeconds > int64(c.windowEnd(s).Sub(c.cfg.Now())/time.Second) ||
			p.MaximumTurns > s.Policy.MaximumTurns-s.Budget.TurnsUsed || p.MaximumTokens > s.Policy.MaximumTokens-s.Budget.TokensUsed {
			return fail(nativecontract.ReasonGrantExpired)
		}
	}
	// Bound local profile/config before the owner marshals/opens anything. Profile
	// qualification and environment filtering belong to the existing resolver.
	if len(launch.Cwd) > 4096 || len(launch.Profile.Environment) > 256 || len(launch.Tools.Token) > 16384 {
		return fail(nativecontract.ReasonInvalidInput)
	}
	size := len(launch.Profile.AccountHome) + len(launch.Profile.UserHome) + len(launch.Profile.WorkspaceRoot) + len(launch.Profile.ConversationRoot)
	for _, value := range launch.Profile.Environment {
		size += len(value)
	}
	for _, pin := range []nativecontract.BinaryPin{launch.Profile.Binary, launch.Profile.SDK} {
		size += len(pin.Path) + len(pin.SHA256) + len(pin.Version) + len(pin.Protocol)
	}
	if size > 64<<10 || len(launch.Options.Model) > 128 || len(launch.Options.ServiceTier) > 32 || len(launch.Options.ModelProfileID) > 36 ||
		launch.Options.Effort != nil && len(*launch.Options.Effort) > 32 {
		return fail(nativecontract.ReasonInvalidInput)
	}
	return c.validate("NativeLaunchConfig", launch.Options)
}

func sameRun(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func (c *Coordinator) effects(b nativecontract.Binding) (Effects, error) {
	out := Effects{Items: []Effect{}}
	after, total := "", 0
	for {
		page, err := c.cfg.Receipts.Page(b, after, 100)
		if err != nil {
			return out, err
		}
		total += len(page.Items)
		if total > nativereceipts.MaxReceipts {
			return out, fail(nativecontract.ReasonBufferFull)
		}
		for _, r := range page.Items {
			switch r.State {
			case "write_started", "write_acknowledged", "turn_started", "uncertain":
				out.Items = append(out.Items, Effect{r.OperationID, r.Operation, r.State, r.SourceSequence})
			}
		}
		if !page.Truncated {
			break
		}
		if page.NextCursor == nil || *page.NextCursor <= after || len(page.Items) == 0 {
			return out, fail(nativecontract.ReasonInvalidInput)
		}
		after = *page.NextCursor
	}
	// Fixed-order receipt pages make this a stable exact reconciliation challenge.
	digest, err := nativereceipts.Digest([]any{b, out.Items})
	out.Digest = digest
	return out, err
}

// No local recovery authority may be decoded from JSON either.
func (*Snapshot) UnmarshalJSON([]byte) error  { return fail(nativecontract.ReasonUnsupported) }
func (*Absence) UnmarshalJSON([]byte) error   { return fail(nativecontract.ReasonUnsupported) }
func (*Successor) UnmarshalJSON([]byte) error { return fail(nativecontract.ReasonUnsupported) }

var _ json.Marshaler = Snapshot{}
