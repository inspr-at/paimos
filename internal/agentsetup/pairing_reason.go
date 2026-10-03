// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import "errors"

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
