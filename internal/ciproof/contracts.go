// SPDX-License-Identifier: AGPL-3.0-only

package ciproof

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed contracts/v1.schema.json
var contractBytes []byte

// Decode validates the offline contract and rejects duplicate keys and trailing
// data before decoding. Syntax validity is not authenticated execution evidence.
func Decode(kind string, raw []byte, dst any) error {
	if len(raw) > 16<<20 {
		return fmt.Errorf("contract size limit")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if err := uniqueJSON(d); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(contractBytes, &schema); err != nil {
		return err
	}
	if kind != "plan" && kind != "obligation" && kind != "receipt" && kind != "binding" {
		return fmt.Errorf("unknown contract")
	}
	schema.Ref = "#/$defs/" + kind
	schema.OneOf = nil // Select one contract, including the nested binding shape.
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	if err := resolved.Validate(value); err != nil {
		return fmt.Errorf("invalid %s contract: %w", kind, err)
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

func uniqueJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	if delim != '{' && delim != '[' {
		return fmt.Errorf("unexpected JSON delimiter")
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return fmt.Errorf("duplicate JSON key")
			}
			seen[s] = true
		}
		if err := uniqueJSON(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}

func validateContract(kind string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var dst any
	return Decode(kind, raw, &dst)
}
