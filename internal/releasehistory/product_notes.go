// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const ProductNotesSchema = "aeon.product-release-notes.v1"
const ProductNotesPath = "internal/releasehistory/data/product-notes.json"
const ProductNotesSource = "embedded-product-notes"

// ProductNotes is a reviewed public projection, not a copy of tenant records.
// Only release-note text and its capture provenance enter the binary.
type ProductNotes struct {
	Schema     string                 `json:"schema"`
	Product    string                 `json:"product"`
	Repository string                 `json:"repository"`
	Releases   map[string]PublicNotes `json:"releases"`
}

type PublicNotes struct {
	SHA256              string       `json:"snapshot_sha256"`
	CapturedAt          *time.Time   `json:"captured_at"`
	Revision            int64        `json:"release_revision"`
	WrittenAfterRelease bool         `json:"written_after_release,omitempty"`
	Items               []TicketNote `json:"items"`
}

func EmptyProductNotes() ProductNotes {
	return ProductNotes{Schema: ProductNotesSchema, Product: "PAIMOS AEON", Repository: "inspr-at/aeon", Releases: map[string]PublicNotes{}}
}

func ReadProductNotes(raw []byte) (ProductNotes, error) {
	var bundle ProductNotes
	if len(raw) > 16<<20 {
		return bundle, fmt.Errorf("product notes exceed 16 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&bundle); err != nil {
		return bundle, fmt.Errorf("invalid product notes JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return bundle, fmt.Errorf("product notes must be one JSON object")
	}
	if bundle.Schema != ProductNotesSchema || bundle.Product != "PAIMOS AEON" || bundle.Repository != "inspr-at/aeon" || bundle.Releases == nil {
		return bundle, fmt.Errorf("product notes identity is invalid")
	}
	for version, notes := range bundle.Releases {
		if !ValidVersion(version) || len(notes.SHA256) != 64 || strings.Trim(notes.SHA256, "0123456789abcdef") != "" || notes.CapturedAt == nil || notes.CapturedAt.IsZero() || notes.Revision < 1 || notes.Items == nil {
			return bundle, fmt.Errorf("product notes provenance is invalid for %s", version)
		}
		seen := map[string]bool{}
		for _, item := range notes.Items {
			if ticketKey.FindString(item.Key) != item.Key || !strings.HasPrefix(item.Key, "AEON-") || seen[item.Key] || (item.Group != "" && item.Group != GroupFeatures && item.Group != GroupFixes && item.Group != GroupOther) {
				return bundle, fmt.Errorf("product note ticket identity or group is invalid")
			}
			seen[item.Key] = true
		}
	}
	return bundle, nil
}

// PublicNotesFromSnapshot accepts stored snapshots only, except when a release
// agent explicitly freezes an unpublished preview during reservation. Binding
// both UUIDs prevents accidentally embedding another tenant/project's notes.
func PublicNotesFromSnapshot(raw []byte, version, tenantID, projectID string, reserve bool) (PublicNotes, error) {
	notes, err := NotesFromSnapshot(raw, version, ProductNotesSource)
	if err != nil {
		return PublicNotes{}, err
	}
	var snapshot NoteSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return PublicNotes{}, err
	}
	if tenantID == "" || projectID == "" || snapshot.TenantID != tenantID || snapshot.ProjectID != projectID {
		return PublicNotes{}, fmt.Errorf("snapshot does not match the explicitly selected tenant and project")
	}
	if !snapshot.Frozen && !reserve {
		return PublicNotes{}, fmt.Errorf("historical export requires a frozen snapshot, not live ticket fields")
	}
	if !reserve && snapshot.Version == "" {
		return PublicNotes{}, fmt.Errorf("historical export requires a recorded version")
	}
	return publicNotes(notes), nil
}

func publicNotes(notes *Notes) PublicNotes {
	out := PublicNotes{SHA256: notes.SHA256, CapturedAt: notes.CapturedAt, Revision: notes.Revision, WrittenAfterRelease: notes.WrittenAfterRelease, Items: []TicketNote{}}
	for _, item := range notes.Items {
		out.Items = append(out.Items, TicketNote{Key: item.Key, Group: item.Group, PillEN: item.PillEN, PillDE: item.PillDE, BenefitEN: item.BenefitEN, BenefitDE: item.BenefitDE})
	}
	return out
}

// AddHistory imports only the frozen notes of an authenticated /api/releases
// export (or a local tag build). Commit annotations and live linked text are
// deliberately ignored. The caller supplies the reviewed export offline.
func (bundle *ProductNotes) AddHistory(history History) error {
	if history.Schema != Schema || history.Product != bundle.Product || history.Repository != bundle.Repository {
		return fmt.Errorf("release history does not identify the selected product")
	}
	for _, rel := range history.Releases {
		if !HasSnapshot(rel) {
			continue
		}
		source := rel.Notes.Source
		if source != "database-snapshot" && source != "v"+rel.Version+":release-notes/"+rel.Version+".json" {
			continue
		}
		if err := bundle.Add(rel.Version, publicNotes(rel.Notes)); err != nil {
			return err
		}
	}
	return nil
}

// Add is insert-only. Re-running an identical export is harmless; changing a
// frozen version requires investigation, not silently replacing the old notes.
func (bundle *ProductNotes) Add(version string, notes PublicNotes) error {
	if old, ok := bundle.Releases[version]; ok {
		a, _ := json.Marshal(old)
		b, _ := json.Marshal(notes)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("product notes for %s already exist and differ", version)
		}
		return nil
	}
	candidate := EmptyProductNotes()
	candidate.Releases[version] = notes
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	if _, err := ReadProductNotes(raw); err != nil {
		return err
	}
	bundle.Releases[version] = notes
	return nil
}

func withProductNotes(h History, bundle ProductNotes) History {
	if h.Product != bundle.Product || h.Repository != bundle.Repository {
		return h
	}
	h.Releases = append([]Release{}, h.Releases...)
	for i, rel := range h.Releases {
		if HasSnapshot(rel) {
			continue
		}
		public, ok := bundle.Releases[rel.Version]
		if !ok {
			continue
		}
		notes := &Notes{Source: ProductNotesSource, SHA256: public.SHA256, CapturedAt: public.CapturedAt, Revision: public.Revision, WrittenAfterRelease: public.WrittenAfterRelease, Items: []NoteItem{}, Gaps: []string{}}
		notes.PublicItems = append([]TicketNote{}, public.Items...)
		h.Releases[i].Notes = notes
	}
	return h
}

// withFrozenGroups makes the selected snapshot the only ticket metadata source.
// A missing capture leaves commit-derived groups, never today's ticket fields.
func withFrozenGroups(h History) History {
	out := h
	out.Releases = make([]Release, len(h.Releases))
	for i, rel := range h.Releases {
		meta := map[string]TicketMeta{}
		if HasSnapshot(rel) {
			items := publicNotes(rel.Notes).Items
			if rel.Notes.PublicItems != nil {
				items = rel.Notes.PublicItems
			}
			for _, item := range items {
				if item.Group == GroupOther {
					continue
				}
				note := TicketNote{Key: item.Key, Group: item.Group, PillEN: item.PillEN, PillDE: item.PillDE, BenefitEN: item.BenefitEN, BenefitDE: item.BenefitDE}
				if item.Group != "" {
					meta[item.Key] = TicketMeta{Bug: item.Group == GroupFixes, PublicBenefit: item.Group == GroupFeatures, Note: &note}
				}
			}
		}
		// Clear annotations from earlier readers before deriving from the capture.
		rel.Changes = append([]Change{}, rel.Changes...)
		for j := range rel.Changes {
			rel.Changes[j].Group = ""
			rel.Changes[j].Linked = nil
		}
		out.Releases[i] = withGroups(History{Releases: []Release{rel}}, meta).Releases[0]
	}
	return out
}
