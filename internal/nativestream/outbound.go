// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

type Publisher interface {
	Publish(context.Context, nativecontract.Publish) (nativecontract.OperationResult, error)
}

// Outbound observes exactly one trusted owned native reader. It never looks up
// vendor history or adopts a foreign process. Finals go directly to FinalSink;
// the live ring holds only independently filtered transient frames.
type Outbound struct {
	mu         sync.Mutex
	binding    nativecontract.Binding
	generation int64
	source     uint64
	next       uint64
	buffer     *Buffer
	projector  *Projector
	finals     nativecontract.FinalSink
	clock      nativecontract.Clock
	access     nativecontract.ValidationContext
	pumping    bool
}

func NewOutbound(binding nativecontract.Binding, generation int64, access nativecontract.ValidationContext, projector *Projector, finals nativecontract.FinalSink, clock nativecontract.Clock) (*Outbound, error) {
	if projector == nil || finals == nil || access.Binding == nil || !access.Binding.Equal(binding) || !access.Private || access.Generation != generation {
		return nil, Refuse(nativecontract.ReasonQualificationMissing)
	}
	buffer, err := NewBuffer(binding, generation, clock)
	if err != nil {
		return nil, err
	}
	return &Outbound{binding: binding, generation: generation, next: 1, buffer: buffer, projector: projector, finals: finals, clock: clockOrDefault(clock), access: access}, nil
}

func (o *Outbound) Observe(ctx context.Context, event nativecontract.ProviderEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if event.Identity.RuntimeEpoch != o.binding.RuntimeEpoch {
		return Refuse(nativecontract.ReasonWrongGeneration)
	}
	if event.SourceSequence == 0 || event.SourceSequence <= o.source {
		return Refuse(nativecontract.ReasonInvalidInput)
	}
	if event.Final != nil {
		if !event.Final.ExpectedBinding.Equal(o.binding) {
			return Refuse(nativecontract.ReasonWrongGeneration)
		}
		if _, err := Encode(o.projector.validator, "NativeFinalPublish", *event.Final); err != nil {
			return err
		}
		// FinalSink owns current final-drain authority, retention and retry
		// semantics. Never assemble a final from deltas or put text in replay.
		receipt, err := o.finals.Persist(ctx, *event.Final)
		if err != nil {
			return Refuse(nativecontract.ReasonNativeFailure)
		}
		if _, err := Encode(o.projector.validator, "NativeFinalReceipt", receipt); err != nil {
			return err
		}
		if receipt.FinalID != event.Final.FinalID {
			return Refuse(nativecontract.ReasonIdentityMismatch)
		}
		if receipt.State != "persisted" {
			return Refuse(nativecontract.ReasonFinalDeliveryExpired)
		}
	}
	parts := []struct {
		kind  string
		value any
	}{}
	if event.Structured != nil {
		parts = append(parts, struct {
			kind  string
			value any
		}{"structured", *event.Structured})
	}
	if event.Expert != nil {
		clean := o.projector.Expert(ctx, o.binding, event.Expert.Provider, []byte(event.Expert.FrameJSON))
		parts = append(parts, struct {
			kind  string
			value any
		}{"expert", clean})
	}
	if event.Receipt != nil {
		parts = append(parts, struct {
			kind  string
			value any
		}{"receipt", *event.Receipt})
	}
	if event.Approval != nil {
		parts = append(parts, struct {
			kind  string
			value any
		}{"approval", *event.Approval})
	}
	if event.Quota != nil {
		parts = append(parts, struct {
			kind  string
			value any
		}{"quota", *event.Quota})
	}
	events := make([]nativecontract.StreamEvent, 0, len(parts))
	for i, part := range parts {
		schema := nativecontract.StreamPayloadSchemas()[part.kind]
		if _, err := Encode(o.projector.validator, schema, part.value); err != nil {
			return err
		}
		seq := strconv.FormatUint(o.next+uint64(i), 10)
		payload, _ := json.Marshal(part.value)
		frame := nativecontract.StreamEvent{Binding: o.binding, Generation: o.generation, Seq: seq, Sequence: seq, Source: "native", Kind: part.kind, Payload: payload, ObservedAt: o.clock.Now(), DroppedFrames: "0"}
		clean, err := o.projector.Sanitize(ctx, frame, o.access)
		if err != nil {
			return err
		}
		events = append(events, clean)
	}
	if len(events) > 0 {
		if err := o.buffer.Append(events); err != nil {
			return err
		}
		o.next += uint64(len(events))
	}
	o.source = event.SourceSequence
	return nil
}

// Pump is one outbound HTTP writer. Failed transient batches stay only in the
// living ring; overflow becomes a visible gap on recovery. No retry creates
// final, input or control authority. A permanent refusal closes/erases the ring.
func (o *Outbound) Pump(ctx context.Context, publisher Publisher) error {
	if publisher == nil {
		return Refuse(nativecontract.ReasonQualificationMissing)
	}
	o.mu.Lock()
	if o.pumping {
		o.mu.Unlock()
		return Refuse(nativecontract.ReasonControllerConflict)
	}
	o.pumping = true
	o.mu.Unlock()
	defer o.buffer.Close()
	position := uint64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		frames := []nativecontract.StreamEvent{}
		end := position
		total := 0
		var wake <-chan struct{}
		for len(frames) < nativecontract.PublishBatchFrames {
			event, next, signal, closed := o.buffer.ReadAfter(end)
			wake = signal
			if closed {
				return Refuse(nativecontract.ReasonWrongGeneration)
			}
			if event.Kind == "" {
				break
			}
			data, err := json.Marshal(event)
			if err != nil {
				return Refuse(nativecontract.ReasonInvalidInput)
			}
			if total+len(data) > nativecontract.PublishBatchBytes-4096 {
				break
			}
			frames = append(frames, event)
			total += len(data)
			end = next
		}
		if len(frames) == 0 {
			if err := Wait(ctx, wake); err != nil {
				return err
			}
			continue
		}
		request := nativecontract.Publish{RequestID: uuid.NewString(), ExpectedBinding: o.binding, Events: frames}
		call, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
		_, err := publisher.Publish(call, request)
		cancel()
		if err == nil {
			position = end
			continue
		}
		var refusal *Failure
		if errors.As(err, &refusal) && refusal.Status >= 400 && refusal.Status < 500 && refusal.Status != http.StatusTooManyRequests && refusal.Status != http.StatusRequestTimeout {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.clock.After(250 * time.Millisecond):
		}
	}
}

func (o *Outbound) Stats() (frames, bytes int, next uint64) { return o.buffer.Stats() }
func (o *Outbound) Close()                                  { o.buffer.Close() }

// HTTPPublisher uses existing paired-daemon/worker proof supplied at each send.
// There is no credential file access, vendor token proxy, network listener or
// redirect forwarding. The owning client provides the existing TLS transport.
type HTTPPublisher struct {
	url       string
	client    *http.Client
	authorize func(*http.Request) error
	validator *nativecontract.Validator
}

func NewHTTPPublisher(baseURL string, client *http.Client, authorize func(*http.Request) error) (*HTTPPublisher, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || client == nil || authorize == nil {
		return nil, Refuse(nativecontract.ReasonQualificationMissing)
	}
	u.Path = stringsTrimSlash(u.Path) + "/api/agentd/native-sessions/events"
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	return &HTTPPublisher{url: u.String(), client: &copyClient, authorize: authorize, validator: v}, nil
}

func (p *HTTPPublisher) Publish(ctx context.Context, request nativecontract.Publish) (nativecontract.OperationResult, error) {
	var result nativecontract.OperationResult
	data, err := Encode(p.validator, "NativePublish", request)
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(data))
	if err != nil {
		return result, Refuse(nativecontract.ReasonInvalidInput)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Aeon-Contract", nativecontract.ContractVersion)
	if err := p.authorize(r); err != nil {
		return result, Refuse(nativecontract.ReasonGrantMissing)
	}
	response, err := p.client.Do(r)
	if err != nil {
		return result, Refuse(nativecontract.ReasonNativeFailure)
	}
	defer response.Body.Close()
	if response.ContentLength > nativecontract.RequestBodyBytes {
		return result, Refuse(nativecontract.ReasonBufferFull)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, nativecontract.RequestBodyBytes+1))
	if err != nil || len(body) > nativecontract.RequestBodyBytes {
		return result, Refuse(nativecontract.ReasonBufferFull)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		var failure nativecontract.Error
		if err := p.validator.DecodeJSON("NativeError", body, &failure); err != nil {
			return result, Refuse(nativecontract.ReasonNativeFailure)
		}
		return result, &Failure{response.StatusCode, failure.Reason}
	}
	if response.Header.Get("Aeon-Contract") != nativecontract.ContractVersion {
		return result, Refuse(nativecontract.ReasonUnsupported)
	}
	if err := p.validator.DecodeJSON("NativeOperationResult", body, &result); err != nil {
		return result, err
	}
	// This acknowledgement carries the real current operation receipt; it does
	// not manufacture a delivery receipt for a transient publication request.
	if result.Receipt.Binding == nil || !result.Receipt.Binding.Equal(request.ExpectedBinding) {
		return result, Refuse(nativecontract.ReasonIdentityMismatch)
	}
	return result, nil
}

func stringsTrimSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}
