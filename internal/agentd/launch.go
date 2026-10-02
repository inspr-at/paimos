// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"github.com/inspr-at/paimos/internal/laneprotocol"
)

const (
	launchPrepared  = "not_attempted"
	launchAttempted = "attempted"
	launchRefused   = "verification_unavailable"
)

var errClaimUnconfirmed = errors.New("claim settlement unconfirmed; no local launch attempted")

func noLocalProcess(r Record) bool {
	return r.ExitObserved || r.LaunchState == launchPrepared || r.LaunchState == launchRefused
}

func validateLaunchRecord(r Record) error {
	switch r.LaunchState {
	case "", launchAttempted:
		return nil
	case launchRefused:
		if r.State == "verification_unavailable" && r.ExecutionMode == VerificationPurpose && r.PID == 0 && r.ClaimRoute == nil && r.Sequence == 0 && len(r.Pending) == 0 {
			return nil
		}
	case launchPrepared:
		if r.ClaimRoute != nil && r.ClaimRoute.AccountID == r.AccountID && len(r.ClaimRoute.Reservations) > 0 && r.PID == 0 && !r.ExitObserved && (r.State == "claim_pending" || r.State == "failed" || r.State == "cancelled" || r.State == "completed") {
			return nil
		}
	}
	return errors.New("invalid durable launch evidence")
}

func (s *Supervisor) refuseVerification(run Run, reason string) error {
	r := Record{VerificationReason: reason, LaunchState: launchRefused, AccountID: run.requestedAccount(), ExecutionMode: run.Purpose,
		TenantID: s.tenantID, PrincipalID: s.principalID, RunID: run.ID, WorkOrderID: run.WorkOrderID,
		Generation: s.generation, State: "verification_unavailable"}
	if err := s.journal.Put(r); err != nil {
		return err
	}
	s.mu.Lock()
	s.runs[run.ID] = &owned{record: r}
	s.mu.Unlock()
	return nil
}

// finishUnlaunched is only called with proof that adapter.Start was not called.
// Persist terminal telemetry before sending it, retaining exactly that report
// through response loss. Never manufacture another run, reservation or turn.
func (s *Supervisor) finishUnlaunched(ctx context.Context, entry *owned) error {
	entry.mu.Lock()
	if entry.record.LaunchState != launchPrepared || entry.process != nil {
		entry.mu.Unlock()
		return ErrNotOwned
	}
	if entry.record.Sequence > 0 {
		err := s.flushReports(ctx, entry)
		entry.mu.Unlock()
		return err
	}
	entry.mu.Unlock()
	t := Telemetry{Kind: "finished", Status: "failed", ErrorCode: "child_exit_failed"}
	if entry.record.LaneEnvelopeID != "" {
		t.LaneSettlement = &laneprotocol.Settlement{ExitConfirmed: true}
	}
	return s.update(ctx, entry, t)
}

// reconcileUnlaunched checks actual server state; a Claim error alone says
// nothing about whether its transaction committed. Caller holds dispatchMu.
func (s *Supervisor) reconcileUnlaunched(ctx context.Context, entry *owned) error {
	entry.mu.Lock()
	record := entry.record
	entry.mu.Unlock()
	if record.LaunchState != launchPrepared || record.State != "claim_pending" {
		return nil
	}
	run, err := s.api.GetRun(ctx, record.RunID)
	if err != nil {
		return errClaimUnconfirmed
	}
	if run.ID != record.RunID || run.WorkOrderID != record.WorkOrderID || run.AgentPrincipalID != record.PrincipalID {
		return ErrScope
	}
	if run.Status == "queued" && run.AccountID == "" && record.ExecutionMode == "managed" && record.Generation == s.generation {
		// A server-observed release invalidates the route, never launch evidence.
		// Keep the prior binding until a new validated route is durably stored.
		entry.mu.Lock()
		defer entry.mu.Unlock()
		next := entry.record
		next.RouteReleased = true
		if err := s.journal.Put(next); err != nil {
			return err
		}
		entry.record = next
		return nil
	}
	if run.AccountID != record.AccountID {
		return ErrScope
	}
	switch run.Status {
	case "queued":
		// Same-process retries reuse the stored exact reservation set. An old
		// generation is never silently replaced across a restart: an earlier
		// HTTP request may still commit. Server expiry can cancel unclaimed
		// verification; until then its accounting remains explicitly pending.
		if record.Generation != s.generation {
			return errClaimUnconfirmed
		}
		return nil
	case "starting":
		// Report using the original claim generation, before any new probe.
		// The server verifies ownership; failure leaves the outbox pending.
		return s.finishUnlaunched(ctx, entry)
	case "completed", "failed", "cancelled":
		entry.mu.Lock()
		defer entry.mu.Unlock()
		next := entry.record
		next.State = run.Status
		if err := s.journal.Put(next); err != nil {
			return err
		}
		entry.record = next
		return nil
	default:
		return errClaimUnconfirmed
	}
}

func (s *Supervisor) recoverUnlaunched(ctx context.Context) error {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	var failures []error
	for _, entry := range entries {
		if err := s.reconcileUnlaunched(ctx, entry); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *Supervisor) hasUnresolvedOldClaim(account string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.runs {
		entry.mu.Lock()
		r := entry.record
		pending := r.AccountID == account && r.Generation != s.generation && r.LaunchState == launchPrepared && (r.State == "claim_pending" || len(r.Pending) > 0 || r.SettlementGap)
		entry.mu.Unlock()
		if pending {
			return true
		}
	}
	return false
}

// A durable refusal is retried on each queue receipt, including after restart.
// The server performs an idempotent terminal transition, never a launch claim.
func (s *Supervisor) reportVerificationRefusal(ctx context.Context, run Run, reason string) error {
	if reason == "" {
		reason = "adapter_unsupported"
	} // older journal
	reporter, ok := s.api.(interface {
		RefuseVerification(context.Context, string, string, string, string) error
	})
	if !ok {
		return ErrVerificationUnavailable
	}
	return reporter.RefuseVerification(ctx, run.ID, s.daemonID, s.generation, reason)
}
