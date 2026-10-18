// SPDX-License-Identifier: AGPL-3.0-only

package nativequota

import (
	"sort"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/capacity"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type window struct {
	reading        *capacity.Reading
	allowed        *bool
	allowanceAt    time.Time
	allowanceReset *time.Time
	reset          *time.Time
	epoch          uint64
}

// Collector is scoped to exactly one owned connection/account/profile. A
// successor gets a new collector; it cannot inherit a predecessor's readings.
// All memory is bounded, normalized and local. No raw frame is retained.
type Collector struct {
	mu             sync.Mutex
	binding        Binding
	qualification  *Qualification
	clock          Clock
	windows        map[string]window
	readiness      Readiness
	sequence       uint64
	seenSequence   bool
	epoch          uint64
	failure        string
	failurePending bool
}

func New(binding Binding, qualification *Qualification, clock Clock) (*Collector, error) {
	if clock == nil || clock.Now().IsZero() || !binding.valid() {
		return nil, ErrRuntime
	}
	// Neither a caller's pointer nor a later profile mutation may retarget us.
	if binding.Runtime.RunID != nil {
		id := *binding.Runtime.RunID
		binding.Runtime.RunID = &id
	}
	c := &Collector{binding: binding, clock: clock, windows: make(map[string]window)}
	if qualification != nil {
		if !qualified(binding, *qualification) {
			return nil, ErrQualification
		}
		q := *qualification
		q.CodexBuckets = append([]string(nil), q.CodexBuckets...)
		c.qualification = &q
	}
	c.readiness = Readiness{SignIn: Fact{State: "unknown"}, BinaryPin: Fact{State: "unknown"},
		HostSlots: Slots{Fact: Fact{State: "unknown"}}, AccountSlots: Slots{Fact: Fact{State: "unknown"}}}
	return c, nil
}

func (c *Collector) ObserveReadiness(binding Binding, readiness Readiness) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return err
	}
	if !readiness.valid(c.clock.Now()) {
		return ErrProtocol
	}
	// Delayed health reports cannot undo a newer successful identity fact.
	if readiness.SignIn.ObservedAt.Before(c.readiness.SignIn.ObservedAt) || readiness.BinaryPin.ObservedAt.Before(c.readiness.BinaryPin.ObservedAt) ||
		readiness.HostSlots.ObservedAt.Before(c.readiness.HostSlots.ObservedAt) || readiness.AccountSlots.ObservedAt.Before(c.readiness.AccountSlots.ObservedAt) {
		return ErrSequence
	}
	c.readiness = readiness
	return nil
}

// Observe consumes only the quota event from this already authenticated owned
// stream. sequence is the owner's monotonic source sequence, not a vendor or
// client claim. Other stream kinds are excluded by the codec. No I/O occurs.
func (c *Collector) Observe(binding Binding, sequence uint64, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return err
	}
	if c.qualification == nil {
		return ErrQualification
	}
	if c.seenSequence && sequence <= c.sequence {
		return ErrSequence
	}
	if err := boundJSON(raw); err != nil {
		return err
	}
	now := c.clock.Now().UTC()
	var patches []patch
	var err error
	if binding.Harness == "claude" {
		patches, err = decodeClaude(raw, now, c.binding.Identity.NativeSessionRef)
	} else {
		patches, err = decodeCodex(raw, now, c.binding.Identity.NativeThreadRef, c.qualification.CodexBuckets)
	}
	if err != nil {
		return err
	}
	// Validate the entire batch and its new scope count before changing state.
	count := len(c.windows)
	for _, p := range patches {
		if _, exists := c.windows[p.scope]; !exists {
			count++
		}
	}
	if count > MaxWindows {
		return ErrBounds
	}
	for _, p := range patches {
		w := c.windows[p.scope]
		if p.reset != nil && (w.reset == nil || !w.reset.Equal(*p.reset)) {
			// An incomplete new window cannot reuse the previous window's usage.
			w.reading = nil
			w.reset = copyTime(p.reset)
		}
		if p.reading != nil {
			w.reading = copyReading(p.reading)
			w.epoch = c.epoch
			w.reset = copyTime(&p.reading.ResetsAt)
			c.failurePending = false
		}
		// A sparse "allowed" event does not recover a retained vendor denial.
		// An explicit denial always fences the named scope, without fabricating
		// utilization or refreshing a remembered percentage.
		if p.allowed != nil && (!*p.allowed || p.complete || w.allowed == nil || *w.allowed) {
			allowed := *p.allowed
			w.allowed = &allowed
			w.allowanceAt = now
			w.allowanceReset = copyTime(p.reset)
			if w.allowanceReset == nil {
				w.allowanceReset = copyTime(w.reset)
			}
		}
		c.windows[p.scope] = w
	}
	c.sequence, c.seenSequence = sequence, true
	return nil
}

// Failed records only a quota check failure. It neither alters readiness nor
// overwrites a vendor stop. A complete later measurement restores its own scope.
func (c *Collector) Failed(binding Binding, category string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return err
	}
	switch category {
	case "timeout", "transport", "protocol", "unsupported":
	default:
		return ErrProtocol
	}
	c.epoch++
	c.failure = category
	c.failurePending = true
	return nil
}

// IdleCapability never launches Claude get_usage (or any other probe). Adding
// a production idle capability needs a separate compiled, pinned qualification.
func (*Collector) IdleCapability() string { return "unsupported" }

func (c *Collector) Snapshot(binding Binding, scope string) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return Snapshot{}, err
	}
	if !scopeValid(scope) {
		return Snapshot{}, ErrScope
	}
	return c.snapshot(scope, c.clock.Now().UTC()), nil
}

func (c *Collector) snapshot(scope string, now time.Time) Snapshot {
	s := Snapshot{Quota: nativecontract.Quota{AccountID: c.binding.AccountID, ProfileBindingRevision: c.binding.ProfileBindingRevision,
		Source: c.binding.Harness + "_stream", Scope: scope, State: "unknown", ObservedAt: now, FreshUntil: now},
		Measurement: "missing", Allowance: Allowance{Fact: Fact{State: "unknown"}}, Readiness: c.readiness}
	if c.qualification == nil {
		s.Quota.Source, s.Quota.State, s.Measurement = "unsupported", "unsupported", "unsupported"
		return s
	}
	w := c.windows[scope]
	if w.allowed != nil && (w.allowanceReset == nil || now.Before(*w.allowanceReset)) {
		if !*w.allowed {
			s.Allowance = Allowance{Fact: Fact{State: "limited", ObservedAt: w.allowanceAt}, ResetsAt: copyTime(w.allowanceReset)}
		} else if !now.Before(w.allowanceAt) && now.Before(w.allowanceAt.Add(FreshFor)) {
			s.Allowance = Allowance{Fact: Fact{State: "allowed", ObservedAt: w.allowanceAt}, ResetsAt: copyTime(w.allowanceReset)}
		}
	}
	// Account-wide stops apply to every bucket, without inventing a percentage.
	global := c.windows["account"]
	if global.allowed != nil && !*global.allowed && (global.allowanceReset == nil || now.Before(*global.allowanceReset)) {
		s.Allowance = Allowance{Fact: Fact{State: "limited", ObservedAt: global.allowanceAt}, ResetsAt: copyTime(global.allowanceReset)}
	}
	if w.reading != nil {
		r := w.reading
		s.Quota.ObservedAt = r.ReadAt
		s.Quota.FreshUntil = minTime(r.ReadAt.Add(FreshFor), r.ResetsAt)
		s.Quota.ResetsAt = copyTime(&r.ResetsAt)
		if now.Before(r.ReadAt) || !now.Before(s.Quota.FreshUntil) {
			s.Measurement = "stale"
		} else if w.epoch == c.epoch {
			s.Quota.UsedPercent = copyFloat(&r.UsedPercent)
			s.Quota.State, s.Measurement = "known", "fresh"
		}
	}
	if c.failure != "" && (w.reading == nil || w.epoch != c.epoch) {
		s.Quota.UsedPercent = nil
		s.Quota.State, s.Measurement, s.Failure = "unknown", "failed", c.failure
	}
	if s.Allowance.State == "limited" {
		s.Quota.State = "limited"
		if w.reading == nil {
			s.Quota.ObservedAt = s.Allowance.ObservedAt
			s.Quota.ResetsAt = copyTime(s.Allowance.ResetsAt)
		}
	}
	return s
}

// ReadingsBody matches the existing /agent-accounts/{id}/readings ingress.
// UsageProbe selects its current enrollment-revision and owner-consent fence.
// It does NOT report available=false or change the sign-in probe ingress.
type ReadingsBody struct {
	Readings        []capacity.Reading `json:"readings"`
	UsageProbe      bool               `json:"usage_probe"`
	BindingRevision int64              `json:"binding_revision"`
}

// Readings returns a bounded, detached snapshot for I2 to send through existing
// account ingress. AccountID is always the checked binding's account; do not
// cache/replay this body after a rebind or bypass server-side consent/privacy.
func (c *Collector) Readings(binding Binding) (ReadingsBody, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.binding.check(binding); err != nil {
		return ReadingsBody{}, err
	}
	now := c.clock.Now().UTC()
	out := ReadingsBody{UsageProbe: true, BindingRevision: c.binding.AccountBindingRevision}
	keys := make([]string, 0, len(c.windows))
	for key := range c.windows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		w := c.windows[key]
		s := c.snapshot(key, now)
		if s.Measurement != "fresh" || w.reading == nil {
			continue
		}
		r := *copyReading(w.reading)
		r.Source, r.RunID, r.Phase, r.Plan = "agentd", "", "", ""
		if s.Allowance.State == "limited" {
			no := false
			r.OrdinaryUsageAllowed = &no
		} else if s.Allowance.State == "allowed" {
			yes := true
			r.OrdinaryUsageAllowed = &yes
		}
		out.Readings = append(out.Readings, r)
	}
	if len(out.Readings) == 0 {
		return ReadingsBody{}, ErrNoReadings
	}
	return out, nil
}

func copyTime(v *time.Time) *time.Time {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func copyFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func copyReading(v *capacity.Reading) *capacity.Reading {
	if v == nil {
		return nil
	}
	out := *v
	if v.OrdinaryUsageAllowed != nil {
		allowed := *v.OrdinaryUsageAllowed
		out.OrdinaryUsageAllowed = &allowed
	}
	return &out
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
