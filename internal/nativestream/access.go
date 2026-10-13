// SPDX-License-Identifier: AGPL-3.0-only

// Package nativestream supplies the bounded, transient transports for an owned
// native session. It neither launches processes nor owns durable chat history.
package nativestream

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/inspr-at/paimos/internal/nativecontract"
)

// Fence is request-scoped authority, supplied by the owning integration. It
// rechecks the authenticated actor, exact current binding, viewer grant, expiry,
// target permissions and owner consent under the access-change fence, and holds
// that fence through deliver. A check followed by an unlocked callback is unsafe.
// Native mutation implementations additionally recheck their grant under their
// final native-write lock. Never hold a database transaction across native I/O.
type Fence func(context.Context, func(nativecontract.ValidationContext) error) error

type Failure struct {
	Status int
	Reason nativecontract.Reason
}

func (e *Failure) Error() string { return string(e.Reason) }

func Refuse(reason nativecontract.Reason) error {
	return &nativecontract.ValidationError{Reason: reason, Path: "$"}
}

func WriteError(w http.ResponseWriter, err error) {
	status, reason := http.StatusServiceUnavailable, nativecontract.ReasonNativeFailure
	var failure *Failure
	var invalid *nativecontract.ValidationError
	switch {
	case errors.As(err, &failure):
		status, reason = failure.Status, failure.Reason
	case errors.As(err, &invalid):
		reason = invalid.Reason
		switch reason {
		case nativecontract.ReasonBufferFull:
			status = http.StatusRequestEntityTooLarge
		case nativecontract.ReasonInvalidInput, nativecontract.ReasonUnsupported:
			status = http.StatusBadRequest
		case nativecontract.ReasonWrongGeneration, nativecontract.ReasonStaleRevision, nativecontract.ReasonCursorExpired, nativecontract.ReasonIdentityMismatch:
			status = http.StatusConflict
		case nativecontract.ReasonGrantMissing, nativecontract.ReasonGrantExpired, nativecontract.ReasonPermissionRevoked, nativecontract.ReasonWrongPrincipal, nativecontract.ReasonControllerExpired:
			status = http.StatusForbidden
		}
	case errors.Is(err, context.DeadlineExceeded):
		status = http.StatusRequestTimeout
	}
	// Never echo native errors, request bodies, paths or authority material.
	Headers(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(nativecontract.Error{Reason: reason, Retryable: status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests})
}

func Headers(w http.ResponseWriter) {
	w.Header().Set(nativecontract.LocalProtocolHeader, nativecontract.LocalProtocol)
	w.Header().Set("Aeon-Contract", nativecontract.ContractVersion)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func clockOrDefault(clock nativecontract.Clock) nativecontract.Clock {
	if clock == nil {
		return wallClock{}
	}
	return clock
}

// Encode bounds wire data before validation. All transport errors are content-free.
func Encode(v *nativecontract.Validator, schema string, value any) ([]byte, error) {
	budget := nativecontract.RequestBodyBytes
	switch schema {
	case "NativeStreamEvent", "NativeExpertFrame", "NativeTerminalFrame":
		budget = nativecontract.ExportedFrameBytes
	case "NativePublish":
		budget = nativecontract.PublishBatchBytes
	case "NativeEventPage", "NativeReceiptPage":
		budget = nativecontract.EventPageBytes
	case "NativeReceipt":
		budget = nativecontract.ReceiptBytes
	}
	nodes := 16384
	if err := preflight(reflect.ValueOf(value), 0, &budget, &nodes); err != nil {
		return nil, err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, Refuse(nativecontract.ReasonInvalidInput)
	}
	if err := v.ValidateJSON(schema, data); err != nil {
		return nil, err
	}
	return data, nil
}

// Check aggregate scalar bytes, collection work and depth before json.Marshal.
// Escaping can expand this already bounded input by at most a fixed factor;
// C0's validator then checks the exact serialized byte budget.
func preflight(value reflect.Value, depth int, budget, nodes *int) error {
	if !value.IsValid() {
		return nil
	}
	*nodes -= 1
	if *nodes < 0 || depth > nativecontract.NativeFrameDecodeDepth {
		return Refuse(nativecontract.ReasonBufferFull)
	}
	if value.Type() == reflect.TypeFor[time.Time]() {
		*budget -= 40
		return nil
	}
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !value.IsNil() {
			return preflight(value.Elem(), depth, budget, nodes)
		}
	case reflect.String:
		*budget -= value.Len()
	case reflect.Array, reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			*budget -= value.Len()
			break
		}
		if value.Len() > nativecontract.EventPageItems {
			return Refuse(nativecontract.ReasonBufferFull)
		}
		for i := 0; i < value.Len(); i++ {
			if err := preflight(value.Index(i), depth+1, budget, nodes); err != nil {
				return err
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if value.Type().Field(i).IsExported() {
				if err := preflight(value.Field(i), depth+1, budget, nodes); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if value.Len() > nativecontract.EventPageItems {
			return Refuse(nativecontract.ReasonBufferFull)
		}
		iterator := value.MapRange()
		for iterator.Next() {
			if err := preflight(iterator.Key(), depth+1, budget, nodes); err != nil {
				return err
			}
			if err := preflight(iterator.Value(), depth+1, budget, nodes); err != nil {
				return err
			}
		}
	}
	if *budget < 0 {
		return Refuse(nativecontract.ReasonBufferFull)
	}
	return nil
}
