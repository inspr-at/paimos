// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/ownedprocess"
)

type deadlineProcess struct {
	*managedFake
	calls    int
	deadline time.Time
	wait     bool
}

func (p *deadlineProcess) Control(ctx context.Context, _, _ string) error {
	p.calls++
	p.deadline, _ = ctx.Deadline()
	if p.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	return ctx.Err()
}
func (p *deadlineProcess) GracefulStop(ctx context.Context) error {
	return p.Control(ctx, "stop", "")
}
func (p *deadlineProcess) ForceStop(ctx context.Context, _ ownedprocess.Identity, deadline time.Time) error {
	if d, ok := ctx.Deadline(); !ok || d != deadline {
		return errors.New("force signal lost the monotonic authorization deadline")
	}
	return p.Control(ctx, "force_stop", "")
}

func TestRemoteControlBudgetsIgnoreWallSkewAndIncludeTransit(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		for _, operation := range []string{"steer", "interrupt", "stop", "rename", "model", "effort", "force_stop"} {
			t.Run(fmt.Sprintf("%d/%s", year, operation), func(t *testing.T) {
				s, a, e, original := managedFixture(t)
				p := &deadlineProcess{managedFake: original}
				e.mu.Lock()
				e.process = p
				e.mu.Unlock()
				identity := p.identity
				identity.DaemonID, identity.Generation = s.daemonID, s.generation
				expires := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
				for _, tc := range []struct {
					name  string
					ttl   int64
					delay time.Duration
				}{
					{"live", 10000, 0},
					{"expired", 0, 0},
					{"negative", -1, 0},
					{"unbounded", 45001, 0},
					{"missing", 0, 0},
					{"slow_response", 10, 30 * time.Millisecond},
				} {
					t.Run(tc.name, func(t *testing.T) {
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
							time.Sleep(tc.delay)
							control := map[string]any{"id": tc.name, "kind": operation, "expires_at": expires, "expected_ownership": identity}
							if tc.name != "missing" {
								control["expires_in_ms"] = tc.ttl
							}
							_ = json.NewEncoder(w).Encode(map[string]any{"controls": []any{control}})
						}))
						defer server.Close()
						controls, err := NewRemote(server.URL, "fixture").YieldHarness(t.Context(), e.harness)
						if err != nil || len(controls) != 1 {
							t.Fatalf("yield: %v", err)
						}
						c := controls[0]
						req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: c.ID, Operation: c.Kind, ExpectedOwnership: c.ExpectedOwnership, ExpiresAt: c.ExpiresAt, deadline: c.deadline}
						if isSetting(operation) {
							req.Value = "high"
						}
						before := p.calls
						_, err = s.control(t.Context(), req, true)
						if tc.name == "live" {
							if err != nil {
								t.Fatal(err)
							}
							if operation != "rename" && (p.calls != before+1 || p.deadline != c.deadline || time.Until(p.deadline) > 10*time.Second) {
								t.Fatal("adapter did not receive the local monotonic budget")
							}
						} else {
							want := ErrControlExpired
							if operation == "force_stop" {
								want = ErrUnsupported
							}
							if !errors.Is(err, want) || p.calls != before {
								t.Fatalf("expired control reached child: calls=%d err=%v", p.calls-before, err)
							}
						}
					})
				}
			})
		}
	}
}

func TestSlowManagedControlCannotRestartBudgetAfterLock(t *testing.T) {
	s, a, e, p := managedFixture(t)
	identity := p.identity
	identity.DaemonID, identity.Generation = s.daemonID, s.generation
	// DB far ahead of this daemon; its expiry must not become a local deadline.
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(20 * time.Millisecond)
	req := ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "slow-lock", Operation: "steer", ExpectedOwnership: &identity, ExpiresAt: &expires, deadline: deadline}
	e.controlMu.Lock()
	result := make(chan error, 1)
	go func() { _, err := s.control(t.Context(), req, true); result <- err }()
	<-time.After(time.Until(deadline))
	e.controlMu.Unlock()
	if err := <-result; !errors.Is(err, ErrControlExpired) {
		t.Fatalf("lock wait extended authorization: %v", err)
	}
	if len(p.texts) != 0 {
		t.Fatal("expired steer reached child")
	}
}

func TestManagedSettingWaitUsesLocalBudget(t *testing.T) {
	for _, year := range []int{2001, 2099} {
		t.Run(fmt.Sprint(year), func(t *testing.T) {
			s, a, e, original := managedFixture(t)
			p := &deadlineProcess{managedFake: original, wait: true}
			e.mu.Lock()
			e.process = p
			e.mu.Unlock()
			identity := p.identity
			identity.DaemonID, identity.Generation = s.daemonID, s.generation
			expires := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
			deadline := time.Now().Add(50 * time.Millisecond)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := s.control(ctx, ControlRequest{TenantID: s.tenantID, PrincipalID: s.principalID, RunID: a.run.ID, Generation: s.generation, CorrelationID: "slow-setting", Operation: "model", Value: "fixture", ExpectedOwnership: &identity, ExpiresAt: &expires, deadline: deadline}, true)
			if !errors.Is(err, context.DeadlineExceeded) || p.calls != 1 || p.deadline != deadline || ctx.Err() != nil {
				t.Fatalf("setting used remote wall deadline: calls=%d err=%v", p.calls, err)
			}
		})
	}
}

func TestManagedQueueKeepsOriginalDeadline(t *testing.T) {
	s, a, e, original := managedFixture(t)
	p := &deadlineProcess{managedFake: original, wait: true}
	e.mu.Lock()
	e.process = p
	e.mu.Unlock()
	identity := p.identity
	identity.DaemonID, identity.Generation = s.daemonID, s.generation
	expires := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	deadline := time.Now().Add(50 * time.Millisecond)
	a.harnessControls = []HarnessControl{
		{ID: "slow-first", Kind: "model", Value: "fixture", ExpectedOwnership: &identity, ExpiresAt: &expires, ExpiresInMS: 50, deadline: deadline},
		{ID: "expired-second", Kind: "steer", Text: "never inject", ExpectedOwnership: &identity, ExpiresAt: &expires, ExpiresInMS: 50, deadline: deadline},
	}
	if err := s.serviceHarness(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || len(a.harnessCompletions) != 2 || a.harnessCompletions[1] != "expired-second:rejected:authorization_expired" {
		t.Fatalf("queue restarted TTL: calls=%d receipts=%v", p.calls, a.harnessCompletions)
	}
}

func TestClaudeControlRechecksDeadlineAtWriteLock(t *testing.T) {
	p := &claudeProcess{wireProcess: &wireProcess{readDone: make(chan struct{})}, controls: map[string]chan bool{}}
	writes := 0
	p.stdin = managedFixtureWriter(func(b []byte) (int, error) { writes++; return len(b), nil })
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	p.writeMu.Lock()
	result := make(chan error, 1)
	go func() { result <- p.Control(ctx, "steer", "expired fixture") }()
	<-ctx.Done()
	p.writeMu.Unlock()
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) || writes != 0 {
		t.Fatalf("expired adapter write: writes=%d err=%v", writes, err)
	}
}
