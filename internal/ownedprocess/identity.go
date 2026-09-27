// SPDX-License-Identifier: AGPL-3.0-only

package ownedprocess

import "time"

// Identity is public, content-free ownership evidence, never a worker lease.
// ProcessID is random for each launch; Generation is random per daemon start.
// The affected scope is this owned process group, including children that join
// it, not arbitrary descendants that escape into another process group.
type Identity struct {
	DaemonID   string    `json:"daemon_id"`
	Generation string    `json:"generation"`
	ProcessID  string    `json:"process_id"`
	RootPID    int       `json:"root_pid"`
	GroupID    int       `json:"group_id"`
	StartedAt  time.Time `json:"started_at"`
}
