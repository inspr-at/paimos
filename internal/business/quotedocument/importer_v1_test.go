// SPDX-License-Identifier: AGPL-3.0-only
package quotedocument

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestHistoricalImporterSenderEvidence(t *testing.T) {
	raw, err := os.ReadFile("testdata/importer-v1-document.json")
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		t.Fatal(err)
	}
	raw = compact.Bytes()
	var source struct {
		Sender json.RawMessage `json:"sender"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	classic := string(source.Sender)
	for _, tc := range []struct {
		name, original, stored string
		want                   bool
	}{
		{"classic full sender", classic, classic, true},
		{"one key", `{"company":"Synthetic"}`, `{"company":"Synthetic"}`, true},
		{"sparse with explicit empty", `{"company":"Synthetic","street":"Example Street 1","email":""}`, `{"company":"Synthetic","street":"Example Street 1","email":""}`, true},
		{"explicit null", `{"company":"Synthetic","street":"Example Street 1","email":null}`, `{"company":"Synthetic","street":"Example Street 1","email":null}`, true},
		{"altered value", classic, strings.Replace(classic, "Example Street 1", "Example Street 2", 1), false},
		{"removed empty field", classic, strings.Replace(classic, `,"register_no":""`, "", 1), false},
		{"null replaced with empty", `{"company":"Synthetic","street":"Example Street 1","email":null}`, `{"company":"Synthetic","street":"Example Street 1","email":""}`, false},
		{"object replaced by null", `{}`, `null`, false},
		{"added unknown field", classic, strings.TrimSuffix(classic, "}") + `,"logo_file_id":"added"}`, false},
		{"renamed field", classic, strings.Replace(classic, `"company":`, `"Company":`, 1), false},
		{"extra case variant", classic, strings.TrimSuffix(classic, "}") + `,"Company":"Synthetic"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := strings.Replace(string(raw), classic, tc.original, 1)
			// Hash the ordered historical bytes independently of Verify/Digest.
			payload := fmt.Sprintf(`{"mode":"document-v1","quote_node_id":"quote","version":1,"offer_no":"A260101-01","document":%s}`, original)
			sum := sha256.Sum256([]byte(payload))
			expected := hex.EncodeToString(sum[:])
			stored := strings.Replace(string(raw), classic, tc.stored, 1)
			// Sort object keys to exercise reconstruction without Postgres.
			// The quotes integration regression separately persists real JSONB.
			var value any
			decoder := json.NewDecoder(strings.NewReader(stored))
			decoder.UseNumber()
			if err := decoder.Decode(&value); err != nil {
				t.Fatal(err)
			}
			reordered, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var doc Document
			if err := json.Unmarshal(reordered, &doc); err != nil {
				t.Fatal(err)
			}
			got, err := Verify("document-v1", "quote", 1, "A260101-01", expected, doc, reordered)
			if err != nil || got != tc.want {
				t.Fatalf("verification = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
