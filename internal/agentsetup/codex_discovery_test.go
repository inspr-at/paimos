// SPDX-License-Identifier: AGPL-3.0-only

package agentsetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/internal/harnesslaunch"
	"github.com/inspr-at/paimos/internal/ownedprocess"
)

// Risk: the guard's successful version/login responses used to bypass Node
// discovery, leaving account/read with ServicePath and no candidate Node.
func TestCodexIdentityRetryUsesCandidateNode(t *testing.T) {
	d, launcher, _, next := codexUpgradeFixture(t)
	var probes []string
	d.CodexProbe = func(_ context.Context, path, home, node, dir string) (string, error) {
		if path != launcher || home != filepath.Join(d.Home, ".codex") || dir == "" {
			t.Fatal("probe escaped its candidate launcher, home or working directory")
		}
		probes = append(probes, node)
		if node == "" {
			return "", harnesslaunch.ErrStart
		}
		if node != next.Path {
			t.Fatal("identity probe used a saved or unrelated interpreter")
		}
		return "agent@example.test", nil
	}
	c, err := d.Detect(t.Context(), "codex", "agent@example.test")
	if err != nil || c.Node != next || len(probes) != 2 || probes[0] != "" || probes[1] != next.Path {
		t.Fatalf("candidate Node retry failed: %v, probes=%v", err, probes)
	}
}

func TestCodexIdentityFailuresExposeOnlyAllowlistedCauses(t *testing.T) {
	private := errors.New("/private/fixture agent@example.test synthetic-token")
	for _, tc := range []struct {
		cause error
		code  string
	}{
		{errors.Join(context.DeadlineExceeded, private), "identity_timeout"},
		{errors.Join(context.Canceled, private), "identity_canceled"},
		{errors.Join(ownedprocess.ErrCleanupUnconfirmed, private), "identity_cleanup_unconfirmed"},
		{errors.Join(harnesslaunch.ErrStart, private), "identity_interpreter_invalid"},
		{identityFailure("identity_response_invalid", private), "identity_response_invalid"},
		{identityFailure("identity_start_failed", private), "identity_start_failed"},
		{identityFailure("identity_rpc_failed", private), "identity_rpc_failed"},
		{private, "identity_probe_failed"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			d, _, _, next := codexUpgradeFixture(t)
			d.NodePath = next.Path
			d.CodexProbe = func(context.Context, string, string, string, string) (string, error) {
				return "", tc.cause
			}
			_, err := d.Detect(t.Context(), "codex", "")
			if err == nil || HarnessFailureReason(err) != "probe_failed" || !strings.Contains(err.Error(), tc.code) {
				t.Fatal("underlying identity failure swallowed", err)
			}
			for _, secret := range []string{"/private/", "agent@example.test", "synthetic-token"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("private identity diagnostic escaped")
				}
			}
			if !errors.Is(err, tc.cause) {
				t.Fatal("underlying diagnostic was not retained locally")
			}
		})
	}
}

func TestCodexIdentityClosesInputBeforeOwnedCleanup(t *testing.T) {
	// A fake ownership waiter makes forced-cleanup failure deterministic.
	// The legacy cancel-first sequence is the negative control; no sleeps,
	// process signals, vendor executable or real account are involved.
	ctx, cancel := context.WithCancel(t.Context())
	closed := false
	wait := func() error {
		if ctx.Err() != nil || !closed {
			return ownedprocess.ErrCleanupUnconfirmed
		}
		return nil
	}
	cancel()
	if !errors.Is(wait(), ownedprocess.ErrCleanupUnconfirmed) {
		t.Fatal("negative control did not reproduce the discarded identity")
	}
	ctx, cancel = context.WithCancel(t.Context())
	if err := finishCodexIdentity(ctx, cancel, func() error { closed = true; return nil }, wait); err != nil || ctx.Err() != context.Canceled {
		t.Fatal("complete identity was forced into teardown before stdin EOF", err)
	}
	// Confirmed output never overrides an unconfirmed group cleanup.
	ctx, cancel = context.WithCancel(t.Context())
	if err := finishCodexIdentity(ctx, cancel, func() error { return nil }, func() error { return ownedprocess.ErrCleanupUnconfirmed }); !errors.Is(err, ownedprocess.ErrCleanupUnconfirmed) {
		t.Fatal("cleanup ownership fence was weakened")
	}
}

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
