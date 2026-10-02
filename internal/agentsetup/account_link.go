// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import "time"

// AccountLinkView carries only the user-facing code and result, never proof.
type AccountLinkView struct {
	AccountID  string     `json:"account_id"`
	RequestID  string     `json:"request_id,omitempty"`
	State      string     `json:"state"`
	ShowPrompt bool       `json:"show_prompt"`
	ShowResult bool       `json:"show_result"`
	Code       string     `json:"user_code,omitempty"`
	URI        string     `json:"verification_uri,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	PersonName string     `json:"person_name,omitempty"`
}
