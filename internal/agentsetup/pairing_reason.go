// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"errors"
	"os"
	"strconv"

	"github.com/inspr-at/paimos/internal/agentsecurity"
)

const (
	PairingSyncFailed         = "pairing_sync_failed"
	PairingLocalUnavailable   = "the local pairing state could not be read or saved"
	PairingServerUnavailable  = "the pairing server could not confirm the lifecycle"
	PairingViewInvalid        = "the pairing response did not match the approved computer"
	PairingAccountUnapproved  = "the pairing response contains an unapproved account"
	PairingFenceUnavailable   = "the local dispatch fence could not be confirmed"
	PairingRuntimeUnavailable = "the approved runtime configuration could not be refreshed"
)

var ErrUnapprovedAccount = errors.New("response contains an unapproved account")

type pairingSyncError struct {
	err    error
	detail string
}

func (e *pairingSyncError) Error() string { return e.err.Error() }
func (e *pairingSyncError) Unwrap() error { return e.err }

// PairingFailureDetail publishes the failed phase, never arbitrary error text,
// server payloads, paths or bindings. Store-open errors use the local fallback.
func PairingFailureDetail(err error) string {
	if errors.Is(err, ErrUnapprovedAccount) {
		return PairingAccountUnapproved
	}
	var failure *pairingSyncError
	if errors.As(err, &failure) {
		if detail := SafePairingDetail(failure.detail); detail != "" {
			return detail
		}
	}
	return PairingLocalUnavailable
}

func SafePairingDetail(detail string) string {
	switch detail {
	case PairingLocalUnavailable, PairingServerUnavailable, PairingViewInvalid,
		PairingAccountUnapproved, PairingFenceUnavailable, PairingRuntimeUnavailable:
		return detail
	}
	return ""
}

// PairingFailureCause retains the first actionable cause without ever logging
// arbitrary errors (HTTP bodies, capabilities, paths or vendor output).
func PairingFailureCause(err error) string {
	var api *APIError
	if errors.As(err, &api) {
		if api.StatusCode >= 100 && api.StatusCode <= 599 {
			return "http_" + strconv.Itoa(api.StatusCode)
		}
		switch api.Code {
		case "unreachable", "unavailable", "rate_limited", "invalid_request", "forbidden", "not_found", "conflict", "pairing_revoked":
			return api.Code
		}
	}
	switch {
	case errors.Is(err, ErrBusy):
		return "store_busy"
	case errors.Is(err, agentsecurity.ErrDenied):
		return "keychain_denied"
	case errors.Is(err, os.ErrNotExist):
		return "state_missing"
	case errors.Is(err, ErrUnsafePath):
		return "unsafe_state"
	case errors.Is(err, ErrCollision):
		return "state_collision"
	case errors.Is(err, ErrUnapprovedAccount):
		return "account_unapproved"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}
	return "unclassified"
}

func PairingRetryable(err error) bool {
	if errors.Is(err, ErrBusy) {
		return true
	}
	if PairingFailureDetail(err) != PairingServerUnavailable {
		return false
	}
	var api *APIError
	if errors.As(err, &api) && api.StatusCode != 0 {
		return api.StatusCode == 408 || api.StatusCode == 429 || api.StatusCode >= 500 && api.StatusCode <= 599
	}
	switch PairingFailureCause(err) {
	case "unreachable", "unavailable", "rate_limited", "timeout":
		return true
	}
	return false
}
