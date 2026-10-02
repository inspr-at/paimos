// SPDX-License-Identifier: AGPL-3.0-only
package quotedocument

import (
	"encoding/json"
	"testing"
)

func TestCanonicalDigestPreservesJSONBMeaningAndLegacyMode(t *testing.T) {
	doc := Document{SchemaVersion: 1, MinimumWriterVersion: 2, Title: "Exact", Currency: "EUR", NetTotalCents: 9007199254740993,
		Sender: json.RawMessage(`{"street":"Example 1","company":"Example"}`), Recipient: json.RawMessage(`{"email":"buyer@example.invalid","name":"Buyer"}`), Legal: json.RawMessage(`{"vat_note":"VAT","intro":"Intro"}`), Layout: json.RawMessage(`{"logo_width_mm":"33","logo_offset_mm":"0"}`),
		Sections: []Section{{ID: "section", Nodes: []TextNode{{ID: "node", Kind: "paragraph", Text: "Bold", Marks: []TextMark{{Start: 0, End: 4, Bold: true}}}}}}, Positions: []Position{}, Profile: &ProfileSnapshot{ID: "profile", Revision: 2, Definition: ProfileDefinition{Schema: "inspr.document-profile.v1"}.Normalized()},
	}
	first, err := Digest(DigestMode, "quote", 1, "A260101-01", doc)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := Digest("document-v1", "quote", 1, "A260101-01", doc)
	if err != nil {
		t.Fatal(err)
	}
	doc.Sender = json.RawMessage(`{ "company": "Example", "street": "Example 1" }`)
	doc.Recipient = json.RawMessage(`{"name":"Buyer","email":"buyer@example.invalid"}`)
	doc.Legal = json.RawMessage(`{"intro":"Intro","vat_note":"VAT"}`)
	second, err := Digest(DigestMode, "quote", 1, "A260101-01", doc)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("canonical digest depends on JSON object order")
	}
	old, err := Digest("document-v1", "quote", 1, "A260101-01", doc)
	if err != nil {
		t.Fatal(err)
	}
	if legacy == old || legacy == first {
		t.Fatal("legacy digest semantics silently changed")
	}
	doc.NetTotalCents--
	changed, err := Digest(DigestMode, "quote", 1, "A260101-01", doc)
	if err != nil {
		t.Fatal(err)
	}
	if first == changed {
		t.Fatal("digest lost integer precision")
	}
	if _, err := Digest("unknown", "quote", 1, "A260101-01", doc); err == nil {
		t.Fatal("unknown digest mode accepted")
	}
}
