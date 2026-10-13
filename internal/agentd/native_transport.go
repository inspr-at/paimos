// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

type NativeStartEnvelope struct {
	Protocol        string                    `json:"protocol"`
	RequestID       string                    `json:"request_id"`
	Method          string                    `json:"method"`
	ExpectedBinding json.RawMessage           `json:"expected_binding"`
	Params          NativeSessionStartRequest `json:"params"`
}

func serveNativeStart(s *Supervisor, authorize func(*http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set(NativeSessionProtocolHeader, NativeSessionProtocol)
		if !authorize(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get(NativeSessionProtocolHeader) != NativeSessionProtocol {
			http.Error(w, "native_protocol_mismatch", http.StatusBadRequest)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		var envelope NativeStartEnvelope
		if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Protocol != NativeSessionProtocol || envelope.Method != "start" || envelope.RequestID != envelope.Params.RequestID || string(envelope.ExpectedBinding) != "null" {
			http.Error(w, "native_invalid_request", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		defer cancel()
		result, err := s.StartNativeSession(ctx, envelope.Params)
		if err != nil {
			code, status := "native_start_unconfirmed", http.StatusConflict
			switch {
			case errors.Is(err, ErrNativeUnqualified):
				code, status = "native_coordinator_unqualified", http.StatusServiceUnavailable
			case errors.Is(err, ErrScope):
				code, status = "native_scope_mismatch", http.StatusBadRequest
			case errors.Is(err, ErrReplay):
				code = "native_request_conflict"
			}
			http.Error(w, code, status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}
