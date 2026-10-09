// SPDX-License-Identifier: AGPL-3.0-only
package agentsetup

import (
	"context"
	"errors"
	"slices"
)

const LedgerCapability = "ledger-v1"
const LedgerGenerationHeader = "X-Aeon-Ledger-Generation"

// RequireLedgerServer checks each member's fresh lifecycle advertisement before
// shared mode. An old server's absent advertisement must never be guessed from
// its version. The caller retains ownership of local import and service changes.
func RequireLedgerServer(view View) error {
	if !slices.Contains(view.ServerCapabilities, LedgerCapability) {
		return errors.New("shared ledger requires a server advertising ledger-v1")
	}
	return nil
}

// EnrollLedger is the server half of handover, including for an existing
// computer. Call only after importing occupancy under the dispatch mutex. S5
// owns that local ledger protocol; this client never invents an empty import.
func (c HTTPClient) EnrollLedger(ctx context.Context, token secret, generation string) (View, error) {
	var out View
	err := c.call(ctx, "POST", "/self/ledger", token, struct {
		Generation string `json:"generation"`
	}{generation}, &out)
	return out, err
}
