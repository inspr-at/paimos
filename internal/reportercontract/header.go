// SPDX-License-Identifier: AGPL-3.0-only

// Package reportercontract declares reporter-facing response headers and pins.
// Each surface emits Aeon-Contract: name/major.minor. An added optional field
// or operation requires a minor bump; a removed field, changed type, or newly
// required field requires a major bump. Versions never enter JSON bodies.
// The coordinator can run `go run scripts/reporter-contract.go since <git-ref>`
// to announce changed reporter response schemas to Pharos and Janus.
package reportercontract

import "net/http"

const Header = "Aeon-Contract"

const (
	StageHandoffs   = "stage-handoffs/1.0"
	StageEvidence   = "stage-evidence/1.0"
	StageResult     = "stage-result/1.0"
	StageLaunch     = "stage-launch/1.0"
	Journey         = "journey/1.3"
	Me              = "me/1.0"
	BaselineBatches = "baseline-batches/1.0"
	Approvals       = "approvals/1.1"
	HarnessSession  = "harness-session/1.2"
)

// WithHeader adds the declared contract before a route writes any status or body.
func WithHeader(version string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(Header, version)
		next(w, r)
	}
}
