// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/hooknote"
	"github.com/inspr-at/paimos/internal/workorders"
)

type AttachedMessageExchange func(context.Context, attachwatch.DeviceRequest) (attachedmsg.Exchange, error)

// NewAttachedMessageExchange freezes the paired origin and rejects redirects.
// The memory-only poll key never crosses the local hook socket. No generic
// inbox API, configuration file or legacy bearer-file client is used here.
func NewAttachedMessageExchange(c *client.Client, origin, computer, pollKey string) (AttachedMessageExchange, error) {
	if c == nil || c.HTTP == nil || ValidateBaseURL(origin) != nil || !workorders.UUID(computer) || !noteNonce(pollKey) {
		return nil, errors.New("attached exchange unavailable")
	}
	pinned := *c
	pinned.BaseURL = origin
	transport := *c.HTTP
	transport.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	pinned.HTTP = &transport
	return func(ctx context.Context, in attachwatch.DeviceRequest) (attachedmsg.Exchange, error) {
		var out attachedmsg.Exchange
		if in.ComputerID != computer || (in.Operation != "message_offer" && in.Operation != "message_receipt" && in.Operation != "message_validate") {
			return out, errors.New("attached exchange rejected")
		}
		in.PollKey = pollKey
		err := pinned.Do(ctx, "POST", "/api/agent-pairing/attach", in, &out)
		// Do not propagate server diagnostics or request material to a hook.
		if err != nil {
			return attachedmsg.Exchange{}, errors.New("attached exchange unavailable")
		}
		return out, nil
	}, nil
}

type noteReceipt struct {
	receipt  attachedmsg.Receipt
	deadline time.Time
}

// AttachedNoteSource is generic solely to compile independently of the parallel
// S2-4 package. Instantiating N=hooknote.Note, B=hooknote.Binding and E=its epoch
// type implements NoteSource exactly, including Settle(ctx,nonce,outcome,epoch).
// B is compared in full; no stdin/session/vendor field selects a grant.
//
// Wiring after AEON-394: NewAttachedNoteSource[hooknote.Note](binding, epoch,
// grant, exchange, hooknote.Enabled(), hooknote.ErrEmpty). The caller supplies
// its non-nil empty-queue sentinel, returned unchanged for an empty claim.
// One source belongs to one approved binding; reapproval constructs a new one.
type AttachedNoteSource[N any, B comparable, E comparable] struct {
	mu       sync.Mutex
	binding  B
	epoch    E
	grant    attachedmsg.Grant
	exchange AttachedMessageExchange
	enabled  bool
	empty    error
	pending  map[string]noteReceipt
}

func NewAttachedNoteSource[N any, B comparable, E comparable](b B, epoch E, g attachedmsg.Grant, exchange AttachedMessageExchange, enabled bool, empty error) *AttachedNoteSource[N, B, E] {
	return &AttachedNoteSource[N, B, E]{binding: b, epoch: epoch, grant: g, exchange: exchange, enabled: enabled, empty: empty, pending: map[string]noteReceipt{}}
}
func noteNonce(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (s *AttachedNoteSource[N, B, E]) request(op string, epoch E) attachwatch.DeviceRequest {
	b := s.grant.Binding
	raw, _ := json.Marshal(b)
	epochJSON, _ := json.Marshal(epoch)
	return attachwatch.DeviceRequest{Operation: op, AttachProtocol: attachwatch.Protocol, MessageProtocol: attachedmsg.Protocol,
		ComputerID: b.ComputerID, RequestID: b.AttachRequestID, Snapshot: s.grant.Snapshot, Digest: b.SnapshotDigest,
		MessageGeneration: b.Generation, MessageConsentDigest: s.grant.Digest, HookReleaseDigest: b.HookReleaseDigest, HookConfigDigest: b.HookConfigDigest,
		QualifiedHarnessVersion: b.HarnessVersion, MessageBinding: raw, MessageEpoch: string(epochJSON)}
}
func (s *AttachedNoteSource[N, B, E]) Offer(ctx context.Context, b B) (N, string, error) {
	var note N
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, attachedmsg.OfferBudget)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.exchange == nil || b != s.binding {
		return note, "", errors.New("attached note unavailable")
	}
	for nonce, p := range s.pending {
		if !start.Before(p.deadline) {
			delete(s.pending, nonce)
		}
	}
	if len(s.pending) >= 5 {
		return note, "", errors.New("attached receipt limit")
	}
	request := s.request("message_offer", s.epoch)
	out, err := s.exchange(ctx, request)
	if err != nil {
		return note, "", err
	}
	if out.State == "empty" && s.empty != nil {
		return note, "", s.empty
	}
	o := out.Offer
	if out.State != "offered" || o == nil {
		return note, "", errors.New("no attached note")
	}
	if o.Binding != s.grant.Binding || o.GrantID != s.grant.ID || o.Epoch != request.MessageEpoch || !workorders.UUID(o.MessageID) || !workorders.UUID(o.DeliveryID) || !noteNonce(o.Nonce) {
		return note, "", errors.New("attached offer binding rejected")
	}
	deadline := start.Add(attachedmsg.OfferBudget)
	if o.Deadline.Before(deadline) {
		deadline = o.Deadline
	}
	// Cache only bounded receipt metadata. Never retain an offline body.
	receipt := attachedmsg.Receipt{DeliveryID: o.DeliveryID, MessageID: o.MessageID, Nonce: o.Nonce, Epoch: o.Epoch}
	s.pending[o.Nonce] = noteReceipt{receipt: receipt, deadline: deadline}
	if _, err = attachedmsg.Frame(o.Binding.OwnerID, o.Owner, o.Body); err != nil || !time.Now().Before(deadline) || ctx.Err() != nil {
		return note, "", errors.New("attached offer expired or invalid")
	}
	// These are precisely S2-4 Note's wire fields; the type parameter preserves
	// its concrete method signature without importing or copying that package.
	wire := struct {
		ID       string `json:"id"`
		Owner    string `json:"owner"`
		Body     string `json:"body"`
		Origin   string `json:"origin"`
		Created  string `json:"created"`
		Deadline string `json:"deadline"`
	}{o.MessageID, o.Owner, o.Body, "owner", o.CreatedAt.UTC().Format(time.RFC3339Nano), deadline.UTC().Format(time.RFC3339Nano)}
	raw, err := json.Marshal(wire)
	if err != nil {
		return note, "", errors.New("invalid attached note")
	}
	if json.Unmarshal(raw, &note) != nil {
		return note, "", errors.New("invalid attached note")
	}
	return note, o.Nonce, nil
}
func (s *AttachedNoteSource[N, B, E]) Settle(ctx context.Context, nonce, outcome string, epoch E) error {
	ctx, cancel := context.WithTimeout(ctx, attachedmsg.OfferBudget)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.exchange == nil || !noteNonce(nonce) || (outcome != "shown" && outcome != "uncertain") {
		return errors.New("attached receipt rejected")
	}
	p, ok := s.pending[nonce]
	if !ok {
		return errors.New("unknown attached receipt")
	}
	in := s.request("message_receipt", epoch)
	p.receipt.Outcome = outcome
	// The supplied per-binding epoch is sent to the database compare-and-settle;
	// there is deliberately no local check followed by an epoch-less network ack.
	p.receipt.Epoch = in.MessageEpoch
	in.MessageReceipt, _ = json.Marshal(p.receipt)
	out, err := s.exchange(ctx, in)
	if err != nil {
		return err
	}
	if out.Offer != nil {
		return errors.New("attached receipt not confirmed")
	}
	switch out.State {
	case "completed":
		if outcome != "shown" {
			return &hooknote.SettledError{Outcome: hooknote.OutcomeShown}
		}
	case "uncertain":
		if outcome != "uncertain" {
			return &hooknote.SettledError{Outcome: hooknote.OutcomeUncertain}
		}
	case "revoked", "expired", "not_delivered", "cancelled":
		return &hooknote.SettledError{Outcome: hooknote.OutcomeUncertain}
	default:
		return errors.New("attached receipt not confirmed")
	}
	delete(s.pending, nonce)
	return nil
}

// Validate makes no body request and never renews the watch lease. A lost reply
// refuses disclosure; the one attempted offer can only become uncertain.
func (s *AttachedNoteSource[N, B, E]) Validate(ctx context.Context, nonce string, b B) error {
	ctx, cancel := context.WithTimeout(ctx, attachedmsg.OfferBudget)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled || s.exchange == nil || b != s.binding {
		return hooknote.ErrRevoked
	}
	pending, ok := s.pending[nonce]
	if !ok || !time.Now().Before(pending.deadline) {
		return hooknote.ErrRevoked
	}
	in := s.request("message_validate", s.epoch)
	in.MessageReceipt, _ = json.Marshal(pending.receipt)
	out, err := s.exchange(ctx, in)
	if err != nil || out.Offer != nil || out.State != "offered" {
		return hooknote.ErrRevoked
	}
	return nil
}

// The concrete port is verified at compile time; qualification remains disabled.
var _ hooknote.NoteSource = (*AttachedNoteSource[hooknote.Note, hooknote.Binding, hooknote.Epoch])(nil)
var _ hooknote.DisclosureValidator = (*AttachedNoteSource[hooknote.Note, hooknote.Binding, hooknote.Epoch])(nil)
