// SPDX-License-Identifier: AGPL-3.0-only
package themes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/inspr-at/paimos/internal/tenant"
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

func TestMalformedThemeIDsFailBeforeDatabaseWork(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Mount(mux)
	p := tenant.Principal{ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TenantID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Kind: tenant.Person}
	// pgtype.UUID.Scan strips these separator positions without validating
	// them. Reject this spelling before it reaches a SQL UUID conversion.
	bad := "00000000x0000x4000x8000x000000000001"
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/themes/" + bad, ""},
		{"GET", "/api/themes?after=" + bad, ""},
		{"PUT", "/api/me/theme", `{"theme_id":"` + bad + `","revision":0}`},
	} {
		expect(t, call(t, mux, p, tc.method, tc.path, tc.body), 400)
	}
	if !validUUID(strings.ToUpper(p.ID)) {
		t.Fatal("valid uppercase UUID rejected")
	}
}

func TestCustomAgentStates(t *testing.T) {
	base := Porcelain()
	if !strings.Contains(string(mustJSON(t, base)), `"custom_states":null`) {
		t.Fatal("default custom states must serialize as null")
	}
	for _, palette := range []string{"standard", "focus", "errors", "monochrome", "deutan", "protan", "tritan"} {
		base.Agents.Palette = palette
		if err := base.validate(); err != nil {
			t.Fatalf("%s: %v", palette, err)
		}
	}
	dark := "#AB71FA"
	states := AgentStates{Working: Accent{Light: "#00870e"}, Waiting: Accent{Light: "#c47a08"}, Throttled: Accent{Light: "#7039c6", Dark: &dark}, Problem: Accent{Light: "#b92229"}, Idle: Accent{Light: "#6a7378"}}
	base.Agents.Palette, base.Agents.CustomStates = "custom", &states
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if !completeValues(raw) {
		t.Fatal("complete custom states rejected")
	}
	var decoded Values
	if err := json.Unmarshal(raw, &decoded); err != nil || !same(decoded, base) {
		t.Fatalf("round trip: %v", err)
	}
	for _, palette := range []string{"custom", "focus"} {
		base.Agents.Palette = palette
		base.Agents.CustomStates = &AgentStates{Working: states.Working, Waiting: states.Waiting, Throttled: states.Throttled, Problem: states.Problem, Idle: Accent{Light: "red"}}
		if !errors.Is(base.validate(), ErrInvalid) {
			t.Fatal("invalid retained colour accepted")
		}
	}
	for _, bad := range []string{
		`null`, `{}`, `{"working":null}`, strings.Replace(string(mustJSON(t, states)), `"dark":null`, `"missing":null`, 1),
		strings.Replace(string(mustJSON(t, states)), `"light":"#00870e"`, `"light":null`, 1),
		strings.Replace(string(mustJSON(t, states)), `"light":"#00870e"`, `"light":"#fff"`, 1),
		strings.Replace(string(mustJSON(t, states)), `"dark":"#AB71FA"`, `"dark":"red"`, 1),
		strings.Replace(string(mustJSON(t, states)), `"light":"#00870e"`, `"light":"#00870e","extra":true`, 1),
	} {
		t.Run(bad, func(t *testing.T) {
			body := `{"name":"Custom","scope":"personal","values":` + strings.Replace(string(raw), string(mustJSON(t, states)), bad, 1) + `}`
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/api/themes", strings.NewReader(body))
			var out CreateInput
			accepted := input(w, r, &out, "name", "scope")
			if accepted && (out.Values == nil || !errors.Is(out.Values.validate(), ErrInvalid)) {
				t.Fatal("invalid custom states accepted")
			}
			if !accepted && w.Code != 400 {
				t.Fatalf("wrong rejection: %d", w.Code)
			}
		})
	}
	// Old snapshots can omit the new optional member, preserving the null default.
	old := strings.Replace(string(raw), `,"custom_states":`+string(mustJSON(t, states)), "", 1)
	old = strings.Replace(old, `"palette":"custom"`, `"palette":"standard"`, 1)
	if !completeValues([]byte(old)) {
		t.Fatal("legacy snapshot rejected")
	}
	if err := json.Unmarshal([]byte(old), &decoded); err != nil {
		t.Fatal(err)
	}
	// Unmarshal into a fresh value, as handlers do.
	var legacy Values
	if err := json.Unmarshal([]byte(old), &legacy); err != nil || legacy.Agents.CustomStates != nil || legacy.validate() != nil {
		t.Fatalf("legacy default: %v", err)
	}
	missing := strings.Replace(old, `"palette":"standard"`, `"palette":"custom"`, 1)
	if completeValues([]byte(missing)) {
		t.Fatal("custom without states accepted")
	}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
