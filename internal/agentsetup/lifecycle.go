// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
)

func (e *Engine) Status(ctx context.Context) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.View.ComputerID == "" {
		return e.progress(s), nil
	}
	return e.reconcile(ctx, s)
}

func (e *Engine) Disconnect(ctx context.Context, accountID string) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.View.ComputerID == "" {
		return e.progress(s), errors.New("computer is not enrolled")
	}
	if s.ComputerCleaned {
		return e.reconcile(ctx, s)
	}
	if accountID != "" {
		found := false
		for _, a := range s.View.Enrollments {
			if a.AccountID == accountID {
				found = true
				break
			}
		}
		if !found {
			return e.progress(s), errors.New("account does not belong to this computer")
		}
		s.Removed[accountID] = true
	} else {
		s.DisconnectAll = true
	}
	s.Phase = "draining"
	if err = e.save(s, false); err != nil {
		return Progress{}, err
	}
	if e.Local == nil {
		return e.progress(s), errors.New("local dispatch fence unavailable")
	}
	// A successful Fence persists even if the local socket is offline. Its
	// status may remain unconfirmed; that is never permission to unload.
	if _, err = e.Local.Fence(ctx, s.View.DaemonID, accountID); err != nil {
		return e.progress(s), err
	}
	_, token, err := ReadRuntime(e.Store.Path())
	if err != nil {
		return e.progress(s), err
	}
	v, err := e.API.Disconnect(ctx, token, accountID)
	if err != nil {
		s.Phase = "server_unconfirmed"
		if saveErr := e.save(s, false); saveErr != nil {
			return Progress{}, saveErr
		}
		// Revoked runtime keys may still reconcile their own tombstone.
		p, reconcileErr := e.reconcile(ctx, s)
		if reconcileErr == nil && p.ServerRevocation == "confirmed" {
			return p, nil
		}
		p = e.progress(s)
		p.ServerRevocation = "unconfirmed"
		p.Action = "Local dispatch is frozen. Restore connectivity and run disconnect again to confirm server revocation."
		return p, err
	}
	if err = validateView(s, v, false); err != nil {
		return e.progress(s), err
	}
	prefix := s.View.RuntimePrefix
	s.View = v
	s.View.RuntimePrefix = prefix
	if err = e.save(s, false); err != nil {
		return Progress{}, err
	}
	return e.reconcile(ctx, s)
}

func (e *Engine) proof(s *snapshot) ProofRequest {
	return ProofRequest{TenantID: s.Response.TenantID, RequestID: s.LifecycleRequestID, LifecycleSecret: s.Lifecycle}
}

func (e *Engine) applyFences(ctx context.Context, s *snapshot, v View) error {
	if e.Local == nil {
		return errors.New("local dispatch fence unavailable")
	}
	if v.ComputerState == "revoked" || v.ComputerState == "draining" || s.DisconnectAll {
		s.DisconnectAll = true
		if _, err := e.Local.Fence(ctx, s.View.DaemonID, ""); err != nil {
			return err
		}
	}
	for _, a := range v.Enrollments {
		if a.State == "revoked" || a.State == "draining" || s.Removed[a.AccountID] {
			s.Removed[a.AccountID] = true
			if _, err := e.Local.Fence(ctx, s.View.DaemonID, a.AccountID); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncFences is the daemon-side reconnect operation. It neither unloads its own
// service nor acknowledges cleanup. The separate helper owns those steps.
func (e *Engine) SyncFences(ctx context.Context) error {
	if err := e.Store.Lock(); err != nil {
		return err
	}
	s, err := e.load()
	if err != nil {
		return err
	}
	if s.View.ComputerID == "" {
		return errors.New("computer enrollment unavailable")
	}
	proof := e.proof(s)
	proof.Progress = &SetupProgress{State: "provisioning"}
	if e.Local != nil {
		if local, err := e.Local.Status(ctx, ""); err == nil && local.DaemonID == s.View.DaemonID && local.Ready {
			proof.Progress.State = "connected"
		}
	}
	v, err := e.API.Reconcile(ctx, proof)
	if err != nil {
		return err
	}
	if err = validateView(s, v, false); err != nil {
		return err
	}
	if err = e.applyFences(ctx, s, v); err != nil {
		return err
	}
	// Preserve current request's immutable response and runtime prefix when
	// the tombstone-only response intentionally omits credential material.
	prefix := s.View.RuntimePrefix
	s.View = v
	s.View.RuntimePrefix = prefix
	return e.save(s, false)
}

func (e *Engine) reconcile(ctx context.Context, s *snapshot) (Progress, error) {
	v, err := e.API.Reconcile(ctx, e.proof(s))
	if err != nil {
		p := e.progress(s)
		p.ServerRevocation = "unconfirmed"
		p.Action = "Restore connectivity, then resume the same setup or disconnect command."
		return p, err
	}
	if err = validateView(s, v, false); err != nil {
		return e.progress(s), err
	}
	prefix := s.View.RuntimePrefix
	s.View = v
	s.View.RuntimePrefix = prefix
	if err = e.applyFences(ctx, s, v); err != nil {
		return e.progress(s), err
	}
	if err = e.save(s, false); err != nil {
		return Progress{}, err
	}
	p := e.progress(s)
	if s.ComputerCleaned {
		proof := e.proof(s)
		proof.Cleaned = s.Cleaned
		proof.ComputerCleaned = true
		ack, err := e.API.Reconcile(ctx, proof)
		if err != nil {
			return p, err
		}
		if ack.Cleanup != "confirmed" {
			return p, errors.New("cleanup acknowledgement pending")
		}
		p.Stage = "disconnected"
		p.ServerRevocation = "confirmed"
		p.LocalProcesses = "drained"
		p.Action = "Disconnected; any retained accounting journal remains available for authorized recovery."
		return p, nil
	}
	if s.DisconnectAll || len(s.Removed) > 0 {
		p.Stage = "draining"
		p.ServerRevocation = "pending"
		if s.DisconnectAll && v.ComputerState == "revoked" {
			p.ServerRevocation = "confirmed"
		}
		if e.Local == nil {
			return p, errors.New("local processes unconfirmed")
		}
		cleaned := append([]string(nil), s.Cleaned...)
		known := map[string]bool{}
		for _, id := range cleaned {
			known[id] = true
		}
		for _, a := range v.Enrollments {
			if !(s.DisconnectAll || s.Removed[a.AccountID]) {
				continue
			}
			if a.State == "connected" {
				p.ServerRevocation = "unconfirmed"
				p.Action = "Local dispatch is frozen; run disconnect again to confirm server revocation."
				continue
			}
			local, err := e.Local.Status(ctx, a.AccountID)
			if err != nil || local.DaemonID != v.DaemonID || local.State != "drained" {
				p.LocalProcesses = "unconfirmed"
				if err == nil {
					p.LocalProcesses = local.State
				}
				continue
			}
			if a.State != "revoked" {
				p.LocalProcesses = "drained"
				p.Action = "Processes exited; server settlement and revocation are still pending."
				continue
			}
			if len(local.SettlementPending) > 0 {
				p.Action = "Local process exit confirmed; retained usage journal needs authorized server accounting recovery."
			}
			if err = e.removeAccount(a.AccountID); err != nil {
				return p, err
			}
			if !known[a.AccountID] {
				cleaned = append(cleaned, a.AccountID)
				known[a.AccountID] = true
			}
			p.ServerRevocation = "confirmed"
		}
		s.Cleaned = cleaned
		if s.DisconnectAll && v.ComputerState == "revoked" {
			local, err := e.Local.Status(ctx, "")
			if err == nil && local.DaemonID == v.DaemonID && local.State == "drained" {
				p.LocalProcesses = "drained"
				if e.Services == nil && s.Service != nil {
					return p, errors.New("service cleanup requires its owning manager")
				}
				if s.Service != nil {
					if err = e.Services.Remove(ctx, e.Store, s.Service, true); err != nil {
						return p, err
					}
				}
				key := []byte("aeon_" + prefix + "_" + string(s.Runtime))
				if err = e.Store.RemoveExact("runtime.key", Hash(key)); err != nil {
					return p, err
				}
				s.ComputerCleaned = true
				s.Runtime = ""
				s.Device = ""
				s.Request.ExistingProof = ""
				s.Phase = "disconnected"
			}
		}
		if err = e.save(s, false); err != nil {
			return p, err
		}
		proof := e.proof(s)
		proof.Cleaned = s.Cleaned
		proof.ComputerCleaned = s.ComputerCleaned
		ack, err := e.API.Reconcile(ctx, proof)
		if err != nil {
			p.Action = "Local cleanup recorded; server acknowledgement pending. Resume status when online."
			return p, err
		}
		if err = validateView(s, ack, false); err != nil {
			return p, err
		}
		if s.ComputerCleaned && ack.Cleanup == "confirmed" {
			p.Stage = "disconnected"
			p.LocalProcesses = "drained"
			p.ServerRevocation = "confirmed"
			p.Action = "Computer disconnected. Vendor sign-ins and project files are preserved."
		}
		allRemoved := len(s.Removed) > 0
		for id := range s.Removed {
			if !known[id] {
				allRemoved = false
			}
		}
		if !s.DisconnectAll && allRemoved {
			p.Stage = "connected"
			p.Action = "Selected enrollment removed; shared daemon and other accounts remain connected."
		}
		return p, nil
	}
	if e.Local != nil {
		local, err := e.Local.Status(ctx, "")
		if err == nil && local.DaemonID == v.DaemonID && local.Ready {
			p.LocalProcesses = local.State
		} else {
			p.Stage = "provisioning"
			p.Action = "Approved daemon connectivity remains unconfirmed."
		}
	}
	if p.Stage != "provisioning" {
		pending, failed := false, false
		for _, a := range v.Enrollments {
			if a.VerificationRunID == "" || a.State != "connected" {
				continue
			}
			switch a.VerificationState {
			case "completed":
			case "failed", "cancelled", "ownership_lost", "expired":
				failed = true
			default:
				pending = true
			}
		}
		switch {
		case failed:
			p.Stage = "verification_failed"
			p.Action = "Verification did not complete. No automatic retry or allowance refill was performed."
		case pending:
			p.Stage = "verification_pending"
			p.Action = "Approved verification is queued or running; setup will not create another run."
		default:
			p.Stage = "connected"
			p.Action = "Computer connected; ongoing limits remain separately controlled."
		}
	}
	return p, nil
}

func (e *Engine) removeAccount(id string) error {
	raw, err := e.Store.Read(RuntimeName, 128<<10)
	if err != nil {
		return err
	}
	var c RuntimeConfig
	if json.Unmarshal(raw, &c) != nil {
		return errors.New("private runtime configuration invalid")
	}
	kept := make([]RuntimeAccount, 0, len(c.Accounts))
	for _, a := range c.Accounts {
		if a.AccountID != id {
			kept = append(kept, a)
		}
	}
	c.Accounts = kept
	raw, _ = json.Marshal(c)
	return e.Store.Write(RuntimeName, raw, false)
}

func (e *Engine) AddHarness(ctx context.Context, candidates []Candidate) (Progress, error) {
	if err := e.Store.Lock(); err != nil {
		return Progress{}, err
	}
	s, err := e.load()
	if err != nil {
		return Progress{}, err
	}
	if s.Request.ExistingComputerID != "" && (s.Phase == "awaiting_approval" || s.Phase == "requesting" || s.Phase == "provisioning") {
		return e.Step(ctx)
	}
	if s.DisconnectAll || s.View.ComputerState != "connected" {
		return e.progress(s), errors.New("Add harness requires a connected computer")
	}
	if len(candidates) == 0 || len(candidates) > 4 {
		return e.progress(s), errors.New("select a signed-in harness account")
	}
	selected := map[string]bool{}
	for _, c := range candidates {
		if selected[c.Harness] || c.Login != "signed_in" || !safeLabel.MatchString(c.Label) || c.Managed {
			return e.progress(s), errors.New("invalid Add harness account choice")
		}
		selected[c.Harness] = true
		for _, a := range s.View.Enrollments {
			if a.State == "connected" && a.Harness == c.Harness && a.Label == c.Label {
				return e.progress(s), errors.New("this harness account is already connected; no new request was created")
			}
		}
	}
	id, err := uuid()
	if err != nil {
		return Progress{}, err
	}
	device, err := randomSecret()
	if err != nil {
		return Progress{}, err
	}
	s.Request.RequestID = id
	s.Request.ExistingComputerID = s.View.ComputerID
	s.Request.ExistingProof = s.Lifecycle
	s.Request.DeviceHash = Hash([]byte(device))
	s.Request.TenantID = s.View.TenantID
	s.Request.TenantSlug = ""
	s.Request.Accounts = nil
	s.Device = device
	for _, c := range candidates {
		key, err := uuid()
		if err != nil {
			return Progress{}, err
		}
		c.Key = key
		s.Request.Accounts = append(s.Request.Accounts, c)
		s.Candidates = append(s.Candidates, LocalCandidate{c, c.Path, c.Home, c.Identity, c.Version})
	}
	s.Response = DeviceResponse{}
	s.NextPoll = e.now()
	s.Phase = "requesting"
	if err = e.save(s, false); err != nil {
		return Progress{}, err
	}
	return e.Step(ctx)
}
