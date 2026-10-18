// SPDX-License-Identifier: AGPL-3.0-only

package hooknote

// Adopt stores the outcome the server already recorded for nonce.
// It replaces the local proposal from Commit. A confirmed shown, dropped,
// or uncertain answer is stored as the server returned it. Commit still
// refuses to upgrade uncertain or to replace one local terminal outcome
// with another; that rule does not apply to a confirmed server answer.
func (r *Registry) Adopt(nonce, outcome string) (string, error) {
	if r == nil {
		return "", ErrPeer
	}
	if !ValidNonce(nonce) || !ValidOutcome(outcome) {
		return "", ErrMalformedNonce
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.receipts == nil {
		r.receipts = map[string]string{}
	}
	r.receipts[nonce] = outcome
	return outcome, nil
}
