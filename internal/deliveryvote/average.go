// SPDX-License-Identifier: AGPL-3.0-only

package deliveryvote

import "fmt"

// FormatAverage renders the mean of integer scores 1–5 as a two-digit decimal
// string. Half rounds away from zero. A non-positive count has no average.
func FormatAverage(sum, count int) *string {
	if count <= 0 || sum <= 0 {
		return nil
	}
	hundredths := (sum*100 + count/2) / count
	if hundredths < 100 || hundredths > 500 {
		return nil
	}
	out := fmt.Sprintf("%d.%02d", hundredths/100, hundredths%100)
	return &out
}
