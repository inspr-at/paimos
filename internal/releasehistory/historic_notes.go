// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const HistoricNotesSchema = "aeon.historic-ticket-export.v1"

// HistoricTicketExport is local input, never embedded. Membership comes from
// the same manifest as the history view, not today's journey membership.
type HistoricTicketExport struct {
	Schema     string           `json:"schema"`
	TenantID   string           `json:"tenant_id"`
	ProjectID  string           `json:"project_node_id"`
	CapturedAt time.Time        `json:"captured_at"`
	Tickets    []HistoricTicket `json:"tickets"`
}

type HistoricTicket struct {
	Key    string          `json:"key"`
	Kind   string          `json:"kind"`
	Fields json.RawMessage `json:"fields"`
}

type HistoricReport struct {
	Releases   int `json:"releases_backfilled"`
	Classified int `json:"ticket_occurrences_classified"`
	Other      int `json:"ticket_occurrences_other"`
	Hidden     int `json:"ticket_occurrences_hidden"`
}

func historicReleaseKeys(rel Release) []string {
	keys := append([]string{}, rel.Tickets...)
	for _, change := range rel.Changes {
		keys = append(keys, change.Tickets...)
	}
	out := []string{}
	for _, key := range uniqueTicketKeys(keys) {
		if strings.HasPrefix(key, "AEON-") {
			out = append(out, key)
		}
	}
	return out
}

// HistoricTicketKeys includes published releases without an existing capture.
// Reserved versions and other products never enter this explicit backfill.
func HistoricTicketKeys(h History) []string {
	var keys []string
	for _, rel := range h.Releases {
		if rel.State == StatePublished && !HasSnapshot(rel) {
			keys = append(keys, historicReleaseKeys(rel)...)
		}
	}
	return uniqueTicketKeys(keys)
}

// AddHistoric freezes a reviewed export of current public fields for historical
// Git membership. Unlike AddHistory, this explicitly records a later capture.
// Classification is exactly the live PPM parser and linked-note projection.
func (bundle *ProductNotes) AddHistoric(h History, raw []byte, tenantID, projectID string) (HistoricReport, error) {
	var report HistoricReport
	if h.Schema != Schema || !sameProductNotes(h, *bundle) {
		return report, fmt.Errorf("historic history must identify PAIMOS AEON")
	}
	var export HistoricTicketExport
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16<<20 || d.Decode(&export) != nil || d.Decode(new(any)) != io.EOF || export.Schema != HistoricNotesSchema || export.CapturedAt.IsZero() || export.Tickets == nil {
		return report, fmt.Errorf("invalid historic ticket export")
	}
	if err := HistoryBindingError(export.TenantID, export.ProjectID, tenantID, projectID); err != nil {
		return report, err
	}
	tickets := map[string]HistoricTicket{}
	meta := map[string]TicketMeta{}
	hidden := map[string]bool{}
	for _, ticket := range export.Tickets {
		if ticketKey.FindString(ticket.Key) != ticket.Key || !strings.HasPrefix(ticket.Key, "AEON-") || ticket.Kind == "" {
			return report, fmt.Errorf("invalid historic ticket identity")
		}
		if _, exists := tickets[ticket.Key]; exists {
			return report, fmt.Errorf("duplicate historic ticket %s", ticket.Key)
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(ticket.Fields, &fields) != nil || fields == nil {
			return report, fmt.Errorf("invalid historic ticket fields")
		}
		if flag, present := fields["hide_from_release_notes"]; present {
			var value bool
			if bytes.Equal(bytes.TrimSpace(flag), []byte("null")) || json.Unmarshal(flag, &value) != nil {
				return report, fmt.Errorf("invalid historic hide flag")
			}
			hidden[ticket.Key] = value
		}
		tickets[ticket.Key] = ticket
		meta[ticket.Key] = ParseTicketMeta(ticket.Kind, ticket.Fields)
	}
	// Stage the whole import so a late missing key/conflict cannot partly apply it.
	candidate := *bundle
	candidate.Releases = make(map[string]PublicNotes, len(bundle.Releases))
	for version, notes := range bundle.Releases {
		candidate.Releases[version] = notes
	}
	for _, rel := range h.Releases {
		if rel.State != StatePublished || HasSnapshot(rel) {
			continue
		}
		keys := historicReleaseKeys(rel)
		selected := []HistoricTicket{}
		for _, key := range keys {
			ticket, ok := tickets[key]
			if !ok {
				return HistoricReport{}, fmt.Errorf("historic export is missing %s", key)
			}
			selected = append(selected, ticket)
			if hidden[key] {
				report.Hidden++
			} else if meta[key].Note == nil {
				report.Other++
			} else {
				report.Classified++
			}
		}
		// Hash the actual version binding and observations; no tenant identifiers,
		// private fields or hidden notes enter the resulting public bundle.
		capture, _ := json.Marshal(struct {
			Version          string               `json:"version"`
			MembershipSource string               `json:"membership_source"`
			Export           HistoricTicketExport `json:"export"`
		}{rel.Version, ManifestMembershipSource, HistoricTicketExport{export.Schema, export.TenantID, export.ProjectID, export.CapturedAt, selected}})
		sum := sha256.Sum256(capture)
		items := linkedNotes(keys, meta)
		if items == nil {
			items = []TicketNote{}
		}
		notes := PublicNotes{SHA256: hex.EncodeToString(sum[:]), CapturedAt: &export.CapturedAt, Revision: 1, WrittenAfterRelease: true, Items: items}
		if err := candidate.Add(rel.Version, notes); err != nil {
			return HistoricReport{}, err
		}
		report.Releases++
	}
	*bundle = candidate
	return report, nil
}
