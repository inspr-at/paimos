// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>
package lifecycleclient

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/backend/agentmessage"
	"github.com/inspr-at/paimos/backend/localjournal"
	"github.com/inspr-at/paimos/backend/runtimeconsumer"
)

type ConsumerEffect func(context.Context, agentmessage.ConsumerPage) (string, error)

var ErrCircuit = errors.New("consumer transient recovery budget exhausted")

// ErrPersistence requires an explicit repair before this consumer can retry.
// It retains ErrUnknown compatibility without exposing private journal details.
var ErrPersistence = fmt.Errorf("consumer recovery journal unavailable: %w", ErrUnknown)

type consumerRecord struct {
	Key          string                            `json:"key"`
	Registration agentmessage.ConsumerRegistration `json:"registration"`
	Lease        string                            `json:"lease"`
	Stream       agentmessage.ConsumerStream       `json:"stream"`
	RequestKey   string                            `json:"request_key,omitempty"`
	Nonce        string                            `json:"nonce,omitempty"`
	Attempt      *agentmessage.ConsumerAttempt     `json:"attempt,omitempty"`
	Phase        string                            `json:"phase"`
	Blocked      string                            `json:"blocked,omitempty"`
	Reason       string                            `json:"reason,omitempty"`
	Failures     int                               `json:"failures"`
	Next         time.Time                         `json:"next,omitempty"`
}
type Consumers struct {
	// Repair takes exclusive ownership after all independent steps drain.
	lifecycleMu sync.RWMutex
	streams     runtimeconsumer.StreamGate
	failedMu    sync.Mutex
	http        *HTTP
	journal     *localjournal.Journal[consumerRecord]
	failed      map[string]consumerRecord
}

func NewConsumers(directory string, h *HTTP) (*Consumers, error) {
	j, e := localjournal.Open(localjournal.Config[consumerRecord]{Directory: directory, Prefix: "fenced-consumers", Version: 1, MaxBytes: 2 << 20, MaxRecords: 512, Key: func(c consumerRecord) (string, error) { return c.Key, nil }, Validate: func(c consumerRecord) error {
		if (c.Blocked != "" && c.Blocked != "outcome_unknown" && c.Blocked != "legacy_handoff_required") || len(c.Key) != 64 || !ValidProof(c.Lease) || c.Failures < 0 || c.Failures > 3 {
			return ErrUnknown
		}
		switch c.Phase {
		case "idle", "claiming", "claimed", "executing", "applied", "unknown":
		default:
			return ErrUnknown
		}
		if c.Phase != "idle" && (!ValidProof(c.Nonce) || uuid.Validate(c.RequestKey) != nil) {
			return ErrUnknown
		}
		if c.Attempt != nil && (uuid.Validate(c.Attempt.ID) != nil || c.Attempt.StreamID != c.Stream.ID || c.Attempt.RequestKey != c.RequestKey) {
			return ErrUnknown
		}
		return nil
	}})
	if e != nil {
		return nil, ErrUnknown
	}
	return &Consumers{http: h, journal: j, failed: map[string]consumerRecord{}}, nil
}
func consumerKey(in agentmessage.ConsumerRegistration) string {
	in.Generation = ""
	raw, _ := json.Marshal(in)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func (c *Consumers) route(s string) string {
	return fmt.Sprintf("/api/projects/%d/consumers/v1%s", c.http.project, s)
}
func (c *Consumers) headers(r consumerRecord) map[string]string {
	h := map[string]string{agentmessage.ConsumerLeaseHeader: r.Lease}
	if r.Nonce != "" {
		h[agentmessage.ConsumerAttemptHeader] = r.Nonce
	}
	return h
}
func (c *Consumers) put(r consumerRecord) error {
	if c.journal.Put(r) != nil {
		// Retain the exact failed checkpoint. A later Step must not load an
		// older durable record and forget backoff, custody, or an applied effect.
		c.failedMu.Lock()
		c.failed[r.Key] = r
		c.failedMu.Unlock()
		return ErrPersistence
	}
	return nil
}

// Step performs the server's claim -> execute -> exact completion sequence.
// Only the first execute response with a payload permits a vendor handoff.
// A lost response or interrupted local effect is quarantined, never redelivered.
func (c *Consumers) Step(ctx context.Context, in agentmessage.ConsumerRegistration, ready bool, effect ConsumerEffect) (returnErr error) {
	c.lifecycleMu.RLock()
	defer c.lifecycleMu.RUnlock()
	// A target's revisions and owner generations share one local gate. The
	// server remains authoritative for replacement targets and stream fencing.
	release, err := c.streams.Acquire(ctx, in.TargetID+":"+in.Kind)
	if err != nil {
		return err
	}
	defer release()
	key := consumerKey(in)
	c.failedMu.Lock()
	latched := false
	for _, failed := range c.failed {
		if failed.Registration.TargetID == in.TargetID && failed.Registration.Kind == in.Kind {
			latched = true
			break
		}
	}
	c.failedMu.Unlock()
	if latched {
		return ErrPersistence
	}
	r := consumerRecord{Key: key, Registration: in, Phase: "idle"}
	found := false
	for _, saved := range c.journal.Snapshot() {
		if saved.Key == key {
			r = saved
			found = true
			break
		}
	}
	if !found {
		lease, e := NewProof()
		if e != nil {
			return e
		}
		r.Lease = lease
		r.Registration.Generation = uuid.NewString()
		if e = c.put(r); e != nil {
			return e
		}
	}
	if r.Blocked == "outcome_unknown" {
		return ErrUnknown
	}
	if r.Blocked == "legacy_handoff_required" && time.Now().Before(r.Next) {
		return ErrHandoff
	}
	if r.Phase == "applied" || r.Phase == "unknown" {
		return c.complete(ctx, &r)
	}
	if r.Phase == "executing" {
		r.Phase = "unknown"
		if e := c.put(r); e != nil {
			return e
		}
		return c.complete(ctx, &r)
	}
	if time.Now().Before(r.Next) {
		return ErrTransport
	}
	if r.Stream.ID == "" && r.Failures >= 3 {
		return ErrCircuit
	}
	defer func() {
		if errors.Is(returnErr, ErrPersistence) {
			return
		}
		if errors.Is(returnErr, ErrUnknown) {
			r.Blocked = "outcome_unknown"
			returnErr = errors.Join(returnErr, c.put(r))
			return
		}
		if errors.Is(returnErr, ErrHandoff) {
			r.Blocked = "legacy_handoff_required"
			r.Next = time.Now().Add(consumerRetryDelay(r.Key, 30*time.Second))
			returnErr = errors.Join(returnErr, c.put(r))
			return
		}
		if returnErr != nil && !errors.Is(returnErr, ErrCircuit) && r.Phase != "unknown" {
			r.Failures++
			if r.Failures > 3 {
				r.Failures = 3
			}
			r.Next = time.Now().Add(consumerRetryDelay(r.Key, time.Duration(1<<r.Failures)*time.Second))
			returnErr = errors.Join(returnErr, c.put(r))
		}
	}()
	// Keep the same proof alive while a transient effect circuit awaits repair.
	// Refreshes are bounded to every thirty seconds; they do not retry effects.
	stream := r.Stream
	refresh := stream.ID == ""
	if !refresh {
		expiry, e := time.Parse(time.RFC3339Nano, stream.ExpiresAt)
		if e != nil || !time.Now().Before(expiry) {
			return ErrOwnership
		}
		refresh = time.Until(expiry) < 90*time.Second
	}
	if refresh {
		if e := c.http.Request(ctx, http.MethodPost, c.route("/streams"), c.headers(r), r.Registration, &stream); e != nil {
			return e
		}
		if stream.SchemaVersion != 1 || uuid.Validate(stream.ID) != nil || stream.Generation != r.Registration.Generation || stream.Kind != in.Kind || stream.Revision <= 0 || (r.Stream.ID != "" && (stream.ID != r.Stream.ID || stream.Revision != r.Stream.Revision)) {
			return ErrOwnership
		}
		r.Stream = stream
		r.Blocked = ""
		if e := c.put(r); e != nil {
			return e
		}
	}
	if r.Failures >= 3 {
		return ErrCircuit
	}
	if !ready {
		return nil
	}
	if r.Phase == "idle" {
		nonce, e := NewProof()
		if e != nil {
			return e
		}
		r.RequestKey = uuid.NewString()
		r.Nonce = nonce
		r.Phase = "claiming"
		r.Attempt = nil
		if e = c.put(r); e != nil {
			return e
		}
	}
	if r.Phase == "claiming" {
		var page agentmessage.ConsumerPage
		e := c.http.Request(ctx, http.MethodPost, c.route("/streams/"+stream.ID+"/claim"), c.headers(r), agentmessage.ConsumerClaim{ExpectedRevision: stream.Revision, RequestKey: r.RequestKey}, &page)
		if e != nil {
			return e
		}
		if page.SchemaVersion != 1 || page.Delivery != nil || page.Attention != nil {
			return ErrOwnership
		}
		if page.Attempt == nil {
			r.Phase = "idle"
			r.Nonce = ""
			r.RequestKey = ""
			r.Failures = 0
			r.Next = time.Time{}
			return c.put(r)
		}
		a := page.Attempt
		if a.StreamID != stream.ID || a.Revision != stream.Revision || a.RequestKey != r.RequestKey || uuid.Validate(a.ID) != nil || a.Cursor <= 0 {
			return ErrOwnership
		}
		r.Attempt = a
		switch a.State {
		case "claimed":
			r.Phase = "claimed"
		case "released", "completed":
			r.Phase = "idle"
			r.Attempt = nil
			r.Nonce = ""
			r.RequestKey = ""
			return c.put(r)
		default:
			r.Phase = "unknown"
		}
		if e = c.put(r); e != nil {
			return e
		}
		if r.Phase == "unknown" {
			return c.complete(ctx, &r)
		}
	}
	r.Phase = "executing"
	if e := c.put(r); e != nil {
		return e
	}
	var page agentmessage.ConsumerPage
	e := c.http.Request(ctx, http.MethodPost, c.route("/streams/"+stream.ID+"/attempts/"+r.Attempt.ID+"/execute"), c.headers(r), map[string]int64{"expected_revision": stream.Revision}, &page)
	if e != nil {
		return e
	}
	if page.SchemaVersion != 1 || page.Attempt == nil || page.Attempt.ID != r.Attempt.ID || page.Attempt.StreamID != stream.ID || page.Attempt.RequestKey != r.RequestKey || page.Attempt.Revision != stream.Revision {
		return ErrOwnership
	}
	if page.Attempt.State == "released" && page.Delivery == nil && page.Attention == nil {
		r.Phase = "idle"
		r.Attempt = nil
		r.Nonce = ""
		r.RequestKey = ""
		return c.put(r)
	}
	if page.Attempt.State != "executing" || (in.Kind == "fallback" && (page.Delivery == nil || page.Attention != nil)) || (in.Kind == "attention" && (page.Attention == nil || page.Delivery != nil)) {
		r.Phase = "unknown"
		if e = c.put(r); e != nil {
			return e
		}
		return c.complete(ctx, &r)
	}
	reason, e := effect(ctx, page)
	if e != nil {
		r.Phase = "unknown"
	} else {
		r.Phase = "applied"
		r.Reason = reason
	}
	if e = c.put(r); e != nil {
		return e
	}
	return c.complete(ctx, &r)
}
func (c *Consumers) complete(ctx context.Context, r *consumerRecord) error {
	if r.Attempt == nil {
		return ErrUnknown
	}
	outcome := "applied"
	reason := r.Reason
	if r.Phase == "unknown" {
		outcome = "outcome_unknown"
		reason = ""
	}
	var out agentmessage.ConsumerResult
	err := c.http.Request(ctx, http.MethodPost, c.route("/streams/"+r.Stream.ID+"/attempts/"+r.Attempt.ID+"/complete"), c.headers(*r), agentmessage.ConsumerCompletion{ExpectedRevision: r.Stream.Revision, Outcome: outcome, EffectiveLevel: "simple", FallbackReason: reason}, &out)
	if errors.Is(err, ErrUnknown) {
		r.Blocked = "outcome_unknown"
		if writeErr := c.put(*r); writeErr != nil {
			return errors.Join(err, writeErr)
		}
	}
	if err != nil {
		return err
	}
	if out.SchemaVersion != 1 || (outcome == "applied" && (out.State != "completed" || out.Cursor != r.Attempt.Cursor)) || (outcome == "outcome_unknown" && out.State != "outcome_unknown") {
		return ErrOwnership
	}
	if outcome == "outcome_unknown" {
		r.Blocked = "outcome_unknown"
		return errors.Join(ErrUnknown, c.put(*r))
	}
	r.Phase = "idle"
	r.Attempt = nil
	r.Nonce = ""
	r.RequestKey = ""
	r.Reason = ""
	r.Failures = 0
	r.Next = time.Time{}
	return c.put(*r)
}

// Repair retains the exact stream proofs and every ambiguous attempt. A claim
// may be retried with its original nonce; executing work can only become unknown.
func (c *Consumers) Repair() error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	// The exclusive lifecycle lock also excludes every failed-map writer.
	// Flush latched checkpoints before evaluating recovery. Successful storage
	// alone cannot clear unknown custody or an executing effect.
	for key, r := range c.failed {
		if c.journal.Put(r) != nil {
			return ErrPersistence
		}
		delete(c.failed, key)
	}
	for _, r := range c.journal.Snapshot() {
		if r.Blocked == "outcome_unknown" || r.Phase == "executing" || r.Phase == "unknown" {
			return ErrUnknown
		}
	}
	for _, r := range c.journal.Snapshot() {
		if r.Stream.ID != "" && r.Phase != "applied" {
			expiry, e := time.Parse(time.RFC3339Nano, r.Stream.ExpiresAt)
			if e != nil {
				return ErrOwnership
			}
			if !time.Now().Before(expiry) {
				lease, e := NewProof()
				if e != nil {
					return e
				}
				r.Lease = lease
				r.Registration.Generation = uuid.NewString()
				r.Stream = agentmessage.ConsumerStream{}
				r.Phase = "idle"
				r.Attempt = nil
				r.Nonce = ""
				r.RequestKey = ""
			}
		}
		r.Failures = 0
		r.Next = time.Time{}
		if e := c.put(r); e != nil {
			return e
		}
	}
	return nil
}

// Stable per-stream jitter disperses retries without changing their base budget
// after journal recovery. Both transient retries and handoff checks add 0–25%.
func consumerRetryDelay(key string, base time.Duration) time.Duration {
	digest := sha256.Sum256([]byte(key))
	fraction := time.Duration(uint16(digest[0])<<8 | uint16(digest[1]))
	return base + fraction*(base/4)/65535
}
