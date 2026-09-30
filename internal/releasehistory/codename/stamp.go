// SPDX-License-Identifier: AGPL-3.0-only

package codename

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// StampVersionFile writes the reservation's codename into version.json: the
// "codename" member right after "release_sequence", every other member kept
// in its order and value. It is idempotent, and it refuses a file whose
// codename differs from Codename(release_sequence). The codename is
// presentation only; the version stays the release's identity.
func StampVersionFile(raw []byte) ([]byte, string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, "", errors.New("version.json: not a JSON object")
	}
	type member struct {
		key   string
		value json.RawMessage
	}
	var members []member
	sequence, have, stamped := 0, "", false
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, "", fmt.Errorf("version.json: %w", err)
		}
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, "", fmt.Errorf("version.json %s: %w", key, err)
		}
		switch key {
		case "release_sequence":
			if err := json.Unmarshal(value, &sequence); err != nil {
				return nil, "", fmt.Errorf("version.json release_sequence: %w", err)
			}
		case "codename":
			stamped = true
			if err := json.Unmarshal(value, &have); err != nil {
				return nil, "", fmt.Errorf("version.json codename: %w", err)
			}
		}
		members = append(members, member{key, value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, "", fmt.Errorf("version.json: %w", err)
	}
	if sequence < 1 {
		return nil, "", errors.New("version.json: release_sequence must be at least 1")
	}
	name := Codename(sequence)
	if stamped {
		if have != name {
			return nil, "", fmt.Errorf("version.json: codename %q, but release_sequence %d is %q", have, sequence, name)
		}
		return raw, name, nil
	}
	value, _ := json.Marshal(name)
	var next []member
	for _, m := range members {
		next = append(next, m)
		if m.key == "release_sequence" {
			next = append(next, member{"codename", value})
		}
	}
	var out bytes.Buffer
	out.WriteString("{\n")
	for i, m := range next {
		k, _ := json.Marshal(m.key)
		out.WriteString("  ")
		out.Write(k)
		out.WriteString(": ")
		if err := json.Indent(&out, m.value, "  ", "  "); err != nil {
			return nil, "", fmt.Errorf("version.json %s: %w", m.key, err)
		}
		if i < len(next)-1 {
			out.WriteByte(',')
		}
		out.WriteByte('\n')
	}
	out.WriteString("}\n")
	return out.Bytes(), name, nil
}
