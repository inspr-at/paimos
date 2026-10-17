// SPDX-License-Identifier: AGPL-3.0-only

package workerlease

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransitionRejectsAmbiguousProofAndNeverLogsValues(t *testing.T) {
	lease := strings.Repeat("synthetic-a7", 4)
	other := strings.Repeat("synthetic-b8", 4)
	for _, tc := range []struct {
		name                 string
		headers              http.Header
		accepted, deprecated bool
	}{
		{"new", http.Header{Header: {Value(lease)}}, true, false},
		{"legacy", http.Header{LegacyHeader: {lease}}, true, true},
		{"same", http.Header{Header: {Value(lease)}, LegacyHeader: {lease}}, true, true},
		{"case", http.Header{Header: {"bEaReR " + lease}}, true, false},
		{"conflict", http.Header{Header: {Value(lease)}, LegacyHeader: {other}}, false, true},
		{"scheme", http.Header{Header: {"Basic " + lease}, LegacyHeader: {lease}}, false, true},
		{"empty-new", http.Header{Header: {""}, LegacyHeader: {lease}}, false, true},
		{"duplicate-new", http.Header{Header: {Value(lease), Value(lease)}}, false, false},
		{"duplicate-old", http.Header{LegacyHeader: {lease, lease}}, false, true},
		{"short", http.Header{Header: {Value("short")}}, false, false},
		{"oversize", http.Header{Header: {Value(strings.Repeat("s", 4097))}}, false, false},
		{"oversize-old", http.Header{LegacyHeader: {strings.Repeat("s", 4097)}}, false, true},
		{"combined", http.Header{Header: {Value(lease) + ", " + Value(other)}}, false, false},
		{"whitespace", http.Header{Header: {Value(" " + lease)}}, false, false},
		{"missing", http.Header{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			r := httptest.NewRequest(http.MethodPost, "/worker", nil)
			r.Header = tc.headers
			r.Header.Set("Authorization", "Bearer "+other)
			got := Read(r)
			if (got == lease) != tc.accepted || !tc.accepted && got != "" {
				t.Fatal("unexpected proof acceptance")
			}
			if strings.Contains(logs.String(), lease) || strings.Contains(logs.String(), other) {
				t.Fatal("deprecation warning exposed a credential")
			}
			if strings.Contains(logs.String(), "deprecated") != tc.deprecated {
				t.Fatal("unexpected deprecation warning")
			}
		})
	}
}
