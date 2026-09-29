// SPDX-License-Identifier: AGPL-3.0-only

package releasehistory

import (
	"encoding/json"
	"strings"
)

// Display groups for one change. The version bump has no group.
const (
	GroupFeatures = "features"
	GroupFixes    = "fixes"
	GroupOther    = "other"
)

// TicketNote is one linked ticket's release-note text. Hidden tickets
// are not noted. Empty strings stay empty; the client picks the language.
// Group is this ticket's own fixes or features, so a commit shared by several
// tickets does not decide it for all of them (AEON-305).
type TicketNote struct {
	Key       string `json:"key"`
	PillEN    string `json:"pill_en"`
	PillDE    string `json:"pill_de"`
	BenefitEN string `json:"benefit_en"`
	BenefitDE string `json:"benefit_de"`
	Group     string `json:"group,omitempty"`
}

// TicketMeta is the part of a linked ticket that decides a change's group.
// Bug is a bug tag, a type of bug, or a kind of bug. PublicBenefit is a
// release-note pill or benefit on a ticket that is not a bug and not hidden.
// A bug stays a fix even when it also carries a pill or benefit. Note is set
// for every non-hidden ticket that has a pill or benefit, including bugs.
type TicketMeta struct {
	Bug           bool
	PublicBenefit bool
	Note          *TicketNote
}

// ParseTicketMeta reads kind and nodes.fields. Tag entries may be names or
// objects with a name, which is how imported tickets store them. A field that
// is not the expected JSON type is ignored.
func ParseTicketMeta(kind string, fields json.RawMessage) TicketMeta {
	var meta TicketMeta
	if strings.EqualFold(strings.TrimSpace(kind), "bug") {
		meta.Bug = true
	}
	if len(strings.TrimSpace(string(fields))) == 0 || string(fields) == "null" {
		return meta
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(fields, &raw) != nil {
		return meta
	}
	var typ string
	if json.Unmarshal(raw["type"], &typ) == nil && strings.EqualFold(strings.TrimSpace(typ), "bug") {
		meta.Bug = true
	}
	if bugTag(raw["tags"]) {
		meta.Bug = true
	}
	var hidden bool
	_ = json.Unmarshal(raw["hide_from_release_notes"], &hidden)
	note := TicketNote{
		PillEN: textField(raw["pill_en"]), PillDE: textField(raw["pill_de"]),
		BenefitEN: textField(raw["benefit_en"]), BenefitDE: textField(raw["benefit_de"]),
	}
	if !hidden && (note.PillEN != "" || note.PillDE != "" || note.BenefitEN != "" || note.BenefitDE != "") {
		meta.Note = &note
		if !meta.Bug {
			meta.PublicBenefit = true
		}
	}
	return meta
}

func textField(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return strings.TrimSpace(text)
}

func bugTag(raw json.RawMessage) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return false
	}
	for _, item := range items {
		var name string
		if json.Unmarshal(item, &name) != nil {
			var obj struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(item, &obj) != nil {
				continue
			}
			name = obj.Name
		}
		if strings.EqualFold(strings.TrimSpace(name), "bug") {
			return true
		}
	}
	return false
}

// GroupChange classifies one commit. Conventional feat and fix prefixes win,
// and test, docs, refactor and chore stay other. A release subject is the
// version bump and returns an empty group. Otherwise the linked tickets
// decide, and several tickets take the strongest group: fixes, then features,
// then other. A missing ticket adds nothing.
func GroupChange(subject string, tickets []string, meta map[string]TicketMeta) string {
	switch kind, _ := Classify(subject); kind {
	case "release":
		return ""
	case "feat":
		return GroupFeatures
	case "fix":
		return GroupFixes
	case "test", "docs", "refactor", "chore":
		return GroupOther
	}
	best := GroupOther
	for _, key := range tickets {
		m, ok := meta[key]
		if !ok {
			continue
		}
		g := GroupOther
		if m.Bug {
			g = GroupFixes
		} else if m.PublicBenefit {
			g = GroupFeatures
		}
		if groupRank(g) > groupRank(best) {
			best = g
		}
	}
	return best
}

// linkedNotes copies the visible pill and benefit for each linked ticket, in
// first-seen order. Hidden tickets and tickets without text are left out.
func linkedNotes(tickets []string, meta map[string]TicketMeta) []TicketNote {
	var out []TicketNote
	seen := map[string]bool{}
	for _, key := range tickets {
		m, ok := meta[key]
		if !ok || m.Note == nil || seen[key] {
			continue
		}
		seen[key] = true
		note := *m.Note
		note.Key = key
		note.Group = GroupFeatures
		if m.Bug {
			note.Group = GroupFixes
		}
		out = append(out, note)
	}
	return out
}

func groupRank(group string) int {
	switch group {
	case GroupFixes:
		return 2
	case GroupFeatures:
		return 1
	default:
		return 0
	}
}

// withGroups returns a copy of h whose changes carry GroupChange. The stored
// history is left untouched.
func withGroups(h History, meta map[string]TicketMeta) History {
	out := h
	out.Releases = make([]Release, len(h.Releases))
	for i, rel := range h.Releases {
		changes := make([]Change, len(rel.Changes))
		copy(changes, rel.Changes)
		for j := range changes {
			g := GroupChange(changes[j].Subject, changes[j].Tickets, meta)
			if g != "" {
				changes[j].Group = g
			}
			if g == GroupFeatures || g == GroupFixes {
				if notes := linkedNotes(changes[j].Tickets, meta); len(notes) > 0 {
					changes[j].Linked = notes
				}
			}
		}
		rel.Changes = changes
		out.Releases[i] = rel
	}
	return out
}
