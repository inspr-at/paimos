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

// productRepository reports this product's GitHub names. The release workflow
// passes GITHUB_REPOSITORY, which is inspr-at/paimos; inspr-at/aeon is the
// same repository.
func productRepository(name string) bool {
	return name == "inspr-at/aeon" || name == "inspr-at/paimos"
}

func aeonHistory(h History) bool {
	return h.Product == "PAIMOS AEON" || productRepository(h.Repository)
}

func sameProductNotes(h History, bundle ProductNotes) bool {
	return h.Product == bundle.Product && h.Product == "PAIMOS AEON" && productRepository(h.Repository) && productRepository(bundle.Repository)
}

// HistoryBindingError rejects a saved release export whose tenant or project
// is missing or different from the explicit selection.
func HistoryBindingError(gotTenant, gotProject, wantTenant, wantProject string) error {
	if !noteUUID.MatchString(wantTenant) || !noteUUID.MatchString(wantProject) || gotTenant != wantTenant || gotProject != wantProject {
		return fmt.Errorf("history does not match the explicitly selected tenant and project")
	}
	return nil
}

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
	if bundle.Schema != ProductNotesSchema || bundle.Product != "PAIMOS AEON" || !productRepository(bundle.Repository) || bundle.Releases == nil {
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
	if history.Schema != Schema || !sameProductNotes(history, *bundle) {
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
	if !sameProductNotes(h, bundle) {
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

// withFrozenGroups makes the selected snapshot the only ticket text source.
// A missing capture leaves commit-derived groups. A capture without a group
// takes only the group from live: a bug is fixes, a visible benefit is features.
func withFrozenGroups(h History, live map[string]TicketMeta) History {
	out := h
	out.Releases = make([]Release, len(h.Releases))
	for i, rel := range h.Releases {
		meta := map[string]TicketMeta{}
		if HasSnapshot(rel) && rel.Notes != nil {
			rel.Notes = applyClassification(rel.Notes, live)
		}
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

// groupFromClassification is the AEON-289 group. Live pill and benefit text is ignored.
func groupFromClassification(meta TicketMeta) string {
	if meta.Bug {
		return GroupFixes
	}
	if meta.PublicBenefit {
		return GroupFeatures
	}
	return GroupOther
}

// applyClassification records a derived group on a copy of notes. A group the
// capture already has is left as captured. Other and unknown tickets stay unset.
func applyClassification(notes *Notes, live map[string]TicketMeta) *Notes {
	if notes == nil || len(live) == 0 {
		return notes
	}
	derived := func(group, key string) string {
		if group != "" {
			return ""
		}
		meta, ok := live[key]
		if !ok {
			return ""
		}
		g := groupFromClassification(meta)
		if g == GroupFixes || g == GroupFeatures {
			return g
		}
		return ""
	}
	change := false
	if notes.PublicItems != nil {
		for _, item := range notes.PublicItems {
			if derived(item.Group, item.Key) != "" {
				change = true
				break
			}
		}
	} else {
		for _, item := range notes.Items {
			if derived(item.Group, item.Key) != "" {
				change = true
				break
			}
		}
	}
	if !change {
		return notes
	}
	notes = cloneNotes(notes)
	if notes.PublicItems != nil {
		for i := range notes.PublicItems {
			if g := derived(notes.PublicItems[i].Group, notes.PublicItems[i].Key); g != "" {
				notes.PublicItems[i].Group = g
			}
		}
		return notes
	}
	for i := range notes.Items {
		if g := derived(notes.Items[i].Group, notes.Items[i].Key); g != "" {
			notes.Items[i].Group = g
		}
	}
	return notes
}

func cloneNotes(n *Notes) *Notes {
	c := *n
	if n.Items != nil {
		c.Items = append([]NoteItem(nil), n.Items...)
	}
	if n.PublicItems != nil {
		c.PublicItems = append([]TicketNote(nil), n.PublicItems...)
	}
	if n.Gaps != nil {
		c.Gaps = append([]string(nil), n.Gaps...)
	}
	return &c
}

func unclassifiedTicketKeys(h History) []string {
	var keys []string
	for _, rel := range h.Releases {
		if !HasSnapshot(rel) || rel.Notes == nil {
			continue
		}
		if rel.Notes.PublicItems != nil {
			for _, item := range rel.Notes.PublicItems {
				if item.Group == "" {
					keys = append(keys, item.Key)
				}
			}
			continue
		}
		for _, item := range rel.Notes.Items {
			if item.Group == "" {
				keys = append(keys, item.Key)
			}
		}
	}
	return uniqueTicketKeys(keys)
}
