// SPDX-License-Identifier: AGPL-3.0-only

package harness

// LiveQuery is the live read's statement, for plan checks.
const LiveQuery = liveQuery

// SetMaxLive lowers one answer's bound for a test and returns the restore.
func SetMaxLive(n int) (restore func()) {
	old := maxLive
	maxLive = n
	return func() { maxLive = old }
}

// SetRemoveStaleBatch lowers the batch removal bound for a test and returns the restore.
func SetRemoveStaleBatch(n int) (restore func()) {
	old := removeStaleBatch
	removeStaleBatch = n
	return func() { removeStaleBatch = old }
}
