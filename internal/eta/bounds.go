// SPDX-License-Identifier: AGPL-3.0-only

package eta

import "time"

// MaxPast and MaxFuture bound a ready or live instant. An overdue instant is
// accepted back to MaxPast. ClockSkew absorbs the gap between a client capping
// a relative duration and the server reading its clock.
const (
	MaxPast   = 30 * 24 * time.Hour
	MaxFuture = 365 * 24 * time.Hour
	ClockSkew = time.Minute
)

// Allowed reports whether at lies in [now-MaxPast, now+MaxFuture], plus ClockSkew.
func Allowed(at, now time.Time) bool {
	return !at.Before(now.Add(-MaxPast-ClockSkew)) && !at.After(now.Add(MaxFuture+ClockSkew))
}
