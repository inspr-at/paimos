// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"errors"
	"time"
)

type startRunPhase uint8

const (
	startRunClaimUnconfirmed startRunPhase = iota
	startRunClaimed
	startRunLaunching
	startRunMonitored
)

// startRunCleanup is the sole owner of startup cleanup while dispatchMu is held.
// Claim uncertainty requires server reconciliation; crossing the possible-fork
// boundary forbids a no-launch settlement. The monitor takes all live resources
// together, even when its initial journal, report or heartbeat returns an error.
// Checkout, routine leases and ledger fences retain their existing durable owner.
type startRunCleanup struct {
	claimed      *claimedRun
	phase        startRunPhase
	registered   bool
	cancelRun    context.CancelFunc
	releasePower func()
}

func (s *Supervisor) finishStartRun(ctx context.Context, cleanup *startRunCleanup, resultErr error) error {
	if cleanup.claimed == nil || cleanup.phase == startRunMonitored {
		return resultErr
	}
	claimed := cleanup.claimed
	entry := claimed.entry
	if cleanup.phase == startRunClaimUnconfirmed {
		return errors.Join(resultErr, s.reconcileUnlaunched(ctx, entry))
	}
	if cleanup.releasePower != nil {
		cleanup.releasePower()
	}
	if cleanup.cancelRun != nil {
		cleanup.cancelRun()
	}
	entry.chat.close()
	if cleanup.phase == startRunLaunching {
		run, profile := claimed.run, claimed.admitted.profile
		s.verificationDiagnosticFor(run.ID, run.AccountID, run.Purpose, "ownership_lost", "start_unconfirmed")
		if errors.Is(resultErr, errModelInvalid) {
			s.reportModelState(entry, "invalid", profile.Model, profile.Effort)
		}
	}
	_ = entry.tools.Close()
	if cleanup.phase == startRunLaunching {
		// A generic Start error cannot prove that no child was forked. Keep
		// the disputed claim and account fence; never emit terminal telemetry.
		entry.mu.Lock()
		entry.record.State = "ownership_lost"
		_ = s.journal.Put(entry.record)
		entry.mu.Unlock()
		s.freezeOnError(claimed.route.AccountID)
	}
	if cleanup.registered {
		s.finishSessionUsage(entry)
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.stopHarness(closeCtx, entry, "process_failed")
		cancel()
	}
	entry.mu.Lock()
	neverLaunched := entry.record.LaunchState == launchPrepared
	entry.mu.Unlock()
	if resultErr != nil && cleanup.phase == startRunClaimed && neverLaunched {
		// Persist once before delivery; response loss leaves the same report
		// in the durable outbox for the existing reconciliation path.
		settleCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resultErr = errors.Join(resultErr, s.finishUnlaunched(settleCtx, entry))
		cancel()
	}
	return resultErr
}
