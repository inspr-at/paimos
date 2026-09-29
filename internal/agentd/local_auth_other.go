//go:build !darwin || !cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"
)

func (systemLocalAuthenticator) Confirm(context.Context, string) error {
	return errors.New("local confirmation unavailable: requires a signed macOS aeon-agentd build with LocalAuthentication; Linux is not supported")
}
