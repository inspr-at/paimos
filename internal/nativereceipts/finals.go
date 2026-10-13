// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// RetentionApproval is resolved from an explicit owner privacy grant. It is
// neither inferred from local history consent nor renewed by retrying.
type RetentionApproval struct {
	OwnerPersonID string    `json:"owner_person_id"`
	Approved      bool      `json:"approved"`
	Until         time.Time `json:"until"`
}

type finalEntry struct {
	Delivery      nativecontract.FinalPublish    `json:"delivery"`
	Grant         nativecontract.FinalDrainGrant `json:"grant"`
	Approval      RetentionApproval              `json:"approval"`
	SHA256        string                         `json:"sha256"`
	TextBytes     int                            `json:"text_bytes"`
	CreatedAt     time.Time                      `json:"created_at"`
	ExpiresAt     time.Time                      `json:"expires_at"`
	Attempts      uint32                         `json:"attempts"`
	NextAttemptAt time.Time                      `json:"next_attempt_at"`
	State         string                         `json:"state"`
	MessageID     *string                        `json:"message_id"`
	Reason        *nativecontract.Reason         `json:"reason"`
}

// FinalStatus exposes metadata only. Pending is local, not a final-persisted
// receipt and not an acknowledgement of any input or process exit.
type FinalStatus struct {
	FinalID, State           string
	MessageID                *string
	Reason                   *nativecontract.Reason
	Attempts                 uint32
	NextAttemptAt, ExpiresAt time.Time
}

func finalStatus(f finalEntry) FinalStatus {
	r := FinalStatus{FinalID: f.Delivery.FinalID, State: f.State, Attempts: f.Attempts, NextAttemptAt: f.NextAttemptAt, ExpiresAt: f.ExpiresAt}
	if f.MessageID != nil {
		id := *f.MessageID
		r.MessageID = &id
	}
	if f.Reason != nil {
		reason := *f.Reason
		r.Reason = &reason
	}
	return r
}

// FinalDigest binds one final ID to exact text/input/turn/runtime/grant. Request
// retries may use a new request UUID; they cannot alter the final's identity.
func FinalDigest(in nativecontract.FinalPublish) (string, error) {
	in.RequestID = ""
	return Digest(in)
}

func (s *Store) recoverFinals() error {
	ids := map[string]bool{}
	bytes := 0
	changed := false
	for i := range s.finals {
		f := &s.finals[i]
		if !validUUID(f.Delivery.FinalID) || ids[f.Delivery.FinalID] || !f.Approval.Approved || !validUUID(f.Approval.OwnerPersonID) ||
			!ValidDigest(f.SHA256) || f.ExpiresAt.IsZero() || f.CreatedAt.IsZero() || f.ExpiresAt.After(f.CreatedAt.Add(MaxRetention)) ||
			f.ExpiresAt.After(f.Grant.ExpiresAt) || f.ExpiresAt.After(f.Approval.Until) || f.Attempts > 128 {
			return Fail(nativecontract.ReasonRetentionUnapproved)
		}
		ids[f.Delivery.FinalID] = true
		if err := Validate(s.validator, "NativeFinalDrainGrant", f.Grant); err != nil {
			return err
		}
		if !f.Grant.Binding.Equal(f.Delivery.ExpectedBinding) || f.Grant.GrantID != f.Delivery.DrainGrantID {
			return Fail(nativecontract.ReasonIdentityMismatch)
		}
		if f.State == "pending" {
			if len(f.Delivery.Text) > MaxTextBytes || len(f.Delivery.Text) != f.TextBytes || !utf8.ValidString(f.Delivery.Text) {
				return Fail(nativecontract.ReasonInvalidInput)
			}
			if err := Validate(s.validator, "NativeFinalPublish", f.Delivery); err != nil {
				return err
			}
			digest, err := FinalDigest(f.Delivery)
			if err != nil || digest != f.SHA256 {
				return Fail(nativecontract.ReasonIdentityMismatch)
			}
			if !s.now().Before(f.ExpiresAt) {
				expireFinal(f)
				changed = true
			}
		} else if f.State != "persisted" && f.State != "rejected" && f.State != "expired" || f.Delivery.Text != "" || f.State == "persisted" && (f.MessageID == nil || !validUUID(*f.MessageID)) || f.State != "persisted" && (f.MessageID != nil || f.Reason == nil) {
			return Fail(nativecontract.ReasonInvalidInput)
		}
		bytes += len(f.Delivery.Text)
	}
	if bytes > MaxFinalBytes {
		return Fail(nativecontract.ReasonBufferFull)
	}
	if changed {
		return s.save("finals.json", s.finals, outboxFileBytes)
	}
	return nil
}

func expireFinal(f *finalEntry) {
	reason := nativecontract.ReasonFinalDeliveryExpired
	f.State, f.Reason, f.Delivery.Text, f.MessageID = "expired", &reason, "", nil
}

// RetainFinal accepts an explicit provider final only after its exact input
// has completed. This method accepts no token chunks or interim tool frames.
func (s *Store) RetainFinal(ctx context.Context, in nativecontract.FinalPublish, grant nativecontract.FinalDrainGrant, approval RetentionApproval, completed nativecontract.Receipt) (FinalStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return FinalStatus{}, err
	}
	if s.closed || s.poisoned {
		return FinalStatus{}, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if len(in.Text) > MaxTextBytes || !utf8.ValidString(in.Text) {
		return FinalStatus{}, Fail(nativecontract.ReasonBufferFull)
	}
	if err := Validate(s.validator, "NativeFinalPublish", in); err != nil {
		return FinalStatus{}, err
	}
	if err := Validate(s.validator, "NativeFinalDrainGrant", grant); err != nil {
		return FinalStatus{}, err
	}
	if !approval.Approved || !validUUID(approval.OwnerPersonID) {
		return FinalStatus{}, Fail(nativecontract.ReasonRetentionUnapproved)
	}
	if !grant.Binding.Equal(in.ExpectedBinding) || grant.GrantID != in.DrainGrantID {
		return FinalStatus{}, Fail(nativecontract.ReasonWrongGeneration)
	}
	if completed.State != "turn_completed" || completed.Operation != "input" || completed.Binding == nil || !completed.Binding.Equal(in.ExpectedBinding) ||
		completed.InputID == nil || *completed.InputID != in.InputID || completed.TurnRef == nil || *completed.TurnRef != in.TurnRef || completed.SourceSequence == "0" {
		return FinalStatus{}, Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err := Validate(s.validator, "NativeReceipt", completed); err != nil {
		return FinalStatus{}, err
	}
	index, err := s.index(in.ExpectedBinding, completed.OperationID)
	if err != nil {
		return FinalStatus{}, err
	}
	if s.entries[index].Receipt.State != "turn_completed" {
		return FinalStatus{}, Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err := Transition(s.entries[index].Receipt, completed); err != nil {
		return FinalStatus{}, err
	}
	digest, err := FinalDigest(in)
	if err != nil {
		return FinalStatus{}, err
	}
	for _, f := range s.finals {
		if f.Delivery.FinalID == in.FinalID {
			if f.SHA256 != digest || f.Approval != approval {
				return FinalStatus{}, Fail(nativecontract.ReasonIdentityMismatch)
			}
			return finalStatus(f), nil
		}
	}
	now := s.now().UTC()
	expires := minTime(now.Add(MaxRetention), minTime(grant.ExpiresAt, approval.Until))
	if !now.Before(expires) {
		return FinalStatus{}, Fail(nativecontract.ReasonPublicationGrantExpired)
	}
	bytes, grantBytes, grantItems := len(in.Text), len(in.Text), 1
	for _, f := range s.finals {
		bytes += len(f.Delivery.Text)
		if f.Grant.GrantID == grant.GrantID {
			grantItems++
			grantBytes += f.TextBytes
		}
	}
	if len(s.finals) >= MaxFinals || bytes > MaxFinalBytes || int64(grantItems) > grant.MaximumFinals || int64(grantBytes) > grant.MaximumBytes {
		return FinalStatus{}, Fail(nativecontract.ReasonBufferFull)
	}
	in.ExpectedBinding = cloneBinding(in.ExpectedBinding)
	grant.Binding = cloneBinding(grant.Binding)
	f := finalEntry{Delivery: in, Grant: grant, Approval: approval, SHA256: digest, TextBytes: len(in.Text), CreatedAt: now, ExpiresAt: expires, State: "pending", NextAttemptAt: now}
	next := append(append([]finalEntry{}, s.finals...), f)
	if err = s.save("finals.json", next, outboxFileBytes); err != nil {
		return FinalStatus{}, err
	}
	s.finals = next
	return finalStatus(f), nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// RetryFinal uses the current authenticated final-only sink on every attempt.
// API failure, lost acknowledgement, child exit and daemon restart keep text
// until a correlated receipt or explicit expiry/rejection. No grant is renewed.
func (s *Store) RetryFinal(ctx context.Context, finalID string, sink nativecontract.FinalSink) (FinalStatus, error) {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return FinalStatus{}, err
	}
	if s.closed || s.poisoned {
		s.mu.Unlock()
		return FinalStatus{}, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	i := -1
	for j, f := range s.finals {
		if f.Delivery.FinalID == finalID {
			i = j
			break
		}
	}
	if i < 0 {
		s.mu.Unlock()
		return FinalStatus{}, Fail(nativecontract.ReasonInvalidInput)
	}
	f := s.finals[i]
	if f.State != "pending" {
		s.mu.Unlock()
		return finalStatus(f), nil
	}
	if !s.now().Before(f.ExpiresAt) {
		expireFinal(&f)
		err := s.replaceFinal(i, f)
		s.mu.Unlock()
		return finalStatus(f), err
	}
	if sink == nil || s.busy[finalID] || s.now().Before(f.NextAttemptAt) || f.Attempts >= 128 {
		s.mu.Unlock()
		return finalStatus(f), Fail(nativecontract.ReasonDraining)
	}
	f.Attempts++
	f.NextAttemptAt = s.now().Add(time.Second * time.Duration(1<<min(f.Attempts-1, 6)))
	if err := s.replaceFinal(i, f); err != nil {
		s.mu.Unlock()
		return finalStatus(f), err
	}
	s.busy[finalID] = true
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ack, err := sink.Persist(ctx, f.Delivery)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.busy, finalID)
	if s.closed || s.poisoned {
		return finalStatus(f), Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if err != nil {
		return finalStatus(f), err
	}
	if err = Validate(s.validator, "NativeFinalReceipt", ack); err != nil {
		return finalStatus(f), err
	}
	if ack.FinalID != finalID || ack.State == "persisted" && (ack.MessageID == nil || ack.Reason != nil) || ack.State != "persisted" && (ack.MessageID != nil || ack.Reason == nil) {
		return finalStatus(f), Fail(nativecontract.ReasonIdentityMismatch)
	}
	f.State, f.MessageID, f.Reason, f.Delivery.Text = ack.State, ack.MessageID, ack.Reason, ""
	if err = s.replaceFinal(i, f); err != nil {
		return finalStatus(s.finals[i]), err
	}
	return finalStatus(f), nil
}

func (s *Store) replaceFinal(i int, f finalEntry) error {
	next := append([]finalEntry{}, s.finals...)
	next[i] = f
	if err := s.save("finals.json", next, outboxFileBytes); err != nil {
		return err
	}
	s.finals = next
	return nil
}

func (s *Store) FinalStatuses() []FinalStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]FinalStatus, 0, len(s.finals))
	for _, f := range s.finals {
		result = append(result, finalStatus(f))
	}
	return result
}

// PruneExpired removes only outcomes whose original publication window ended.
// Possible native writes are never pruned here. Inspect statuses before pruning.
func (s *Store) PruneExpired(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	next := []finalEntry{}
	for _, f := range s.finals {
		if s.busy[f.Delivery.FinalID] || s.now().Before(f.ExpiresAt) {
			next = append(next, f)
		}
	}
	if err := s.save("finals.json", next, outboxFileBytes); err != nil {
		return err
	}
	s.finals = next
	return nil
}
