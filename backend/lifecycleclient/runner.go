// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/lifecycleintents"
	"github.com/inspr-at/paimos/backend/localjournal"
)

type Result struct {
	SessionID string `json:"session_id,omitempty"`
	Reason    string `json:"reason"`
}
type Executor interface {
	Prepare(context.Context, lifecycleintents.Intent) error
	Execute(context.Context, lifecycleintents.Intent) (Result, error)
	Committed(context.Context, lifecycleintents.Intent) error
}
type record struct {
	ID     string                  `json:"id"`
	Digest string                  `json:"digest"`
	Phase  string                  `json:"phase"`
	Result Result                  `json:"result"`
	Intent lifecycleintents.Intent `json:"intent"`
}
type Runner struct {
	mu        sync.Mutex
	authority lifecycleintents.RuntimeAuthority
	executor  Executor
	journal   *localjournal.Journal[record]
}

func NewRunner(directory, namespace string, authority lifecycleintents.RuntimeAuthority, executor Executor) (*Runner, error) {
	if authority == nil || executor == nil || uuid.Validate(namespace) != nil {
		return nil, ErrOwnership
	}
	j, err := localjournal.Open(localjournal.Config[record]{Directory: directory, Prefix: "lifecycle-" + namespace, Version: 1, MaxBytes: 2 << 20, MaxRecords: 4096,
		Key: func(r record) (string, error) { return r.ID, nil }, Validate: func(r record) error {
			if uuid.Validate(r.ID) != nil || len(r.Digest) != 64 || r.Intent.ID != r.ID || intentDigest(r.Intent) != r.Digest {
				return ErrUnknown
			}
			switch r.Phase {
			case "reserved", "executing", "outcome", "terminal", "quarantined":
			default:
				return ErrUnknown
			}
			if r.Phase == "outcome" || r.Phase == "terminal" || r.Phase == "quarantined" {
				if r.Result.Reason != "applied" && r.Result.Reason != "outcome_unknown" && r.Result.Reason != "failed" {
					return ErrUnknown
				}
			}
			if r.Result.SessionID != "" && uuid.Validate(r.Result.SessionID) != nil {
				return ErrUnknown
			}
			return nil
		}})
	if err != nil {
		return nil, ErrUnknown
	}
	return &Runner{authority: authority, executor: executor, journal: j}, nil
}
func intentDigest(in lifecycleintents.Intent) string {
	raw, _ := json.Marshal(struct {
		Project    int64
		Request    lifecycleintents.Request
		Generation string
	}{in.ProjectID, in.Request, in.NewGeneration})
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:])
}

// Step makes at most one effect attempt. Even a lost executing response leaves
// a durable unknown; retrying the claim never authorizes a second local effect.
func (r *Runner) Step(ctx context.Context, runtime lifecycleintents.Runtime) (stepErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	expires, err := time.Parse(time.RFC3339Nano, runtime.ExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		return ErrOwnership
	}
	quarantined := false
	for _, saved := range r.journal.Snapshot() {
		quarantined = quarantined || saved.Phase == "quarantined"
	}
	defer func() {
		if stepErr == nil && quarantined {
			stepErr = ErrUnknown
		}
	}()
	for _, saved := range r.journal.Snapshot() {
		if saved.Phase == "outcome" {
			return r.complete(ctx, runtime, saved)
		}
	}
	in, err := r.authority.Claim(ctx, runtime.ID)
	if err != nil || in == nil {
		return err
	}
	if cleanup, ok := r.executor.(interface{ Forget(string) }); ok {
		defer cleanup.Forget(in.ID)
	}
	if !acceptedIntent(*in) || uuid.Validate(in.ID) != nil || in.Request.RuntimeID != runtime.ID || in.Request.RuntimeGeneration != runtime.Generation || in.ProjectID != runtime.ProjectID || (in.State != "claimed" && in.State != "executing") {
		return ErrOwnership
	}
	c := record{ID: in.ID, Digest: intentDigest(*in), Phase: "reserved", Intent: *in}
	found := false
	for _, previous := range r.journal.Snapshot() {
		if previous.ID == in.ID {
			c = previous
			found = true
			break
		}
	}
	if c.Phase == "quarantined" {
		return ErrUnknown
	}
	if c.Digest != intentDigest(*in) {
		return ErrOwnership
	}
	if !found {
		if err = r.journal.Put(c); err != nil {
			return ErrUnknown
		}
	}
	if c.Phase == "reserved" && in.State == "claimed" {
		if err = r.executor.Prepare(ctx, *in); err != nil {
			c.Phase = "outcome"
			c.Result = Result{Reason: "failed"}
		} else {
			c.Phase = "executing"
			if err = r.journal.Put(c); err != nil {
				return ErrUnknown
			}
			transition := lifecycleintents.Transition{RuntimeID: runtime.ID, RuntimeGeneration: runtime.Generation, ExpectedRevision: in.Revision, State: "executing"}
			executing, e := r.authority.Transition(ctx, in.ID, transition)
			if e != nil {
				return e
			}
			if executing.State != "executing" || executing.Revision <= in.Revision || intentDigest(executing) != c.Digest {
				return ErrOwnership
			}
			in = &executing
			c.Intent = executing
			if err = r.journal.Put(c); err != nil {
				return ErrUnknown
			}
			result, e := r.executor.Execute(ctx, *in)
			if e != nil || result.Reason != "applied" {
				result = Result{Reason: "outcome_unknown"}
			}
			c.Phase = "outcome"
			c.Result = result
		}
	} else if c.Phase == "reserved" || c.Phase == "executing" {
		c.Phase = "outcome"
		c.Result = Result{Reason: "outcome_unknown"}
	}
	c.Intent = *in
	if err = r.journal.Put(c); err != nil {
		return ErrUnknown
	}
	return r.complete(ctx, runtime, c)
}

func (r *Runner) complete(ctx context.Context, runtime lifecycleintents.Runtime, c record) error {
	in := c.Intent
	state := "failed"
	if c.Result.Reason == "applied" {
		state = "completed"
	}
	out, err := r.authority.Transition(ctx, in.ID, lifecycleintents.Transition{RuntimeID: runtime.ID, RuntimeGeneration: runtime.Generation, ExpectedRevision: in.Revision, State: state, Reason: c.Result.Reason, ResultSessionID: c.Result.SessionID})
	if errors.Is(err, lifecycleintents.ErrConflict) {
		// The authority refused this exact recorded revision/outcome. Preserve
		// the effect evidence without treating it as accepted or retrying it.
		// A later claim can advance once the server has terminalized this ID.
		c.Phase = "quarantined"
		if r.journal.Put(c) != nil {
			return ErrUnknown
		}
		return ErrUnknown
	}
	if err != nil {
		return err
	}
	if out.State != state || out.Reason != c.Result.Reason || out.ResultSessionID != c.Result.SessionID || intentDigest(out) != c.Digest {
		return ErrOwnership
	}
	if out.State == "completed" {
		if err = r.executor.Committed(ctx, out); err != nil {
			return err
		}
	}
	c.Phase = "terminal"
	if err = r.journal.Put(c); err != nil {
		return ErrUnknown
	}
	return nil
}
