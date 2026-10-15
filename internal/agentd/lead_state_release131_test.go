// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"encoding/json"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Frozen release-131 decoder and startup fence, copied verbatim apart from
// symbol names from internal/agentd/lead_runtime.go at v261012211543.0.0
// (955dad613f7878aa685cb1694b38b6b8b82671cb). Do not update with the writer.
type release131LeadRecord struct {
	Version      int                    `json:"version"`
	Pickup       agentsetup.LeadPickup  `json:"pickup"`
	Launch       *agentsetup.LeadLaunch `json:"launch,omitempty"`
	Workspace    RoutineWorkspace       `json:"workspace"`
	Group        string                 `json:"group"`
	Attempted    bool                   `json:"attempted"`
	Ownership    *ownedprocess.Identity `json:"ownership,omitempty"`
	ExitObserved bool                   `json:"exit_observed"`
	ExitReported bool                   `json:"exit_reported"`
	Abandoned    bool                   `json:"abandoned,omitempty"`
}

func release131CheckRetainedLeads(store *agentsetup.Store) error {
	names, err := store.Names(4096)
	if err != nil {
		return err
	}
	for _, name := range names {
		if len(name) < len("lead-runtime-.json") || name[:13] != "lead-runtime-" {
			continue
		}
		raw, err := store.Read(name, 32768)
		if err != nil {
			return ErrProcessesUnconfirmed
		}
		var r release131LeadRecord
		if json.Unmarshal(raw, &r) != nil || r.Version != 1 || r.Attempted && (!r.ExitReported || !r.ExitObserved || r.Ownership == nil) || !r.Attempted && (r.Ownership != nil || r.ExitObserved || r.ExitReported) {
			return ErrProcessesUnconfirmed
		}
	}
	return nil
}
