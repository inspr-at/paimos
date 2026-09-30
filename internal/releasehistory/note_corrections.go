// SPDX-License-Identifier: AGPL-3.0-only
package releasehistory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const NoteCorrectionsPath = "internal/releasehistory/data/product-note-corrections.json"

// NoteCorrection is an additive review record. It cannot create a public item,
// so a hidden member can never become visible through the correction layer.
type NoteCorrection struct {
	Version   string  `json:"version"`
	Key       string  `json:"key"`
	SHA256    string  `json:"snapshot_sha256"`
	Reason    string  `json:"reason"`
	Group     string  `json:"group,omitempty"`
	PillEN    *string `json:"pill_en,omitempty"`
	PillDE    *string `json:"pill_de,omitempty"`
	BenefitEN *string `json:"benefit_en,omitempty"`
	BenefitDE *string `json:"benefit_de,omitempty"`
}

func withNoteCorrections(h History, bundle ProductNotes, raw []byte) (History, error) {
	var layer struct {
		Schema      string           `json:"schema"`
		Corrections []NoteCorrection `json:"corrections"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 16<<20 || d.Decode(&layer) != nil || d.Decode(new(any)) != io.EOF || layer.Schema != "aeon.product-note-corrections.v1" || layer.Corrections == nil {
		return History{}, fmt.Errorf("invalid product note corrections")
	}
	byVersion := map[string][]NoteCorrection{}
	seen := map[string]bool{}
	for _, c := range layer.Corrections {
		notes, ok := bundle.Releases[c.Version]
		id := c.Version + ":" + c.Key
		found := false
		for _, item := range notes.Items {
			if item.Key == c.Key {
				found = true
			}
		}
		if !ok || !found || c.SHA256 != notes.SHA256 || strings.TrimSpace(c.Reason) == "" || seen[id] || (c.Group != "" && c.Group != GroupFeatures && c.Group != GroupFixes && c.Group != GroupOther) || (c.Group == "" && c.PillEN == nil && c.PillDE == nil && c.BenefitEN == nil && c.BenefitDE == nil) {
			return History{}, fmt.Errorf("invalid correction binding for %s", id)
		}
		for _, text := range []*string{c.PillEN, c.PillDE, c.BenefitEN, c.BenefitDE} {
			if text != nil && strings.TrimSpace(*text) == "" {
				return History{}, fmt.Errorf("correction text must be nonblank")
			}
		}
		seen[id] = true
		byVersion[c.Version] = append(byVersion[c.Version], c)
	}
	if !sameProductNotes(h, bundle) {
		return h, nil
	}
	h.Releases = append([]Release{}, h.Releases...)
	for i, rel := range h.Releases {
		if rel.Notes == nil || rel.Notes.Source != ProductNotesSource || len(byVersion[rel.Version]) == 0 {
			continue
		}
		notes := cloneNotes(rel.Notes)
		for _, c := range byVersion[rel.Version] {
			for j := range notes.PublicItems {
				item := &notes.PublicItems[j]
				if item.Key != c.Key {
					continue
				}
				if c.Group != "" {
					item.Group = c.Group
				}
				if c.PillEN != nil {
					item.PillEN = *c.PillEN
				}
				if c.PillDE != nil {
					item.PillDE = *c.PillDE
				}
				if c.BenefitEN != nil {
					item.BenefitEN = *c.BenefitEN
				}
				if c.BenefitDE != nil {
					item.BenefitDE = *c.BenefitDE
				}
			}
		}
		notes.Corrections = append([]NoteCorrection{}, byVersion[rel.Version]...)
		h.Releases[i].Notes = notes
	}
	return h, nil
}
