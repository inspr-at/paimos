// SPDX-License-Identifier: AGPL-3.0-only

package deliveryvote

import "testing"

func TestFormatReworkRate(t *testing.T) {
	if FormatReworkRate(0, 4) != nil || FormatReworkRate(1, 0) != nil || FormatReworkRate(3, 2) != nil {
		t.Fatal("no rate")
	}
	got := FormatReworkRate(1, 4)
	if got == nil || *got != "1/4" {
		t.Fatalf("quarter: %v", got)
	}
}

func TestFormatAverage(t *testing.T) {
	if FormatAverage(0, 0) != nil || FormatAverage(1, 0) != nil {
		t.Fatal("no votes")
	}
	got := FormatAverage(8, 2)
	if got == nil || *got != "4.00" {
		t.Fatalf("4 and 4: %v", got)
	}
	got = FormatAverage(9, 2)
	if got == nil || *got != "4.50" {
		t.Fatalf("5 and 4: %v", got)
	}
	got = FormatAverage(4, 3)
	if got == nil || *got != "1.33" {
		t.Fatalf("1, 1 and 2: %v", got)
	}
	got = FormatAverage(5, 1)
	if got == nil || *got != "5.00" {
		t.Fatalf("five: %v", got)
	}
}
