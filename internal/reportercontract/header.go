// SPDX-License-Identifier: AGPL-3.0-only

// Package reportercontract declares reporter-facing response headers and pins.
// Each surface emits Aeon-Contract: name/major.minor. A new response field
// (optional or required) or operation requires a minor bump when existing readers
// tolerate extra fields. Removed fields, changed types and newly requiring an
// existing optional field require a major bump, as do new required request
// fields. Versions never enter JSON bodies.
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
	Me              = "me/1.3"
	BaselineBatches = "baseline-batches/1.0"
	Approvals       = "approvals/1.1"
	HarnessSession  = "harness-session/2.8"
)

// WithHeader adds the declared contract before a route writes any status or body.
func WithHeader(version string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(Header, version)
		next(w, r)
	}
}
