// SPDX-License-Identifier: AGPL-3.0-only

// Package delivery contains the shared primitives for release-container
// planning. Mutations must use the reconciled global lock order and check
// current authority in their final transaction; these primitives grant none.
package delivery

import (
	"errors"
	"strings"
)

const (
	rankDigits    = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	MaxRankDigits = 32
	MaxSeedItems  = 5000
)

// Conflict is a refused operation with a stable reason for future API callers.
// A database unique violation is an implementation error, not a Conflict.
type Conflict struct {
	Code    string
	Message string
}

func (e *Conflict) Error() string { return e.Message }

var (
	ErrInvalidRank        = errors.New("rank must have 1–32 base-62 digits and no trailing zero")
	ErrRankBounds         = errors.New("lower rank must precede upper rank")
	ErrRankSpaceExhausted = &Conflict{"rank_space_exhausted", "This list has no room between these two items. Move the item next to a different neighbour."}
	ErrSeedCount          = errors.New("rank seed count must be between 0 and 5000")
)

// ValidRank matches the database CHECK and C collation, without allocation.
func ValidRank(key string) bool {
	if len(key) == 0 || len(key) > MaxRankDigits || key[len(key)-1] == '0' {
		return false
	}
	for i := range len(key) {
		if digit(key[i]) < 0 {
			return false
		}
	}
	return true
}

func digit(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 36
	default:
		return -1
	}
}

// Between returns a canonical fraction strictly between its neighbours. An
// empty lower bound means zero; an empty upper bound means one. Work and
// allocation stop at the 32-digit limit, even for a crowded gap.
func Between(lower, upper string) (string, error) {
	if lower != "" && !ValidRank(lower) || upper != "" && !ValidRank(upper) {
		return "", ErrInvalidRank
	}
	if upper != "" && lower >= upper {
		return "", ErrRankBounds
	}
	var out [MaxRankDigits]byte
	li, ui := 0, 0
	upperOpen := upper == ""
	for n := range out {
		lo, hi := 0, 62
		if li < len(lower) {
			lo = digit(lower[li])
		}
		if !upperOpen {
			// A canonical upper key cannot end while its remaining value
			// is greater than the lower bound.
			hi = digit(upper[ui])
		}
		if hi-lo >= 2 {
			out[n] = rankDigits[(lo+hi)/2]
			return string(out[:n+1]), nil
		}
		out[n] = rankDigits[lo]
		li++
		ui++
		if hi > lo {
			// Adjacent digits: stay under the upper neighbour, then
			// choose a fraction above the remaining lower suffix.
			upperOpen = true
		}
	}
	return "", ErrRankSpaceExhausted
}

// After uses the append rule, preserving headroom in a newly seeded list.
// An empty list starts at V; a final z appends 1 rather than carrying.
func After(key string) (string, error) {
	if key == "" {
		return "V", nil
	}
	if !ValidRank(key) {
		return "", ErrInvalidRank
	}
	last := digit(key[len(key)-1])
	if last == 61 {
		if len(key) == MaxRankDigits {
			return "", ErrRankSpaceExhausted
		}
		return key + "1", nil
	}
	return key[:len(key)-1] + string(rankDigits[last+1]), nil
}

// SeedRanks returns ordered, evenly distributed short keys in the lower half
// of the space. The caller supplies items in their final order (final sequence
// order for adopted published releases). The last digit is always 1..U, leaving
// append headroom. No floating-point rounding or automatic rebalancing occurs.
func SeedRanks(count int) ([]string, error) {
	if count < 0 || count > MaxSeedItems {
		return nil, ErrSeedCount
	}
	keys := make([]string, count)
	if count == 0 {
		return keys, nil
	}
	width, prefixes := 1, 1
	for prefixes < count+1 {
		prefixes *= 62
		width++
	}
	// Each prefix in the lower half has 30 permitted final digits.
	slots := (prefixes / 2) * 30
	for i := range count {
		ordinal := (i + 1) * slots / (count + 1)
		prefix, last := ordinal/30, ordinal%30+1
		key := []byte(strings.Repeat("0", width))
		key[width-1] = rankDigits[last]
		for j := width - 2; j >= 0; j-- {
			key[j] = rankDigits[prefix%62]
			prefix /= 62
		}
		keys[i] = string(key)
	}
	return keys, nil
}
