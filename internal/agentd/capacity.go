// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
)

type capacityAPI interface {
	ReportCapacity(context.Context, string, []capacity.Reading) error
}

// LatestCapacityReadAt lets idle capture respect observations from external
// harnesses and previous daemon generations as well as this process's streams.
func (r *Remote) LatestCapacityReadAt(ctx context.Context, id string) (time.Time, error) {
	var readings []capacity.Reading
	if err := r.Client.Do(ctx, "GET", "/api/agent-accounts/"+url.PathEscape(id)+"/readings", nil, &readings); err != nil {
		return time.Time{}, err
	}
	var at time.Time
	for _, v := range readings {
		if v.Source != "estimate" && v.ReadAt.After(at) {
			at = v.ReadAt
		}
	}
	return at, nil
}

func (r *Remote) ReportCapacity(ctx context.Context, id string, readings []capacity.Reading) error {
	readings = append([]capacity.Reading(nil), readings...)
	sort.SliceStable(readings, func(i, j int) bool {
		if readings[i].Source != readings[j].Source {
			return readings[i].Source < readings[j].Source
		}
		return readings[i].Phase < readings[j].Phase
	})
	for len(readings) > 0 {
		n := 1
		for n < len(readings) && readings[n].Source == readings[0].Source && readings[n].Phase == readings[0].Phase {
			n++
		}
		if n > 32 {
			return errors.New("capacity snapshot exceeds 32 windows")
		}
		if err := r.Client.Do(ctx, "POST", "/api/agent-accounts/"+url.PathEscape(id)+"/readings", struct {
			Readings []capacity.Reading `json:"readings"`
		}{readings[:n]}, nil); err != nil {
			return err
		}
		readings = readings[n:]
	}
	return nil
}

// The existing supervisor heartbeat flushes this bounded normalized outbox;
// a vendor stream reader never waits on HTTP. No raw frames enter the journal.
func (s *Supervisor) observeCapacity(e *owned, readings []capacity.Reading) {
	e.mu.Lock()
	if e.record.Generation != s.generation || e.harnessArchived {
		e.mu.Unlock()
		return
	}
	// Replace the pending snapshot for each phase/source, preserving start/end
	// attribution but not buckets omitted from a subsequent capture.
	for _, r := range readings {
		for key, old := range e.capacityPending {
			if old.Source == r.Source && old.Phase == r.Phase && !old.ReadAt.After(r.ReadAt) {
				delete(e.capacityPending, key)
			}
		}
	}
	if e.capacityPending == nil {
		e.capacityPending = map[string]capacity.Reading{}
	}
	for _, r := range readings {
		r.RunID = e.record.RunID
		key := r.Source + "/" + r.WindowKind + "/" + r.Bucket + "/" + r.Phase
		if len(e.capacityPending) < 192 || e.capacityPending[key].WindowKind != "" {
			e.capacityPending[key] = r
		}
	}
	id := e.record.AccountID
	e.mu.Unlock()
	s.mu.Lock()
	if s.capacityLast == nil {
		s.capacityLast = map[string]time.Time{}
	}
	for _, r := range readings {
		if r.ReadAt.After(s.capacityLast[id]) {
			s.capacityLast[id] = r.ReadAt
		}
	}
	s.mu.Unlock()
}
func (s *Supervisor) flushCapacity(ctx context.Context, e *owned) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	api, ok := s.api.(capacityAPI)
	if !ok {
		return
	}
	e.mu.Lock()
	if e.record.Generation != s.generation || e.harnessArchived {
		e.mu.Unlock()
		return
	}
	id := e.record.AccountID
	items := []capacity.Reading{}
	keys := []string{}
	for k, v := range e.capacityPending {
		keys = append(keys, k)
		items = append(items, v)
	}
	e.mu.Unlock()
	if len(items) == 0 {
		return
	}
	if api.ReportCapacity(ctx, id, items) != nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, k := range keys {
		if e.capacityPending[k].ReadAt.Equal(items[i].ReadAt) {
			delete(e.capacityPending, k)
		}
	}
}
func (p *codexProcess) readCapacity(ctx context.Context, phase string) {
	// Optional vendor extension: quota failure must not break run accounting.
	op, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	raw, err := p.request(op, "jsonrpc", "account/rateLimits/read", map[string]any{})
	if err != nil {
		return
	}
	// Optional quota capture must not extend the process-stop deadline if
	// an unrelated observer currently owns the event lock.
	if !p.eventMu.TryLock() {
		return
	}
	defer p.eventMu.Unlock()
	p.emitCapacityReadings(p.capacityParser.CodexSnapshot(raw, time.Now().UTC()), phase)
}
func (p *codexProcess) emitCapacity(raw []byte, phase string) {
	p.emitCapacityReadings(p.capacityParser.Codex(raw, time.Now().UTC()), phase)
}
func (p *codexProcess) emitCapacityReadings(readings []capacity.Reading, phase string) {
	for i := range readings {
		readings[i].Phase = phase
	}
	if len(readings) > 0 {
		p.observe(AdapterEvent{Capacity: readings})
	}
}

// CaptureCapacity is a quota-neutral fallback for accounts without a first
// run. The vendor owns authentication; identity is checked on the same process
// before reading quota. No credential files or auth response are published.
func (a *CodexAdapter) CaptureCapacity(ctx context.Context, key string) []capacity.Reading {
	home, err := localHome(a.Homes, key)
	if err != nil || strings.TrimSpace(a.Emails[key]) == "" {
		return nil
	}
	p, err := launchWire(a.Path, []string{"app-server", "--listen", "stdio://"}, home, withEnv("CODEX_HOME", home), "jsonrpc", func(AdapterEvent) {})
	if err != nil {
		return nil
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = p.Stop(cleanup)
		_ = p.finishReader()
	}()
	op, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := p.request(op, "jsonrpc", "initialize", map[string]any{"clientInfo": map[string]string{"name": "aeon-capacity", "version": "1"}}); err != nil {
		return nil
	}
	if p.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}) != nil {
		return nil
	}
	raw, err := p.request(op, "jsonrpc", "account/read", map[string]any{"refreshToken": false})
	var identity struct {
		Account *struct {
			Type  string `json:"type"`
			Email string `json:"email"`
		} `json:"account"`
	}
	if err != nil || json.Unmarshal(raw, &identity) != nil || identity.Account == nil || identity.Account.Type != "chatgpt" || !strings.EqualFold(strings.TrimSpace(identity.Account.Email), strings.TrimSpace(a.Emails[key])) {
		return nil
	}
	raw, err = p.request(op, "jsonrpc", "account/rateLimits/read", map[string]any{})
	if err != nil {
		return nil
	}
	parser := capacity.Parser{}
	readings := parser.CodexSnapshot(raw, time.Now().UTC())
	for i := range readings {
		readings[i].Source = "agentd"
	}
	return readings
}

// RunCapacityCaptures owns its own ticker and request deadline; dispatch polling
// never pays for an app-server launch. Cancel and join this loop before Close.
func (s *Supervisor) RunCapacityCaptures(ctx context.Context) {
	ticker := time.NewTicker(min(s.capacityInterval, 30*time.Second))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.captureIdleCapacity(ctx, time.Now())
		}
	}
}
func (s *Supervisor) captureIdleCapacity(ctx context.Context, now time.Time) {
	api, ok := s.api.(capacityAPI)
	if !ok {
		return
	}
	s.mu.Lock()
	accounts := append([]EnrolledAccount(nil), s.accounts...)
	s.mu.Unlock()
	for _, a := range accounts {
		if ctx.Err() != nil {
			return
		}
		// Claim the capture slot atomically with StartRun. Release the dispatch
		// mutex before IO; new dispatch attempts defer immediately while capturing.
		if !s.dispatchMu.TryLock() {
			continue
		}
		if !s.dispatchAllowed(a.ID) {
			s.dispatchMu.Unlock()
			continue
		}
		s.mu.Lock()
		capture, canCapture := s.adapters[a.Harness].(interface {
			CaptureCapacity(context.Context, string) []capacity.Reading
		})
		due := canCapture && s.probedAccounts[a.ID] && !s.blockedAccounts[a.ID] && !s.capacityCapturing && now.Sub(s.capacityLast[a.ID]) >= s.capacityInterval && now.Sub(s.capacityAttempt[a.ID]) >= s.capacityInterval
		entries := make([]*owned, 0, len(s.runs))
		for _, e := range s.runs {
			entries = append(entries, e)
		}
		s.mu.Unlock()
		for _, e := range entries {
			e.mu.Lock()
			busy := e.record.AccountID == a.ID && (!noLocalProcess(e.record) || e.record.State == "claim_pending")
			e.mu.Unlock()
			if busy {
				due = false
			}
		}
		if !due {
			s.dispatchMu.Unlock()
			continue
		}
		s.mu.Lock()
		s.capacityCapturing = true
		s.capacityAttempt[a.ID] = now
		s.mu.Unlock()
		s.dispatchMu.Unlock()
		op, cancel := context.WithTimeout(ctx, 10*time.Second)
		var readings []capacity.Reading
		fresh := false
		if latest, ok := s.api.(interface {
			LatestCapacityReadAt(context.Context, string) (time.Time, error)
		}); ok {
			at, err := latest.LatestCapacityReadAt(op, a.ID)
			fresh = err != nil || now.Sub(at) < s.capacityInterval
			if err == nil {
				s.mu.Lock()
				if at.After(s.capacityLast[a.ID]) {
					s.capacityLast[a.ID] = at
				}
				s.mu.Unlock()
			}
		}
		if !fresh && s.dispatchAllowed(a.ID) {
			readings = capture.CaptureCapacity(op, a.Key)
		}
		if len(readings) > 0 && api.ReportCapacity(op, a.ID, readings) == nil {
			s.mu.Lock()
			for _, r := range readings {
				if r.ReadAt.After(s.capacityLast[a.ID]) {
					s.capacityLast[a.ID] = r.ReadAt
				}
			}
			s.mu.Unlock()
		}
		cancel()
		s.mu.Lock()
		s.capacityCapturing = false
		s.mu.Unlock()
	}
}
