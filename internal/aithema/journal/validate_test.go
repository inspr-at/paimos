// SPDX-License-Identifier: AGPL-3.0-only

package journal

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/aithema/tokens"
)

func fixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	obj, err := decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	return object(obj["doc"])
}

func TestPinnedProvenanceAndRFC8785Golden(t *testing.T) {
	raw, err := os.ReadFile("testdata/provenance.json")
	if err != nil {
		t.Fatal(err)
	}
	var provenance struct {
		Revision string `json:"revision"`
		Files    []struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(raw, &provenance); err != nil || provenance.Revision == "" || len(provenance.Files) == 0 {
		t.Fatal("missing fixture provenance")
	}
	for _, file := range provenance.Files {
		raw, err := os.ReadFile(file.Path)
		if err != nil || digest(raw) != file.SHA256 {
			t.Fatalf("pinned fixture differs: %s", file.Path)
		}
	}
	raw, err = os.ReadFile("testdata/canonical/rfc8785-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Input     string `json:"input_json"`
		Canonical string `json:"canonical"`
		Hex       string `json:"canonical_utf8_hex"`
		SHA       string `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	canonical, err := tokens.CanonicalJSON([]byte(golden.Input))
	if err != nil || string(canonical) != golden.Canonical || hex.EncodeToString(canonical) != golden.Hex || digest(canonical) != golden.SHA {
		t.Fatal("RFC 8785 canonical bytes differ from the binding golden vector")
	}
}

func TestDesignInputCanonicalByteLimits(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := fixture(t, "valid/record.design-input.json")
	data := object(doc["data"])
	data["screen_ir"] = map[string]any{"text": strings.Repeat("<", 100000)}
	data["tokens"] = map[string]any{"text": strings.Repeat("<", 15000)}
	for _, field := range []string{"screen_ir", "tokens"} {
		canonical, err := tokens.CanonicalJSON(marshal(data[field]))
		if err != nil {
			t.Fatal(err)
		}
		data[field+"_sha256"] = digest(canonical)
	}
	if _, err := v.Validate(marshal(doc), "aithema.journal.record"); err != nil {
		t.Fatalf("HTML escaping inflated canonical input size: %v", err)
	}
	for field, limit := range map[string]int{"screen_ir": 512 << 10, "tokens": 64 << 10} {
		t.Run(field, func(t *testing.T) {
			original := data[field]
			data[field] = map[string]any{"text": strings.Repeat("x", limit)}
			canonical, err := tokens.CanonicalJSON(marshal(data[field]))
			if err != nil {
				t.Fatal(err)
			}
			data[field+"_sha256"] = digest(canonical)
			_, err = v.Validate(marshal(doc), "aithema.journal.record")
			errorIs(t, err, 400, "invalid_request")
			data[field] = original
			canonical, _ = tokens.CanonicalJSON(marshal(original))
			data[field+"_sha256"] = digest(canonical)
		})
	}
}
func TestBindingFixtures(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	for _, category := range []string{"valid", "invalid"} {
		files, err := filepath.Glob("testdata/" + category + "/*.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			t.Run(category+"/"+filepath.Base(file), func(t *testing.T) {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				wrapper, err := decode(raw)
				if err != nil {
					t.Fatal(err)
				}
				contract := text(wrapper["contract"])
				if _, ok := v.schemas[contract]; !ok {
					return
				}
				_, err = v.Validate(marshal(wrapper["doc"]), contract)
				if (err == nil) != (category == "valid") {
					t.Fatalf("validation %v, want valid=%v", err, category == "valid")
				}
			})
		}
	}
}
func TestExactNumbersUnicodeAndCanonicalDigests(t *testing.T) {
	v, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	doc := fixture(t, "valid/record.design-input.json")
	body := object(doc["data"])
	body["screen_ir"] = map[string]any{"<key>": "<&>", "😀": json.Number("1e+21"), "\ue000": json.Number("0.0000001")}
	canonical, err := tokens.CanonicalJSON(marshal(body["screen_ir"]))
	if err != nil {
		t.Fatal(err)
	}
	body["screen_ir_sha256"] = digest(canonical)
	if _, err := v.Validate(marshal(doc), "aithema.journal.record"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"contract":"aithema.budget.message","major":1,"minor":0,"min_reader":0,"type":"admit_request","body":{"attempt_id":"0f8e9d2c-3b4a-4c5d-8e6f-7a8b9c0d1e2f:1:spec:1","sid":"0f8e9d2c-3b4a-4c5d-8e6f-7a8b9c0d1e2f","worker_generation":1,"auth_epoch":1,"lane":"spec","currency":"EUR","max_micro":9007199254740991.1}}`, `{"a":1,"a":2}`, `{"a":"\ud800"}`, `{"a":1e999999}`, `{"a":1} {"b":2}`} {
		if obj, err := decode([]byte(raw)); err == nil {
			if _, err := v.Validate(marshal(obj), "aithema.budget.message"); err == nil {
				t.Fatalf("accepted invalid input")
			}
		}
	}
	doc = fixture(t, "valid/budget.admit-request.json")
	object(doc["body"])["max_micro"] = json.Number("9007199254740991.0")
	if _, err := v.Validate(marshal(doc), "aithema.budget.message"); err != nil {
		t.Fatalf("exact safe integer decimal rejected: %v", err)
	}
}
