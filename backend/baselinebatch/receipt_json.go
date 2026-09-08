// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const BuiltReceiptMaxJSONBytes = 64 << 10

const receiptJSONMaxDepth = 8

var (
	ErrDuplicateJSONField = errors.New("duplicate json field")
	ErrTrailingJSON       = errors.New("expected exactly one JSON value")
)

// DecodeBuiltReceiptJSON reads at most BuiltReceiptMaxJSONBytes+1 from r and
// decodes one closed built-receipt object. Duplicate names (including
// encoding/json case-fold collisions), unknown fields, trailing values, and
// oversized bodies fail before any typed validation.
func DecodeBuiltReceiptJSON(r io.Reader) (BuiltReceiptRequest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, BuiltReceiptMaxJSONBytes+1))
	if err != nil {
		return BuiltReceiptRequest{}, fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	if len(bytes.TrimSpace(raw)) == 0 || len(raw) > BuiltReceiptMaxJSONBytes {
		return BuiltReceiptRequest{}, fmt.Errorf("%w: receipt json size", ErrInvalid)
	}
	if err := rejectDuplicateJSONNames(raw); err != nil {
		return BuiltReceiptRequest{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req BuiltReceiptRequest
	if err := dec.Decode(&req); err != nil {
		return BuiltReceiptRequest{}, fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return BuiltReceiptRequest{}, fmt.Errorf("%w: %w", ErrInvalid, ErrTrailingJSON)
	}
	return req, nil
}

func rejectDuplicateJSONNames(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanJSONValueForDuplicates(dec, 0); err != nil {
		return err
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %w", ErrInvalid, ErrTrailingJSON)
	}
	return nil
}

func scanJSONValueForDuplicates(dec *json.Decoder, depth int) error {
	if depth > receiptJSONMaxDepth {
		return fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	delim, isDelim := token.(json.Delim)
	if !isDelim {
		return nil
	}
	switch delim {
	case '{':
		seen := []string{}
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return fmt.Errorf("%w: receipt json", ErrInvalid)
			}
			name, ok := nameToken.(string)
			if !ok {
				return fmt.Errorf("%w: receipt json", ErrInvalid)
			}
			for _, previous := range seen {
				if strings.EqualFold(previous, name) {
					return fmt.Errorf("%w: %w", ErrInvalid, ErrDuplicateJSONField)
				}
			}
			seen = append(seen, name)
			if err := scanJSONValueForDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanJSONValueForDuplicates(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("%w: receipt json", ErrInvalid)
	}
	return nil
}
