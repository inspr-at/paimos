// SPDX-License-Identifier: AGPL-3.0-only

package usagedashboard

import (
	"math/big"
	"testing"
)

func TestParseUSDKeepsTwelvePlaces(t *testing.T) {
	n, err := parseUSD("0.000000400000")
	if err != nil || n.Cmp(big.NewInt(400000)) != 0 {
		t.Fatalf("parse %v %v", n, err)
	}
	other, err := parseUSD("0.0000004")
	if err != nil || other.Cmp(n) != 0 {
		t.Fatalf("short form %v %v", other, err)
	}
	sum := new(big.Int).Add(n, other)
	if got := formatUSD(sum); got != "0.000000800000" {
		t.Fatalf("sum %s", got)
	}
	for _, raw := range []string{"-1.00", "1e-6", "1.0000000000001", "", "USD 1.00", "1."} {
		if _, err := parseUSD(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
