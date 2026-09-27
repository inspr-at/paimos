// SPDX-License-Identifier: AGPL-3.0-only

package sessionusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

func decodeLine(line []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(line)))
	dec.UseNumber()
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON", ErrMalformed)
	}
	var extra any
	if err := dec.Decode(&extra); !isEOF(err) {
		return nil, fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return decodeObject(raw, nil)
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}

func decodeObject(raw json.RawMessage, allowed map[string]struct{}) (map[string]json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil, fmt.Errorf("%w: expected an object", ErrMalformed)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("%w: expected an object", ErrMalformed)
	}
	fields := make(map[string]json.RawMessage)
	for dec.More() {
		keyTok, err := dec.Token()
		key, ok := keyTok.(string)
		if err != nil || !ok {
			return nil, fmt.Errorf("%w: invalid object key", ErrMalformed)
		}
		if _, dup := fields[key]; dup {
			return nil, fmt.Errorf("%w: duplicate key", ErrAmbiguous)
		}
		if allowed != nil {
			if _, ok := allowed[key]; !ok {
				return nil, fmt.Errorf("%w: unknown counter field", ErrAmbiguous)
			}
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON value", ErrMalformed)
		}
		fields[key] = value
	}
	tok, err = dec.Token()
	if err != nil || tok != json.Delim('}') {
		return nil, fmt.Errorf("%w: unclosed object", ErrMalformed)
	}
	var extra any
	if err := dec.Decode(&extra); !isEOF(err) {
		return nil, fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return fields, nil
}

func parseCount(raw json.RawMessage) (int64, error) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s[0] == '"' || s == "null" {
		return 0, fmt.Errorf("%w: token is not an integer", ErrMalformed)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("%w: token is not a canonical integer", ErrMalformed)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > maxToken {
		return 0, fmt.Errorf("%w: token out of range", ErrMalformed)
	}
	return int64(n), nil
}

func parseString(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", fmt.Errorf("%w: expected a string", ErrMalformed)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%w: expected a string", ErrMalformed)
	}
	return s, nil
}

func allow(keys ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		out[key] = struct{}{}
	}
	return out
}
