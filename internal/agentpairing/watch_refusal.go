// SPDX-License-Identifier: AGPL-3.0-only
package agentpairing

// Preserve existing statuses, codes and messages. This extra cause is only
// attached after the computer proof and principal or person owner were checked.
type attachDiagnostic struct {
	error
	cause string
}

func (e *attachDiagnostic) Unwrap() error { return e.error }

func attachFailure(status int, code, message, cause string) error {
	return &attachDiagnostic{error: fail(status, code, message), cause: cause}
}
