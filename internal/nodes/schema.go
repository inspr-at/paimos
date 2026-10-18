// SPDX-License-Identifier: AGPL-3.0-only

package nodes

import (
	"encoding/json"

	"github.com/inspr-at/paimos/internal/fieldschema"
)

type jsSchema = fieldschema.Schema

func compileSchema(raw json.RawMessage) (*jsSchema, error) {
	s, err := fieldschema.Compile(raw)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	return s, nil
}

func decodeValue(raw []byte) (any, error) { return fieldschema.Decode(raw) }

func validateFields(schema *jsSchema, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if string(raw) == "null" {
		return nil, badRequest("fields must be a JSON object")
	}
	v, err := decodeValue(raw)
	if err != nil {
		return nil, badRequest("fields must be a JSON object")
	}
	fields, ok := v.(map[string]any)
	if !ok {
		return nil, badRequest("fields must be a JSON object")
	}
	if marked, present := fields["no_release_needed"]; present {
		if _, ok := marked.(bool); !ok {
			return nil, unprocessable("no_release_needed must be a boolean")
		}
	}
	if schema != nil {
		if err := schema.Validate(v); err != nil {
			return nil, unprocessable("fields do not match the kind schema: " + err.Error())
		}
	}
	stored, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return stored, nil
}
