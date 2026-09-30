// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/inspr-at/paimos/internal/ticketbenefits"
)

// NoteReservation binds an explicitly reviewed scope to version.json before a
// release PR. It does not claim journey membership or a published Git tag.
type NoteReservation struct {
	Version  string   `json:"version"`
	Channel  string   `json:"release_channel"`
	Sequence int      `json:"release_sequence"`
	Tickets  []string `json:"tickets"`
}

// ReadNoteReservation requires an explicit list, including [] for an internal
// release. Git mentions cannot infer the scope of an unpublished release.
func ReadNoteReservation(repo, version string, keys []string) (NoteReservation, error) {
	raw, err := os.ReadFile(filepath.Join(repo, "version.json"))
	if err != nil {
		return NoteReservation{}, err
	}
	var head versionFile
	if json.Unmarshal(raw, &head) != nil || head.Product != "PAIMOS AEON" || head.Version != version || !ValidVersion(version) || head.ReleaseChannel == "" || head.ReleaseSequence < 1 || head.VersionScheme != SchemeOf(version) || keys == nil {
		return NoteReservation{}, fmt.Errorf("reserve must match version.json's product, version, scheme, channel and sequence; require an explicit ticket list")
	}
	for _, key := range keys {
		if ticketKey.FindString(key) != key || !strings.HasPrefix(key, "AEON-") {
			return NoteReservation{}, fmt.Errorf("invalid reservation ticket key")
		}
	}
	if len(uniqueTicketKeys(keys)) != len(keys) {
		return NoteReservation{}, fmt.Errorf("duplicate reservation ticket key")
	}
	keys = append([]string{}, keys...)
	slices.Sort(keys)
	return NoteReservation{Version: version, Channel: head.ReleaseChannel, Sequence: head.ReleaseSequence, Tickets: keys}, nil
}

// AddReserved freezes only the selected release, from its own reviewed scope.
// New public notes require all four bilingual fields; hidden members add no text.
func (bundle *ProductNotes) AddReserved(raw []byte, reservation NoteReservation, tenantID, projectID string) error {
	export, tickets, meta, hidden, err := readTicketExport(raw, tenantID, projectID)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(export.Reservation)
	b, _ := json.Marshal(reservation)
	if export.Reservation == nil || !bytes.Equal(a, b) || !ValidVersion(reservation.Version) || reservation.Channel == "" || reservation.Sequence < 1 || reservation.Tickets == nil || len(tickets) != len(reservation.Tickets) {
		return fmt.Errorf("export does not match the selected reservation and ticket scope")
	}
	for _, key := range reservation.Tickets {
		ticket, ok := tickets[key]
		if !ok {
			return fmt.Errorf("reservation export is missing %s", key)
		}
		if !hidden[key] {
			if issues := ticketbenefits.Issues(ticket.Fields); len(issues) > 0 {
				return fmt.Errorf("reservation ticket %s: %s", key, strings.Join(issues, "; "))
			}
		}
	}
	items := linkedNotes(reservation.Tickets, meta)
	if items == nil {
		items = []TicketNote{}
	}
	// The raw export includes the exact reservation binding and observations.
	// Only its digest and public projection enter the distributable bundle.
	sum := sha256.Sum256(raw)
	return bundle.Add(reservation.Version, PublicNotes{ReleaseChannel: reservation.Channel, ReleaseSequence: reservation.Sequence, SHA256: hex.EncodeToString(sum[:]), CapturedAt: &export.CapturedAt, Revision: 1, Items: items})
}
