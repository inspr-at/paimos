// SPDX-License-Identifier: AGPL-3.0-only
package cli

import "github.com/inspr-at/paimos/internal/agentactivity"

type hookActivity struct {
	Session  string                 `json:"session_id"`
	Activity agentactivity.Activity `json:"activity"`
}
