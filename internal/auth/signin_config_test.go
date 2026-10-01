// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOIDCDisplayNameFromEnv(t *testing.T) {
	t.Setenv(envAppEnv, envDev)
	t.Setenv(envSessionKeyFile, "")
	for _, tc := range []struct{ name, input, want string }{
		{"unset", "", ""},
		{"whitespace only", " \t\r\n\u00a0\u2003", ""},
		{"trim", " Acme SSO ", "Acme SSO"},
		{"configured name", "INSPR ID", "INSPR ID"},
		{"collapse whitespace", " Acme\t \n\r\u00a0\u2003SSO ", "Acme SSO"},
		{"strip controls", "Ac\x01me\x7f\u0080 SSO", "Acme SSO"},
		{"strip bidi controls", "\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202eAcme\u2066\u2067\u2068\u2069 SSO", "Acme SSO"},
		{"controls only", " \x01\u202e\u2066\t", ""},
		{"very long name", strings.Repeat("A", 4096), strings.Repeat("A", 48)},
		{"cap runes rather than bytes", strings.Repeat("界🙂", 30), strings.Repeat("界🙂", 24)},
		{"trim truncated whitespace", strings.Repeat("A", 47) + " B", strings.Repeat("A", 47)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envOIDCDisplayName, tc.input)
			cfg, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OIDCDisplayName != tc.want {
				t.Fatalf("display name = %q, want %q", cfg.OIDCDisplayName, tc.want)
			}
			if !utf8.ValidString(cfg.OIDCDisplayName) || utf8.RuneCountInString(cfg.OIDCDisplayName) > 48 {
				t.Fatal("display name violates the API's 48-character limit")
			}
		})
	}
}

func TestMePublicSignInConfig(t *testing.T) {
	for _, name := range []string{"", "Acme SSO", "INSPR ID", "<b>SSO</b>"} {
		for _, environment := range []string{"dev", "prod"} {
			t.Run(environment+"/"+name, func(t *testing.T) {
				mod := &Module{cfg: Config{Env: environment, OIDCDisplayName: name}}
				response := httptest.NewRecorder()
				mod.handleMe(response, httptest.NewRequest(http.MethodGet, "/api/me", nil))
				if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("unauthenticated config: status %d, cache %q", response.Code, response.Header().Get("Cache-Control"))
				}
				var got map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				// No other authentication configuration is public.
				if len(got) != 3 || got["error"] != "unauthorized" || got["dev_mode"] != (environment == envDev) || got["oidc_display_name"] != name {
					t.Fatalf("public sign-in config = %v", got)
				}
				body, err := json.Marshal(mod.meJSONFrom(meView{}))
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if got["oidc_display_name"] != name || got["dev_mode"] != (environment == envDev) {
					t.Fatal("authenticated config differs from public sign-in config")
				}
			})
		}
	}
}
