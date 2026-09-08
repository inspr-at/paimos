// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package baselinebatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecodeBuiltReceiptJSONRejectsDuplicatesUnknownTrailingAndSize(t *testing.T) {
	valid, err := json.Marshal(calendarReceipt("receipt-json-01"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeBuiltReceiptJSON(bytes.NewReader(valid))
	if err != nil || got.IdempotencyKey != "receipt-json-01" {
		t.Fatalf("valid decode=%+v err=%v", got, err)
	}

	dupCommit := duplicateJSONField(t, valid, "commit", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if _, err := DecodeBuiltReceiptJSON(bytes.NewReader(dupCommit)); !errors.Is(err, ErrDuplicateJSONField) {
		t.Fatalf("duplicate commit: %v", err)
	}
	dupCAS := duplicateJSONField(t, valid, "expected_implementation_execution", int64(7))
	if _, err := DecodeBuiltReceiptJSON(bytes.NewReader(dupCAS)); !errors.Is(err, ErrDuplicateJSONField) {
		t.Fatalf("duplicate CAS: %v", err)
	}
	dupKey := duplicateJSONField(t, valid, "idempotency_key", "receipt-json-other")
	if _, err := DecodeBuiltReceiptJSON(bytes.NewReader(dupKey)); !errors.Is(err, ErrDuplicateJSONField) {
		t.Fatalf("duplicate idempotency: %v", err)
	}

	unknown := append(append([]byte{}, bytes.TrimSuffix(valid, []byte(`}`))...), []byte(`,"handoff_secret":"no"}`)...)
	if _, err := DecodeBuiltReceiptJSON(bytes.NewReader(unknown)); !errors.Is(err, ErrInvalid) || errors.Is(err, ErrDuplicateJSONField) {
		t.Fatalf("unknown field: %v", err)
	}
	trailing := append(append([]byte{}, valid...), []byte("\n{\"x\":1}")...)
	if _, err := DecodeBuiltReceiptJSON(bytes.NewReader(trailing)); !errors.Is(err, ErrTrailingJSON) {
		t.Fatalf("trailing JSON: %v", err)
	}
	if _, err := DecodeBuiltReceiptJSON(strings.NewReader(strings.Repeat("a", BuiltReceiptMaxJSONBytes+8))); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversize: %v", err)
	}
	limited := &countingReader{Reader: strings.NewReader(strings.Repeat("a", 1<<20))}
	if _, err := DecodeBuiltReceiptJSON(limited); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bounded oversize: %v", err)
	}
	if limited.n > BuiltReceiptMaxJSONBytes+1 {
		t.Fatalf("read %d bytes from oversized source", limited.n)
	}
}

type countingReader struct {
	io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.n += n
	return n, err
}

func duplicateJSONField(t *testing.T, raw []byte, field string, extra any) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(body[field])
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(extra)
	if err != nil {
		t.Fatal(err)
	}
	needle := []byte(`"` + field + `":` + string(first))
	dup := []byte(`"` + field + `":` + string(first) + `,"` + field + `":` + string(second))
	if !bytes.Contains(raw, needle) {
		t.Fatalf("missing %s in %s", field, raw)
	}
	return bytes.Replace(raw, needle, dup, 1)
}
