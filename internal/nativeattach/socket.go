// SPDX-License-Identifier: AGPL-3.0-only

package nativeattach

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

type socketWire struct {
	socket    string
	http      *http.Client
	transport *http.Transport
	validator *nativecontract.Validator
}

func newSocketWire(socket string, v *nativecontract.Validator) (*socketWire, error) {
	if !filepath.IsAbs(socket) || filepath.Clean(socket) != socket || agentsetup.CheckSocket(socket) != nil {
		return nil, errors.New("protected owner socket unavailable")
	}
	tr := &http.Transport{ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10, MaxConnsPerHost: 3, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		if agentsetup.CheckSocket(socket) != nil {
			return nil, errors.New("protected owner socket unavailable")
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &socketWire{socket: socket, transport: tr, validator: v, http: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (w *socketWire) close() { w.transport.CloseIdleConnections() }

func (*socketWire) path(b nativecontract.Binding, method string) string {
	if method == "renew" {
		method = "attach/renew"
	}
	return "/v1/native-sessions/" + b.OwnedSessionID + "/" + method
}

func (w *socketWire) request(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, error) {
	// Read through the existing no-follow, owner-only store. The proof is used
	// only as a header, never displayed or placed in a URL or diagnostics.
	proof, err := agentsetup.ReadPrivateFile(w.socket+".token", 32)
	if err != nil || len(proof) != 32 {
		clear(proof)
		return nil, errors.New("owner socket proof unavailable")
	}
	defer clear(proof)
	r, err := http.NewRequestWithContext(ctx, method, "http://agentd"+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid local session request")
	}
	r.Header.Set("Authorization", "Bearer "+string(proof))
	r.Header.Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	r.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		r.Header.Set("Accept", "text/event-stream, application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	res, err := w.http.Do(r)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("local native session unavailable; outcome not confirmed")
	}
	if res.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
		res.Body.Close()
		return nil, errors.New("native session protocol mismatch")
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusAccepted {
		defer res.Body.Close()
		raw, readErr := readBounded(res.Body, nativecontract.ReceiptBytes)
		if readErr == nil && w.validator.ValidateJSON("NativeError", raw) == nil {
			var out nativecontract.Error
			if json.Unmarshal(raw, &out) == nil {
				return nil, &nativecontract.ValidationError{Reason: out.Reason, Path: "$"}
			}
		}
		return nil, fmt.Errorf("native session request refused (HTTP %d)", res.StatusCode)
	}
	return res, nil
}

func readBounded(r io.Reader, limit int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil || len(raw) > limit {
		return nil, errors.New("invalid or oversized native response")
	}
	return raw, nil
}

func (w *socketWire) post(ctx context.Context, b nativecontract.Binding, method, requestID string, params any, schema string, headers map[string]string, dest any) error {
	payload, err := json.Marshal(params)
	if err != nil {
		return errors.New("invalid local session command")
	}
	body, err := json.Marshal(nativecontract.LocalEnvelope{Protocol: nativecontract.LocalProtocol, RequestID: requestID, Method: method, ExpectedBinding: &b, Params: payload})
	if err != nil {
		return errors.New("invalid local session command")
	}
	if err = w.validator.ValidateJSON("NativeLocalEnvelope", body); err != nil {
		return safeValidation(err)
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	res, err := w.request(ctx, http.MethodPost, w.path(b, method), body, headers)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := readBounded(res.Body, nativecontract.RequestBodyBytes)
	if err != nil {
		return err
	}
	if err = w.validator.ValidateJSON(schema, raw); err != nil {
		return safeValidation(err)
	}
	return json.Unmarshal(raw, dest)
}

func readCoordinates(b nativecontract.Binding, generation int64) url.Values {
	return url.Values{
		"expected_runtime_epoch":      {b.RuntimeEpoch},
		"expected_harness_session_id": {b.HarnessSessionID},
		"expected_binding_revision":   {strconv.FormatInt(b.BindingRevision, 10)},
		"generation":                  {strconv.FormatInt(generation, 10)},
	}
}

func (w *socketWire) receipts(ctx context.Context, b nativecontract.Binding, generation int64, headers map[string]string, after string, dest *nativecontract.ReceiptPage) error {
	q := readCoordinates(b, generation)
	q.Set("limit", "25")
	if after != "" {
		q.Set("after", after)
	}
	ctx, cancel := context.WithTimeout(ctx, nativecontract.LocalOperationSeconds*time.Second)
	defer cancel()
	res, err := w.request(ctx, http.MethodGet, w.path(b, "receipts")+"?"+q.Encode(), nil, headers)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := readBounded(res.Body, nativecontract.ReceiptPageBytes)
	if err != nil {
		return err
	}
	if err = w.validator.ValidateJSON("NativeReceiptPage", raw); err != nil {
		return safeValidation(err)
	}
	if err = json.Unmarshal(raw, dest); err != nil {
		return err
	}
	for _, r := range dest.Items {
		if r.Binding != nil && !r.Binding.Equal(b) {
			return ErrBindingChanged
		}
	}
	if dest.NextCursor != nil && (len(*dest.NextCursor) > 512 || strings.ContainsAny(*dest.NextCursor, "\r\n")) {
		return errors.New("invalid receipt cursor")
	}
	return nil
}
