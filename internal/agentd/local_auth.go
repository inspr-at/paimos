// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import "context"

// LocalAuthenticator is injected in tests only. Production uses LocalAuthentication
// inside agentd; no socket field, helper, command or environment can confirm it.
type LocalAuthenticator interface {
	Confirm(context.Context, string) error
}

type systemLocalAuthenticator struct{}
