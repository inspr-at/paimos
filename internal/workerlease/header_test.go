// SPDX-License-Identifier: AGPL-3.0-only

package workerlease

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestTransitionRejectsAmbiguousProofAndNeverLogsValues(t *testing.T) {
	lease := strings.Repeat("synthetic-a7", 4)
	other := strings.Repeat("synthetic-b8", 4)
	key := "aeon_" + strings.Repeat("ab", 16) + "_" + strings.Repeat("cd", 16)
	for _, tc := range []struct {
		name                 string
		headers              http.Header
		accepted, deprecated bool
	}{
		{"new", http.Header{"Authorization": {Value(key, lease)}}, true, false},
		{"legacy", http.Header{LegacyHeader: {lease}}, true, true},
		{"same", http.Header{"Authorization": {Value(key, lease)}, LegacyHeader: {lease}}, true, true},
		{"case", http.Header{"Authorization": {"aEoN-wOrKeR " + key + " " + lease}}, true, false},
		{"conflict", http.Header{"Authorization": {Value(key, lease)}, LegacyHeader: {other}}, false, true},
		{"malformed", http.Header{"Authorization": {"Aeon-Worker " + key}, LegacyHeader: {lease}}, false, true},
		{"empty-new", http.Header{"Authorization": {"Aeon-Worker"}, LegacyHeader: {lease}}, false, true},
		{"duplicate-new", http.Header{"Authorization": {Value(key, lease), Value(key, lease)}}, false, false},
		{"duplicate-old", http.Header{LegacyHeader: {lease, lease}}, false, true},
		{"short", http.Header{"Authorization": {Value(key, "short")}}, false, false},
		{"oversize", http.Header{"Authorization": {Value(key, strings.Repeat("s", 4097))}}, false, false},
		{"oversize-old", http.Header{LegacyHeader: {strings.Repeat("s", 4097)}}, false, true},
		{"combined", http.Header{"Authorization": {Value(key, lease) + ", " + Value(key, other)}}, false, false},
		{"whitespace", http.Header{"Authorization": {"Aeon-Worker " + key + "  " + lease}}, false, false},
		{"hop-by-hop", http.Header{"Proxy-Authorization": {"Bearer " + lease}}, false, false},
		{"missing", http.Header{}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			r := httptest.NewRequest(http.MethodPost, "/worker", nil)
			r.Header = tc.headers.Clone()
			if len(r.Header.Values("Authorization")) == 0 {
				r.Header.Set("Authorization", "Bearer "+other)
			}
			got := Read(r)
			if (got == lease) != tc.accepted || !tc.accepted && got != "" {
				t.Fatal("unexpected proof acceptance")
			}
			if strings.Contains(logs.String(), lease) || strings.Contains(logs.String(), other) || strings.Contains(logs.String(), key) {
				t.Fatal("deprecation warning exposed a credential")
			}
			if strings.Contains(logs.String(), "deprecated") != tc.deprecated {
				t.Fatal("unexpected deprecation warning")
			}
		})
	}
}

// Risk: the generation proof rides a hop-by-hop header, so Traefik and Caddy
// drop it and every worker call 403s after the client upgrade.
func TestWorkerSchemeSurvivesHopByHopProxy(t *testing.T) {
	lease := strings.Repeat("synthetic-a7", 4)
	key := "aeon_" + strings.Repeat("ab", 16) + "_" + strings.Repeat("cd", 16)
	var got, token string
	var malformed bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = Read(r)
		token, malformed = AgentToken(r.Header.Get("Authorization"))
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get(LegacyHeader) != "" {
			t.Error("worker proof left Authorization")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(target))
	t.Cleanup(proxy.Close)
	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	Install(req.Header, key, lease)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got != lease || malformed || token != key {
		t.Fatal("worker proof did not survive a reverse proxy")
	}
}
