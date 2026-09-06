// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

// Package runtimeconsumer supervises existing durable delivery contracts. It
// stores effect receipts and bounded recovery state, never a second message queue.
package runtimeconsumer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/inspr-at/paimos/backend/localjournal"
)

var (
	ErrAuthority   = errors.New("consumer generation authority unavailable")
	ErrOwnership   = errors.New("consumer ownership or target revision changed")
	ErrUnknown     = errors.New("consumer effect outcome unknown")
	ErrUnsupported = errors.New("consumer primitive unsupported")
	ErrConflict    = errors.New("consumer singleton conflict")
	ErrLegacy      = errors.New("legacy receiver handoff required")
	ErrDeferred    = errors.New("owned receiver is busy; keep canonical FIFO pending")
)

// Binding contains no delivery target reference or credential. Generation is
// the exact daemon generation; Session is the locally owned child generation.
type Binding struct {
	Instance, Machine, Generation, Session, Address, Kind, Revision string
	Project                                                         int64
}

func digest(v any) string {
	raw, _ := json.Marshal(v)
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}
func (b Binding) Key() string { return digest(b) }
func (b Binding) Stream() string {
	return digest(struct {
		Instance, Machine, Address, Kind string
		Project                          int64
	}{b.Instance, b.Machine, b.Address, b.Kind, b.Project})
}
func (b Binding) valid() bool {
	return b.Instance != "" && b.Machine != "" && b.Generation != "" && b.Session != "" && b.Address != "" && b.Project > 0 && b.Revision != "" && (b.Kind == "primary" || b.Kind == "fallback" || b.Kind == "attention" || b.Kind == "intents")
}

type Work struct {
	Revision string
	ID       string
	Cursor   int64
	Payload  any
}
type Outcome struct {
	Level  string `json:"level"`
	Reason string `json:"reason,omitempty"`
}

func (o Outcome) valid() bool {
	if o.Level != "simple" && o.Level != "steer" {
		return false
	}
	switch o.Reason {
	case "", "idle", "not_steerable", "unsupported", "policy_capped", "transport_error":
		return true
	}
	return false
}

// A Driver must verify authenticated generation/target ownership at Poll and
// again at Complete. Local file locks cannot supply missing remote authority.
type Driver interface {
	Verify(context.Context, Binding) error
	Poll(context.Context, Binding) (*Work, error)
	Prepare(context.Context, Binding, Work) error
	Execute(context.Context, Binding, Work) (Outcome, error)
	Complete(context.Context, Binding, Work, Outcome) error
}

type Evidence struct {
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Reason      string    `json:"reason,omitempty"`
	Generation  string    `json:"generation"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	Failures    int       `json:"failures"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	Attention   bool      `json:"attention"`
}
type checkpoint struct {
	Key     string  `json:"key"`
	Binding string  `json:"binding"`
	Phase   string  `json:"phase"`
	Outcome Outcome `json:"outcome,omitempty"`
}
type circuit struct {
	Key      string   `json:"key"`
	Evidence Evidence `json:"evidence"`
}
type Supervisor struct {
	unlock   func()
	stopped  bool
	mu       sync.Mutex
	stepMu   sync.Mutex
	driver   Driver
	receipts *localjournal.Journal[checkpoint]
	circuits *localjournal.Journal[circuit]
	now      func() time.Time
	states   map[string]Evidence
}

func New(directory string, driver Driver) (_ *Supervisor, returnErr error) {
	unlock, err := acquireLock(directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			unlock()
		}
	}()
	if driver == nil {
		return nil, ErrAuthority
	}
	receipts, err := localjournal.Open(localjournal.Config[checkpoint]{Directory: directory, Prefix: "consumer-effects", Version: 1, MaxBytes: 2 << 20, MaxRecords: 4096,
		Key: func(c checkpoint) (string, error) { return c.Key, nil }, Validate: func(c checkpoint) error {
			if len(c.Key) != 64 || len(c.Binding) != 64 {
				return ErrUnknown
			}
			if c.Phase != "pending" && c.Phase != "applied" && c.Phase != "acked" {
				return ErrUnknown
			}
			if c.Phase != "pending" && !c.Outcome.valid() {
				return ErrUnknown
			}
			return nil
		}})
	if err != nil {
		return nil, err
	}
	circuits, err := localjournal.Open(localjournal.Config[circuit]{Directory: directory, Prefix: "consumer-circuits", Version: 1, MaxBytes: 256 << 10, MaxRecords: 512,
		Key: func(c circuit) (string, error) { return c.Key, nil }, Validate: func(c circuit) error {
			if len(c.Key) != 64 || c.Evidence.Failures < 0 || c.Evidence.Failures > 3 {
				return ErrUnknown
			}
			switch c.Evidence.State {
			case "ready", "backoff", "circuit_open", "stopped":
			default:
				return ErrUnknown
			}
			return nil
		}})
	if err != nil {
		return nil, err
	}
	return &Supervisor{unlock: unlock, driver: driver, receipts: receipts, circuits: circuits, now: time.Now, states: map[string]Evidence{}}, nil
}

// Step runs at most one canonical leased item. The in-memory gate also drains
// target changes: an old step finishes/cancels before a replacement can execute.
func (s *Supervisor) Step(ctx context.Context, b Binding) error {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.stopped {
		return ErrOwnership
	}
	if !b.valid() {
		return ErrAuthority
	}
	state := Evidence{Kind: b.Kind, Generation: b.Generation, State: "ready"}
	for _, c := range s.circuits.Snapshot() {
		if c.Key == b.Stream() {
			state = c.Evidence
			state.Generation = b.Generation
			break
		}
	}
	s.publish(b, state)
	if state.State == "circuit_open" {
		return ErrUnknown
	}
	if s.now().Before(state.NextAttempt) {
		return nil
	}
	err := s.step(ctx, b)
	if errors.Is(err, ErrDeferred) {
		err = nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		state.State = "ready"
		state.Reason = ""
		state.Failures = 0
		state.NextAttempt = time.Time{}
		state.LastSuccess = s.now().UTC()
	} else {
		state.Failures++
		if state.Failures > 3 {
			state.Failures = 3
		}
		state.Reason = reason(err)
		state.State = "backoff"
		// Deterministic bounded jitter avoids synchronized restarts, including
		// restart recovery. Three failures open the persisted stream circuit.
		key := b.Stream()
		jitter := time.Duration(key[0]%9) * 100 * time.Millisecond
		state.NextAttempt = s.now().Add(time.Second*time.Duration(1<<state.Failures) + jitter)
		if state.Failures == 3 || errors.Is(err, ErrUnknown) || errors.Is(err, ErrOwnership) {
			state.State = "circuit_open"
			state.Attention = true
		}
	}
	if e := s.circuits.Put(circuit{b.Stream(), state}); e != nil {
		state.State = "circuit_open"
		state.Reason = "journal_unavailable"
		state.Attention = true
		s.publish(b, state)
		return ErrUnknown
	}
	s.publish(b, state)
	return err
}
func (s *Supervisor) step(ctx context.Context, b Binding) error {
	if err := s.driver.Verify(ctx, b); err != nil {
		return err
	}
	work, err := s.driver.Poll(ctx, b)
	if err != nil {
		return err
	}
	if work == nil {
		return nil
	}
	if work.ID == "" || len(work.ID) > 128 || work.Cursor <= 0 {
		return ErrAuthority
	}
	key := digest(struct {
		Instance, Kind, ID string
		Project            int64
	}{b.Instance, b.Kind, work.ID, b.Project})
	bindingDigest := digest(struct{ Binding, TargetRevision string }{b.Key(), work.Revision})
	record := checkpoint{Key: key, Binding: bindingDigest, Phase: "pending"}
	found := false
	for _, c := range s.receipts.Snapshot() {
		if c.Key == key {
			record = c
			found = true
			break
		}
	}
	if found {
		if record.Binding != bindingDigest {
			return ErrOwnership
		}
		if record.Phase == "pending" {
			return ErrUnknown
		}
	} else {
		if err := s.driver.Prepare(ctx, b, *work); err != nil {
			return err
		}
		if err := s.driver.Verify(ctx, b); err != nil {
			return err
		}
		if err := s.receipts.Put(record); err != nil {
			return ErrUnknown
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		outcome, err := s.driver.Execute(ctx, b, *work)
		if err != nil {
			return ErrUnknown
		}
		if !outcome.valid() {
			return ErrUnknown
		}
		record.Phase = "applied"
		record.Outcome = outcome
		if err := s.receipts.Put(record); err != nil {
			return ErrUnknown
		}
	}
	if err := s.driver.Verify(ctx, b); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.driver.Complete(ctx, b, *work, record.Outcome); err != nil {
		return err
	}
	record.Phase = "acked"
	if err := s.receipts.Put(record); err != nil {
		return ErrUnknown
	}
	return nil
}
func reason(err error) string {
	switch {
	case errors.Is(err, ErrAuthority):
		return "authority_unavailable"
	case errors.Is(err, ErrOwnership):
		return "ownership_changed"
	case errors.Is(err, ErrLegacy):
		return "legacy_handoff_required"
	case errors.Is(err, ErrConflict):
		return "singleton_conflict"
	case errors.Is(err, ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrUnknown):
		return "outcome_unknown"
	}
	return "transport_unavailable"
}
func (s *Supervisor) publish(b Binding, e Evidence) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[b.Key()] = e
}
func (s *Supervisor) Snapshot() []Evidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Evidence, 0, len(s.states))
	for _, e := range s.states {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Generation < out[j].Generation
	})
	return out
}

// Retain retires only in-memory projections; durable receipts/circuits survive
// generation changes. Call after the preceding Step has drained.
func (s *Supervisor) Retain(bindings []Binding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := map[string]bool{}
	for _, b := range bindings {
		keep[b.Key()] = true
	}
	for key := range s.states {
		if !keep[key] {
			delete(s.states, key)
		}
	}
}
func (s *Supervisor) Stop() {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, e := range s.states {
		e.State = "stopped"
		s.states[k] = e
	}
}

// Repair reopens only a transient-failure circuit after exact binding authority
// is verified again. Pending receipts remain quarantined; no receipt is erased.
func (s *Supervisor) Repair(ctx context.Context, b Binding) error {
	s.stepMu.Lock()
	defer s.stepMu.Unlock()
	if s.stopped || !b.valid() {
		return ErrOwnership
	}
	if err := s.driver.Verify(ctx, b); err != nil {
		return err
	}
	for _, c := range s.circuits.Snapshot() {
		if c.Key != b.Stream() {
			continue
		}
		if c.Evidence.Reason != "transport_unavailable" && c.Evidence.Reason != "authority_unavailable" && c.Evidence.Reason != "" {
			return ErrUnknown
		}
		// Every retained unknown is checked again by Step even if it belongs to
		// another binding. Conservatively refuse repair while any effect is pending.
		for _, receipt := range s.receipts.Snapshot() {
			if receipt.Phase == "pending" {
				return ErrUnknown
			}
		}
		state := Evidence{Kind: b.Kind, Generation: b.Generation, State: "ready"}
		if err := s.circuits.Put(circuit{b.Stream(), state}); err != nil {
			return ErrUnknown
		}
		s.publish(b, state)
		return nil
	}
	return nil
}
