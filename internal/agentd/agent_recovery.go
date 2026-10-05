// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

const recoveryCapability = "session_recovery_v1"

type RecoveryRequest struct {
	ID          string                `json:"id"`
	SessionID   string                `json:"session_id"`
	ProjectID   string                `json:"project_id"`
	RunID       *string               `json:"run_id"`
	Action      string                `json:"action"`
	Ownership   ownedprocess.Identity `json:"expected_ownership"`
	ExpiresAt   time.Time             `json:"expires_at"`
	ExpiresInMS int64                 `json:"expires_in_ms"`
	deadline    time.Time
}

// A pre-action rejected receipt prevents a crash from authorizing a second
// signal. Only the outcome is updated; keys, leases and handovers stay out.
type RecoveryReport struct {
	ID      string `json:"id"`
	Outcome string `json:"outcome"`
}
type agentRecoveryAPI interface {
	ClaimAgentRecoveries(context.Context, string, string) ([]RecoveryRequest, error)
	CompleteAgentRecovery(context.Context, string, string, RecoveryReport) error
}

func (r *Remote) ClaimAgentRecoveries(ctx context.Context, daemon, generation string) ([]RecoveryRequest, error) {
	started := time.Now()
	var out []RecoveryRequest
	err := r.Client.Do(ctx, "POST", "/api/harness-recoveries/claim", map[string]string{"daemon_id": daemon, "generation": generation}, &out)
	var status *client.StatusError
	if errors.As(err, &status) && status.Status == http.StatusNotFound {
		return nil, nil
	} // older server
	if err != nil {
		return nil, err
	}
	if len(out) > 10 {
		return nil, ErrScope
	}
	for i := range out {
		out[i].deadline = started.Add(time.Duration(max(int64(0), min(out[i].ExpiresInMS, int64(45000)))) * time.Millisecond)
	}
	return out, nil
}
func (r *Remote) CompleteAgentRecovery(ctx context.Context, daemon, generation string, report RecoveryReport) error {
	return r.Client.Do(ctx, "POST", "/api/harness-recoveries/"+url.PathEscape(report.ID)+"/complete", map[string]string{"daemon_id": daemon, "generation": generation, "outcome": report.Outcome}, nil)
}

func (s *Supervisor) recoverAgents(ctx context.Context) error {
	api, ok := s.api.(agentRecoveryAPI)
	if !ok {
		return nil
	}
	s.recoveryMu.Lock()
	defer s.recoveryMu.Unlock()
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for _, entry := range s.runs {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	// Flush exact outcomes before fetching new commands. A failed report never
	// authorizes resending the stop or spawning a continuation locally.
	for _, entry := range entries {
		if err := s.flushRecoveryReports(ctx, api, entry); err != nil {
			return err
		}
	}
	if !s.dispatchAllowed("") {
		return nil
	}
	requests, err := api.ClaimAgentRecoveries(ctx, s.daemonID, s.generation)
	if err != nil {
		return err
	}
	for _, q := range requests {
		if q.RunID == nil {
			continue
		}
		s.mu.Lock()
		entry := s.runs[*q.RunID]
		s.mu.Unlock()
		if entry == nil {
			if err := api.CompleteAgentRecovery(ctx, s.daemonID, s.generation, RecoveryReport{ID: q.ID, Outcome: "rejected"}); err != nil {
				return err
			}
			continue
		}
		entry.mu.Lock()
		if len(entry.record.RecoveryReports) >= 10 {
			entry.mu.Unlock()
			return ErrControlUnconfirmed
		}
		entry.record.RecoveryReports = append(entry.record.RecoveryReports, RecoveryReport{ID: q.ID, Outcome: "rejected"})
		err = s.journal.Put(entry.record)
		entry.mu.Unlock()
		if err != nil {
			return err
		}
		outcome := s.applyAgentRecovery(ctx, entry, q)
		entry.mu.Lock()
		entry.record.RecoveryReports[len(entry.record.RecoveryReports)-1].Outcome = outcome
		err = s.journal.Put(entry.record)
		entry.mu.Unlock()
		if err != nil {
			return err
		}
		if err = s.flushRecoveryReports(ctx, api, entry); err != nil {
			return err
		}
	}
	return nil
}

func (s *Supervisor) flushRecoveryReports(ctx context.Context, api agentRecoveryAPI, entry *owned) error {
	for {
		entry.mu.Lock()
		if len(entry.record.RecoveryReports) == 0 || entry.record.Generation != s.generation {
			entry.mu.Unlock()
			return nil
		}
		report := entry.record.RecoveryReports[0]
		entry.mu.Unlock()
		err := api.CompleteAgentRecovery(ctx, s.daemonID, s.generation, report)
		var status *client.StatusError
		// A terminal rejection remains visible on the server. Never convert an
		// expired claim or revoked authority into a successful local restart.
		terminal := errors.As(err, &status) && (status.Status == 403 || status.Status == 404 || status.Status == 409 || status.Status == 410)
		if err != nil && !terminal {
			return err
		}
		entry.mu.Lock()
		entry.record.RecoveryReports = entry.record.RecoveryReports[1:]
		err = s.journal.Put(entry.record)
		entry.mu.Unlock()
		if err != nil {
			return err
		}
	}
}

func (s *Supervisor) applyAgentRecovery(ctx context.Context, entry *owned, q RecoveryRequest) string {
	if q.deadline.IsZero() || time.Until(q.deadline) <= 0 || q.RunID == nil || q.Ownership.DaemonID != s.daemonID || q.Ownership.Generation != s.generation {
		return "rejected"
	}
	ctx, cancel := context.WithDeadline(ctx, q.deadline)
	defer cancel()
	entry.mu.Lock()
	bound := entry.record.Generation == s.generation && entry.record.RunID == *q.RunID && entry.harness.ID == q.SessionID && entry.harness.ProjectID == q.ProjectID && !entry.harnessArchived
	exited := entry.record.ExitObserved
	lastOwnership := entry.harness.Ownership
	done := entry.monitorDone
	account := entry.record.AccountID
	entry.mu.Unlock()
	if !bound || !s.dispatchAllowed(account) {
		return "rejected"
	}
	if exited && (lastOwnership == nil || lastOwnership.DaemonID != q.Ownership.DaemonID || lastOwnership.Generation != q.Ownership.Generation || lastOwnership.ProcessID != q.Ownership.ProcessID || lastOwnership.RootPID != q.Ownership.RootPID || lastOwnership.GroupID != q.Ownership.GroupID || !lastOwnership.StartedAt.Equal(q.Ownership.StartedAt)) {
		return "rejected"
	}
	if q.Action == "restart" {
		if !exited {
			// The existing signal boundary independently rechecks PID identity,
			// daemon generation and the same monotonic authorization deadline.
			_, err := s.control(ctx, ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: *q.RunID, Generation: s.generation, CorrelationID: "recover:" + q.ID, Operation: "stop", ExpectedOwnership: &q.Ownership, ExpiresAt: &q.ExpiresAt, deadline: q.deadline}, true)
			if err != nil {
				return "rejected"
			}
		}
		if done == nil {
			return "rejected"
		}
		select {
		case <-done:
		case <-ctx.Done():
			return "rejected"
		}
		entry.mu.Lock()
		exited = entry.record.ExitObserved
		settled := len(entry.record.Pending) == 0 && !entry.record.SettlementGap
		entry.mu.Unlock()
		if !exited || !settled || ctx.Err() != nil {
			return "rejected"
		}
		return "exited"
	}
	if q.Action != "reconnect" || exited {
		return "rejected"
	}
	// Match the ordinary harness cycle's lock order. Probe reporting without
	// consuming controls or injecting inbox messages into the child.
	entry.harnessMu.Lock()
	defer entry.harnessMu.Unlock()
	entry.controlMu.Lock()
	defer entry.controlMu.Unlock()
	entry.mu.Lock()
	proc := entry.process
	active := entry.record.State == "running"
	session := entry.harness
	sequence := entry.heartbeatSeq + 1
	entry.mu.Unlock()
	recovery, ok := proc.(RecoveryProcess)
	if !ok || !active {
		return "rejected"
	}
	current, err := recovery.Ownership()
	expected := q.Ownership
	expected.DaemonID, expected.Generation = "", ""
	current.DaemonID, current.Generation = "", ""
	if err != nil || current != expected || ctx.Err() != nil {
		return "rejected"
	}
	// Refresh reporting through the exact lease already owned in memory. No
	// auth-home or CLI config is opened and no credential is manufactured.
	session.ActivitySequence = sequence
	session.Model, session.ReasoningEffort = "", ""
	current.DaemonID, current.Generation = s.daemonID, s.generation
	session.Ownership = &current
	// Use the transport directly: the normal heartbeat wrapper may deliver a
	// pending pause to the child, which is outside this reconnect request.
	if err = s.api.HeartbeatHarness(ctx, session, "working"); err != nil {
		return "rejected"
	}
	entry.mu.Lock()
	entry.heartbeatSeq = max(entry.heartbeatSeq, sequence)
	entry.mu.Unlock()
	if !entry.inboxCapable {
		return "rejected"
	}
	// Drain leases are replayed until settled. Leave any returned message for
	// the normal cycle rather than completing it or acting on it here.
	if _, err = s.api.DrainHarness(ctx, session); err != nil {
		return "rejected"
	}
	return "reconnected"
}
