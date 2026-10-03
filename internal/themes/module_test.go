// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error
}

func (w *deadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return w.err
}

type bodyReader func([]byte) (int, error)

func (read bodyReader) Read(p []byte) (int, error) { return read(p) }

func TestInputBoundsReadTime(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		readErr    error
		status     int
		valid      bool
	}{
		{"valid", `{"name":"Copper","scope":"personal"}`, nil, 200, true},
		{"invalid", `{`, nil, 400, false},
		{"oversized", strings.Repeat("x", (8<<10)+1), nil, 413, false},
		{"timeout", "", os.ErrDeadlineExceeded, 408, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			source := strings.NewReader(tc.body)
			read := false
			r := httptest.NewRequest("POST", "/api/themes", nil)
			r.Body = io.NopCloser(bodyReader(func(p []byte) (int, error) {
				read = true
				if len(w.deadlines) != 1 || w.deadlines[0].IsZero() {
					t.Fatal("body was read before its deadline was set")
				}
				if tc.readErr != nil {
					return 0, tc.readErr
				}
				return source.Read(p)
			}))
			var out CreateInput
			if valid := input(w, r, &out, "name", "scope"); valid != tc.valid || w.Code != tc.status {
				t.Fatalf("valid=%t HTTP %d; want %t HTTP %d: %s", valid, w.Code, tc.valid, tc.status, w.Body.String())
			}
			if !read || len(w.deadlines) != 2 || !w.deadlines[1].IsZero() {
				t.Fatalf("body read=%t; deadline was not cleared: %v", read, w.deadlines)
			}
		})
	}
}

func TestInputDeadlineFailureStopsBeforeRead(t *testing.T) {
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("deadline unavailable")}
	r := httptest.NewRequest("POST", "/api/themes", nil)
	r.Body = io.NopCloser(bodyReader(func([]byte) (int, error) {
		t.Fatal("body read despite failure to set a supported deadline")
		return 0, io.EOF
	}))
	var out CreateInput
	if input(w, r, &out, "name", "scope") || w.Code != 500 {
		t.Fatalf("expected deadline failure, HTTP %d: %s", w.Code, w.Body.String())
	}
}
