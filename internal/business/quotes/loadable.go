// SPDX-License-Identifier: AGPL-3.0-only

package quotes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// The editor reads profile and document lists without null checks: a JSON
// null where it expects [] or {} crashes the quote page (AEON-274). Go encodes
// a nil slice or map as null, so every definition is normalised when it is
// read from a revision or written to one, and stored JSON is checked before it
// is saved. Documents that were already digested are not re-encoded: their
// content hash covers the bytes as they were frozen.

// normalized returns the definition with empty lists and maps instead of nil.
func (d profileDefinition) normalized() profileDefinition {
	if d.Fonts == nil {
		d.Fonts = []profileFont{}
	}
	if d.PositionsTable.Columns == nil {
		d.PositionsTable.Columns = []profileColumn{}
	}
	for _, m := range []*map[string]string{&d.Colors, &d.Typography, &d.Cover, &d.Sections, &d.Labels} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	return d
}

// normalizedSnapshot copies a snapshot with a normalized definition.
func normalizedSnapshot(s *documentProfileSnapshot) *documentProfileSnapshot {
	if s == nil {
		return nil
	}
	out := *s
	out.Definition = s.Definition.normalized()
	return &out
}

// jsonKind reports the JSON type of one raw value: null, array, object or scalar.
func jsonKind(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	switch {
	case len(raw) == 0:
		return "missing"
	case raw[0] == '[':
		return "array"
	case raw[0] == '{':
		return "object"
	case bytes.Equal(raw, []byte("null")):
		return "null"
	}
	return "scalar"
}

func requireKinds(prefix string, fields map[string]json.RawMessage, want map[string]string) error {
	for key, kind := range want {
		if got := jsonKind(fields[key]); got != kind {
			return fmt.Errorf("%s%s must be an %s, not %s", prefix, key, kind, got)
		}
	}
	return nil
}

// checkProfileJSON verifies a stored profile definition has every list and
// object the editor reads.
func checkProfileJSON(prefix string, raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("%s must be an object", prefix)
	}
	if err := requireKinds(prefix+".", fields, map[string]string{
		"fonts": "array", "colors": "object", "typography": "object", "page": "object", "cover": "object", "sections": "object",
		"positions_table": "object", "totals": "object", "payment_terms": "object", "acceptance": "object", "footer": "object", "labels": "object",
	}); err != nil {
		return err
	}
	var table map[string]json.RawMessage
	if err := json.Unmarshal(fields["positions_table"], &table); err != nil {
		return err
	}
	return requireKinds(prefix+".positions_table.", table, map[string]string{"columns": "array"})
}

// CheckLoadableDocument verifies that a stored quote document (a draft or a
// version snapshot) decodes and has every list the quote page reads, so the
// page opens without a client error.
func CheckLoadableDocument(raw []byte) error {
	if _, err := decodeDocument(raw); err != nil {
		return err
	}
	return checkDocumentJSON(raw)
}

func checkDocumentJSON(raw []byte) error {
	var doc struct {
		Profile   json.RawMessage `json:"profile"`
		Sections  json.RawMessage `json:"sections"`
		Positions json.RawMessage `json:"positions"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return err
	}
	if jsonKind(doc.Sections) != "array" || jsonKind(doc.Positions) != "array" {
		return errors.New("document.sections and document.positions must be arrays")
	}
	var sections []map[string]json.RawMessage
	if err := json.Unmarshal(doc.Sections, &sections); err != nil {
		return err
	}
	for i, section := range sections {
		if jsonKind(section["nodes"]) != "array" {
			return fmt.Errorf("document.sections[%d].nodes must be an array", i)
		}
	}
	if kind := jsonKind(doc.Profile); kind == "missing" || kind == "null" {
		return nil
	}
	var profile map[string]json.RawMessage
	if err := json.Unmarshal(doc.Profile, &profile); err != nil {
		return errors.New("document.profile must be an object")
	}
	return checkProfileJSON("document.profile.definition", profile["definition"])
}

// marshalProfile encodes a definition for a new profile revision and refuses
// one the editor could not read.
func marshalProfile(d profileDefinition) ([]byte, error) {
	raw, err := json.Marshal(d.normalized())
	if err != nil {
		return nil, err
	}
	if err := checkProfileJSON("definition", raw); err != nil {
		return nil, bad("invalid document profile: " + err.Error())
	}
	return raw, nil
}

// normalizedDraft returns the document with empty lists instead of nil and a
// normalized profile snapshot. Only drafts, and a draft as it is being frozen
// (before its digest is computed), are normalized; a frozen version is never
// rewritten. A draft copied from an old snapshot with null lists therefore
// stays issuable. The caller's sections are not modified.
func normalizedDraft(doc quoteDocument) quoteDocument {
	doc.Profile = normalizedSnapshot(doc.Profile)
	if doc.Positions == nil {
		doc.Positions = []documentPosition{}
	}
	sections := make([]documentSection, len(doc.Sections))
	for i, section := range doc.Sections {
		if section.Nodes == nil {
			section.Nodes = []textNode{}
		}
		sections[i] = section
	}
	doc.Sections = sections
	return doc
}

// marshalDraft encodes a draft document, normalized. Drafts carry no digest.
func marshalDraft(doc quoteDocument) ([]byte, error) {
	return marshalDocument(normalizedDraft(doc))
}

// sameProfile compares two snapshots as the editor reads them, so a stored
// null list and an empty one are the same profile.
func sameProfile(a, b *documentProfileSnapshot) bool {
	left, _ := json.Marshal(normalizedSnapshot(a))
	right, _ := json.Marshal(normalizedSnapshot(b))
	return bytes.Equal(left, right)
}

// marshalDocument encodes a quote document for a draft or version and refuses
// one the quote page could not open. It does not rewrite the document: a
// version's digest is computed over the same value.
func marshalDocument(doc quoteDocument) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := checkDocumentJSON(raw); err != nil {
		return nil, bad("invalid quote document: " + err.Error())
	}
	return raw, nil
}
