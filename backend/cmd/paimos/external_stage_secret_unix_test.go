//go:build darwin || linux

// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package main

import (
	"math"
	"testing"
)

func TestNonNegativeIntToUint64Bounds(t *testing.T) {
	for _, test := range []struct {
		name  string
		value int
		want  uint64
		ok    bool
	}{
		{name: "negative", value: -1},
		{name: "zero", value: 0, want: 0, ok: true},
		{name: "maximum", value: math.MaxInt, want: uint64(math.MaxInt), ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := nonNegativeIntToUint64(test.value)
			if got != test.want || ok != test.ok {
				t.Fatalf("nonNegativeIntToUint64(%d) = (%d, %t), want (%d, %t)", test.value, got, ok, test.want, test.ok)
			}
		})
	}
}
