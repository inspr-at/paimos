// SPDX-License-Identifier: AGPL-3.0-only
package quotedocument

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

const DigestMode = "document-v2"

// Digest has one definition at native and import boundaries. V2 sorts every
// object (including RawMessage party/legal/layout snapshots), preserving exact
// integers and array order across Postgres jsonb round trips. V1 is retained
// verbatim for historical evidence; never relabel or rewrite an issued digest.
func Digest(mode, id string, version int, offerNo string, doc Document) (string, error) {
	if mode != DigestMode && mode != "document-v1" {
		return "", errors.New("unsupported quote document digest")
	}
	payload := struct {
		Mode        string   `json:"mode"`
		QuoteNodeID string   `json:"quote_node_id"`
		Version     int      `json:"version"`
		OfferNo     string   `json:"offer_no"`
		Document    Document `json:"document"`
	}{mode, id, version, offerNo, doc}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	if mode == DigestMode {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return "", err
		}
		raw, err = json.Marshal(value)
		if err != nil {
			return "", err
		}
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func IsDocument(mode string) bool { return mode == DigestMode || mode == "document-v1" }
