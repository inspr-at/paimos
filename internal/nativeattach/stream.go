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

// readStream has bounded per-frame and queue memory and a rotation deadline.
// The server must still authorize each frame/keepalive. There is no reconnect
// to a new binding or automatic resending of uncertain input.
func (s *Session) readStream(ctx context.Context, send func(streamItem) bool) {
	ctx, cancel := context.WithTimeout(ctx, (nativecontract.StreamRotationSeconds+5)*time.Second)
	defer cancel()
	b := s.binding
	q := url.Values{"view": {"structured"}, "expected_runtime_epoch": {b.RuntimeEpoch}, "expected_harness_session_id": {b.HarnessSessionID}, "expected_binding_revision": {strconv.FormatInt(b.BindingRevision, 10)}}
	res, err := s.wire.request(ctx, http.MethodGet, s.wire.path(b, "stream")+"?"+q.Encode(), nil, s.headers(false))
	if err != nil {
		send(streamItem{err: err})
		return
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || strings.Split(res.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		send(streamItem{err: errors.New("invalid native event stream")})
		return
	}
	validator := s.validator
	validation := nativecontract.ValidationContext{Binding: &b, OwnedSessionID: b.OwnedSessionID, PrincipalKind: "person", Generation: s.generation, Capabilities: &s.snapshot.Capabilities, View: "structured"}
	guard := nativecontract.SequenceGuard{Binding: b, Generation: s.generation}
	first := true
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
					send(streamItem{err: safeValidation(err)})
					return
				}
				var event nativecontract.StreamEvent
				if json.Unmarshal(data, &event) != nil {
					send(streamItem{err: errors.New("invalid native event")})
					return
				}
				want, _ := nativecontract.EventID(event.Generation, mustSequence(event.Seq))
				if id != want || (kind != "" && kind != event.Kind && kind != "message") {
					send(streamItem{err: errors.New("native event correlation mismatch")})
					return
				}
				if first {
					guard.Next = mustSequence(event.Seq)
					first = false
				}
				if event.Kind != "gap" {
					if err = guard.Accept(event); err != nil {
						send(streamItem{err: errors.New("native stream sequence gap; attach again explicitly")})
						return
					}
				}
				if !send(streamItem{event: event}) {
					return
				}
				if event.Kind == "gap" || event.Kind == "end" {
					return
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
			send(streamItem{err: errors.New("native stream frame exceeds limit")})
			return
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if len(data) != 0 {
				data = append(data, '\n')
			}
			if len(data)+len(value) > nativecontract.ExportedFrameBytes {
				send(streamItem{err: errors.New("native stream data exceeds limit")})
				return
			}
			data = append(data, value...)
		case "id":
			if len(value) > 64 {
				send(streamItem{err: errors.New("invalid native event id")})
				return
			}
			id = value
		case "event":
			kind = value
		}
	}
	if ctx.Err() != nil {
		send(streamItem{err: ctx.Err()})
		return
	}
	if scan.Err() != nil {
		send(streamItem{err: errors.New("native stream disconnected; delivery not confirmed")})
		return
	}
	send(streamItem{err: io.EOF})
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
		s.authorityLost = true
		s.capability = ""
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
