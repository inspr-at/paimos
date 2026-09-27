// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"encoding/json"
	"math/big"
)

// vendorMicros converts a vendor JSON decimal to integer microdollars.
// It matches internal/agentd usdMicros, including half-up rounding. The
// decimal never passes through a binary float. sessionusage uses it only
// to reject a malformed cost; the amount is not reported.
func vendorMicros(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || len(raw) > 64 || raw[0] == '"' || string(raw) == "null" {
		return 0, false
	}
	value, ok := new(big.Rat).SetString(string(raw))
	if !ok || value.Sign() < 0 {
		return 0, false
	}
	value.Mul(value, big.NewRat(1_000_000, 1))
	n := new(big.Int).Quo(value.Num(), value.Denom())
	remainder := new(big.Int).Rem(value.Num(), value.Denom())
	if remainder.Mul(remainder, big.NewInt(2)).Cmp(value.Denom()) >= 0 {
		n.Add(n, big.NewInt(1))
	}
	if !n.IsInt64() {
		return 0, false
	}
	return n.Int64(), true
}
