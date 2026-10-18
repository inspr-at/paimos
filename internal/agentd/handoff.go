// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/workorders"
)

// WorkerPickup is content-free and persisted before the network claim. The
// worktree digest binds to this daemon host, never to a caller's path on server.
type WorkerPickup struct {
	TicketRevision    time.Time `json:"ticket_revision"`
	WorkOrderRevision int64     `json:"work_order_revision"`
	BriefSHA256       string    `json:"brief_sha256"`
	WorktreeID        string    `json:"worktree_id"`
}

func (s *Supervisor) workerPickup(run Run, node Node, order WorkOrder) (*WorkerPickup, error) {
	var trace struct {
		Assignment *struct {
			RunID string `json:"run_id"`
			WorkerPickup
		} `json:"worker_assignment"`
	}
	if len(run.Trace) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(run.Trace, &trace); err != nil {
		return nil, errors.New("invalid assignment trace")
	}
	if trace.Assignment == nil {
		return nil, nil
	}
	h := trace.Assignment
	if h.RunID != run.ID || h.TicketRevision.IsZero() || h.WorkOrderRevision < 1 || order.Revision != h.WorkOrderRevision {
		return nil, errors.New("assignment order revision changed")
	}
	criteria := make([]string, 0, len(order.Criteria))
	for _, c := range order.Criteria {
		criteria = append(criteria, c.Description)
	}
	brief, err := workorders.BriefDigest(node.Title, node.Body, criteria)
	if err != nil {
		return nil, err
	}
	if brief != h.BriefSHA256 {
		return nil, errors.New("assignment brief changed")
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		return nil, errors.New("checkout host identity unavailable")
	}
	identity, _ := json.Marshal([]string{host, s.workspace})
	sum := sha256.Sum256(append([]byte("aeon.worker.worktree.v1\x00"), identity...))
	return &WorkerPickup{TicketRevision: h.TicketRevision, WorkOrderRevision: h.WorkOrderRevision, BriefSHA256: brief, WorktreeID: fmt.Sprintf("%x", sum)}, nil
}

func (s *Supervisor) claimWorker(ctx context.Context, run Run, ids []string, pickup *WorkerPickup) error {
	if pickup == nil {
		return s.api.Claim(ctx, run.ID, s.daemonID, s.generation, ids)
	}
	api, ok := s.api.(interface {
		ClaimHandoff(context.Context, string, string, string, []string, WorkerPickup) error
	})
	if !ok {
		return errors.New("lead assignment pickup unavailable")
	}
	return api.ClaimHandoff(ctx, run.ID, s.daemonID, s.generation, ids, *pickup)
}

// A checkout fence is independent of tenant, daemon and state-root selection.
// The owner record survives a crash: releasing the OS lock is not exit proof.
// Only the same local journal may recover it; Close clears it after confirmed
// exit (or durable proof that no launch was attempted), never on an error path.
// Caller holds dispatchMu throughout acquisition, claim and possible launch.
func (s *Supervisor) acquireCheckout(runID string) error {
	s.mu.Lock()
	entries := make([]*owned, 0, len(s.runs))
	for id, entry := range s.runs {
		if id != runID {
			entries = append(entries, entry)
		}
	}
	s.mu.Unlock()
	for _, entry := range entries {
		entry.mu.Lock()
		unresolved := entry.record.ExecutionMode != VerificationPurpose && !noLocalProcess(entry.record)
		entry.mu.Unlock()
		if unresolved {
			return fmt.Errorf("checkout already has an unconfirmed writer: %w", ErrProcessesUnconfirmed)
		}
	}
	if s.checkout != nil {
		return nil
	}
	store, err := agentsetup.OpenStore(filepath.Join(s.workspace, ".aeon-agentd"), true)
	if err != nil {
		return fmt.Errorf("open checkout fence: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = store.Close()
		}
	}()
	if err := store.Lock(); err != nil {
		return fmt.Errorf("checkout ownership fence: %w", err)
	}
	owner, err := json.Marshal([]string{s.tenantID, s.principalID, s.daemonID, s.state.Path()})
	if err != nil {
		return err
	}
	if len(owner) > 4096 {
		return errors.New("checkout owner exceeds local bound")
	}
	previous, err := store.Read("owner.json", 4096)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read checkout owner: %w", err)
	}
	if err == nil && string(previous) != "[]" && string(previous) != string(owner) {
		return fmt.Errorf("checkout prior writer exit is unconfirmed: %w", ErrProcessesUnconfirmed)
	}
	if err := store.Write("owner.json", owner, false); err != nil {
		return fmt.Errorf("persist checkout owner: %w", err)
	}
	s.checkout = store
	keep = true
	return nil
}

func (s *Supervisor) releaseCheckout() error {
	// A restarted supervisor may have only read its confirmed-exit journal.
	// Recover its own fence for clean shutdown; never clear another owner's.
	if s.checkout == nil {
		store, err := agentsetup.OpenStore(filepath.Join(s.workspace, ".aeon-agentd"), false)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := store.Lock(); err != nil {
			_ = store.Close()
			if errors.Is(err, agentsetup.ErrBusy) {
				return nil
			}
			return err
		}
		owner, err := store.Read("owner.json", 4096)
		expected, _ := json.Marshal([]string{s.tenantID, s.principalID, s.daemonID, s.state.Path()})
		if errors.Is(err, os.ErrNotExist) || err == nil && string(owner) != string(expected) {
			return store.Close()
		}
		if err != nil {
			_ = store.Close()
			return err
		}
		s.checkout = store
	}
	// Called by Close only after the lifecycle and monitor exit gates pass.
	if err := s.checkout.Write("owner.json", []byte("[]"), false); err != nil {
		return err
	}
	if err := s.checkout.Close(); err != nil {
		return err
	}
	s.checkout = nil
	return nil
}
