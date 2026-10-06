// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"errors"
	"net/url"
)

// VerificationApprovalURL reads the existing enrollment without contacting
// the daemon, migrating the vault, or changing any local lifecycle state.
// The URL selects an account; only its signed-in owner can authorize a check.
func (e *Engine) VerificationApprovalURL(account string) (string, error) {
	if !uuidPattern.MatchString(account) {
		return "", errors.New("verify requires an enrolled account UUID")
	}
	s, err := e.loadSnapshot(true)
	if err != nil {
		return "", err
	}
	if err = ValidateOrigin(s.Origin); err != nil {
		return "", err
	}
	if s.View.ComputerState != "connected" || s.Removed[account] {
		return "", errors.New("account is not connected")
	}
	for _, a := range s.View.Enrollments {
		if a.AccountID != account || a.State != "connected" {
			continue
		}
		u, _ := url.Parse(s.Origin)
		u.Path = "/agents"
		u.RawQuery = url.Values{"verify_account": {account}}.Encode()
		return u.String(), nil
	}
	return "", errors.New("account is not enrolled on this computer")
}
