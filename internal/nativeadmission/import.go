// SPDX-License-Identifier: AGPL-3.0-only

package nativeadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/inspr-at/paimos/internal/agentsetup"
	"github.com/inspr-at/paimos/internal/nativecontract"
)

// HeldGroups supplies this package's retained occupancy to the existing owner
// import, alongside its worker/lead journal. Call under the owner's dispatch
// mutex. It provides no authority to adopt processes or release another group.
// A ledger rebuild with unresolved old-generation writers remains blocked;
// old salted identity fingerprints cannot be relabeled as fresh login proof.
func (g *Gate) HeldGroups(ctx context.Context) ([]agentsetup.LedgerGroup, error) {
	groups := []agentsetup.LedgerGroup{}
	err := g.locked(ctx, func(context.Context) error {
		names, err := g.opts.State.Names(maxRecords)
		if err != nil {
			return err
		}
		if len(names) > maxRecords {
			return denial(nativecontract.ReasonBufferFull)
		}
		for _, name := range names {
			var group agentsetup.LedgerGroup
			switch {
			case strings.HasPrefix(name, "session-") && strings.HasSuffix(name, ".json"):
				r, err := g.read(strings.TrimSuffix(strings.TrimPrefix(name, "session-"), ".json"))
				if err != nil {
					return err
				}
				if r.State == "released" {
					continue
				}
				state := "pending"
				if r.Attempted {
					state = "launching"
				}
				if r.PID > 0 && r.IdentityDigest != "" {
					state = "running"
				}
				group = agentsetup.LedgerGroup{ID: r.Group, Instance: r.MemberID, Generation: r.Generation, State: state, Holds: []agentsetup.LedgerLogin{r.Login}, PID: r.PID, StartedAt: r.StartedAt}
			case strings.HasPrefix(name, "profile-change-") && strings.HasSuffix(name, ".json"):
				raw, err := g.opts.State.Read(name, 4096)
				if err != nil {
					return err
				}
				var r profileChangeRecord
				d := json.NewDecoder(bytes.NewReader(raw))
				d.DisallowUnknownFields()
				if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF || r.Version != 1 || name != profileChangeName(r.AccountID) {
					return denial(nativecontract.ReasonOwnershipUnconfirmed)
				}
				if r.Released {
					continue
				}
				group = agentsetup.LedgerGroup{ID: r.Group, Instance: r.MemberID, Generation: r.Generation, State: "pending", Holds: []agentsetup.LedgerLogin{r.Login}}
			default:
				continue
			}
			if group.Instance != g.opts.MemberID || group.Generation != g.opts.Generation {
				return agentsetup.ErrLedgerGeneration
			}
			if !hexID(group.ID, 32) || len(group.Holds[0].Harness) < 1 || len(group.Holds[0].Harness) > 32 ||
				!(group.Holds[0].Identity == "unknown" || hexID(group.Holds[0].Identity, 64)) {
				return denial(nativecontract.ReasonOwnershipUnconfirmed)
			}
			groups = append(groups, group)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return groups, nil
}
