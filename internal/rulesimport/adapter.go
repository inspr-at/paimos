// SPDX-License-Identifier: AGPL-3.0-only

package rulesimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// expectedDraft is the mapping this importer will apply once AR1 freezes a DTO.
// These are obligations, not invented HTTP paths.
var expectedDraft = []string{
	"draft write for exactly one tenant and one trust context",
	"rule fields: layer, set, identity, text, why, details, strength, roles, harnesses, enabled, expires, source lineage",
	"stable identity replay without a second copy of identical content",
	"contradictions preserved for the operator; no automatic precedence",
	"no publish call and no cross-tenant copy",
}

func adapterFromPath(path string) AdapterGap {
	gap := AdapterGap{
		Ready:    false,
		Reason:   "AR1 draft DTO is not registered. No endpoint contract was invented; preview stays local.",
		Expected: append([]string(nil), expectedDraft...),
	}
	if path == "" {
		return gap
	}
	body, sum, err := readDoctrine(path)
	if err != nil {
		gap.Reason = fmt.Sprintf("AR1 draft path could not be read (%v). No endpoint contract was invented.", errKind(err))
		return gap
	}
	digest := sha256.Sum256([]byte(body))
	if hex.EncodeToString(digest[:]) != sum {
		gap.Reason = "AR1 draft hash mismatch. No endpoint contract was invented."
		return gap
	}
	gap.DraftSHA = sum
	gap.DraftBytes = len(body)
	gap.Reason = "An explicit AR1 draft file was read and left uninterpreted. This importer does not invent endpoint fields from it."
	return gap
}

func errKind(err error) string {
	switch {
	case err == nil:
		return ""
	default:
		return err.Error()
	}
}
