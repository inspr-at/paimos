// SPDX-License-Identifier: AGPL-3.0-only

package nativereceipts

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// WriteAuthority rechecks the current actor/controller, exact owned writer
// and monotonic claim deadline under the supervisor's native mutation lock.
// The callback is required; an accepted/queued intent is never authority.
type WriteAuthority interface {
	RequireWrite(context.Context, nativecontract.Binding, Intent) error
}

// InputWriter serializes inputs. It is constructed only for an already-owned
// runtime; it does not open, adopt, resume or signal a process.
type InputWriter struct {
	mu        sync.Mutex
	Journal   *Store
	Authority WriteAuthority
	Transport nativecontract.NativeTransport
}

func (w *InputWriter) Send(ctx context.Context, actorID, operationID string, in nativecontract.Input) (nativecontract.Receipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Journal == nil || w.Authority == nil || w.Transport == nil {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonQualificationMissing)
	}
	if len(in.Text) > MaxTextBytes || !utf8.ValidString(in.Text) {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonInvalidInput)
	}
	if err := Validate(w.Journal.validator, "NativeInput", in); err != nil {
		return nativecontract.Receipt{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	identity := w.Transport.Identity()
	if identity.RuntimeEpoch != in.ExpectedBinding.RuntimeEpoch || !ValidDigest(identity.ProcessIdentityDigest) {
		return nativecontract.Receipt{}, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	digest, err := Digest(in)
	if err != nil {
		return nativecontract.Receipt{}, err
	}
	b := cloneBinding(in.ExpectedBinding)
	inputID := in.InputID
	intent := Intent{Receipt: nativecontract.Receipt{RequestID: in.RequestID, OperationID: operationID, Binding: &b, Operation: "input", State: "accepted", InputID: &inputID, SourceSequence: "0", UpdatedAt: w.Journal.now().UTC()}, ActorID: actorID, PayloadSHA256: digest, ExpiresAt: in.ExpiresAt, ProcessIdentityDigest: identity.ProcessIdentityDigest}
	r, err := w.Journal.Accept(ctx, intent)
	if err != nil {
		return r, err
	}
	if r.State != "accepted" && r.State != "queued" {
		return r, Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err = w.Authority.RequireWrite(ctx, b, intent); err != nil {
		return r, err
	}
	if identity != w.Transport.Identity() {
		return r, Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	if r, err = w.Journal.BeginWrite(ctx, b, operationID); err != nil {
		return r, err
	}
	// File fsync can outlast a short claim. Recheck the current deadline and
	// owned runtime again after that disk barrier, immediately before stdin.
	err = ctx.Err()
	if err == nil {
		err = w.Authority.RequireWrite(ctx, b, intent)
	}
	if err == nil && identity != w.Transport.Identity() {
		err = Fail(nativecontract.ReasonOwnershipUnconfirmed)
	}
	var e nativecontract.NativeEvidence
	if err == nil {
		e, err = w.Transport.Send(ctx, in)
	}
	if err == nil && e.Stage == "write_acknowledged" {
		r, err = w.Journal.Observe(ctx, b, operationID, e)
	} else if err == nil {
		err = Fail(nativecontract.ReasonInputOutcomeUnconfirmed)
	}
	if err != nil {
		// Cancellation does not erase possible I/O. The durable marker already
		// suffices on crash; publish an honest uncertain result when possible.
		reason := nativecontract.ReasonInputOutcomeUnconfirmed
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer stop()
		uncertain, journalErr := w.Journal.SetIntentState(cleanup, b, operationID, "uncertain", &reason)
		if journalErr == nil {
			r = uncertain
		} else {
			return r, journalErr
		}
	}
	return r, err
}
