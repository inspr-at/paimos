// SPDX-License-Identifier: AGPL-3.0-only
package rules

import "time"

// RulesDeliveryManifest is pre-launch issuance, never evidence of delivery.
type RulesDeliveryManifest struct {
	ID               string        `json:"id"`
	RunID            string        `json:"run_id"`
	SessionID        string        `json:"session_id"`
	Generation       string        `json:"daemon_generation"`
	ComputerID       string        `json:"computer_id"`
	BodySHA256       string        `json:"body_sha256"`
	ByteSize         int           `json:"byte_size"`
	Harness          string        `json:"harness"`
	RepositoryCommit string        `json:"repository_commit,omitempty"`
	Pins             []DeliveryPin `json:"pins"`
	IssuedAt         time.Time     `json:"issued_at"`
	ExpiresAt        time.Time     `json:"expires_at"`
}

type DeliveryPin struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	BlobSHA    string `json:"blob_sha"`
}

type RulesDeliveryPrepare struct {
	RunID      string          `json:"run_id"`
	Generation string          `json:"daemon_generation"`
	BodySHA256 string          `json:"body_sha256"`
	Repository RepositoryRules `json:"repository"`
}

// RulesDeliveryAck is issued by agentd only after a matching bridge start ack.
// It proves transmitted bytes, not model obedience, execution or authority.
type RulesDeliveryAck struct {
	ManifestID     string    `json:"manifest_id"`
	RunID          string    `json:"run_id"`
	Generation     string    `json:"daemon_generation"`
	BodySHA256     string    `json:"body_sha256"`
	BridgeSHA256   string    `json:"bridge_sha256"`
	AcknowledgedAt time.Time `json:"acknowledged_at"`
}

type RulesDeliveryEvidence struct {
	Manifest       RulesDeliveryManifest `json:"manifest"`
	Evidence       string                `json:"evidence"`
	AcknowledgedAt time.Time             `json:"acknowledged_at"`
	RecordedAt     time.Time             `json:"recorded_at"`
}

const RulesDeliveryAcknowledged = "agentd_transmitted_bytes_acknowledged"
