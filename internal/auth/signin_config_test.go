// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOIDCDisplayNameFromEnv(t *testing.T) {
	t.Setenv(envAppEnv, envDev)
	t.Setenv(envSessionKeyFile, "")
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {" \t\n", ""}, {" Acme SSO ", "Acme SSO"}, {"INSPR ID", "INSPR ID"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv(envOIDCDisplayName, tc.input)
			cfg, err := FromEnv()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.OIDCDisplayName != tc.want {
				t.Fatalf("display name = %q, want %q", cfg.OIDCDisplayName, tc.want)
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
