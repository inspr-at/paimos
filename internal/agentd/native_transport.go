// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
	"github.com/inspr-at/paimos/internal/nativestream"
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
		nativestream.Headers(w)
		if !authorize(r) {
			nativestream.WriteError(w, &nativestream.Failure{Status: http.StatusUnauthorized, Reason: nativecontract.ReasonGrantMissing})
			return
		}
		if r.Header.Get(nativecontract.LocalProtocolHeader) != nativecontract.LocalProtocol {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonUnsupported))
			return
		}
		if r.URL.RawQuery != "" {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonInvalidInput))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, nativecontract.RequestBodyBytes)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonBufferFull))
			return
		}
		validator, err := nativecontract.NewValidator()
		if err != nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonQualificationMissing))
			return
		}
		envelope, err := validator.DecodeLocal(raw, nativecontract.LocalProtocol, "start", nativecontract.ValidationContext{PrincipalKind: "person"})
		if err != nil {
			nativestream.WriteError(w, err)
			return
		}
		var local nativecontract.LocalStart
		if err = validator.DecodeJSON("NativeLocalStart", envelope.Params, &local); err != nil {
			nativestream.WriteError(w, err)
			return
		}
		if s.native == nil || s.native.config == nil || s.native.config.StartLocal == nil {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonQualificationMissing))
			return
		}
		peer, ok := r.Context().Value(attachPeerKey{}).(attachObservation)
		current, peerErr := observeAttachProcess(peer.PID)
		if !ok || peerErr != nil || peer.UID != os.Getuid() || !sameAttachProcessIdentity(peer, current) {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonWrongPrincipal))
			return
		}
		if local.Intent.Role != "coordinator" || local.Intent.Profile != "native" || local.Intent.Harness != Claude {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonUnsupported))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), nativecontract.LocalOperationSeconds*time.Second)
		defer cancel()
		result, err := s.native.config.StartLocal(ctx, local)
		if err != nil {
			nativestream.WriteError(w, err)
			return
		}
		if result.Receipt.RequestID != local.Intent.RequestID || result.Receipt.Operation != "start" {
			nativestream.WriteError(w, nativestream.Refuse(nativecontract.ReasonIdentityMismatch))
			return
		}
		if _, err := nativestream.Encode(validator, "NativeOperationResult", result); err != nil {
			nativestream.WriteError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if result.Receipt.State == "accepted" || result.Receipt.State == "queued" {
			w.WriteHeader(http.StatusAccepted)
		}
		_ = json.NewEncoder(w).Encode(result)
	}
}
