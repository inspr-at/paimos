// SPDX-License-Identifier: AGPL-3.0-only

package nativeadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type profileChangeRecord struct {
	Version                                               int `json:"version"`
	AccountID, MemberID, Generation, Group, ProfileDigest string
	ProfileRevision                                       int64
	Released                                              bool
	Login                                                 agentsetup.LedgerLogin
}

func profileChangeName(account string) string { return "profile-change-" + account + ".json" }

func (g *Gate) profileClear(account string) error {
	raw, err := g.opts.State.Read(profileChangeName(account), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var r profileChangeRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || r.Version != 1 || r.AccountID != account ||
		!hexID(r.Group, 32) || !hexID(r.MemberID, 32) || !uuid(r.Generation) || !hexID(r.ProfileDigest, 64) || !r.Released {
		return denial(nativecontract.ReasonOwnershipUnconfirmed)
	}
	return nil
}

func (g *Gate) writeProfileChange(r profileChangeRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return g.opts.State.Write(profileChangeName(r.AccountID), raw, false)
}

// Attempt invokes only the owning lifecycle's fork callback. Possible-fork
// evidence is durable BEFORE that callback; an error is not proof of no fork.
// There is no retry of an attempted epoch and no foreign PID adoption.
func (g *Gate) Attempt(ctx context.Context, p *Permit, start func(context.Context, nativecontract.LocalProfile) (nativecontract.NativeIdentity, error)) error {
	if start == nil {
		return denial(nativecontract.ReasonInvalidInput)
	}
	return g.locked(ctx, func(ctx context.Context) error {
		r, profile, err := g.current(ctx, p)
		if err != nil {
			return err
		}
		if r.State != "reserved" || r.Attempted {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		if err = g.opts.Ledger.Claimed(r.MemberID, r.Generation, r.Group, p.request.Login); err != nil {
			return err
		}
		r.Attempted, r.State = true, "possible_fork"
		if err = g.write(r); err != nil {
			return err
		}
		if err = g.opts.Ledger.Launching(r.MemberID, r.Generation, r.Group); err != nil {
			return err
		}
		// Repeat identity and current server admission immediately at the fork
		// boundary, after the durable writes. The profile/login hold serializes
		// person/OPS profile changes for the entire lifetime of the process.
		_, profile, err = g.current(ctx, p)
		if err != nil {
			return err
		}
		identity, startErr := start(ctx, profile)
		valid := identity.RuntimeEpoch == r.Binding.RuntimeEpoch && identity.PID > 0 &&
			hexID(identity.ProcessIdentityDigest, 64) && !identity.StartedAt.IsZero() && !identity.StartedAt.After(g.opts.Now())
		r.State = "exit_unconfirmed"
		if valid {
			r.IdentityDigest, r.PID, r.StartedAt = identity.ProcessIdentityDigest, identity.PID, identity.StartedAt
			if startErr == nil {
				r.State = "running"
			}
		}
		if err = g.write(r); err != nil {
			return errors.Join(startErr, err)
		}
		if !valid {
			return errors.Join(startErr, denial(nativecontract.ReasonOwnershipUnconfirmed))
		}
		if err = g.opts.Ledger.Running(r.MemberID, r.Generation, r.Group, identity.PID, identity.StartedAt); err != nil {
			return errors.Join(startErr, err)
		}
		return startErr
	})
}

func (g *Gate) release(r record) error {
	// Journal first: interruption leaves the shared ledger conservatively held.
	// A successor cannot acquire that login until Release commits too.
	r.State = "released"
	if err := g.write(r); err != nil {
		return err
	}
	return g.opts.Ledger.Release(r.MemberID, r.Generation, r.Group)
}

// Abandon is safe only before this owner crossed the possible-fork boundary.
// Expiry, a cancelled request, API outage or a failed Attempt never qualifies.
func (g *Gate) Abandon(ctx context.Context, p *Permit) error {
	return g.locked(ctx, func(context.Context) error {
		r, err := g.owned(p)
		if err != nil {
			return err
		}
		if r.Attempted {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return g.release(r)
	})
}

// ConfirmExit accepts the owning process proof only. Turn/final events, detach,
// missing heartbeats and root exit without group absence never release a slot.
func (g *Gate) ConfirmExit(ctx context.Context, p *Permit, evidence nativecontract.ExitEvidence) error {
	return g.locked(ctx, func(context.Context) error {
		r, err := g.owned(p)
		if err != nil {
			return err
		}
		if !r.Attempted || r.IdentityDigest == "" || evidence.ProcessIdentityDigest != r.IdentityDigest ||
			!evidence.RootExited || !evidence.GroupAbsent || evidence.ObservedAt.Before(r.StartedAt) ||
			evidence.ObservedAt.IsZero() || evidence.ObservedAt.After(g.opts.Now()) {
			return denial(nativecontract.ReasonOwnershipUnconfirmed)
		}
		return g.release(r)
	})
}

// ChangeProfile reserves the SAME shared login while a qualified person/OPS
// callback changes its sign-in. It also refuses this member's live/uncertain
// account users even if a proposed new login identity differs. A callback's
// unconfirmed completion retains the durable group and needs owner recovery.
// The returned group is evidence for that recovery, never authority or a token.
func (g *Gate) ChangeProfile(ctx context.Context, profile nativecontract.ProfileRequest, login agentsetup.LedgerLogin, change func(context.Context) (confirmed bool, err error)) (string, error) {
	if change == nil || !uuid(profile.AccountID) || !uuid(profile.TenantID) || !uuid(profile.OwnerPersonID) ||
		!opaqueKey(profile.WorkspaceID) || profile.BindingRevision < 1 || !opaqueKey(profile.AccountKey) ||
		login.Harness != profile.Harness || len(profile.Harness) < 1 || len(profile.Harness) > 32 ||
		!(login.Identity == "unknown" || hexID(login.Identity, 64)) {
		return "", denial(nativecontract.ReasonInvalidInput)
	}
	var group string
	err := g.locked(ctx, func(ctx context.Context) error {
		if err := g.opts.Authority.AuthorizeProfileChange(ctx, profile); err != nil {
			return err
		}
		if err := g.profileClear(profile.AccountID); err != nil {
			return err
		}
		names, err := g.opts.State.Names(maxRecords)
		if err != nil {
			return err
		}
		if len(names) > maxRecords {
			return denial(nativecontract.ReasonBufferFull)
		}
		for _, name := range names {
			if !strings.HasPrefix(name, "session-") || !strings.HasSuffix(name, ".json") {
				continue
			}
			r, err := g.read(strings.TrimSuffix(strings.TrimPrefix(name, "session-"), ".json"))
			if err != nil {
				return err
			}
			if r.AccountID == profile.AccountID && r.State != "released" {
				return denial(nativecontract.ReasonAccountFull)
			}
		}
		// Resolve before changing sign-in; no auth file/token handling here.
		resolved, err := g.opts.Profiles.Resolve(ctx, profile)
		if err != nil {
			return err
		}
		if resolved.AccountID != profile.AccountID || resolved.ProfileBindingRevision != profile.BindingRevision {
			return denial(nativecontract.ReasonIdentityMismatch)
		}
		if err = boundProfile(resolved); err != nil {
			return err
		}
		group, err = agentsetup.LedgerID()
		if err != nil {
			return err
		}
		if _, err = g.opts.Ledger.Acquire(g.opts.MemberID, g.opts.Generation, group, []agentsetup.LedgerCandidate{{AccountID: profile.AccountID, Login: login}}); err != nil {
			return err
		}
		// Private evidence survives a crash between login/logout and completion.
		r := profileChangeRecord{Version: 1, AccountID: profile.AccountID, MemberID: g.opts.MemberID,
			Generation: g.opts.Generation, Group: group, ProfileDigest: profileDigest(resolved), ProfileRevision: profile.BindingRevision, Login: login}
		if err = g.writeProfileChange(r); err != nil {
			return err
		}
		if err = g.opts.Authority.AuthorizeProfileChange(ctx, profile); err != nil {
			r.Released = true
			if writeErr := g.writeProfileChange(r); writeErr != nil {
				return errors.Join(err, writeErr)
			}
			return errors.Join(err, g.opts.Ledger.Release(g.opts.MemberID, g.opts.Generation, group))
		}
		confirmed, changeErr := change(ctx)
		if !confirmed {
			return errors.Join(changeErr, denial(nativecontract.ReasonOwnershipUnconfirmed))
		}
		r.Released = true
		if err = g.writeProfileChange(r); err != nil {
			return errors.Join(changeErr, err)
		}
		return errors.Join(changeErr, g.opts.Ledger.Release(g.opts.MemberID, g.opts.Generation, group))
	})
	return group, err
}
