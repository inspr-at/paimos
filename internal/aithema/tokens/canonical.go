// SPDX-License-Identifier: AGPL-3.0-only

package tokens

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"

	"github.com/go-json-experiment/json/jsontext"
)

// CanonicalJSON implements RFC 8785, including UTF-16 property ordering and
// ECMAScript number spelling. It never mutates its input. Claims must be
// validated before calling this: JCS itself converts numbers to binary64.
func CanonicalJSON(raw []byte) ([]byte, error) {
	// The pinned formatter saturates overflowing numbers. JCS requires those
	// to fail, so check representability before its binary64 conversion.
	d := jsontext.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := d.ReadToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid canonical JSON")
		}
		if tok.Kind() == '0' {
			if _, err := strconv.ParseFloat(tok.String(), 64); err != nil {
				return nil, errors.New("invalid canonical JSON number")
			}
		}
	}
	v := jsontext.Value(bytes.Clone(raw))
	if err := v.Canonicalize(); err != nil {
		return nil, errors.New("invalid canonical JSON")
	}
	return []byte(v), nil
}

// decodeObject rejects duplicate members, invalid Unicode and trailing values,
// retaining json.Number until the contract's exact integer checks complete.
func decodeObject(raw []byte) (map[string]any, error) {
	if !jsontext.Value(raw).IsValid() {
		return nil, ErrClaims
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var obj map[string]any
	if err := d.Decode(&obj); err != nil || obj == nil {
		return nil, ErrClaims
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrClaims
	}
	return obj, nil
}
