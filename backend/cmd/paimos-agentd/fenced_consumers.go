// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/inspr-at/paimos/backend/agentd"
	"github.com/inspr-at/paimos/backend/agentmessage"
	harness "github.com/inspr-at/paimos/backend/agentmessage/harness"
	"github.com/inspr-at/paimos/backend/lifecycleclient"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

func (p *projectLifecycle) consume(ctx context.Context) error {
	var sessions []agentd.Session
	for _, s := range p.owner.supervisor.Status().Sessions {
		if s.ProjectID == p.config.ProjectID && s.State == agentd.StateRunning && p.bound[s.ID] {
			sessions = append(sessions, s)
		}
	}
	results := make([][]runtimeconsumer.Evidence, len(sessions))
	failures := make([]error, len(sessions))
	runConsumerTasks(len(sessions), func(i int) {
		results[i], failures[i] = p.consumeSession(ctx, sessions[i])
	})
	var evidence []runtimeconsumer.Evidence
	seen := map[string]bool{}
	for _, result := range results {
		evidence = append(evidence, result...)
		for _, e := range result {
			seen[e.Kind] = true
		}
	}
	for _, kind := range []string{"fallback", "attention"} {
		if !seen[kind] {
			evidence = append(evidence, runtimeconsumer.Evidence{Kind: kind, Generation: p.owner.supervisor.Status().DaemonID, ProjectID: p.config.ProjectID, State: "unavailable", Reason: "receiver_not_configured"})
		}
	}
	p.owner.mu.Lock()
	p.consumerEvidence = evidence
	p.owner.mu.Unlock()
	return errors.Join(failures...)
}

// Each task owns its evidence until all session work drains. Only the caller
// publishes the complete snapshot or advances lifecycle/repair state.
func (p *projectLifecycle) consumeSession(ctx context.Context, s agentd.Session) ([]runtimeconsumer.Evidence, error) {
	var evidence []runtimeconsumer.Evidence
	var failures []error
	var inventory struct {
		Targets []agentmessage.Target `json:"targets"`
	}
	if e := p.authority.Request(ctx, http.MethodGet, fmt.Sprintf("/api/projects/%d/message-targets?address=%s", s.ProjectID, url.QueryEscape(s.Identity)), nil, nil, &inventory); e != nil {
		failures = append(failures, e)
		// An unread inventory cannot establish that an optional receiver is
		// absent. Keep its failed coverage even when another session succeeds.
		for _, kind := range []string{"fallback", "attention"} {
			evidence = append(evidence, runtimeconsumer.Evidence{Kind: kind, Generation: p.owner.supervisor.Status().DaemonID, ProjectID: p.config.ProjectID, State: "unavailable", Reason: "transport_unavailable", Failures: 1})
		}
		return evidence, errors.Join(failures...)
	}
	for _, kind := range []string{"fallback", "attention"} {
		selected, selectionErr := selectOwnedConsumerTarget(inventory.Targets, p.owner.reporter.instance, s, kind)
		if selectionErr != nil {
			evidence = append(evidence, runtimeconsumer.Evidence{Kind: kind, Generation: p.owner.supervisor.Status().DaemonID, ProjectID: p.config.ProjectID, State: "unavailable", Reason: "authority_unavailable", Failures: 1})
			failures = append(failures, selectionErr)
			continue
		}
		if selected == nil {
			continue
		}
		registration := agentmessage.ConsumerRegistration{RuntimeID: p.record.Runtime.ID, RuntimeGeneration: p.record.Runtime.Generation, SessionID: s.Reporter.PublicSessionID, SessionGeneration: s.ID, Kind: kind, TargetID: selected.ID, TargetVersion: int64(selected.Version)}
		// The exact owned receiver must be idle before execute releases its
		// payload. Foreign private target references are quarantined.
		ready := p.owner.supervisor.InboxReady(s.ID)
		err := p.consumers.Step(ctx, registration, ready, func(callCtx context.Context, page agentmessage.ConsumerPage) (string, error) {
			return p.deliverConsumer(callCtx, s, *selected, kind, page)
		})
		e := runtimeconsumer.Evidence{Kind: kind, Generation: p.owner.supervisor.Status().DaemonID, ProjectID: p.config.ProjectID, State: "ready", LastSuccess: time.Now().UTC()}
		if err != nil {
			e.LastSuccess = time.Time{}
			e.State = "backoff"
			e.Reason = "transport_unavailable"
			e.Failures = 1
			failures = append(failures, err)
			if errors.Is(err, lifecycleclient.ErrCircuit) {
				e.State = "circuit_open"
				e.Reason = "transport_unavailable"
				e.Failures = 3
				e.Attention = true
			} else if errors.Is(err, lifecycleclient.ErrUnknown) {
				e.State = "circuit_open"
				e.Reason = "outcome_unknown"
				e.Attention = true
			} else if errors.Is(err, lifecycleclient.ErrHandoff) {
				e.State = "unavailable"
				e.Reason = "legacy_handoff_required"
			} else if errors.Is(err, lifecycleclient.ErrOwnership) {
				e.State = "unavailable"
				e.Reason = "authority_unavailable"
			}
		}
		evidence = append(evidence, e)
	}
	return evidence, errors.Join(failures...)
}

func (p *projectLifecycle) deliverConsumer(ctx context.Context, s agentd.Session, target agentmessage.Target, kind string, page agentmessage.ConsumerPage) (string, error) {
	var body, ref, adapter, targetKind, reason string
	if page.Attempt == nil {
		return "", lifecycleclient.ErrOwnership
	}
	if kind == "fallback" {
		d := page.Delivery
		if d == nil || d.Envelope == nil || d.Work.DeliveryID != page.Attempt.ResourceID || d.Envelope.Cursor != page.Attempt.Cursor || d.Envelope.To != s.Identity || d.Work.Instance != p.owner.reporter.instance || d.Work.ProjectID != s.ProjectID || d.Work.State != "leased" || d.Work.MaximumLevel != "simple" {
			return "", lifecycleclient.ErrOwnership
		}
		body = nativeMessageText(*d.Envelope)
		ref = d.Work.TargetRef
		adapter = d.Work.Adapter
		targetKind = d.Work.TargetKind
		reason = d.Work.FallbackReason
	} else {
		a := page.Attention
		if a == nil || a.Work == nil || a.Work.BatchID != page.Attempt.ResourceID || a.NextCursor != page.Attempt.Cursor || a.Address != s.Identity || a.Work.Instance != p.owner.reporter.instance || a.Work.ProjectID != s.ProjectID || a.Work.State != "leased" || (a.Work.MaximumLevel != "simple" && a.Work.MaximumLevel != "steer") {
			return "", lifecycleclient.ErrOwnership
		}
		body = a.Frame
		ref = a.Work.TargetRef
		adapter = a.Work.Adapter
		targetKind = a.Work.TargetKind
	}
	if body == "" || len(body) > 64<<10 || ref == "" || adapter != target.Adapter || targetKind != target.TargetKind {
		return "", lifecycleclient.ErrOwnership
	}
	plugin, e := harness.Resolve(adapter)
	if e != nil || plugin.ValidateTarget(ctx, ref) != nil {
		return "", lifecycleclient.ErrOwnership
	}
	// The owned connection is the only consumer for its own private thread.
	if ref == s.HarnessSessionID {
		if !p.ownsManagedSession(agentd.Session{Adapter: s.Adapter, AccountLabel: s.AccountLabel, AccountKey: s.AccountKey}) {
			return "", lifecycleclient.ErrOwnership
		}
		if s.AccountKey != "" {
			if !p.owner.supervisor.HasAccount(s.Adapter, s.AccountKey) {
				return "", lifecycleclient.ErrOwnership
			}
		} else if p.owner.supervisor.ProbeAccount(ctx, s.Adapter) != s.AccountLabel {
			return "", lifecycleclient.ErrOwnership
		}
		receipt, e := p.owner.supervisor.Inbox(ctx, s.ID, agentd.ControlRequest{Instance: p.owner.reporter.instance, ProjectID: s.ProjectID, Identity: s.Identity, CorrelationID: page.Attempt.ID, Text: body})
		if e != nil || receipt.SessionID != s.ID || receipt.EffectiveLevel != "simple" || receipt.CorrelationID != page.Attempt.ID {
			return "", lifecycleclient.ErrUnknown
		}
		return reason, nil
	}
	// A public target registry row cannot prove ownership of another vendor
	// session. Migration must bind the exact owned private reference first.
	return "", lifecycleclient.ErrOwnership
}

func (p *projectLifecycle) publishHealth(ctx context.Context) error {
	if time.Since(p.healthAt) < 30*time.Second {
		return nil
	}
	layers := []runtimeconsumer.Evidence{}
	for _, e := range p.owner.primary.Snapshot() {
		if e.Kind == "primary" {
			layers = append(layers, e)
		}
	}
	layers = append(layers, p.consumerEvidence...)
	status := p.owner.supervisor.RuntimeStatus(ctx)
	reporter := runtimeconsumer.Evidence{Kind: "reporter", State: "ready", ProjectID: p.config.ProjectID, LastSuccess: status.ReporterLastSuccess}
	if status.ReporterUnavailable || status.ReporterLastSuccess.IsZero() {
		reporter.State = "unavailable"
		reporter.Reason = "authority_unavailable"
	}
	layers = append(layers, reporter)
	for _, kind := range []string{"reporter", "primary", "fallback", "attention"} {
		state, reason, count := healthEvidence(kind, p.config.ProjectID, layers, time.Now())
		in := agentmessage.RuntimeHealthInput{RuntimeID: p.record.Runtime.ID, RuntimeGeneration: p.record.Runtime.Generation, Layer: kind, State: state, Reason: reason, FailureCount: count, Sequence: p.record.Health[kind].Sequence + 1}
		p.record.Health[kind] = in
		if e := p.owner.journal.Put(p.record); e != nil {
			return e
		}
		var out agentmessage.RuntimeHealthResult
		if e := p.authority.Request(ctx, http.MethodPost, fmt.Sprintf("/api/projects/%d/consumers/v1/runtime-health", p.config.ProjectID), nil, in, &out); e != nil {
			return e
		}
		if out.SchemaVersion != 1 || out.Sequence != in.Sequence || out.State != in.State {
			return lifecycleclient.ErrOwnership
		}
	}
	p.healthAt = time.Now()
	return nil
}

func healthEvidence(kind string, project int64, layers []runtimeconsumer.Evidence, now time.Time) (string, string, int) {
	seen := false
	for _, e := range layers {
		if e.Kind != kind || e.ProjectID != project {
			continue
		}
		seen = true
		if e.State == "ready" && !e.LastSuccess.IsZero() && now.Sub(e.LastSuccess) < 45*time.Second && !e.LastSuccess.After(now.Add(5*time.Second)) {
			continue
		}
		if e.Reason == "outcome_unknown" || e.Reason == "legacy_handoff_required" {
			return "unhealthy", "consumer_authority_unavailable", 1
		}
		if e.State == "circuit_open" {
			return "unhealthy", "consumer_crash_loop", 3
		}
		if e.Reason == "authority_unavailable" || e.Reason == "receiver_not_configured" || e.LastSuccess.IsZero() {
			return "unhealthy", "consumer_authority_unavailable", 1
		}
		return "unhealthy", "consumer_transport_failed", 1
	}
	if !seen {
		return "unhealthy", "consumer_authority_unavailable", 1
	}
	return "healthy", "recovered", 0
}

// Match the server's primary-first, highest-version attention selection. A
// steer-capable target still receives attention through the simple owned inbox.
func selectOwnedConsumerTarget(targets []agentmessage.Target, instance string, session agentd.Session, kind string) (*agentmessage.Target, error) {
	var selected *agentmessage.Target
	for i := range targets {
		t := &targets[i]
		if !t.Enabled || t.Instance != instance || t.ProjectID != session.ProjectID || t.Address != session.Identity {
			continue
		}
		if kind == "fallback" && t.Adapter == "codex" && t.Role == "simple_fallback" && t.MaximumLevel == "simple" {
			if selected != nil {
				return nil, lifecycleclient.ErrOwnership
			}
			selected = t
		}
		if kind == "attention" && session.Role == "coordinator" && (t.Adapter == "codex" || t.Adapter == "claude_resume") && (t.MaximumLevel == "simple" || t.MaximumLevel == "steer") {
			if selected == nil || t.Role == "primary" && selected.Role != "primary" || (t.Role == "primary") == (selected.Role == "primary") && t.Version > selected.Version {
				selected = t
			}
		}
	}
	return selected, nil
}
