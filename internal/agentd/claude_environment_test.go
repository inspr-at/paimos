// SPDX-License-Identifier: AGPL-3.0-only
package agentd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeAccountEnvironmentProbeAndRun(t *testing.T) {
	for _, defaultAccount := range []bool{true, false} {
		t.Run(fmt.Sprintf("default=%t", defaultAccount), func(t *testing.T) {
			userHome := privateHome(t)
			t.Setenv("HOME", userHome)
			for _, name := range []string{"CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "NODE_OPTIONS"} {
				t.Setenv(name, "inherited-fixture-value")
			}
			home := filepath.Join(userHome, ".claude")
			if !defaultAccount {
				home = filepath.Join(userHome, "other-claude")
			}
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			wantHome := home
			configCheck := fmt.Sprintf(`[ "$CLAUDE_CONFIG_DIR" = %q ]`, home)
			if defaultAccount {
				wantHome = userHome
				configCheck = `[ "${CLAUDE_CONFIG_DIR+x}" != x ]`
			}
			// The stand-in only confirms authentication under the intended
			// environment. It prints no environment or inherited value.
			path := fakeScript(t, fmt.Sprintf(`[ "$HOME" = %q ] && %s && [ "${ANTHROPIC_API_KEY+x}" != x ] && [ "${ANTHROPIC_BASE_URL+x}" != x ] && [ "${CLAUDE_CODE_USE_BEDROCK+x}" != x ] && [ "${NODE_OPTIONS+x}" != x ] || exit 2
printf '%%s\n' '{"loggedIn":true,"email":"fixture@example.test","authMethod":"claude.ai"}'`, wantHome, configCheck))
			node, sdk := claudeAdapterDependencies(t, path)
			a := NewClaudeAdapter(node, sdk, path, map[string]string{"account": home})
			a.SetExpectedEmails(map[string]string{"account": "fixture@example.test"})
			if !a.ProbeStatus(t.Context(), "account").OK {
				t.Error("probe did not use the account environment")
			}

			r := adapterRequest(t)
			r.Profile.Harness = Claude
			path = fakeVendorPath(t, "claude")
			node, sdk = claudeAdapterDependencies(t, path)
			a = NewClaudeAdapter(node, sdk, path, map[string]string{"account": home})
			p, err := a.Start(t.Context(), r, func(AdapterEvent) {})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = p.Stop(t.Context()); _ = p.Wait() })
			values := map[string]string{}
			for _, entry := range p.(*claudeProcess).cmd.Env {
				name, value, _ := strings.Cut(entry, "=")
				values[name] = value
			}
			if values["HOME"] != wantHome {
				t.Fatal("run used the wrong HOME")
			}
			config, present := values["CLAUDE_CONFIG_DIR"]
			if defaultAccount && present || !defaultAccount && (!present || config != home) {
				t.Fatal("run used the wrong config directory policy")
			}
			wantCount := 5
			if defaultAccount {
				wantCount = 4
			}
			if len(values) != wantCount || values["PATH"] != strings.Join([]string{filepath.Dir(node), filepath.Dir(path), "/usr/bin", "/bin"}, string(os.PathListSeparator)) || values["LANG"] != "C" || values["LC_ALL"] != "C" {
				t.Fatal("run environment was not minimal and pinned")
			}
		})
	}
}
