// SPDX-License-Identifier: AGPL-3.0-only
package openrouter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCatalogValidationAndCache(t *testing.T) {
	for _, s := range []string{"stealth/space-bunny-alpha", "vendor/model:free", "a/b"} {
		if !ValidModel("openrouter", s) {
			t.Fatal("valid slug rejected")
		}
	}
	for _, s := range []string{"model", "https://openrouter.ai/a/b", "a/b/c", "a/b:free:high", "a/b\n", "../a", "sk-obviously-fake/key"} {
		if ValidModel("openrouter", s) {
			t.Fatal("invalid slug accepted")
		}
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/models" || r.Header.Get("Authorization") != "" {
			t.Error("catalog request is not public read-only")
		}
		fmt.Fprint(w, `{"data":[{"id":"vendor/test","pricing":{"prompt":"0","completion":"0"}}]}`)
	}))
	defer server.Close()
	c := Catalog{Client: Client{Base: server.URL}}
	if status, note := c.Lookup(t.Context(), "vendor/test"); status != "known" || !note {
		t.Fatal(status, note)
	}
	if status, _ := c.Lookup(t.Context(), "vendor/absent"); status != "unknown" {
		t.Fatal(status)
	}
	if calls != 1 {
		t.Fatal("catalog not cached")
	}
}
func TestCatalogFailureIsAdvisoryAndBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	c := Catalog{Client: Client{Base: server.URL, HTTP: &http.Client{Timeout: 20 * time.Millisecond}}}
	started := time.Now()
	if status, note := c.Lookup(context.Background(), "stealth/test"); status != "unknown" || !note {
		t.Fatal(status, note)
	}
	if time.Since(started) > time.Second {
		t.Fatal("lookup exceeded bound")
	}
}
func TestKeyProjectionAndNoRedirect(t *testing.T) {
	const fake = "obviously-fake-openrouter-key"
	leakTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("key followed redirect") }))
	defer leakTarget.Close()
	for _, mode := range []string{"ok", "denied", "malformed", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/key" || r.Header.Get("Authorization") != "Bearer "+fake {
					t.Error("unexpected key check")
				}
				switch mode {
				case "ok":
					fmt.Fprint(w, `{"data":{"label":"private-label","usage":3.5,"limit":10,"limit_remaining":6.5}}`)
				case "denied":
					w.WriteHeader(401)
					fmt.Fprint(w, fake)
				case "malformed":
					fmt.Fprint(w, fake)
				case "redirect":
					http.Redirect(w, r, leakTarget.URL, 302)
				}
			}))
			defer server.Close()
			v, err := (Client{Base: server.URL}).CheckKey(t.Context(), fake)
			if mode == "ok" {
				if err != nil || v.Usage == nil || *v.Usage != 3.5 || *v.Remaining != 6.5 {
					t.Fatal("credits not projected")
				}
			} else if err == nil || strings.Contains(err.Error(), fake) {
				t.Fatal("unsafe error")
			}
			if mode == "denied" && !errors.Is(err, ErrKey) {
				t.Fatal("denial not classified")
			}
		})
	}
}

func TestNullKeyCapDoesNotImplyBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" {
			t.Error("broader management credential endpoint requested")
		}
		fmt.Fprint(w, `{"data":{"usage":2,"limit":null,"limit_remaining":0}}`)
	}))
	defer server.Close()
	credits, err := (Client{Base: server.URL}).CheckKey(t.Context(), "synthetic-key-value")
	if err != nil || credits.Limit != nil || credits.Remaining != nil || credits.Usage == nil || *credits.Usage != 2 {
		t.Fatal("null cap became credit evidence")
	}
}
