// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
)

// Status reads one atomically replaced snapshot and live, read-only daemon
// telemetry. It never locks, reconciles, fences, saves or acknowledges cleanup.
func (e *Engine) Status(ctx context.Context) (Progress, error) {
	s, err := e.loadSnapshot(true)
	if errors.Is(err, os.ErrNotExist) {
		return Progress{Schema: "aeon.agent-setup.v1", Stage: "provisioning", LocalProcesses: "unconfirmed", Action: "Setup in progress; the first complete snapshot is not available yet."}, nil
	}
	if err != nil {
		return Progress{}, err
	}
	if s.View.ComputerID == "" {
		return e.progress(s), nil
	}
	if s.DisconnectAll || s.Phase == "draining" || s.Phase == "server_unconfirmed" || s.ComputerCleaned {
		p := e.progress(s)
		p.Action = "Setup cleanup is in progress. Run disconnect to resume reconciliation."
		if s.ComputerCleaned {
			p.Stage = "disconnected"
			p.Action = "Local cleanup recorded; run disconnect to confirm server acknowledgement."
		}
		return p, nil
	}
	return e.connectionProgress(ctx, s), nil
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
	tenant := s.Response.TenantID
	if tenant == "" {
		tenant = s.Request.TenantID
	}
	return ProofRequest{TenantID: tenant, RequestID: s.LifecycleRequestID, LifecycleSecret: s.Lifecycle}
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
		if local, err := e.Local.Status(ctx, ""); err == nil && local.DaemonID == s.View.DaemonID {
			proof.Progress = observedProgress(s.View, local)
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
	p.AccountingState = v.AccountingState
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
	cleanupPending := s.DisconnectAll
	for _, a := range v.Enrollments {
		if s.Removed[a.AccountID] && (a.Cleanup != "confirmed" || a.State != "revoked") {
			cleanupPending = true
		}
	}
	if cleanupPending {
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
			p.Action = "Local cleanup recorded; server acknowledgement pending. Resume disconnect when online."
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
		acknowledged := map[string]bool{}
		for _, a := range ack.Enrollments {
			if a.Cleanup == "confirmed" && a.State == "revoked" {
				acknowledged[a.AccountID] = true
			}
		}
		for id := range s.Removed {
			if !acknowledged[id] {
				allRemoved = false
			}
		}
		p.Accounts = ack.Enrollments
		if !s.DisconnectAll && allRemoved {
			p.Stage = "connected"
			p.Action = "Selected enrollment removed; shared daemon and other accounts remain connected."
		}
		return p, nil
	}
	return e.connectionProgress(ctx, s), nil
}

func (e *Engine) connectionProgress(ctx context.Context, s *snapshot) Progress {
	v := s.View
	p := e.progress(s)
	p.AccountingState = v.AccountingState

	if e.Local != nil {
		local, err := e.Local.Status(ctx, "")
		if err == nil && local.DaemonID == v.DaemonID {
			accountReport := local.AccountStatuses != nil
			local = enrollmentReadiness(v, local)
			p.HarnessDetails, p.HarnessStatuses = local.HarnessDetails, local.HarnessStatuses
			p.BlockedAccounts = append([]BlockedAccount(nil), local.BlockedAccounts...)
			// A Claude hold keeps its own stage: repin is a Claude-only flow.
			if issue := local.HarnessErrors["claude"]; issue != "" && (!accountReport || local.HarnessDetails["claude"].Reason == "repin_pending") {
				if local.HarnessDetails["claude"].Reason == "repin_pending" {
					p.Stage = "repin_pending"
					p.LocalProcesses = local.State
					p.Action = "Waiting for repin; the daemon retries automatically after active runs finish."
					return p
				}
				p.Stage = "blocked"
				p.LocalProcesses = local.State
				p.Action = issue
				return p
			}
			if stage, action := readinessAction(v, local); action != "" {
				p.Stage, p.Action, p.LocalProcesses = stage, action, local.State
				return p
			}
			observed := observedProgress(v, local)
			if local.ProfilePermissions && !local.Ready {
				p.Stage = "blocked"
				p.Action = "The pi local profile must be a private directory. Review its permissions, then resume setup."
				return p
			}
			if local.HarnessFailed && !local.Ready {
				p.Stage = "blocked"
				p.Action = "An approved harness failed to start. Restore its pinned installation and interpreter, then resume setup."
				if len(local.BlockedAccounts) > 0 {
					p.Action = "No approved harness can start. Run the fix listed for each blocked account; the daemon resumes it on its next check."
				}
				return p
			}
			if observed.State == "login_required" {
				p.Stage = "login_required"
				p.Action = "An approved vendor account is no longer signed in with its approved identity. Use normal vendor login, then resume setup."
				return p
			}
			for _, a := range v.Enrollments {
				reason := local.VerificationReasons[a.VerificationRunID]
				if reason == "" {
					reason = a.VerificationReason
				}
				if a.State == "connected" && reason != "" && local.Ready {
					p.Stage, p.LocalProcesses = "verification_unavailable", local.State
					p.Action = verificationMessage(a.Harness, reason)
					return p
				}
			}
			if observed.State == "connected" && observed.ErrorCode == "verification_unavailable" {
				p.Stage = "verification_unavailable"
				p.LocalProcesses = local.State
				p.Action = "This installed harness cannot enforce safe verification, so its verification was not launched. The computer remains paired. Use a qualified harness version, or choose Connect only during a fresh authenticated pairing approval."
				return p
			}
		}
		if err == nil && local.DaemonID == v.DaemonID && local.Ready {
			p.LocalProcesses = local.State
		} else {
			p.Stage = "provisioning"
			p.Action = "Daemon connectivity is unconfirmed: no heartbeat from the approved daemon. Check that its service is running."
			if err == nil && local.DaemonID == v.DaemonID {
				p.Stage = "blocked"
				p.Action = "The daemon responded but supplied no account readiness reason. Update the helper, then resume setup."
			}
			return p
		}
	}
	if p.Stage != "provisioning" {
		pending, failed, unavailable := false, false, false
		for _, a := range v.Enrollments {
			if a.VerificationRunID == "" || a.State != "connected" {
				continue
			}
			switch a.VerificationState {
			case "completed":
			case "unavailable":
				unavailable = true
			case "failed", "cancelled", "ownership_lost", "expired":
				failed = true
			default:
				pending = true
			}
		}
		switch {
		case unavailable:
			p.Stage = "verification_unavailable"
			p.Action = "Computer remains paired; the selected verification mode is unavailable. No run or allowance was retried."
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
	return p
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
	addingClaude := false
	for _, c := range candidates {
		addingClaude = addingClaude || c.Harness == "claude"
	}
	// Renewing one existing account depends only on that account's pin,
	// launcher and interpreter. Another harness's saved Claude dependencies
	// must not block the repair. New enrollments, and Claude's own path,
	// still validate the shared pins.
	if !renewsExistingPin(s, candidates) {
		if err := e.checkSavedClaudeDependencies(s, e.ClaudeDependencies, addingClaude); err != nil {
			return Progress{Stage: "blocked", Action: err.Error()}, err
		}
	}
	if s.Request.ExistingComputerID != "" && (s.Phase == "awaiting_approval" || s.Phase == "requesting" || s.Phase == "provisioning") {
		return e.Step(ctx)
	}
	if s.DisconnectAll || s.View.ComputerState != "connected" {
		return e.progress(s), errors.New("Add harness requires a connected computer")
	}
	if len(candidates) == 0 || len(candidates) > 5 {
		return e.progress(s), errors.New("select a signed-in harness account")
	}
	selected := map[string]bool{}
	for _, c := range candidates {
		if selected[c.Harness] || c.Login != "signed_in" || !safeLabel.MatchString(c.Label) {
			return e.progress(s), errors.New("invalid Add harness account choice")
		}
		selected[c.Harness] = true
		if c.Harness != "grok" {
			if err := validateNode(c.Path, s.Request.Workspace, c.Interpreter()); err != nil {
				return Progress{Stage: "blocked", Action: err.Error()}, err
			}
		}
		for _, a := range s.View.Enrollments {
			if a.State == "connected" && a.Harness == c.Harness && a.Label == c.Label {
				if len(candidates) == 1 && !s.Removed[a.AccountID] {
					return e.renewPin(s, a, c)
				}
				return e.progress(s), errors.New("this harness account is already connected; no new request was created")
			}
		}
	}
	if addingClaude && s.NodePath == "" {
		deps, err := (Discovery{}).ResolveClaudeDependencies(e.ClaudeDependencies, s.Request.Workspace)
		if err != nil {
			return Progress{Stage: "blocked", Action: err.Error()}, err
		}
		s.NodePath, s.ClaudeSDKPath = deps.NodePath, deps.SDKPath
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

// renewsExistingPin is the single-account repair of a connected non-Claude
// enrollment. It matches the signed-in candidate to that enrollment by
// harness and label, and it does not cover a new enrollment.
func renewsExistingPin(s *snapshot, candidates []Candidate) bool {
	if s == nil || len(candidates) != 1 {
		return false
	}
	c := candidates[0]
	if c.Harness == "claude" || c.Harness == "grok" || c.Login != "signed_in" {
		return false
	}
	for _, a := range s.View.Enrollments {
		if a.State == "connected" && !s.Removed[a.AccountID] && a.Harness == c.Harness && a.Label == c.Label {
			return true
		}
	}
	return false
}

// renewPin is the add_harness repair for a connected account whose own
// interpreter pin is blocked: it swaps only that Node pin for the one just
// discovered. No request, approval, credential, identity or launcher changes,
// and a healthy pin is never replaced. The daemon lifts the block on its next
// runtime check. Claude pins are shared and change only through repin.
func (e *Engine) renewPin(s *snapshot, enrolled Enrollment, c Candidate) (Progress, error) {
	exists := errors.New("this harness account is already connected; no new request was created")
	if c.Harness == "claude" || c.Harness == "grok" {
		return e.progress(s), exists
	}
	raw, err := e.Store.Read(RuntimeName, 128<<10)
	var config RuntimeConfig
	if err != nil || json.Unmarshal(raw, &config) != nil || config.Schema != "aeon.agent-runtime.v1" || config.Origin != s.Origin || config.Workspace != s.Request.Workspace || config.TenantID != s.View.TenantID || config.PrincipalID != s.BoundPrincipal || config.ComputerID != s.BoundComputer || config.DaemonID != s.BoundDaemon {
		return e.progress(s), errors.New("pin renewal runtime ownership does not match this pairing")
	}
	index := -1
	for i, a := range config.Accounts {
		if a.AccountID == enrolled.AccountID && a.Key == enrolled.AccountKey && a.Harness == c.Harness {
			index = i
		}
	}
	candidate := -1
	for i, local := range s.Candidates {
		if local.Candidate.Key == enrolled.AccountKey && local.Candidate.Harness == c.Harness {
			candidate = i
		}
	}
	if index < 0 || candidate < 0 || !pinBlocked(config, enrolled.AccountID) {
		return e.progress(s), exists
	}
	current := config.Accounts[index]
	if current.Path != c.Path || current.Home != c.Home || current.Identity != c.Identity {
		return e.progress(s), errors.New("the signed-in account or executable differs from the enrolled one; remove this enrollment, then add the harness again")
	}
	current.Node, current.PiNode = c.Node, c.PiNode
	config.Accounts[index] = current
	if pinBlocked(config, enrolled.AccountID) {
		return Progress{Stage: "blocked", Action: "the discovered interpreter is still unusable; pass --node-path to an installed Node executable outside the workspace"}, errors.New("interpreter pin still blocked")
	}
	id, err := uuid()
	if err != nil {
		return Progress{}, err
	}
	old := s.Candidates[candidate].Candidate.Interpreter()
	event, _ := json.Marshal(struct {
		Kind      string             `json:"kind"`
		ID        string             `json:"id"`
		At        time.Time          `json:"at"`
		AccountID string             `json:"account_id"`
		Harness   string             `json:"harness"`
		Old       harnesslaunch.Node `json:"old"`
		New       harnesslaunch.Node `json:"new"`
	}{"interpreter_pin_renewed", id, e.now(), enrolled.AccountID, c.Harness, old, c.Interpreter()})
	if err := e.Store.Write("pin-renewal-"+id+".json", event, true); err != nil {
		return Progress{}, err
	}
	s.Candidates[candidate].Candidate.Node, s.Candidates[candidate].Candidate.PiNode = c.Node, c.PiNode
	if err := e.save(s, false); err != nil {
		return Progress{}, err
	}
	raw, _ = json.Marshal(config)
	if err := e.Store.Write(RuntimeName, raw, false); err != nil {
		return Progress{}, errors.New("pin renewal recorded but runtime update incomplete; rerun add-harness")
	}
	p := e.progress(s)
	p.Action = "Interpreter pin renewed; the daemon resumes this account on its next check. No approval, sign-in or credential changed."
	return p, nil
}

func pinBlocked(c RuntimeConfig, accountID string) bool {
	for _, block := range AccountPinBlocks(c) {
		if block.AccountID == accountID {
			return true
		}
	}
	return false
}

func observedProgress(v View, local LocalStatus) *SetupProgress {
	local = enrollmentReadiness(v, local)
	p := &SetupProgress{State: "provisioning", HarnessStatuses: local.HarnessStatuses, HarnessDetails: local.HarnessDetails}
	// A runtime hold or pin block is not an installation failure. Setup stays
	// complete even when every harness is held; per-harness details carry the
	// reason and fix. Harnesses that are only starting prove nothing yet.
	held := local.HarnessFailed || local.ProfilePermissions || len(local.BlockedAccounts) > 0 || len(local.HarnessErrors) > 0
	for _, detail := range local.HarnessDetails {
		held = held || detail.State == "blocked"
	}
	if local.LoginRequired && !local.Ready && !held {
		p.State = "login_required"
		p.ErrorCode = "login_required"
		return p
	}
	if local.Ready || held {
		p.State = "connected"
	}
	for _, a := range v.Enrollments {
		if a.State != "connected" || a.VerificationRunID == "" || (a.VerificationState != "queued" && a.VerificationState != "unavailable") {
			continue
		}
		if a.VerificationState == "unavailable" {
			p.ErrorCode = "verification_unavailable"
			return p
		}
		for _, id := range local.VerificationUnavailable {
			if a.AccountID == id {
				p.ErrorCode = "verification_unavailable"
				return p
			}
		}
	}
	return p
}
