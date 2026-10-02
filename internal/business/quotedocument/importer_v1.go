// SPDX-License-Identifier: AGPL-3.0-only
package quotedocument

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Frozen copies of the historical importer types. Field order, map-shaped
// prose (including explicitly present false/zero values), and the raw profile
// definition are part of document-v1's byte contract. Do not reuse the evolving
// native model here or change any stored digest to fit the new model.
type importerV1Document struct {
	SchemaVersion        int                  `json:"schema_version"`
	MinimumWriterVersion int                  `json:"minimum_writer_version"`
	Title                string               `json:"title"`
	Subtitle             string               `json:"subtitle"`
	ProjectRef           string               `json:"project_ref"`
	OfferDate            string               `json:"offer_date"`
	ValidUntil           string               `json:"valid_until"`
	Currency             string               `json:"currency"`
	Sender               json.RawMessage      `json:"sender"`
	Recipient            map[string]any       `json:"recipient"`
	Legal                map[string]string    `json:"legal"`
	Layout               map[string]string    `json:"layout"`
	Sections             []importerV1Section  `json:"sections"`
	Positions            []importerV1Position `json:"positions"`
	NetTotalCents        int64                `json:"net_total_cents"`
	Profile              *importerV1Profile   `json:"profile,omitempty"`
}

type importerV1Profile struct {
	ID         string          `json:"id"`
	Revision   int             `json:"revision"`
	Definition json.RawMessage `json:"definition"`
}

type importerV1Section struct {
	ID      string           `json:"id"`
	Heading string           `json:"heading"`
	Body    string           `json:"body"`
	Nodes   []map[string]any `json:"nodes"`
}

type importerV1Position struct {
	ID             string `json:"id"`
	PricingSource  string `json:"pricing_source"`
	ShortText      string `json:"short_text"`
	LongText       string `json:"long_text"`
	Quantity       string `json:"quantity"`
	UnitLabel      string `json:"unit_label"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	TotalCents     int64  `json:"total_cents"`
	Currency       string `json:"currency"`
}

// Verify retains both native and importer v1 readers. Stored JSON is needed
// because decoding prose into TextNode loses the historical map's presence
// semantics. New v2 documents have exactly one canonical verification path.
func Verify(mode, id string, version int, offerNo, expected string, doc Document, stored []byte) (bool, error) {
	sum, err := Digest(mode, id, version, offerNo, doc)
	if err != nil || sum == expected || mode != "document-v1" {
		return sum == expected && err == nil, err
	}
	var historical importerV1Document
	decoder := json.NewDecoder(bytes.NewReader(stored))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&historical); err != nil {
		return false, nil
	}
	// Recipient, legal/layout and prose were maps in the old writer. Sender
	// was raw source JSON; support its stored order and the source's sorted object
	// order without changing the frozen rest of the document.
	for _, sortedSender := range []bool{false, true} {
		if sortedSender {
			var sender map[string]any
			decoder := json.NewDecoder(bytes.NewReader(historical.Sender))
			decoder.UseNumber()
			if err := decoder.Decode(&sender); err != nil {
				return false, err
			}
			historical.Sender, err = json.Marshal(sender)
			if err != nil {
				return false, err
			}
		}
		payload := struct {
			Mode        string             `json:"mode"`
			QuoteNodeID string             `json:"quote_node_id"`
			Version     int                `json:"version"`
			OfferNo     string             `json:"offer_no"`
			Document    importerV1Document `json:"document"`
		}{mode, id, version, offerNo, historical}
		raw, err := json.Marshal(payload)
		if err != nil {
			return false, err
		}
		digest := sha256.Sum256(raw)
		if hex.EncodeToString(digest[:]) == expected {
			return true, nil
		}
	}
	return false, nil
}
