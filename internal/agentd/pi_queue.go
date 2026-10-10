// SPDX-License-Identifier: AGPL-3.0-only

package agentd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"unicode/utf8"

	"github.com/inspr-at/paimos/internal/localjournal"
)

// piHeldQueue is private vendor state. It is never projected to AEON or
// silently resumed after a daemon generation changes.
type piHeldQueue struct {
	TenantID    string   `json:"tenant_id"`
	PrincipalID string   `json:"principal_id"`
	RunID       string   `json:"run_id"`
	Generation  string   `json:"generation"`
	Steering    []string `json:"steering"`
	FollowUp    []string `json:"follow_up"`
	Ambiguous   bool     `json:"ambiguous"`
}

func openPiQueue(r StartRequest) (*localjournal.Journal[piHeldQueue], error) {
	if !filepath.IsAbs(r.StateRoot) || r.TenantID == "" || r.PrincipalID == "" || r.Run.ID == "" || r.Generation == "" {
		return nil, errors.New("Pi queue binding unavailable")
	}
	id := sha256.Sum256([]byte(r.TenantID + "\x00" + r.PrincipalID + "\x00" + r.Run.ID))
	return localjournal.Open(localjournal.Config[piHeldQueue]{Directory: r.StateRoot, Prefix: "aeon-pi-" + hex.EncodeToString(id[:12]), Version: 2, MaxBytes: 2 << 20, MaxRecords: 1,
		Key: func(q piHeldQueue) (string, error) { return q.RunID, nil },
		Validate: func(q piHeldQueue) error {
			if q.TenantID != r.TenantID || q.PrincipalID != r.PrincipalID || q.RunID != r.Run.ID || q.Generation != r.Generation || !validPiQueue(q) {
				return errors.New("Pi held queue binding mismatch")
			}
			return nil
		},
	})
}

func validPiQueue(q piHeldQueue) bool {
	if len(q.Steering)+len(q.FollowUp) > 256 {
		return false
	}
	bytes := 0
	for _, part := range append(append([]string{}, q.Steering...), q.FollowUp...) {
		// Held parts include inbox input, which may use the full inbox bound.
		if part == "" || len(part) > inboxTextLimit || !utf8.ValidString(part) {
			return false
		}
		bytes += len(part)
	}
	return bytes <= 1<<20
}
