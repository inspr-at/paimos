// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	identity, _ := json.Marshal([]string{s.tenantID, s.daemonID, s.workspace})
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
