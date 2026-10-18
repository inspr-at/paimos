// SPDX-License-Identifier: AGPL-3.0-only

// Package modelreport defines content-free evidence shared by clients and servers.
package modelreport

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// Observation contains content-free, retryable harness evidence.
type Observation struct {
	ReportID string `json:"report_id"`
	Harness  string `json:"harness"`
	Model    string `json:"model"`
	Effort   string `json:"effort"`
	Status   string `json:"status"`
}

// EvidenceID derives a UUID-shaped id from public evidence, never from secrets.
func EvidenceID(evidence string) string {
	sum := sha256.Sum256([]byte(evidence))
	sum[6] = (sum[6] & 15) | 0x50
	sum[8] = (sum[8] & 63) | 0x80
	h := hex.EncodeToString(sum[:16])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

var modelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
var effortRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ValidTuple bounds automatic hints without rejecting unrelated session metadata.
func ValidTuple(model, effort string) bool {
	return len(model) <= 128 && modelRE.MatchString(model) && len(effort) <= 32 && effortRE.MatchString(effort)
}
