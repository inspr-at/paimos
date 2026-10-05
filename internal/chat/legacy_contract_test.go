// SPDX-License-Identifier: AGPL-3.0-only

package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"regexp"
	"testing"

	"gopkg.in/yaml.v3"
)

// Reportercontract pins cover /me, approvals, journey, stage and harness
// responses. These pinned message/receipt snapshots close the remaining gap.
func TestStrictLegacyResponseSnapshotsUnchanged(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	snapshot, err := os.ReadFile("testdata/legacy-response-snapshots.json")
	if err != nil {
		t.Fatal(err)
	}
	var pinned struct {
		Baseline string            `json:"baseline"`
		SHA256   map[string]string `json:"sha256"`
	}
	if err = json.Unmarshal(snapshot, &pinned); err != nil {
		t.Fatal(err)
	}
	if len(pinned.SHA256) != 3 || pinned.Baseline != "1a8942d2" {
		t.Fatal("incomplete baseline snapshot")
	}
	blocks := regexp.MustCompile(`(?m)^    ([A-Za-z][A-Za-z0-9_]*):`)
	indexes := blocks.FindAllSubmatchIndex(raw, -1)
	found := map[string]bool{}
	for i, index := range indexes {
		name := string(raw[index[2]:index[3]])
		expected, ok := pinned.SHA256[name]
		if !ok {
			continue
		}
		end := len(raw)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		sum := sha256.Sum256(raw[index[0]:end])
		actual := hex.EncodeToString(sum[:])
		if actual != expected {
			t.Errorf("legacy %s response differs from %s snapshot", name, pinned.Baseline)
		}
		found[name] = true
	}
	if len(found) != 3 {
		t.Fatal("legacy snapshot schema missing")
	}
}
