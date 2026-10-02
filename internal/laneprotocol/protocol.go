// SPDX-License-Identifier: AGPL-3.0-only

// Package laneprotocol holds the content-free daemon/server lane contract.
package laneprotocol

import "time"

const Capability = "lane-execution-v1"
const StopAllowanceMS int64 = 2000

type Grant struct {
	EnvelopeID      string    `json:"envelope_id"`
	RunID           string    `json:"run_id"`
	ProjectID       string    `json:"project_id"`
	WorkspaceID     string    `json:"workspace_id"`
	DaemonID        string    `json:"daemon_id"`
	Generation      string    `json:"daemon_generation"`
	RemainingMS     int64     `json:"remaining_ms"`
	StopAllowanceMS int64     `json:"stop_allowance_ms"`
	ExpiresAt       time.Time `json:"expires_at"`
}
type Settlement struct {
	ElapsedMS     int64 `json:"elapsed_ms"`
	ExitConfirmed bool  `json:"exit_confirmed"`
}
