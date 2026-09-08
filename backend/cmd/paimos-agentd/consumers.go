// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type nativeConsumers struct {
	reporter    *cliReporter
	controller  *agentd.Supervisor
	supervisor  *runtimeconsumer.Supervisor
	mu          sync.Mutex
	reconcileMu sync.Mutex
	evidence    []runtimeconsumer.Evidence
	bindings    map[string]agentd.Session
	targets     map[string]string
}

func newNativeConsumers(root, instance string, r *cliReporter) (*nativeConsumers, error) {
	dir, err := agentd.InstanceStateDir(root, instance)
	if err != nil {
		return nil, err
	}
	c := &nativeConsumers{reporter: r, bindings: map[string]agentd.Session{}, targets: map[string]string{}}
	c.supervisor, err = runtimeconsumer.New(dir, c)
	if err != nil {
		return nil, errors.New("private consumer recovery journal unavailable")
	}
	return c, nil
}
func (c *nativeConsumers) Run(ctx context.Context) {
	defer c.supervisor.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		c.reconcile(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (c *nativeConsumers) reconcile(ctx context.Context) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	status := c.controller.Status()
	var bindings []runtimeconsumer.Binding
	var evidence []runtimeconsumer.Evidence
	for _, session := range status.Sessions {
		if ctx.Err() != nil {
			break
		}
		if terminalAgentdState(session.State) || session.State != agentd.StateRunning || session.Reporter.PublicSessionID == "" || session.Reporter.Closed {
			continue
		}
		// Registration owns the immutable target. No target reference is discovered
		// from host/PID hints, and no old process or external thread is adopted.
		bound := runtimeconsumer.Binding{Instance: status.Instance, Machine: c.reporter.host, Generation: status.DaemonID, Session: session.ID, Address: session.Identity, Project: session.ProjectID, Kind: "primary", Revision: session.Reporter.PublicSessionID}
		bindings = append(bindings, bound)
		c.mu.Lock()
		c.bindings[bound.Key()] = session
		c.mu.Unlock()
	}
	runConsumerTasks(len(bindings), func(i int) {
		stepCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		_ = c.supervisor.Step(stepCtx, bindings[i])
	})
	c.supervisor.Retain(bindings)
	keep := map[string]bool{}
	for _, b := range bindings {
		keep[b.Key()] = true
	}
	c.mu.Lock()
	for k := range c.bindings {
		if !keep[k] {
			delete(c.bindings, k)
			delete(c.targets, k)
		}
	}
	c.mu.Unlock()
	if len(bindings) == 0 {
		evidence = append(evidence, runtimeconsumer.Evidence{Kind: "primary", State: "unavailable", Reason: "no_owned_registered_generation", Generation: status.DaemonID})
	}
	for _, kind := range []string{"fallback", "attention"} {
		evidence = append(evidence, runtimeconsumer.Evidence{Kind: kind, State: "unavailable", Reason: "authority_unavailable", Generation: status.DaemonID})
	}
	c.mu.Lock()
	c.evidence = evidence
	c.mu.Unlock()
}
func (c *nativeConsumers) Snapshot() []runtimeconsumer.Evidence {
	c.mu.Lock()
	out := append([]runtimeconsumer.Evidence{}, c.evidence...)
	c.mu.Unlock()
	return append(out, c.supervisor.Snapshot()...)
}
func (c *nativeConsumers) local(b runtimeconsumer.Binding) (agentd.Session, error) {
	status := c.controller.Status()
	if status.Instance != b.Instance || status.DaemonID != b.Generation || c.reporter.host != b.Machine {
		return agentd.Session{}, runtimeconsumer.ErrOwnership
	}
	for _, s := range status.Sessions {
		if s.ID == b.Session && s.Reporter.PublicSessionID == b.Revision && !s.Reporter.Closed && s.ProjectID == b.Project && s.Identity == b.Address && s.State == agentd.StateRunning && s.PID > 0 {
			return s, nil
		}
	}
	return agentd.Session{}, runtimeconsumer.ErrOwnership
}
func (c *nativeConsumers) command(ctx context.Context, b runtimeconsumer.Binding, operation string, extra ...string) ([]byte, error) {
	session, err := c.local(b)
	if err != nil {
		return nil, err
	}
	_, agent, err := reporterIdentity(session)
	if err != nil {
		return nil, runtimeconsumer.ErrAuthority
	}
	lease, err := c.reporter.leases.GetOrCreate(session.ID)
	if err != nil {
		return nil, runtimeconsumer.ErrAuthority
	}
	args := []string{"--json", "harness", operation, "--project", strconv.FormatInt(b.Project, 10), "--session", session.Reporter.PublicSessionID}
	var stdin io.Reader
	if operation != "status" {
		args = append(args, "--agent", agent, "--worker-lease-file", "-")
		stdin = strings.NewReader(lease)
	}
	args = append(args, extra...)
	callCtx, cancel := context.WithTimeout(ctx, reporterSessionTimeout)
	defer cancel()
	raw, err := c.reporter.run(callCtx, c.reporter.paimosPath, args, c.reporter.environment, stdin)
	if err != nil {
		return nil, errors.New("consumer authenticated request unavailable")
	}
	if len(raw) > reporterOutputLimit {
		return nil, runtimeconsumer.ErrAuthority
	}
	return raw, nil
}
func (c *nativeConsumers) Verify(ctx context.Context, b runtimeconsumer.Binding) error {
	if _, err := c.local(b); err != nil {
		return err
	}
	raw, err := c.command(ctx, b, "status")
	if err != nil {
		return err
	}
	var remote harnessSessionResponse
	if json.Unmarshal(raw, &remote) != nil || remote.ID != b.Revision || remote.ProjectID != b.Project || remote.Host != b.Machine || remote.Harness+":"+remote.AgentName != b.Address || remote.Phase == "stopped" || remote.ManagementMode != "managed" || !remote.Capabilities.Inbox || uuid.Validate(remote.MessageTargetID) != nil {
		return runtimeconsumer.ErrAuthority
	}
	// The public session is immutable-bound to this target. Both drain and
	// complete verify its private worker lease and TargetID on the server.
	c.mu.Lock()
	defer c.mu.Unlock()
	if previous := c.targets[b.Key()]; previous != "" && previous != remote.MessageTargetID {
		return runtimeconsumer.ErrOwnership
	}
	c.targets[b.Key()] = remote.MessageTargetID
	session := c.bindings[b.Key()]
	if session.Reporter.PublicSessionID != b.Revision {
		return runtimeconsumer.ErrOwnership
	}
	return nil
}
func (c *nativeConsumers) Poll(ctx context.Context, b runtimeconsumer.Binding) (*runtimeconsumer.Work, error) {
	// Inventory contains only redacted metadata. Detect a conflicting legacy
	// target without scanning command lines or signalling an unowned process.
	query := url.Values{"address": []string{b.Address}}
	route := fmt.Sprintf("/api/projects/%d/message-targets?%s", b.Project, query.Encode())
	inventoryCtx, cancel := context.WithTimeout(ctx, reporterSessionTimeout)
	inventory, inventoryErr := c.reporter.run(inventoryCtx, c.reporter.paimosPath, []string{"--json", "curl", route}, c.reporter.environment, nil)
	cancel()
	var inventoryResponse struct {
		Targets []agentmessage.Target `json:"targets"`
	}
	if inventoryErr != nil || len(inventory) > reporterOutputLimit || json.Unmarshal(inventory, &inventoryResponse) != nil {
		return nil, runtimeconsumer.ErrAuthority
	}
	c.mu.Lock()
	targetID := c.targets[b.Key()]
	c.mu.Unlock()
	own := false
	for _, target := range inventoryResponse.Targets {
		if target.Instance != b.Instance || target.ProjectID != b.Project || target.Address != b.Address {
			return nil, runtimeconsumer.ErrAuthority
		}
		if target.Enabled && target.Role == "primary" {
			if target.ID == targetID && target.Adapter == agentmessage.AdapterManagedHarness {
				own = true
				continue
			}
			if target.Adapter != agentmessage.AdapterManagedHarness {
				return nil, runtimeconsumer.ErrLegacy
			}
			return nil, runtimeconsumer.ErrOwnership
		}
	}
	if !own {
		return nil, runtimeconsumer.ErrOwnership
	}
	raw, err := c.command(ctx, b, "drain")
	if err != nil {
		return nil, err
	}
	var page agentmessage.InboxPage
	if json.Unmarshal(raw, &page) != nil || page.Address != b.Address || len(page.Messages) > 100 {
		return nil, runtimeconsumer.ErrAuthority
	}
	var work *runtimeconsumer.Work
	for _, message := range page.Messages {
		delivery := message.DeliveryWork
		if delivery == nil {
			return nil, runtimeconsumer.ErrAuthority
		}
		if delivery.State != "leased" || delivery.Adapter != agentmessage.AdapterManagedHarness {
			continue
		}
		if delivery.TargetRef != "" || delivery.Instance != b.Instance || delivery.ProjectID != b.Project || message.To != b.Address || uuid.Validate(delivery.DeliveryID) != nil || work != nil {
			return nil, runtimeconsumer.ErrAuthority
		}
		work = &runtimeconsumer.Work{ID: delivery.DeliveryID, Cursor: message.Cursor, Payload: message, Revision: targetID}
	}
	if work == nil && len(page.Messages) > 0 {
		return nil, runtimeconsumer.ErrConflict
	}
	return work, nil
}
func (c *nativeConsumers) Prepare(_ context.Context, b runtimeconsumer.Binding, w runtimeconsumer.Work) error {
	session, err := c.local(b)
	if err != nil {
		return err
	}
	m, ok := w.Payload.(agentmessage.Envelope)
	if !ok || m.DeliveryWork == nil || m.DeliveryWork.DeliveryID != w.ID || m.Cursor != w.Cursor {
		return runtimeconsumer.ErrAuthority
	}
	if m.DeliveryWork.RequestedLevel != "simple" && m.DeliveryWork.RequestedLevel != "steer" {
		return runtimeconsumer.ErrUnsupported
	}
	if !c.controller.SupportsInbox(session.ID) || session.Adapter != "codex" && session.Adapter != "claude" && session.Adapter != "pi" && session.Adapter != "cursor" {
		return runtimeconsumer.ErrUnsupported
	}
	if c.controller.DeliveryHeld(session.ID) {
		return runtimeconsumer.ErrDeferred
	}
	if (m.DeliveryWork.RequestedLevel == "simple" || m.DeliveryWork.MaximumLevel == "simple" || !session.Steerable) && !c.controller.InboxReady(session.ID) {
		return runtimeconsumer.ErrDeferred
	}
	body := nativeMessageText(m)
	if body == "" || len(body) > 64<<10 {
		return runtimeconsumer.ErrUnsupported
	}
	return nil
}
func nativeMessageText(message agentmessage.Envelope) string {
	var text []string
	for _, part := range message.Parts {
		if part.Kind == "text" && part.Text != "" {
			text = append(text, part.Text)
		}
	}
	return strings.Join(text, "\n")
}
func (c *nativeConsumers) Execute(ctx context.Context, b runtimeconsumer.Binding, w runtimeconsumer.Work) (runtimeconsumer.Outcome, error) {
	session, err := c.local(b)
	if err != nil {
		return runtimeconsumer.Outcome{}, err
	}
	message := w.Payload.(agentmessage.Envelope)
	request := agentd.ControlRequest{Instance: b.Instance, ProjectID: b.Project, Identity: b.Address, CorrelationID: w.ID, Text: nativeMessageText(message)}
	outcome := runtimeconsumer.Outcome{Level: "simple"}
	var receipt agentd.Receipt
	if message.DeliveryWork.RequestedLevel == "steer" && message.DeliveryWork.MaximumLevel == "steer" && session.Steerable && !c.controller.InboxReady(session.ID) {
		receipt, err = c.controller.Steer(ctx, b.Session, request)
		outcome.Level = "steer"
	} else {
		if message.DeliveryWork.RequestedLevel == "steer" {
			outcome.Reason = "not_steerable"
			if c.controller.InboxReady(session.ID) {
				outcome.Reason = "idle"
			}
			if message.DeliveryWork.MaximumLevel == "simple" {
				outcome.Reason = "policy_capped"
			}
		}
		receipt, err = c.controller.Inbox(ctx, b.Session, request)
	}
	if err != nil {
		return runtimeconsumer.Outcome{}, err
	}
	if receipt.SessionID != b.Session || receipt.Instance != b.Instance || receipt.ProjectID != b.Project || receipt.Identity != b.Address || receipt.CorrelationID != w.ID || receipt.EffectiveLevel != outcome.Level {
		return runtimeconsumer.Outcome{}, runtimeconsumer.ErrUnknown
	}
	return outcome, nil
}
func (c *nativeConsumers) Complete(ctx context.Context, b runtimeconsumer.Binding, w runtimeconsumer.Work, o runtimeconsumer.Outcome) error {
	raw, err := c.command(ctx, b, "complete-delivery", "--cursor", strconv.FormatInt(w.Cursor, 10), "--delivery-id", w.ID, "--effective-level", o.Level, "--fallback-reason", o.Reason)
	if err != nil {
		return err
	}
	var cursor agentmessage.CursorState
	if json.Unmarshal(raw, &cursor) != nil || cursor.Address != b.Address || cursor.Cursor < w.Cursor {
		return runtimeconsumer.ErrUnknown
	}
	return nil
}

var _ runtimeconsumer.Driver = (*nativeConsumers)(nil)
var _ agentd.RuntimeConsumers = (*nativeConsumers)(nil)

func (c *nativeConsumers) Repair(ctx context.Context) error {
	return c.RepairProject(ctx, 0)
}
func (c *nativeConsumers) RepairProject(ctx context.Context, project int64) error {
	for !c.reconcileMu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer c.reconcileMu.Unlock()
	c.mu.Lock()
	bindings := make(map[string]agentd.Session, len(c.bindings))
	for key, session := range c.bindings {
		bindings[key] = session
	}
	c.mu.Unlock()
	if len(bindings) == 0 {
		return runtimeconsumer.ErrAuthority
	}
	matched := false
	for key, session := range bindings {
		if project > 0 && session.ProjectID != project {
			continue
		}
		matched = true
		status := c.controller.Status()
		b := runtimeconsumer.Binding{Instance: status.Instance, Machine: c.reporter.host, Generation: status.DaemonID, Session: session.ID, Address: session.Identity, Project: session.ProjectID, Kind: "primary", Revision: session.Reporter.PublicSessionID}
		if b.Key() != key {
			return runtimeconsumer.ErrOwnership
		}
		if err := c.supervisor.Repair(ctx, b); err != nil {
			return err
		}
		if err := c.supervisor.Step(ctx, b); err != nil {
			return err
		}
	}
	if !matched {
		return runtimeconsumer.ErrAuthority
	}
	return nil
}

// The fixed worker set drains before reconciliation publishes or retires any
// binding. Session count cannot create unbounded goroutines or external calls.
func runConsumerTasks(count int, task func(int)) {
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(count, runtimeconsumer.MaxConcurrentStreams) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				task(i)
			}
		}()
	}
	for i := range count {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
