// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"errors"
	"math/big"
	"strings"
)

const usdScale = 12

// parseUSD reads a non-negative decimal into an integer scaled by 10^12.
// More than 12 fractional digits, a sign, or an exponent is rejected so a sum
// cannot silently round.
func parseUSD(raw string) (*big.Int, error) {
	whole, frac, dotted := strings.Cut(raw, ".")
	if dotted && frac == "" {
		return nil, errBadUSD
	}
	if !dotted {
		frac = ""
	}
	if whole == "" || !digitsOnly(whole) || !digitsOnly(frac) || len(frac) > usdScale {
		return nil, errBadUSD
	}
	if len(frac) < usdScale {
		frac += strings.Repeat("0", usdScale-len(frac))
	}
	n := new(big.Int)
	if _, ok := n.SetString(whole+frac, 10); !ok {
		return nil, errBadUSD
	}
	return n, nil
}

func formatUSD(n *big.Int) string {
	s := n.String()
	if len(s) <= usdScale {
		s = strings.Repeat("0", usdScale+1-len(s)) + s
	}
	split := len(s) - usdScale
	return s[:split] + "." + s[split:]
}

func digitsOnly(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

var errBadUSD = errors.New("invalid estimated cost")
