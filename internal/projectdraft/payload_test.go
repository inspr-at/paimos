// SPDX-License-Identifier: AGPL-3.0-only
package projectdraft

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// Risk: the public handoff stores whatever passes this parser, so every v1
// bound, type and unknown field must be refused, and a newer version must be
// told apart from a malformed document.
func validDoc() map[string]any {
	return map[string]any{
		"version":     1,
		"source":      map[string]any{"product": "aithema", "host": "start.example.com", "session_id": "0f4c2a1e-7d1b-4c55-9a0e-2b8f6d3c1a90"},
		"language":    "de",
		"title":       "Vorbestell-App für die Bäckerei",
		"summary":     "Eine Bäckerei mit zwei Filialen möchte Vorbestellungen.",
		"facts":       []any{"Zwei Filialen", "  ", "Rund 120 Vorbestellungen pro Woche"},
		"open_points": []any{"Online-Zahlung schon zum Start?"},
		"transcript":  []any{map[string]any{"role": "user", "text": "Hallo"}, map[string]any{"role": "assistant", "text": "Worum geht es?", "at": "2026-10-16T09:42:10.123+02:00"}},
	}
}

func encodeDoc(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePayloadV1(t *testing.T) {
	got, err := ParsePayload(encodeDoc(t, validDoc()))
	if err != nil {
		t.Fatalf("valid payload: %v", err)
	}
	if strings.Join(got.Facts, "|") != "Zwei Filialen|Rund 120 Vorbestellungen pro Woche" {
		t.Fatalf("blank facts are dropped, others kept: %q", got.Facts)
	}
	if got.Transcript[0].At != nil || got.Transcript[1].At == nil || got.Transcript[1].At.Format("15:04:05") != "07:42:10" {
		t.Fatalf("at is optional and normalized to UTC: %+v", got.Transcript)
	}
	cases := map[string]func(map[string]any){
		"unknown field":        func(d map[string]any) { d["destination"] = "ppm" },
		"unknown source field": func(d map[string]any) { d["source"].(map[string]any)["tenant"] = "x" },
		"missing transcript":   func(d map[string]any) { delete(d, "transcript") },
		"null facts":           func(d map[string]any) { d["facts"] = nil },
		"other product":        func(d map[string]any) { d["source"].(map[string]any)["product"] = "other" },
		"bad host":             func(d map[string]any) { d["source"].(map[string]any)["host"] = "evil host/" },
		"bad language":         func(d map[string]any) { d["language"] = "fr" },
		"title over 200":       func(d map[string]any) { d["title"] = strings.Repeat("ä", 201) },
		"summary over 4000":    func(d map[string]any) { d["summary"] = strings.Repeat("x", 4001) },
		"fact over 500":        func(d map[string]any) { d["facts"] = []any{strings.Repeat("x", 501)} },
		"51 open points":       func(d map[string]any) { d["open_points"] = make([]any, 51) },
		"2001 turns":           func(d map[string]any) { d["transcript"] = make([]any, 2001) },
		"unknown role":         func(d map[string]any) { d["transcript"] = []any{map[string]any{"role": "system", "text": "x"}} },
		"turn without text":    func(d map[string]any) { d["transcript"] = []any{map[string]any{"role": "user"}} },
		"malformed at": func(d map[string]any) {
			d["transcript"] = []any{map[string]any{"role": "user", "text": "x", "at": "yesterday"}}
		},
		"NUL in text":              func(d map[string]any) { d["title"] = "a\x00b" },
		"version as string":        func(d map[string]any) { d["version"] = "1" },
		"missing version":          func(d map[string]any) { delete(d, "version") },
		"fact of the wrong type":   func(d map[string]any) { d["facts"] = []any{42} },
		"transcript of wrong type": func(d map[string]any) { d["transcript"] = "Hallo" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := validDoc()
			mutate(doc)
			if _, err := ParsePayload(encodeDoc(t, doc)); !errors.Is(err, errSchema) {
				t.Fatalf("got %v, want errSchema", err)
			}
		})
	}
	t.Run("newer version", func(t *testing.T) {
		doc := validDoc()
		doc["version"], doc["something_new"] = 2, true
		if _, err := ParsePayload(encodeDoc(t, doc)); !errors.Is(err, errVersion) {
			t.Fatalf("got %v, want errVersion", err)
		}
	})
	t.Run("trailing document", func(t *testing.T) {
		if _, err := ParsePayload(append(encodeDoc(t, validDoc()), []byte(` {}`)...)); !errors.Is(err, errSchema) {
			t.Fatalf("got %v, want errSchema", err)
		}
	})
	t.Run("over the wire bound", func(t *testing.T) {
		if _, err := ParsePayload([]byte(strings.Repeat(" ", MaxPayloadBytes+1))); !errors.Is(err, errSchema) {
			t.Fatalf("got %v, want errSchema", err)
		}
	})
}

// Risk: conversation text is rendered by the Markdown viewer; a pasted link
// or heading must stay text.
func TestMarkdownTextStaysText(t *testing.T) {
	for in, want := range map[string]string{
		"[click](https://evil.example)": `\[click\](https://evil.example)`,
		"# Heading":                     `\# Heading`,
		"1. first":                      `1\. first`,
		"- item and *bold* <b>":         `\- item and \*bold\* \<b\>`,
		"Online-Zahlung ab 6:30 Uhr?":   "Online-Zahlung ab 6:30 Uhr?",
	} {
		if got := markdownText(in); got != want {
			t.Errorf("markdownText(%q) = %q, want %q", in, got, want)
		}
	}
}

// Risk: one shared bucket for everyone behind the proxy, or a forged
// X-Forwarded-For from a client that is not a proxy.
func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8")}
	request := func(remote, forwarded string) string {
		r := httptest.NewRequest("POST", "/import/project-draft", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		return clientIP(r, trusted)
	}
	if got := request("198.51.100.7:4100", "203.0.113.9"); got != "198.51.100.7" {
		t.Fatalf("untrusted peer forged its client: %s", got)
	}
	if got := request("127.0.0.1:4100", "203.0.113.9, 10.1.2.3"); got != "203.0.113.9" {
		t.Fatalf("trusted proxies skipped: %s", got)
	}
	if got := request("127.0.0.1:4100", "spoofed, 203.0.113.9"); got != "203.0.113.9" {
		t.Fatalf("rightmost untrusted hop wins: %s", got)
	}
	if got := request("127.0.0.1:4100", ""); got != "127.0.0.1" {
		t.Fatalf("no header keeps the peer: %s", got)
	}
}
