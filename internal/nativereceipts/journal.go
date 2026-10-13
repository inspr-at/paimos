// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"context"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

func validUUID(s string) bool { _, err := uuid.Parse(s); return len(s) == 36 && err == nil }

func (s *Store) validateIntent(in Intent) error {
	if err := Validate(s.validator, "NativeReceipt", in.Receipt); err != nil {
		return err
	}
	if in.Receipt.Binding == nil || !validUUID(in.ActorID) || !ValidDigest(in.PayloadSHA256) || in.ExpiresAt.IsZero() ||
		in.Receipt.Operation == "input" && in.Receipt.InputID == nil ||
		in.Receipt.Operation == "stop" && !ValidDigest(in.ProcessIdentityDigest) ||
		in.Receipt.Operation == "approval" && !ValidDigest(in.EffectDigest) {
		return Fail(nativecontract.ReasonInvalidInput)
	}
	return nil
}

func (s *Store) recover() error {
	if len(s.entries) > MaxReceipts || len(s.finals) > MaxFinals {
		return Fail(nativecontract.ReasonBufferFull)
	}
	ids, requests := map[string]bool{}, map[string]bool{}
	changed := false
	for i := range s.entries {
		in := &s.entries[i]
		if err := s.validateIntent(*in); err != nil {
			return err
		}
		if len(in.Reports) < 1 || len(in.Reports) > 16 || in.ReportOffset < 0 || in.ReportOffset > len(in.Reports) || in.ReportAttempts > 65535 {
			return Fail(nativecontract.ReasonInvalidInput)
		}
		for j, report := range in.Reports {
			if err := Validate(s.validator, "NativeReceipt", report); err != nil {
				return err
			}
			if j > 0 {
				if err := Transition(in.Reports[j-1], report); err != nil {
					return err
				}
			}
		}
		if err := Transition(in.Reports[len(in.Reports)-1], in.Receipt); err != nil {
			return err
		}
		if in.Reports[len(in.Reports)-1].State != in.Receipt.State {
			return Fail(nativecontract.ReasonIdentityMismatch)
		}
		key := in.Receipt.Binding.TenantID + "/" + in.Receipt.OperationID
		req := in.Receipt.Binding.TenantID + "/" + in.Receipt.RequestID
		if ids[key] || requests[req] {
			return Fail(nativecontract.ReasonIdentityMismatch)
		}
		ids[key], requests[req] = true, true
		if in.Receipt.State == "write_started" {
			r := nativecontract.ReasonInputOutcomeUnconfirmed
			if in.Receipt.Operation == "stop" {
				r = nativecontract.ReasonOwnershipUnconfirmed
			}
			in.Receipt.State, in.Receipt.Reason, in.Receipt.UpdatedAt = "uncertain", &r, s.now().UTC()
			if len(in.Reports) == 16 {
				return Fail(nativecontract.ReasonBufferFull)
			}
			in.Reports = append(in.Reports, cloneReceipt(in.Receipt))
			changed = true
		}
	}
	if changed {
		if err := s.save("receipts.json", s.entries, journalFileBytes); err != nil {
			return err
		}
	}
	return s.recoverFinals()
}

// Accept is idempotent for one exact actor/payload/runtime. No prompt, tool
// body or native stdin bytes are accepted by this metadata-only API.
func (s *Store) Accept(ctx context.Context, in Intent) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nativecontract.Receipt{}, err
	}
	if s.closed || s.poisoned {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	in.Receipt = cloneReceipt(in.Receipt)
	if err := s.validateIntent(in); err != nil {
		return nativecontract.Receipt{}, err
	}
	if in.Receipt.State != "accepted" || in.Receipt.SourceSequence != "0" || in.Receipt.Reason != nil {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonInvalidInput)
	}
	for _, prior := range s.entries {
		if prior.Receipt.Binding.TenantID == in.Receipt.Binding.TenantID && (prior.Receipt.OperationID == in.Receipt.OperationID || prior.Receipt.RequestID == in.Receipt.RequestID ||
			in.Receipt.Operation == "input" && prior.Receipt.Operation == "input" && prior.Receipt.Binding.OwnedSessionID == in.Receipt.Binding.OwnedSessionID && sameString(prior.Receipt.InputID, in.Receipt.InputID)) {
			if !SameIntent(prior, in) {
				return nativecontract.Receipt{}, Fail(nativecontract.ReasonIdentityMismatch)
			}
			return cloneReceipt(prior.Receipt), nil
		}
	}
	if !s.now().Before(in.ExpiresAt) {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonGrantExpired)
	}
	if len(s.entries) == MaxReceipts {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonBufferFull)
	}
	pending := 0
	for _, prior := range s.entries {
		if prior.Receipt.Binding.TenantID == in.Receipt.Binding.TenantID && prior.Receipt.Binding.OwnedSessionID == in.Receipt.Binding.OwnedSessionID &&
			prior.Receipt.Operation == "input" && prior.Receipt.State != "turn_completed" && prior.Receipt.State != "rejected" && prior.Receipt.State != "expired" {
			pending++
		}
	}
	if in.Receipt.Operation == "input" && pending >= MaxPendingInputs {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonBufferFull)
	}
	in.Receipt.UpdatedAt = s.now().UTC()
	in.Reports, in.ReportOffset, in.ReportAttempts = []nativecontract.Receipt{cloneReceipt(in.Receipt)}, 0, 0
	next := append(append([]Intent{}, s.entries...), in)
	if err := s.save("receipts.json", next, journalFileBytes); err != nil {
		return nativecontract.Receipt{}, err
	}
	s.entries = next
	return cloneReceipt(in.Receipt), nil
}

func (s *Store) index(binding nativecontract.Binding, operationID string) (int, error) {
	if s.closed || s.poisoned {
		return -1, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	for i, in := range s.entries {
		if in.Receipt.OperationID == operationID && in.Receipt.Binding.TenantID == binding.TenantID {
			if !in.Receipt.Binding.Equal(binding) {
				return -1, Fail(nativecontract.ReasonWrongGeneration)
			}
			return i, nil
		}
	}
	return -1, Fail(nativecontract.ReasonInvalidInput)
}

func (s *Store) advance(i int, after nativecontract.Receipt) (nativecontract.Receipt, error) {
	before := s.entries[i].Receipt
	if err := Validate(s.validator, "NativeReceipt", after); err != nil {
		return nativecontract.Receipt{}, err
	}
	if err := Transition(before, after); err != nil {
		return nativecontract.Receipt{}, err
	}
	if before.State == after.State {
		return cloneReceipt(before), nil
	}
	if len(s.entries[i].Reports) == 16 {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonBufferFull)
	}
	after.UpdatedAt = s.now().UTC()
	next := append([]Intent{}, s.entries...)
	next[i].Receipt = cloneReceipt(after)
	next[i].Reports = append(append([]nativecontract.Receipt{}, next[i].Reports...), cloneReceipt(after))
	if err := s.save("receipts.json", next, journalFileBytes); err != nil {
		return nativecontract.Receipt{}, err
	}
	s.entries = next
	return cloneReceipt(after), nil
}

// Queue, Reject and Expire do not constitute provider evidence. Once I/O is
// possible, failure is uncertain rather than a safely retryable rejection.
func (s *Store) SetIntentState(ctx context.Context, b nativecontract.Binding, id, state string, reason *nativecontract.Reason) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nativecontract.Receipt{}, err
	}
	if state != "queued" && state != "rejected" && state != "expired" && state != "uncertain" {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonInvalidInput)
	}
	i, err := s.index(b, id)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	after := cloneReceipt(s.entries[i].Receipt)
	after.State, after.Reason = state, reason
	return s.advance(i, after)
}

// BeginWrite must commit before Send/Write/Interrupt/Close/Decide touches the
// native transport. The caller holds the exact native writer lock and rechecks
// its monotonic authorization deadline and runtime under that lock first.
// Retrying a possible or acknowledged write never grants another write.
func (s *Store) BeginWrite(ctx context.Context, b nativecontract.Binding, id string) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nativecontract.Receipt{}, err
	}
	i, err := s.index(b, id)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	before := s.entries[i].Receipt
	if before.State != "accepted" && before.State != "queued" {
		return cloneReceipt(before), Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if !s.now().Before(s.entries[i].ExpiresAt) {
		return cloneReceipt(before), Fail(nativecontract.ReasonGrantExpired)
	}
	after := cloneReceipt(before)
	after.State = "write_started"
	return s.advance(i, after)
}

// Observe consumes correlated evidence from the qualified owned provider
// reader. A queue result, turn completion or arbitrary final cannot imply exit.
func (s *Store) Observe(ctx context.Context, b nativecontract.Binding, id string, e nativecontract.NativeEvidence) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nativecontract.Receipt{}, err
	}
	i, err := s.index(b, id)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	in := s.entries[i]
	if e.RequestID != in.Receipt.RequestID || e.ObservedAt.IsZero() || in.Receipt.InputID != nil && e.InputID != *in.Receipt.InputID ||
		in.Receipt.InputID == nil && e.InputID != "" || in.EffectDigest != e.EffectDigest {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonIdentityMismatch)
	}
	if e.Stage != "write_acknowledged" && e.Stage != "turn_started" && e.Stage != "turn_completed" && e.Stage != "applied" {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonInvalidInput)
	}
	after := cloneReceipt(in.Receipt)
	after.State, after.SourceSequence, after.Reason = e.Stage, strconv.FormatUint(e.SourceSequence, 10), nil
	if e.TurnRef != "" {
		after.TurnRef = &e.TurnRef
	}
	return s.advance(i, after)
}

func (s *Store) ConfirmExit(ctx context.Context, b nativecontract.Binding, id string, sequence uint64, e nativecontract.ExitEvidence) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nativecontract.Receipt{}, err
	}
	i, err := s.index(b, id)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	in := s.entries[i]
	if in.Receipt.Operation != "stop" || !e.RootExited || !e.GroupAbsent || e.ObservedAt.IsZero() || e.ProcessIdentityDigest != in.ProcessIdentityDigest {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	after := cloneReceipt(in.Receipt)
	after.State, after.SourceSequence, after.Reason = "exit_confirmed", strconv.FormatUint(sequence, 10), nil
	return s.advance(i, after)
}

func (s *Store) Get(b nativecontract.Binding, id string) (nativecontract.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, err := s.index(b, id)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	return cloneReceipt(s.entries[i].Receipt), nil
}

// Page is a metadata-only keyset page. The owner-socket Host performs caller
// authorization before using this local port. The server has its own scoped
// cursor and current permission checks.
func (s *Store) Page(b nativecontract.Binding, after string, limit int) (nativecontract.ReceiptPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := nativecontract.ReceiptPage{Items: []nativecontract.Receipt{}}
	if s.closed || s.poisoned {
		return o, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if limit == 0 {
		limit = 25
	}
	if limit < 1 || limit > 100 || after != "" && !validUUID(after) {
		return o, Fail(nativecontract.ReasonInvalidInput)
	}
	items := []nativecontract.Receipt{}
	for _, in := range s.entries {
		if in.Receipt.Binding.Equal(b) && in.Receipt.OperationID > after {
			items = append(items, cloneReceipt(in.Receipt))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].OperationID < items[j].OperationID })
	if len(items) > limit {
		o.Truncated = true
		items = items[:limit]
		cursor := items[len(items)-1].OperationID
		o.NextCursor = &cursor
	}
	o.Items = items
	return o, nil
}
