// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
	"testing"
)

type priorityInboxAPI struct {
	*levelHintAPI
	loseCompletion bool
}

func (a *priorityInboxAPI) CompleteHarnessDelivery(ctx context.Context, s HarnessSession, d HarnessDelivery) error {
	if a.loseCompletion {
		a.loseCompletion = false
		return errors.New("fixture lost completion")
	}
	return a.levelHintAPI.CompleteHarnessDelivery(ctx, s, d)
}

// Risk: a busy owner ignores exact-session priority input, or a lost server
// completion injects it twice despite the durable pre-I/O local receipt.
func exercisePriorityInboxReplay(t *testing.T) {
	s, api, p := testSupervisor(t)
	e := &owned{record: Record{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: "run", Generation: s.generation, State: "running", PID: p.PID(), Controls: map[string]replay{}}, harness: HarnessSession{ID: "session", ProjectID: "project", Activity: "busy"}, process: p, inboxCapable: true, managedPolicy: true, replies: map[string]InboxReplyTarget{}, harnessWake: make(chan struct{}, 1)}
	s.runs["run"] = e
	a := &priorityInboxAPI{levelHintAPI: &levelHintAPI{hintAPI: &hintAPI{fakeAPI: api, queued: []HarnessDelivery{{ID: "priority", MessageID: "message", Priority: true, Level: "simple", Body: "a recorded decision"}}}}, loseCompletion: true}
	s.api = a
	if err := s.serviceHarnessCycle(t.Context(), e, false); !errors.Is(err, ErrControlUnconfirmed) {
		t.Fatal("lost completion was hidden", err)
	}
	if err := s.serviceHarnessCycle(t.Context(), e, false); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 1 || len(a.queued) != 0 {
		t.Fatalf("priority input replayed or lost: writes=%d pending=%d", calls, len(a.queued))
	}
}
