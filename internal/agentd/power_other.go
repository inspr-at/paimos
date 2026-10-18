//go:build !darwin

// SPDX-License-Identifier: AGPL-3.0-only
package agentd

func (systemPower) PreventIdleSleep() (func(), error) { return func() {}, nil }
