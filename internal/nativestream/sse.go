// SPDX-License-Identifier: AGPL-3.0-only

package nativestream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// ServeEvents checks rights while writing every frame and keepalive. In
// particular, receiving a previously queued frame is never delivery authority.
// The response writer must support write deadlines; an unbounded writer fails
// closed. The caller acquires its authorized subscription before invoking this.
func ServeEvents(w http.ResponseWriter, r *http.Request, sub nativecontract.Subscription, fence Fence, projector *Projector, clock nativecontract.Clock) {
	defer sub.Close()
	clock = clockOrDefault(clock)
	rc := http.NewResponseController(w)
	if fence == nil || projector == nil {
		WriteError(w, Refuse(nativecontract.ReasonGrantMissing))
		return
	}
	if err := rc.SetWriteDeadline(time.Now().Add(nativecontract.StreamWriteDeadlineSeconds * time.Second)); err != nil {
		WriteError(w, Refuse(nativecontract.ReasonQualificationMissing))
		return
	}
	defer rc.SetWriteDeadline(time.Time{})
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	rotation := clock.After(nativecontract.StreamRotationSeconds * time.Second)
	keepalive := clock.After(nativecontract.KeepaliveSeconds * time.Second)
	started := false
	var last nativecontract.StreamEvent
	start := func() {
		if !started {
			Headers(w)
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			started = true
		}
	}
	write := func(event nativecontract.StreamEvent) error {
		data, err := json.Marshal(event)
		if err != nil || len(data) > nativecontract.ExportedFrameBytes {
			return Refuse(nativecontract.ReasonBufferFull)
		}
		id, err := nativecontract.EventID(event.Generation, sequence(event))
		if err != nil {
			return err
		}
		if err := rc.SetWriteDeadline(time.Now().Add(nativecontract.StreamWriteDeadlineSeconds * time.Second)); err != nil {
			return err
		}
		start()
		if _, err := fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", id, event.Kind, data); err != nil {
			return err
		}
		return rc.Flush()
	}
	end := func(reason string) {
		if !started {
			return
		}
		// Content-free closure is allowed after revocation; no queued payload,
		// account fact, terminal URL or private capability is included.
		last.Kind = "end"
		last.Payload, _ = json.Marshal(nativecontract.End{Reason: reason, ReloadSnapshot: reason != "detached"})
		last.ObservedAt = clock.Now()
		last.DroppedFrames = "0"
		_ = write(last)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-rotation:
			end("rotation")
			return
		case <-keepalive:
			err := fence(ctx, func(access nativecontract.ValidationContext) error {
				if access.Private {
					return Refuse(nativecontract.ReasonWrongPrincipal)
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if last.Kind == "" {
					return nil
				}
				if err := rc.SetWriteDeadline(time.Now().Add(nativecontract.StreamWriteDeadlineSeconds * time.Second)); err != nil {
					return err
				}
				start()
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return err
				}
				return rc.Flush()
			})
			if err != nil {
				end(closureReason(err))
				return
			}
			keepalive = clock.After(nativecontract.KeepaliveSeconds * time.Second)
		case event, ok := <-sub.Events():
			if !ok {
				err := fence(ctx, func(access nativecontract.ValidationContext) error { return nil })
				if err != nil {
					end(closureReason(err))
				} else {
					end("runtime_changed")
				}
				return
			}
			err := fence(ctx, func(access nativecontract.ValidationContext) error {
				if access.Private {
					return Refuse(nativecontract.ReasonWrongPrincipal)
				}
				if !Allows(access.View, event.Kind) {
					return Refuse(nativecontract.ReasonUnsupported)
				}
				clean, err := projector.Sanitize(ctx, event, access)
				if err != nil {
					return err
				}
				if !started && clean.Kind != "snapshot" {
					return Refuse(nativecontract.ReasonInvalidInput)
				}
				if started && (clean.Generation != last.Generation || !clean.Binding.Equal(last.Binding)) {
					return Refuse(nativecontract.ReasonWrongGeneration)
				}
				if err := write(clean); err != nil {
					return err
				}
				last = clean
				return nil
			})
			if err != nil {
				if !started {
					WriteError(w, err)
				} else {
					end(closureReason(err))
				}
				return
			}
			if event.Kind == "end" {
				return
			}
			if event.Kind == "gap" {
				end("runtime_changed")
				return
			}
		}
	}
}

func closureReason(err error) string {
	var invalid *nativecontract.ValidationError
	if errors.As(err, &invalid) {
		switch invalid.Reason {
		case nativecontract.ReasonGrantMissing, nativecontract.ReasonGrantExpired, nativecontract.ReasonPermissionRevoked, nativecontract.ReasonWrongPrincipal, nativecontract.ReasonControllerExpired:
			return "revoked"
		case nativecontract.ReasonWrongGeneration, nativecontract.ReasonIdentityMismatch:
			return "runtime_changed"
		}
	}
	return "unsupported"
}
