// PAIMOS — Your Professional & Personal AI Project OS
// Copyright (C) 2026 Markus Barta <markus@barta.com>

package pirpc

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestReaderPreservesUnicodeLineSeparators(t *testing.T) {
	// U+2028 LINE SEPARATOR and U+2029 PARAGRAPH SEPARATOR must stay inside one record.
	payload := map[string]string{"type": "message_update", "content": "a\u2028b\u2029c"}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := string(body) + "\nsecond\n"
	reader := NewReader(strings.NewReader(input))
	first, err := reader.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, body) {
		t.Fatalf("first line=%q want exact payload without split", first)
	}
	second, err := reader.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "second" {
		t.Fatalf("second=%q", second)
	}
}

func TestReaderStripsTrailingCR(t *testing.T) {
	reader := NewReader(strings.NewReader("{\"type\":\"ok\"}\r\n"))
	line, err := reader.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "{\"type\":\"ok\"}" {
		t.Fatalf("line=%q", line)
	}
}

func TestReaderRejectsOversizeFrame(t *testing.T) {
	reader := NewReader(strings.NewReader(strings.Repeat("x", MaxFrameBytes+1) + "\n"))
	_, err := reader.ReadLine()
	if err != ErrFrameTooLarge {
		t.Fatalf("err=%v want ErrFrameTooLarge", err)
	}
}

func TestReaderEOFPartialLine(t *testing.T) {
	reader := NewReader(strings.NewReader("{\"type\":\"tail\"}"))
	line, err := reader.ReadLine()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != "{\"type\":\"tail\"}" {
		t.Fatalf("line=%q", line)
	}
	_, err = reader.ReadLine()
	if err != io.EOF {
		t.Fatalf("err=%v want EOF", err)
	}
}

func TestWriteLineAddsLFOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLine(&buf, map[string]string{"type": "prompt"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Contains(buf.String(), "\r") {
		t.Fatalf("output=%q", buf.String())
	}
}
