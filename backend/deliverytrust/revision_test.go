// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package deliverytrust

import (
	"bytes"
	"math"
	"testing"
)

func TestRevisionWriterPreservesSignedIntegerBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name     string
		signed   int64
		unsigned uint64
	}{
		{name: "negative one", signed: -1, unsigned: math.MaxUint64},
		{name: "minimum", signed: math.MinInt64, unsigned: uint64(1) << 63},
		{name: "zero", signed: 0, unsigned: 0},
		{name: "maximum", signed: math.MaxInt64, unsigned: math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signed := newRevisionWriter()
			signed.int64(tc.signed)
			unsigned := newRevisionWriter()
			unsigned.uint64(tc.unsigned)
			if !bytes.Equal(signed.Sum(nil), unsigned.Sum(nil)) {
				t.Fatalf("signed %d did not retain its two's-complement bytes", tc.signed)
			}
		})
	}
}
