// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/attachedmsg"
	"github.com/inspr-at/paimos/internal/attachwatch"
	"github.com/inspr-at/paimos/internal/client"
	"github.com/inspr-at/paimos/internal/hooknote"
)

// Exact Note method shape from the independent S2-4 contract. This package
// cannot import hooknote until integration, so no source from 394 is copied.
type sourceNote struct{ ID, Owner, Body, Origin, Created, Deadline string }
type sourceBinding struct {
	Session, Generation string
	PID                 int
}
type sourceEpoch struct {
	Generation string
	Counter    uint64
}
type sourcePort interface {
	Offer(context.Context, sourceBinding) (sourceNote, string, error)
	Settle(context.Context, string, string, sourceEpoch) error
}

var _ sourcePort = (*AttachedNoteSource[sourceNote, sourceBinding, sourceEpoch])(nil)

func sourceFixture() (sourceBinding, attachedmsg.Grant, *attachedmsg.Offer) {
	b := sourceBinding{attachedmsg.UUID(), attachedmsg.UUID(), 44}
	g := attachedmsg.Grant{ID: attachedmsg.UUID(), Binding: attachedmsg.Binding{TenantID: attachedmsg.UUID(), SessionID: b.Session, Generation: b.Generation, ComputerID: attachedmsg.UUID(), AttachRequestID: attachedmsg.UUID(), OwnerID: attachedmsg.UUID()}, Snapshot: attachwatch.Snapshot{}}
	g.Binding.SnapshotDigest = g.Snapshot.Digest()
	g.Digest = g.Binding.Digest()
	epoch, _ := json.Marshal(sourceEpoch{Generation: b.Generation, Counter: 7})
	o := &attachedmsg.Offer{Binding: g.Binding, GrantID: g.ID, DeliveryID: attachedmsg.UUID(), MessageID: attachedmsg.UUID(), Nonce: strings.Repeat("a", 64), Epoch: string(epoch), Body: "one private note", Owner: "Owner", CreatedAt: time.Now(), Deadline: time.Now().Add(attachedmsg.OfferBudget)}
	return b, g, o
}
func TestAttachedNoteSourcePortAndEpoch(t *testing.T) {
	b, g, o := sourceFixture()
	epoch := sourceEpoch{Generation: b.Generation, Counter: 7}
	empty := errors.New("fixture empty queue")
	offers, receipts := 0, 0
	exchange := func(_ context.Context, in attachwatch.DeviceRequest) (attachedmsg.Exchange, error) {
		if in.Operation == "message_offer" {
			offers++
			if offers > 1 {
				return attachedmsg.Exchange{State: "empty"}, nil
			}
			if in.MessageEpoch != o.Epoch {
				t.Fatal("wrong epoch")
			}
			return attachedmsg.Exchange{State: "offered", Offer: o}, nil
		}
		receipts++
		var r attachedmsg.Receipt
		if json.Unmarshal(in.MessageReceipt, &r) != nil || r.Nonce != o.Nonce || r.MessageID != o.MessageID || r.DeliveryID != o.DeliveryID {
			t.Fatal("receipt association lost")
		}
		if r.Epoch != o.Epoch {
			return attachedmsg.Exchange{State: "uncertain"}, nil
		}
		return attachedmsg.Exchange{State: "completed"}, nil
	}
	src := NewAttachedNoteSource[sourceNote](b, epoch, g, exchange, false, empty)
	if _, _, err := src.Offer(t.Context(), b); err == nil || offers != 0 {
		t.Fatal("default switch made request")
	}
	src = NewAttachedNoteSource[sourceNote](b, epoch, g, exchange, true, empty)
	wrong := b
	wrong.PID++
	if _, _, err := src.Offer(t.Context(), wrong); err == nil || offers != 0 {
		t.Fatal("another process selected grant")
	}
	n, nonce, err := src.Offer(t.Context(), b)
	if err != nil || nonce != o.Nonce || n.ID != o.MessageID || n.Body != o.Body || n.Origin != "owner" || n.Deadline == "" {
		t.Fatal("offer mapping failed")
	}
	if err = src.Settle(t.Context(), nonce, "shown", sourceEpoch{Generation: epoch.Generation, Counter: 8}); err == nil || receipts != 1 {
		t.Fatal("epoch was checked only locally or uncertainty treated as success")
	}
	if err = src.Settle(t.Context(), nonce, "shown", sourceEpoch{Generation: attachedmsg.UUID(), Counter: epoch.Counter}); err == nil || receipts != 2 {
		t.Fatal("epoch generation was lost or uncertainty treated as success")
	}
	if err = src.Settle(t.Context(), nonce, "shown", epoch); err != nil {
		t.Fatal(err)
	}
	if n, nonce, err = src.Offer(t.Context(), b); err != empty || !errors.Is(err, empty) || n != (sourceNote{}) || nonce != "" {
		t.Fatal("empty claim lost its sentinel or replayed cached content")
	}
}
func TestAttachedNoteSourceEmptySentinel(t *testing.T) {
	for _, which := range []string{"empty", "empty_with_offer", "nil_sentinel", "missing_offer", "unknown_state"} {
		t.Run(which, func(t *testing.T) {
			b, g, o := sourceFixture()
			empty := errors.New("caller empty sentinel")
			out := attachedmsg.Exchange{State: "empty"}
			wantEmpty := true
			switch which {
			case "empty_with_offer":
				out.Offer = o
			case "nil_sentinel":
				empty = nil
				wantEmpty = false
			case "missing_offer":
				out.State = "offered"
				wantEmpty = false
			case "unknown_state":
				out.State, out.Offer = "unknown", o
				wantEmpty = false
			}
			src := NewAttachedNoteSource[sourceNote](b, sourceEpoch{Generation: b.Generation, Counter: 7}, g, func(context.Context, attachwatch.DeviceRequest) (attachedmsg.Exchange, error) {
				return out, nil
			}, true, empty)
			n, nonce, err := src.Offer(t.Context(), b)
			if err == nil || n != (sourceNote{}) || nonce != "" || len(src.pending) != 0 {
				t.Fatal("empty or invalid claim released content or cached a receipt")
			}
			if wantEmpty && (err != empty || !errors.Is(err, empty)) {
				t.Fatal("empty claim did not return the exact caller sentinel")
			}
			if !wantEmpty && errors.Is(err, empty) {
				t.Fatal("invalid claim was reported as an empty queue")
			}
		})
	}
}
func TestAttachedNoteSourceRejectsForeignExpiredAndOversize(t *testing.T) {
	for _, which := range []string{"foreign", "expired", "nonce", "oversize", "epoch"} {
		t.Run(which, func(t *testing.T) {
			b, g, o := sourceFixture()
			switch which {
			case "foreign":
				o.Binding.ServiceEpoch = attachedmsg.UUID()
			case "expired":
				o.Deadline = time.Now().Add(-time.Second)
			case "nonce":
				o.Nonce = "bad"
			case "oversize":
				o.Body = strings.Repeat("x", attachedmsg.MaxBody+1)
			case "epoch":
				o.Epoch = "8"
			}
			s := NewAttachedNoteSource[sourceNote](b, sourceEpoch{Generation: b.Generation, Counter: 7}, g, func(context.Context, attachwatch.DeviceRequest) (attachedmsg.Exchange, error) {
				return attachedmsg.Exchange{State: "offered", Offer: o}, nil
			}, true, errors.New("fixture empty queue"))
			n, nonce, err := s.Offer(t.Context(), b)
			if err == nil || nonce != "" || n.Body != "" {
				t.Fatal("invalid offer released")
			}
		})
	}
}
func TestAttachedMessageExchangePinsOriginAndRejectsRedirect(t *testing.T) {
	var leaks atomic.Int64
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaks.Add(1); w.WriteHeader(200) }))
	defer other.Close()
	var calls atomic.Int64
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/agent-pairing/attach" {
			t.Error("wrong route")
		}
		w.Header().Set("Location", other.URL+"/collect")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer good.Close()
	c := client.New(other.URL, "fixture-runtime-key")
	// A permissive caller client and wrong base URL must not weaken the paired pin.
	c.HTTP.CheckRedirect = nil
	computer := attachedmsg.UUID()
	exchange, err := NewAttachedMessageExchange(c, good.URL, computer, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"message_offer", "message_receipt"} {
		if _, err = exchange(t.Context(), attachwatch.DeviceRequest{Operation: operation, ComputerID: computer}); err == nil {
			t.Fatal("redirect accepted")
		}
	}
	if leaks.Load() != 0 || calls.Load() != 2 {
		t.Fatal("authority crossed paired origin")
	}
	if _, err = exchange(t.Context(), attachwatch.DeviceRequest{Operation: "message_offer", ComputerID: attachedmsg.UUID()}); err == nil || calls.Load() != 2 {
		t.Fatal("alternate computer accepted")
	}
}

func TestHookNotePortRechecksRemoteAuthorityBeforeDisclosure(t *testing.T) {
	_, g, o := sourceFixture()
	b := hooknote.Binding{SessionID: g.Binding.SessionID, MessageGeneration: g.Binding.Generation, DaemonGeneration: g.Binding.DaemonEpoch, Epoch: hooknote.Epoch{Generation: g.Binding.Generation, Counter: 7}}
	epochRaw, _ := json.Marshal(b.Epoch)
	o.Epoch = string(epochRaw)
	authorized := true
	validations := 0
	exchange := func(_ context.Context, in attachwatch.DeviceRequest) (attachedmsg.Exchange, error) {
		switch in.Operation {
		case "message_offer":
			return attachedmsg.Exchange{State: "offered", Offer: o}, nil
		case "message_validate":
			validations++
			var receipt attachedmsg.Receipt
			if json.Unmarshal(in.MessageReceipt, &receipt) != nil || receipt.Nonce != o.Nonce || receipt.Epoch != o.Epoch || in.MessageConsentDigest != g.Digest {
				t.Fatal("disclosure check lost its consent or attempt binding")
			}
			if !authorized {
				return attachedmsg.Exchange{State: "uncertain"}, nil
			}
			return attachedmsg.Exchange{State: "offered"}, nil
		default:
			t.Fatal("validation attempted a write")
			return attachedmsg.Exchange{}, errors.New("unexpected operation")
		}
	}
	src := NewAttachedNoteSource[hooknote.Note](b, b.Epoch, g, exchange, true, hooknote.ErrEmpty)
	note, nonce, err := src.Offer(t.Context(), b)
	if err != nil || note.Body != o.Body {
		t.Fatal("concrete offer port failed")
	}
	if src.Validate(t.Context(), nonce, b) != nil || validations != 1 {
		t.Fatal("fresh current authority failed")
	}
	authorized = false
	if !errors.Is(src.Validate(t.Context(), nonce, b), hooknote.ErrRevoked) || validations != 2 {
		t.Fatal("withdrawn remote consent was not rechecked")
	}
	wrong := b
	wrong.MessageGeneration = attachedmsg.UUID()
	if !errors.Is(src.Validate(t.Context(), nonce, wrong), hooknote.ErrRevoked) || validations != 2 {
		t.Fatal("foreign generation reached the paired exchange")
	}
}
