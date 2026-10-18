// SPDX-License-Identifier: AGPL-3.0-only
package agentdwire

import (
	"context"
	"errors"
	"strings"

	"github.com/inspr-at/paimos/internal/stepup"
)

// ConfirmStepUp verifies the daemon's paired origin BEFORE asking it to prompt.
// The POST body contains only challenge_id, never agent-provided prompt text.
func (c Client) ConfirmStepUp(ctx context.Context, origin, id string) (string, error) {
	if !stepup.ValidID(id) {
		return "", errors.New("invalid Touch ID challenge")
	}
	var info stepup.Info
	if err := c.lifecycleRequest(ctx, "GET", "/v1/step-up", nil, &info); err != nil {
		return "", err
	}
	if info.Origin != strings.TrimRight(origin, "/") || !stepup.ValidID(info.ComputerID) {
		return "", errors.New("local agentd belongs to a different instance; select this instance's agentd_state_root")
	}
	if !info.Ready {
		return "", errors.New("Touch ID needs a pairing upgrade; run aeon-agentd status for the prerequisite command")
	}
	var out stepup.Proof
	if err := c.lifecycleRequest(ctx, "POST", "/v1/step-up", stepup.Request{ChallengeID: id}, &out); err != nil {
		return "", err
	}
	if out.ChallengeID != id || !stepup.ValidSignature(out.Signature) {
		return "", errors.New("invalid local Touch ID proof")
	}
	return out.Signature, nil
}
