//go:build !darwin || !cgo

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"context"
	"errors"

	"github.com/inspr-at/paimos/internal/attachwatch"
)

func CurrentLocalAuthCapability() string { return attachwatch.LocalAuthUnsupported }

func (systemLocalAuthenticator) Confirm(context.Context, string) error {
	return errors.New("local confirmation unavailable: requires a signed macOS paimos-agentd or aeon-agentd build with LocalAuthentication; Linux is not supported")
}
