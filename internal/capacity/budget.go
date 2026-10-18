// SPDX-License-Identifier: AGPL-3.0-only
package capacity

import (
	"errors"
	"math"
	"time"
)

// Budget is money, with no fabricated percent window or replenishment time.
// A key cap and an account balance are distinct; null means unknown.
type Budget struct {
	Currency        string    `json:"currency"`
	Source          string    `json:"source"`
	ReadAt          time.Time `json:"read_at"`
	KeyUsageUSD     float64   `json:"key_usage_usd"`
	KeyLimitUSD     *float64  `json:"key_limit_usd"`
	KeyRemainingUSD *float64  `json:"key_remaining_usd"`
	BalanceUSD      *float64  `json:"balance_usd"`
}

func (b Budget) Validate(now time.Time) error {
	valid := func(v float64) bool { return v >= 0 && v <= 1e12 && !math.IsNaN(v) && !math.IsInf(v, 0) }
	if b.Currency != "USD" || b.Source != "agentd" || b.ReadAt.IsZero() || b.ReadAt.After(now.Add(time.Minute)) || !valid(b.KeyUsageUSD) {
		return errors.New("invalid capacity budget")
	}
	for _, p := range []*float64{b.KeyLimitUSD, b.KeyRemainingUSD, b.BalanceUSD} {
		if p != nil && !valid(*p) {
			return errors.New("invalid capacity budget")
		}
	}
	if b.KeyRemainingUSD != nil && (b.KeyLimitUSD == nil || *b.KeyRemainingUSD > *b.KeyLimitUSD) {
		return errors.New("invalid key budget")
	}
	return nil
}

func (b *Budget) UnmarshalJSON(raw []byte) error {
	type wire Budget
	var v wire
	if err := requiredJSON(raw, &v, "currency", "source", "read_at", "key_usage_usd"); err != nil {
		return err
	}
	*b = Budget(v)
	return nil
}
