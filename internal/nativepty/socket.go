// SPDX-License-Identifier: AGPL-3.0-only

package nativepty

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
)

type socketWire struct {
	socket    string
	client    *http.Client
	transport *http.Transport
	validator *nativecontract.Validator
}

func NewSocketWire(socket string) (TakeWire, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || agentsetup.CheckSocket(socket) != nil {
		return nil, errors.New("protected owner socket unavailable")
	}
	v, err := nativecontract.NewValidator()
	if err != nil {
		return nil, err
	}
	t := &http.Transport{ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, MaxConnsPerHost: 3, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if agentsetup.CheckSocket(socket) != nil {
			return nil, errors.New("protected owner socket unavailable")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &socketWire{socket: socket, transport: t, validator: v, client: &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (w *socketWire) Close() { w.transport.CloseIdleConnections() }
func localPath(b nativecontract.Binding, method string) string {
	switch method {
	case "renew":
		method = "attach/renew"
	case "resize":
		method = "terminal/resize"
	case "pty_input":
		method = "terminal/input"
	}
	return "/v1/native-sessions/" + b.OwnedSessionID + "/" + method
}
func (w *socketWire) request(ctx context.Context, method, path string, body []byte, viewer, capability string) (*http.Response, error) {
	proof, err := agentsetup.ReadPrivateFile(w.socket+".token", 32)
	if err != nil || len(proof) != 32 {
		clear(proof)
		return nil, errors.New("owner socket proof unavailable")
	}
	defer clear(proof)
	r, err := http.NewRequestWithContext(ctx, method, "http://agentd"+path, bytes.NewReader(body))
	if err != nil {
		return nil, refuse(nativecontract.ReasonInvalidInput)
	}
	r.Header.Set("Authorization", "Bearer "+string(proof))
	r.Header.Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set(nativecontract.ViewerHeader, viewer)
	r.Header.Set(nativecontract.ControlHeader, capability)
	if method == http.MethodGet {
		r.Header.Set("Accept", "text/event-stream")
	}
	res, err := w.client.Do(r)
	if err != nil {
		return nil, safeError(ctx.Err())
	}
	if res.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		res.Body.Close()
		return nil, refuse(nativecontract.ReasonUnsupported)
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		defer res.Body.Close()
		data, err := readBounded(res.Body, nativecontract.ReceiptBytes)
		if err == nil && w.validator.ValidateJSON("NativeError", data) == nil {
			var out nativecontract.Error
			_ = json.Unmarshal(data, &out)
			return nil, refuse(out.Reason)
		}
		return nil, refuse(nativecontract.ReasonNativeFailure)
	}
	return res, nil
}
func readBounded(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(data) > limit {
		return nil, refuse(nativecontract.ReasonBufferFull)
	}
	return data, nil
}
func (w *socketWire) Post(ctx context.Context, binding nativecontract.Binding, method, requestID string, params any, schema, viewer, capability string, dest any) error {
	requestSchema := map[string]string{"attach": "NativeAttachIntent", "renew": "NativeAttachRenew", "detach": "NativeDetach", "take": "NativeTakeIntent", "return": "NativeReturnIntent", "resize": "NativeResize", "pty_input": "NativePTYInput"}[method]
	if requestSchema == "" {
		return refuse(nativecontract.ReasonUnsupported)
	}
	payload, err := nativestream.Encode(w.validator, requestSchema, params)
	if err != nil {
		return safeError(err)
	}
	body, err := nativestream.Encode(w.validator, "NativeLocalEnvelope", nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: requestID, Method: method, ExpectedBinding: &binding, Params: payload})
	if err != nil {
		return safeError(err)
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	res, err := w.request(ctx, http.MethodPost, localPath(binding, method), body, viewer, capability)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, err := readBounded(res.Body, nativecontract.RequestBodyBytes)
	if err != nil {
		return err
	}
	if err = w.validator.ValidateJSON(schema, data); err != nil {
		return safeError(err)
	}
	return json.Unmarshal(data, dest)
}

// Watch has no input/device-reply/paste callback. It consumes the exact current
// generation once; loss/gaps/end require explicit reconnect and renewed control.
func (w *socketWire) Watch(ctx context.Context, view nativecontract.SessionView, viewer string, consume func(nativecontract.StreamEvent) error) error {
	if view.CurrentBinding == nil {
		return refuse(nativecontract.ReasonWrongGeneration)
	}
	b := cloneBinding(*view.CurrentBinding)
	q := url.Values{"view": {"terminal"}, "expected_runtime_epoch": {b.RuntimeEpoch}, "expected_harness_session_id": {b.HarnessSessionID}, "expected_binding_revision": {strconv.FormatInt(b.BindingRevision, 10)}}
	ctx, cancel := context.WithTimeout(ctx, (nativecontract.StreamRotationSeconds+5)*time.Second)
	defer cancel()
	res, err := w.request(ctx, http.MethodGet, localPath(b, "stream")+"?"+q.Encode(), nil, viewer, "")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || strings.Split(res.Header.Get("Content-Type"), ";")[0] != "text/event-stream" {
		return refuse(nativecontract.ReasonInvalidInput)
	}
	validation := nativecontract.ValidationContext{Binding: &b, OwnedSessionID: b.OwnedSessionID, PrincipalKind: "person", Generation: view.Generation, Capabilities: &view.Capabilities, View: "terminal", Mode: "terminal", State: "taken"}
	guard := nativecontract.SequenceGuard{Binding: b, Generation: view.Generation}
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 4096), nativecontract.ExportedFrameBytes+1024)
	var data []byte
	var id, kind string
	budget := 0
	first := true
	for scan.Scan() {
		line := strings.TrimSuffix(scan.Text(), "\r")
		if line == "" {
			if len(data) > 0 {
				if err = w.validator.ValidateBoundJSON("NativeStreamEvent", data, validation); err != nil {
					return safeError(err)
				}
				var event nativecontract.StreamEvent
				_ = json.Unmarshal(data, &event)
				seq, err := strconv.ParseUint(event.Seq, 10, 64)
				if err != nil {
					return refuse(nativecontract.ReasonInvalidInput)
				}
				want, _ := nativecontract.EventID(event.Generation, seq)
				if id != want || kind != event.Kind {
					return refuse(nativecontract.ReasonIdentityMismatch)
				}
				if first {
					if event.Kind != "snapshot" {
						return refuse(nativecontract.ReasonWrongGeneration)
					}
					guard.Next, first = seq, false
				}
				if err = guard.Accept(event); err != nil {
					return safeError(err)
				}
				if err = consume(event); err != nil {
					return err
				}
				if event.Kind == "gap" || event.Kind == "end" {
					return refuse(nativecontract.ReasonCursorExpired)
				}
			}
			data, id, kind, budget = nil, "", "", 0
			continue
		}
		budget += len(line)
		if budget > nativecontract.ExportedFrameBytes+1024 {
			return refuse(nativecontract.ReasonBufferFull)
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			if len(data) > 0 {
				data = append(data, '\n')
			}
			if len(data)+len(value) > nativecontract.ExportedFrameBytes {
				return refuse(nativecontract.ReasonBufferFull)
			}
			data = append(data, value...)
		case "id":
			if len(value) > 64 {
				return refuse(nativecontract.ReasonInvalidInput)
			}
			id = value
		case "event":
			if len(value) > 64 {
				return refuse(nativecontract.ReasonInvalidInput)
			}
			kind = value
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("native terminal disconnected; control must be reacquired explicitly")
}
