// SPDX-License-Identifier: AGPL-3.0-only

package agentpairing_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/agentpairing"
	"github.com/inspr-at/paimos/internal/config"
)

func TestNixGuideBelongsToDeploymentOnly(t *testing.T) {
	linux := &config.PairingNixGuide{ModuleURL: "https://other.test/linux.nix", ServiceOption: "other.agent.enable", Platforms: []string{"linux"}, ServiceNote: "Review service activation."}
	for _, tc := range []struct {
		name, origin, platform string
		guide                  *config.PairingNixGuide
	}{
		{"unconfigured", "https://unconfigured.test", "", nil},
		{"macOS", origin, "macOS only", nixGuideFixture()},
		{"Linux", "https://linux.test", "Linux only", linux},
		{"invalid", origin, "", &config.PairingNixGuide{ModuleURL: "javascript:alert(1)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			agentpairing.New(nil, tc.origin, "tenant", tc.guide).Mount(mux)
			r := httptest.NewRequest("GET", "/api/agent-pairing/guide?module_url=https://attacker.invalid", nil)
			r.Host = "attacker.invalid"
			r.Header.Set("X-Forwarded-Host", "attacker.invalid")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			var payload struct {
				Managed *agentpairing.ManagedSetup `json:"managed_setup"`
			}
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil {
				t.Fatal("guide unavailable")
			}
			html := httptest.NewRecorder()
			agentpairing.GuidePage(http.NotFoundHandler(), nil, tc.origin, tc.guide).ServeHTTP(html, httptest.NewRequest("GET", "/agents/register-agent", nil))
			for _, body := range []string{w.Body.String(), html.Body.String()} {
				if strings.Contains(body, "markus-barta/nixcfg") || strings.Contains(body, "uzumaki") || strings.Contains(body, "attacker.invalid") {
					t.Fatal("guide leaked a personal or peer-supplied module")
				}
			}
			if tc.platform == "" {
				if payload.Managed != nil || strings.Contains(html.Body.String(), "<h2>Nix / Home Manager") {
					t.Fatal("unconfigured or invalid module advertised")
				}
			} else {
				m := payload.Managed
				if m == nil || m.ModuleURL != tc.guide.ModuleURL || m.ServiceOption != tc.guide.ServiceOption || m.Command != "aeon-agentd pair --url '"+tc.origin+"'" {
					t.Fatal("instance-specific module binding lost")
				}
				for _, note := range []string{tc.platform, "PATH", "release pin", "approve as a person"} {
					if !strings.Contains(html.Body.String(), note) {
						t.Fatalf("HTML guide lacks %s", note)
					}
				}
				if !strings.Contains(m.PlatformNote, tc.platform) || !strings.Contains(m.PrerequisiteNote, "PATH") {
					t.Fatal("machine guide lost platform or pin prerequisites")
				}
			}
			// Configuration is process-owned; even authenticated peers cannot
			// mutate it through the public guide route.
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest("PUT", "/api/agent-pairing/guide", strings.NewReader(`{"module_url":"https://attacker.invalid"}`)))
			if w.Code != http.StatusMethodNotAllowed {
				t.Fatal("guide accepted a configuration write")
			}
		})
	}
}
