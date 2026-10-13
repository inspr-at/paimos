// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

type streamItem struct {
	event nativecontract.StreamEvent
	err   error
}

var errStreamRotation = errors.New("native stream rotation")

// readStream has bounded per-frame and queue memory and a rotation deadline.
// Only planned rotation resumes the same viewer and gapless sequence guard.
// The server must still authorize each subscription and frame/keepalive.
// There is no reconnect to a new binding or resending of uncertain input.
func (s *Session) readStream(ctx context.Context, send func(streamItem) bool) {
	guard := nativecontract.SequenceGuard{Binding: s.binding, Generation: s.generation}
	lastID := ""
	for {
		err := s.readStreamOnce(ctx, &guard, &lastID, send)
		if errors.Is(err, errStreamRotation) {
			if ctx.Err() == nil {
				continue
			}
			err = ctx.Err()
		}
		if err != nil {
			send(streamItem{err: err})
		}
		return
	}
}

func (s *Session) readStreamOnce(ctx context.Context, guard *nativecontract.SequenceGuard, lastID *string, send func(streamItem) bool) error {
	ctx, cancel := context.WithCancelCause(ctx)
	rotation := s.clock.After((nativecontract.StreamRotationSeconds + 5) * time.Second)
	deadlineDone := make(chan struct{})
	go func() {
		defer close(deadlineDone)
		select {
		case <-rotation:
			cancel(errStreamRotation)
		case <-ctx.Done():
		}
	}()
	defer func() {
		cancel(nil)
		<-deadlineDone
	}()
	b := s.binding
	q := url.Values{"view": {"structured"}, "expected_runtime_epoch": {b.RuntimeEpoch}, "expected_harness_session_id": {b.HarnessSessionID}, "expected_binding_revision": {strconv.FormatInt(b.BindingRevision, 10)}}
	headers := s.headers(false)
	if *lastID != "" {
		headers["Last-Event-ID"] = *lastID
	}
	res, err := s.wire.request(ctx, http.MethodGet, s.wire.path(b, "stream")+"?"+q.Encode(), nil, headers)
	if err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || strings.Split(res.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		return errors.New("invalid native event stream")
	}
	validator := s.validator
	validation := nativecontract.ValidationContext{Binding: &b, OwnedSessionID: b.OwnedSessionID, PrincipalKind: "person", Generation: s.generation, Capabilities: &s.snapshot.Capabilities, View: "structured"}
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 4096), nativecontract.ExportedFrameBytes+1024)
	var data []byte
	var id, kind string
	frameBytes := 0
	for scan.Scan() {
		line := strings.TrimSuffix(scan.Text(), "\r")
		if line == "" {
			if len(data) != 0 {
				if err = validator.ValidateBoundJSON("NativeStreamEvent", data, validation); err != nil {
					return safeValidation(err)
				}
				var event nativecontract.StreamEvent
				if json.Unmarshal(data, &event) != nil {
					return errors.New("invalid native event")
				}
				want, _ := nativecontract.EventID(event.Generation, mustSequence(event.Seq))
				if id != want || (kind != "" && kind != event.Kind && kind != "message") {
					return errors.New("native event correlation mismatch")
				}
				if *lastID == "" {
					guard.Next = mustSequence(event.Seq)
				}
				if event.Kind != "gap" {
					if err = guard.Accept(event); err != nil {
						return errors.New("native stream sequence gap; attach again explicitly")
					}
					*lastID = id
				}
				if event.Kind == "end" {
					var end nativecontract.End
					_ = json.Unmarshal(event.Payload, &end)
					if end.Reason == "rotation" {
						return errStreamRotation
					}
				}
				if !send(streamItem{event: event}) {
					return nil
				}
				if event.Kind == "gap" || event.Kind == "end" {
					return nil
				}
			}
			data, id, kind, frameBytes = nil, "", "", 0
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		frameBytes += len(line)
		if frameBytes > nativecontract.ExportedFrameBytes+1024 {
			return errors.New("native stream frame exceeds limit")
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if len(data) != 0 {
				data = append(data, '\n')
			}
			if len(data)+len(value) > nativecontract.ExportedFrameBytes {
				return errors.New("native stream data exceeds limit")
			}
			data = append(data, value...)
		case "id":
			if len(value) > 64 {
				return errors.New("invalid native event id")
			}
			id = value
		case "event":
			kind = value
		}
	}
	if ctx.Err() != nil {
		return context.Cause(ctx)
	}
	if scan.Err() != nil {
		return errors.New("native stream disconnected; delivery not confirmed")
	}
	return io.EOF
}

func mustSequence(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }

func (s *Session) observe(event nativecontract.StreamEvent) error {
	if !s.binding.Equal(event.Binding) || event.Generation != s.generation {
		s.authorityLost = true
		return ErrBindingChanged
	}
	switch event.Kind {
	case "snapshot":
		var snapshot nativecontract.SessionView
		_ = json.Unmarshal(event.Payload, &snapshot)
		if snapshot.CurrentBinding == nil || !s.binding.Equal(*snapshot.CurrentBinding) {
			s.authorityLost = true
			return ErrBindingChanged
		}
		s.snapshot = snapshot
		if s.control && (snapshot.Controller == nil || snapshot.Controller.ClientID != s.clientID || snapshot.Controller.Epoch != s.epoch) {
			s.authorityLost = true
			s.capability = ""
		}
	case "controller":
		var controller nativecontract.ControllerView
		_ = json.Unmarshal(event.Payload, &controller)
		if s.control && (controller.ClientID != s.clientID || controller.Epoch != s.epoch) {
			s.authorityLost = true
			s.capability = ""
		}
	case "control.lease_expired", "control.ended_by", "stream.paused":
		// These revoke the holder's control, not unrelated read-only viewers.
		// A later binding change still requires every viewer to attach again.
		if s.control {
			s.authorityLost = true
			s.capability = ""
		}
	case "receipt":
		var receipt nativecontract.Receipt
		_ = json.Unmarshal(event.Payload, &receipt)
		s.observeReceipt(receipt)
	case "structured":
		var frame nativecontract.StructuredFrame
		_ = json.Unmarshal(event.Payload, &frame)
		if frame.Kind == "turn_state" && frame.Reference != nil && frame.State != nil {
			switch *frame.State {
			case "running", "started", "turn_started":
				s.turn = *frame.Reference
			case "completed", "turn_completed", "idle":
				s.turn = ""
			}
		}
	case "assistant.final":
		s.turn = ""
	}
	return nil
}
