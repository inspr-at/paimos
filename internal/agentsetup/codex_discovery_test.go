// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexDiscoveryStderrAndOptionalEmail(t *testing.T) {
	for _, tc := range []struct {
		name, response, context, label string
	}{
		{"email", `{"account":{"type":"chatgpt","email":"agent@example.test"}}`, "", "agent@example.test"},
		{"no email", `{"account":{"type":"chatgpt"}}`, "", CodexChatGPTLogin},
		{"null email", `{"account":{"type":"chatgpt","email":null}}`, "", CodexChatGPTLogin},
		{"empty email", `{"account":{"type":"chatgpt","email":""}}`, "", CodexChatGPTLogin},
		{"explicit context", `{"account":{"type":"chatgpt"}}`, "agent@example.test", ""},
		{"wrong account", `{"account":{"type":"chatgpt","email":"other@example.test"}}`, "agent@example.test", ""},
		{"signed out", `{"account":null}`, "", ""},
		{"api key", `{"account":{"type":"apiKey"}}`, "", ""},
		{"invalid email", `{"account":{"type":"chatgpt","email":"bad\nlabel"}}`, "", ""},
		{"blank email", `{"account":{"type":"chatgpt","email":" "}}`, "", ""},
		{"rpc unavailable", `null`, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := physicalTemp(t)
			if err := os.Mkdir(filepath.Join(home, ".codex"), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(home, "codex")
			// These are synthetic protocol responses; no vendor credential file
			// is present. Only login status writes stderr, just like the real CLI.
			script := fmt.Sprintf(`#!/bin/sh
case "$1" in
 --version) echo 'codex-cli 0.157.1'; exit;;
 login) echo 'Logged in using ChatGPT' >&2; exit;;
 app-server) [ "$CODEX_HOME" = '%s/.codex' ] || exit 4;;
 *) exit 9;;
esac
IFS= read -r line
printf '%%s\n' '{"id":1,"result":{}}'
IFS= read -r line
IFS= read -r line
printf '%%s\n' '{"id":2,"result":%s}'
`, home, tc.response)
			if err := os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODEX_HOME", "")
			d := Discovery{Home: home, LookPath: func(name string) (string, error) {
				if name == "codex" {
					return path, nil
				}
				return "", os.ErrNotExist
			}}
			candidates := d.Available(t.Context(), tc.context)
			if tc.label == "" {
				if len(candidates) != 0 {
					t.Fatal("unverified account offered")
				}
				return
			}
			if len(candidates) != 1 || candidates[0].Label != tc.label || candidates[0].Identity != tc.label || candidates[0].Login != "signed_in" || candidates[0].Home != filepath.Join(home, ".codex") {
				t.Fatalf("unexpected candidates: %+v", candidates)
			}
			e, api, _, opts, _ := engineFixture(t)
			defer e.Store.Close()
			opts.Candidates = candidates
			approveFixture(t, e, api, opts)
			config, err := ReadRuntimeConfig(e.Store.Path())
			if err != nil || len(config.Accounts) != 1 || config.Accounts[0].Identity != tc.label {
				t.Fatal("identity was not preserved through enrollment", err)
			}
		})
	}
}

func TestCodexGuardWrapperPinsNodeWithoutBypassingGuard(t *testing.T) {
	d, entry, node := npmFixture(t, "codex")
	guard := filepath.Join(d.Home, "codex-guard")
	// Force the same missing-interpreter failure even on Linux test machines
	// that happen to have a system Node. The guard must run on every probe.
	script := fmt.Sprintf("#!/bin/sh\n[ \"${PATH%%%%:*}\" = %q ] || exit 127\nexport AEON_TEST_GUARD=present\nexec %q \"$@\"\n", filepath.Dir(node.Path), entry)
	if err := os.WriteFile(guard, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(entry)
	if err != nil {
		t.Fatal(err)
	}
	guarded := strings.Replace(string(raw), "#!/usr/bin/env node\n", "#!/usr/bin/env node\n[ \"$AEON_TEST_GUARD\" = present ] || exit 90\n", 1)
	if err := os.WriteFile(entry, []byte(guarded), 0700); err != nil {
		t.Fatal(err)
	}
	d.NodePath = ""
	d.LookPath = func(name string) (string, error) {
		if name == "codex" {
			return guard, nil
		}
		if name == "node" {
			return node.Path, nil
		}
		return "", os.ErrNotExist
	}
	c, err := d.Detect(t.Context(), "codex", "")
	if err != nil || c.Path != guard || c.Node != node || c.Login != "signed_in" {
		t.Fatal("guarded npm Codex was not offered", err)
	}
	if err := os.Chmod(node.Path, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Detect(t.Context(), "codex", ""); err == nil {
		t.Fatal("fallback accepted writable interpreter")
	}
	if err := os.Chmod(node.Path, 0700); err != nil {
		t.Fatal(err)
	}
	d.Workspace = d.Home
	if _, err := d.Detect(t.Context(), "codex", ""); err == nil {
		t.Fatal("fallback accepted workspace interpreter")
	}
}
