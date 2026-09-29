// SPDX-License-Identifier: AGPL-3.0-only
package agentdwire

import (
	"context"
	"github.com/inspr-at/paimos/internal/agentd"
)

// Attach carries public choices only. Runtime and device proofs remain in agentd.
func (c Client) Attach(ctx context.Context, in agentd.AttachLocalRequest) (agentd.AttachLocalView, error) {
	var out agentd.AttachLocalView
	err := c.lifecycleRequest(ctx, "POST", "/v1/attach", in, &out)
	return out, err
}
