// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"context"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// ReportSink acknowledges persisted metadata, never native delivery. The native
// store adapter maps C0 OperationResult.Receipt to this narrow retry port.
type ReportSink interface {
	Report(context.Context, nativecontract.Receipt) (nativecontract.Receipt, error)
}

// FlushReports retries the bounded, ordered metadata journal independently of
// native stdin. A lost API ack simply replays the same receipt; no input body is
// journaled, reconstructed or reinjected. Only one flusher per Store is allowed
// by the supervisor; a mismatched ack never advances the durable report offset.
func (s *Store) FlushReports(ctx context.Context, b nativecontract.Binding, limit int, sink ReportSink) (int, error) {
	if limit < 1 || limit > 100 || sink == nil {
		return 0, Fail(nativecontract.ReasonInvalidInput)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	count := 0
	for count < limit {
		s.mu.Lock()
		if s.closed || s.poisoned {
			s.mu.Unlock()
			return count, Fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		i := -1
		for j, in := range s.entries {
			if in.Receipt.Binding.Equal(b) && in.ReportOffset < len(in.Reports) {
				i = j
				break
			}
		}
		if i < 0 {
			s.mu.Unlock()
			return count, nil
		}
		in := s.entries[i]
		r := cloneReceipt(in.Reports[in.ReportOffset])
		next := append([]Intent{}, s.entries...)
		next[i].ReportAttempts = min(in.ReportAttempts+1, 65535)
		if err := s.save("receipts.json", next, journalFileBytes); err != nil {
			s.mu.Unlock()
			return count, err
		}
		s.entries = next
		s.mu.Unlock()
		ack, err := sink.Report(ctx, r)
		if err != nil {
			return count, err
		}
		if err := Validate(s.validator, "NativeReceipt", ack); err != nil {
			return count, err
		}
		if ack.State != r.State {
			return count, Fail(nativecontract.ReasonIdentityMismatch)
		}
		if err := Transition(r, ack); err != nil {
			return count, err
		}
		s.mu.Lock()
		if s.closed || s.poisoned {
			s.mu.Unlock()
			return count, Fail(nativecontract.ReasonOwnershipUnconfirmed)
		}
		// A concurrent observation may append reports; the old prefix must
		// still match. Advancing this offset cannot touch native transport.
		if s.entries[i].ReportOffset != in.ReportOffset {
			s.mu.Unlock()
			return count, Fail(nativecontract.ReasonIdentityMismatch)
		}
		next = append([]Intent{}, s.entries...)
		next[i].ReportOffset++
		if err := s.save("receipts.json", next, journalFileBytes); err != nil {
			s.mu.Unlock()
			return count, err
		}
		s.entries = next
		s.mu.Unlock()
		count++
	}
	return count, nil
}
