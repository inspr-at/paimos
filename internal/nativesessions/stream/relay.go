// SPDX-License-Identifier: AGPL-3.0-only

// Package stream is the transient server side of the native-session relay.
// Durable finals, receipts, authorization and runtime ownership stay with C0's
// Store/FinalSink and the owning supervisor, wired only by I1/I2.
package stream

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

type liveSession struct {
	mu       sync.Mutex
	buffer   *nativestream.Buffer
	snapshot nativecontract.SessionView
	viewers  int
}

type Relay struct {
	mu           sync.Mutex
	sessions     map[string]*liveSession
	viewers      int
	viewerCounts map[string]int
	projector    *nativestream.Projector
	validator    *nativecontract.Validator
	clock        nativecontract.Clock
}

type systemClock struct{}

func sessionKey(binding nativecontract.Binding) string {
	return binding.TenantID + ":" + binding.OwnedSessionID
}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func NewRelay(projector *nativestream.Projector, clock nativecontract.Clock) (*Relay, error) {
	if projector == nil {
		return nil, nativestream.Refuse(nativecontract.ReasonQualificationMissing)
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Relay{sessions: map[string]*liveSession{}, viewerCounts: map[string]int{}, projector: projector, clock: clock, validator: v}, nil
}

// Register accepts only a trusted current Store projection, never a publisher's
// supplied identity. Replacement closes and erases the old runtime's ring.
// The session registry itself is bounded; integration removes retired entries.
func (r *Relay) Register(snapshot nativecontract.SessionView) error {
	if snapshot.CurrentBinding == nil {
		return nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
	}
	if _, err := nativestream.Encode(r.validator, "NativeSessionView", snapshot); err != nil {
		return err
	}
	buffer, err := nativestream.NewBuffer(*snapshot.CurrentBinding, snapshot.Generation, r.clock)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := sessionKey(*snapshot.CurrentBinding)
	old := r.sessions[key]
	if old != nil {
		old.mu.Lock()
		defer old.mu.Unlock()
		if old.snapshot.CurrentBinding != nil && old.snapshot.CurrentBinding.Equal(*snapshot.CurrentBinding) && old.snapshot.Generation == snapshot.Generation {
			buffer.Close()
			if snapshot.Revision < old.snapshot.Revision {
				return nativestream.Refuse(nativecontract.ReasonStaleRevision)
			}
			old.snapshot = cloneSnapshot(snapshot)
			return nil
		}
		if snapshot.Generation <= old.snapshot.Generation {
			buffer.Close()
			return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
		}
		old.buffer.Close()
	} else if len(r.sessions) >= nativecontract.ViewersGlobal {
		buffer.Close()
		return &nativestream.Failure{Status: 429, Reason: nativecontract.ReasonBufferFull}
	}
	r.sessions[key] = &liveSession{buffer: buffer, snapshot: cloneSnapshot(snapshot)}
	return nil
}

func (r *Relay) Remove(binding nativecontract.Binding) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.sessions[sessionKey(binding)]; session != nil {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.snapshot.CurrentBinding == nil || session.snapshot.CurrentBinding.Equal(binding) {
			session.buffer.Close()
			delete(r.sessions, sessionKey(binding))
		}
	}
}

func cloneSnapshot(snapshot nativecontract.SessionView) nativecontract.SessionView {
	data, _ := json.Marshal(snapshot)
	var clone nativecontract.SessionView
	_ = json.Unmarshal(data, &clone)
	return clone
}

func (r *Relay) find(binding nativecontract.Binding) (*liveSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions[sessionKey(binding)]
	if session == nil {
		return nil, nativestream.Refuse(nativecontract.ReasonWrongGeneration)
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.snapshot.CurrentBinding == nil || !session.snapshot.CurrentBinding.Equal(binding) {
		return nil, nativestream.Refuse(nativecontract.ReasonWrongGeneration)
	}
	return session, nil
}

// Publish checks the paired daemon/worker binding under the current access fence
// and independently filters the entire batch before changing shared RAM state.
func (r *Relay) Publish(ctx context.Context, request nativecontract.Publish, fence nativestream.Fence) error {
	if fence == nil {
		return nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	data, err := nativestream.Encode(r.validator, "NativePublish", request)
	if err != nil {
		return err
	}
	return fence(ctx, func(access nativecontract.ValidationContext) error {
		if !access.Private || access.PrincipalKind != "agent" {
			return nativestream.Refuse(nativecontract.ReasonWrongPrincipal)
		}
		if err := r.validator.ValidateBoundJSON("NativePublish", data, access); err != nil {
			return err
		}
		session, err := r.find(request.ExpectedBinding)
		if err != nil {
			return err
		}
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.snapshot.Generation != access.Generation || session.snapshot.CurrentBinding == nil || !session.snapshot.CurrentBinding.Equal(request.ExpectedBinding) {
			return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
		}
		if request.Snapshot != nil {
			if request.Snapshot.CurrentBinding == nil || !request.Snapshot.CurrentBinding.Equal(request.ExpectedBinding) || request.Snapshot.Generation != session.snapshot.Generation {
				return nativestream.Refuse(nativecontract.ReasonWrongGeneration)
			}
			if request.Snapshot.Revision < session.snapshot.Revision {
				return nativestream.Refuse(nativecontract.ReasonStaleRevision)
			}
		}
		frames := make([]nativecontract.StreamEvent, 0, len(request.Events))
		for _, event := range request.Events {
			clean, err := r.projector.Sanitize(ctx, event, access)
			if err != nil {
				return err
			}
			frames = append(frames, clean)
		}
		if len(frames) > 0 {
			if err := session.buffer.Append(frames); err != nil {
				return err
			}
		}
		if request.Snapshot != nil {
			session.snapshot = cloneSnapshot(*request.Snapshot)
		}
		return nil
	})
}

// Viewer is a trusted, request-scoped result of authenticating an attach viewer
// ID and its exact caller/session/view/runtime grant. Positions confer no rights.
type Viewer struct {
	Query nativecontract.StreamQuery
	Fence nativestream.Fence
}

type viewerKey struct{}

func WithViewer(ctx context.Context, viewer Viewer) context.Context {
	return context.WithValue(ctx, viewerKey{}, viewer)
}

var _ nativecontract.FrameSource = (*Relay)(nil)

func (r *Relay) Subscribe(ctx context.Context, binding nativecontract.Binding) (nativecontract.Subscription, error) {
	viewer, ok := ctx.Value(viewerKey{}).(Viewer)
	if !ok || viewer.Fence == nil {
		return nil, nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	return r.SubscribeView(ctx, binding, viewer)
}

type subscription struct {
	events chan nativecontract.StreamEvent
	cancel context.CancelFunc
	once   sync.Once
}

func (s *subscription) Events() <-chan nativecontract.StreamEvent { return s.events }
func (s *subscription) Close() error                              { s.once.Do(s.cancel); return nil }

func (r *Relay) SubscribeView(ctx context.Context, binding nativecontract.Binding, viewer Viewer) (nativecontract.Subscription, error) {
	if viewer.Fence == nil || viewer.Query.View != "structured" && viewer.Query.View != "expert" && viewer.Query.View != "terminal" {
		return nil, nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	session, err := r.find(binding)
	if err != nil {
		return nil, err
	}
	var initial nativecontract.StreamEvent
	var position uint64
	err = viewer.Fence(ctx, func(access nativecontract.ValidationContext) error {
		if access.Private || access.Binding == nil || !access.Binding.Equal(binding) || access.Generation != viewer.Query.Generation || access.View != viewer.Query.View {
			return nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
		}
		var err error
		position, err = session.buffer.Position(viewer.Query.After, viewer.Query.Generation)
		if err != nil {
			return err
		}
		session.mu.Lock()
		snapshot := cloneSnapshot(session.snapshot)
		session.mu.Unlock()
		payload, _ := json.Marshal(snapshot)
		seq := strconv.FormatUint(position, 10)
		initial = nativecontract.StreamEvent{Binding: binding, Generation: viewer.Query.Generation, Sequence: seq, Seq: seq, Source: "native", Kind: "snapshot", Payload: payload, ObservedAt: r.clock.Now(), DroppedFrames: "0"}
		initial, err = r.projector.Sanitize(ctx, initial, access)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.sessions[sessionKey(binding)] != session {
		r.mu.Unlock()
		return nil, nativestream.Refuse(nativecontract.ReasonWrongGeneration)
	}
	if r.viewerCounts[sessionKey(binding)] >= nativecontract.ViewersPerOwnedSession || r.viewers >= nativecontract.ViewersGlobal {
		r.mu.Unlock()
		return nil, &nativestream.Failure{Status: 429, Reason: nativecontract.ReasonBufferFull}
	}
	session.viewers++
	r.viewerCounts[sessionKey(binding)]++
	r.viewers++
	r.mu.Unlock()
	lifetime, cancel := context.WithCancel(ctx)
	sub := &subscription{events: make(chan nativecontract.StreamEvent), cancel: cancel}
	go r.follow(lifetime, sub, session, viewer, initial, position)
	return sub, nil
}

func (r *Relay) follow(ctx context.Context, sub *subscription, session *liveSession, viewer Viewer, initial nativecontract.StreamEvent, position uint64) {
	defer func() {
		sub.Close()
		close(sub.events)
		r.mu.Lock()
		session.viewers--
		r.viewerCounts[sessionKey(initial.Binding)]--
		if r.viewerCounts[sessionKey(initial.Binding)] == 0 {
			delete(r.viewerCounts, sessionKey(initial.Binding))
		}
		r.viewers--
		r.mu.Unlock()
	}()
	send := func(event nativecontract.StreamEvent) error {
		return viewer.Fence(ctx, func(access nativecontract.ValidationContext) error {
			if access.Private || access.View != viewer.Query.View {
				return nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
			}
			clean, err := r.projector.Sanitize(ctx, event, access)
			if err != nil {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-r.clock.After(nativecontract.StreamWriteDeadlineSeconds * time.Second):
				return nativestream.Refuse(nativecontract.ReasonBufferFull)
			case sub.events <- clean:
				return nil
			}
		})
	}
	if err := send(initial); err != nil {
		return
	}
	for {
		event, next, wake, closed := session.buffer.ReadAfter(position)
		if event.Kind != "" {
			position = next
			if !nativestream.Allows(viewer.Query.View, event.Kind) {
				continue
			}
			if err := send(event); err != nil {
				return
			}
			if event.Kind == "gap" || event.Kind == "end" {
				return
			}
			continue
		}
		if closed {
			return
		}
		if err := nativestream.Wait(ctx, wake); err != nil {
			return
		}
	}
}

func (r *Relay) Page(ctx context.Context, binding nativecontract.Binding, query nativecontract.EventQuery, viewer Viewer) (nativecontract.EventPage, error) {
	var result nativecontract.EventPage
	if viewer.Fence == nil {
		return result, nativestream.Refuse(nativecontract.ReasonGrantMissing)
	}
	err := viewer.Fence(ctx, func(access nativecontract.ValidationContext) error {
		if access.Private || access.Binding == nil || !access.Binding.Equal(binding) || access.Generation != query.Generation || access.View != viewer.Query.View {
			return nativestream.Refuse(nativecontract.ReasonIdentityMismatch)
		}
		session, err := r.find(binding)
		if err != nil {
			return err
		}
		result, err = session.buffer.Page(query, func(kind string) bool { return nativestream.Allows(viewer.Query.View, kind) })
		if err != nil {
			return err
		}
		for i, event := range result.Items {
			clean, err := r.projector.Sanitize(ctx, event, access)
			if err != nil {
				return err
			}
			result.Items[i] = clean
		}
		return nil
	})
	return result, err
}

func (r *Relay) Stats(binding nativecontract.Binding) (frames, bytes, viewers int, err error) {
	session, err := r.find(binding)
	if err != nil {
		return 0, 0, 0, err
	}
	frames, bytes, _ = session.buffer.Stats()
	r.mu.Lock()
	viewers = session.viewers
	r.mu.Unlock()
	return frames, bytes, viewers, nil
}
