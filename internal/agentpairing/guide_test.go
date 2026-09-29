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
	"github.com/inspr-at/paimos/internal/version"
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
				if m == nil || m.ModuleURL != tc.guide.ModuleURL || m.ServiceOption != tc.guide.ServiceOption || m.Command != `env "$HOME/.nix-profile/bin/aeon-agentd" pair --url '`+tc.origin+`'` {
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

func TestGuideCommandsHaveNoPlaceholders(t *testing.T) {
	old := version.Version
	version.Version = "260929113854.0.0"
	t.Cleanup(func() { version.Version = old })
	mux := http.NewServeMux()
	agentpairing.New(nil, origin, "tenant", nixGuideFixture()).Mount(mux)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/agent-pairing/guide", nil)
	r.Host = "attacker.invalid"
	mux.ServeHTTP(w, r)
	var guide struct {
		Pair    string                       `json:"setup_command"`
		Brew    string                       `json:"homebrew_command"`
		Targets []agentpairing.InstallTarget `json:"install_targets"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &guide) != nil {
		t.Fatal("guide unavailable")
	}
	want := "aeon-agentd pair --url '" + origin + "'"
	brew := "brew install inspr-at/tap/aeon-agentd\nenv \"$(brew --prefix)/bin/aeon-agentd\" pair --url '" + origin + "'"
	if guide.Pair != want || guide.Brew != brew || len(guide.Targets) != 4 {
		t.Fatal("guide commands differ from instance")
	}
	for _, target := range guide.Targets {
		if strings.Contains(target.Command, "<verified") || strings.Contains(target.Command, "<absolute") {
			t.Fatal("installer has placeholders")
		}
	}
	html := httptest.NewRecorder()
	agentpairing.GuidePage(http.NotFoundHandler(), nil, origin, nixGuideFixture()).ServeHTTP(html, httptest.NewRequest("GET", "/agents/register-agent", nil))
	for _, forbidden := range []string{"placeholder", "&lt;verified", "attacker.invalid", "Install only the matching verified release"} {
		if strings.Contains(html.Body.String(), forbidden) {
			t.Fatalf("HTML includes %s", forbidden)
		}
	}
	for _, required := range []string{"brew install inspr-at/tap/aeon-agentd", "$(brew --prefix)/bin/aeon-agentd", "$HOME/.nix-profile/bin/aeon-agentd", "add-harness", "aeon-agentd disconnect", "brew uninstall aeon-agentd", "~/.local/bin", "Nix / Home Manager", "This formula matches this Aeon’s version", "helper/instance version mismatch", "does not drain or restart", "verify Touch ID on the new daemon", "Trouble?", "usage: paimos-agentd setup|status…", "Nix or Home Manager on this Mac? Choose macOS · Nix.", "Setting up another computer, or letting an agent do it? Share this address:"} {
		if !strings.Contains(html.Body.String(), required) {
			t.Fatalf("HTML missing %s", required)
		}
	}
}
